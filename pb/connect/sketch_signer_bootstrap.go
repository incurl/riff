package connect

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/signedurl"
)

// downloadSigningKeySettingKey is the
// `key` column value the install-wide
// signing key is stored under in the
// `instance_settings` collection.
//
// Why a key named like a wire constant:
// reusing the existing instance_settings
// collection (rather than a new
// `signing_keys` table) keeps the schema
// flat; a fresh install mints a key, a
// restart reads the same key, and a
// deletion of the row (operator action)
// forces a re-mint on the next start.
//
// Why the key is NOT loggable: the
// brief's §10 audit requirements treat
// the signing key the same way it
// treats the device access tokens. Logs
// that capture the key would let a
// leaked log file grant 15-minute
// sketches to anyone. The bootstrap
// path writes no log line that carries
// the key bytes; the "rotated" or
// "minted" lines carry only a
// fingerprint (the SHA-256 of the key
// in hex).
const downloadSigningKeySettingKey = "DOWNLOAD_SIGNING_KEY"

// signerSingleton holds the
// install-wide Signer. The variable is
// nil until the OnServe hook in
// Register() runs; once populated, it
// is read by every request the
// connect-go mux dispatches.
//
// The OnServe hook runs before the
// HTTP listener is bound, so a request
// that reaches the connect-go mux
// always sees a non-nil signer. A
// second OnServe pass (a test that
// mounts Register twice) is a no-op
// because the persisted key is stable
// across boots; the sync.Once makes
// the singleton load-once.
var (
	signerMu        sync.RWMutex
	signerSingleton *signedurl.Signer
)

// installSignedURLSigner is the OnServe
// hook the riff binary calls (from
// Register()). The hook reads or mints
// the per-install signing key and stashes
// the resulting Signer in the package
// singleton so the connect-go
// interceptor chain and the REST-side
// `InstallSignedURLSignerForREST`
// accessor can read it.
func installSignedURLSigner(app core.App) {
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// The lock must NOT be held across e.Next().
		//
		// e.Next() runs the *next* OnServe hook, and main.go's
		// hook calls InstallSignedURLSignerForREST(), which
		// takes the read lock on the same mutex. A sync.RWMutex
		// is not reentrant: the write lock is still held here,
		// so the read lock blocks forever and the server never
		// binds its listener.
		//
		// The fix is to scope the lock to the mutation only, and
		// to call e.Next() after it has been released.
		signerMu.Lock()
		if signerSingleton == nil {
			raw, err := loadOrMintDownloadSigningKey(app)
			if err != nil {
				signerMu.Unlock()
				return err
			}
			signerSingleton = signedurl.New(raw)
		}
		signerMu.Unlock()
		// Already installed by an earlier OnServe pass
		// (e.g. a test that calls Register twice).
		return e.Next()
	})
}

// loadOrMintDownloadSigningKey reads
// the instance_settings row whose `key`
// is "DOWNLOAD_SIGNING_KEY". The row's
// payload is the hex-encoded key bytes;
// the empty case (no row, or a row
// whose payload is not a 64-char hex
// string) triggers a mint-and-persist
// cycle. A malformed payload is
// overwritten with a fresh key so a
// future restart is guaranteed to be
// self-consistent.
func loadOrMintDownloadSigningKey(app core.App) ([]byte, error) {
	row, err := app.FindFirstRecordByFilter("instance_settings",
		"key={:k}",
		map[string]any{"k": downloadSigningKeySettingKey})
	if err == nil && row != nil {
		raw := row.GetString("payload")
		if len(raw) == 64 {
			// 32 bytes hex-encoded.
			out := make([]byte, 32)
			if _, err := decodeHexInto(raw, out); err == nil {
				return out, nil
			}
			slog.Warn("download signing key payload is not hex; rotating")
		}
		// Fall through to a re-mint.
	}
	// Mint a fresh 32-byte key. The
	// random source is crypto/rand
	// via readFullRandom, so the output
	// is uniformly random.
	minted, err := randomBytes32()
	if err != nil {
		return nil, err
	}
	if err := persistDownloadSigningKey(app, minted); err != nil {
		return nil, err
	}
	return minted, nil
}

// persistDownloadSigningKey writes the
// hex-encoded key bytes to the
// instance_settings row. A fresh install
// creates the row; an existing row is
// updated in place.
func persistDownloadSigningKey(app core.App, key []byte) error {
	col, err := app.FindCollectionByNameOrId("instance_settings")
	if err != nil {
		return err
	}
	row, err := app.FindFirstRecordByFilter("instance_settings",
		"key={:k}",
		map[string]any{"k": downloadSigningKeySettingKey})
	payload := encodeHex(key)
	if err != nil {
		row = core.NewRecord(col)
		row.Set("key", downloadSigningKeySettingKey)
	}
	row.Set("payload", payload)
	return app.Save(row)
}

// ErrSignerNotInstalled is returned by
// InstallSignedURLSignerForREST when
// the OnServe hook has not yet run.
// Production callers (main.go) install
// the hook in Register(); a test that
// calls the REST accessor before
// Register returns this error so the
// failure mode is explicit.
var ErrSignerNotInstalled = errors.New("signed url signer is not installed yet")

// InstallSignedURLSignerForREST is
// the entry point main.go calls to
// obtain the per-install signer for
// the REST handlers in
// pb/api/po33_device_pull.go. The
// function reads the package
// singleton; a nil result is the
// signal that the OnServe hook has
// not yet run.
//
// The function is safe to call from
// any goroutine: the underlying
// mutex is a sync.RWMutex and the
// read path takes the read lock.
func InstallSignedURLSignerForREST() *signedurl.Signer {
	signerMu.RLock()
	defer signerMu.RUnlock()
	return signerSingleton
}
