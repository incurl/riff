package hooks

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/storage"
)

// storageBridgeLog is the slog logger for the storage bridge.
// Pulled out so tests can substitute a discard handler.
var storageBridgeLog = slog.Default()

// storageBridgeTimeout caps how long a single bridge upload may
// take. 30s matches PocketBase's default upload timeout.
const storageBridgeTimeout = 30 * time.Second

// storageBridgeConfigCacheTTL is how long a resolved storage
// config is reused before the bridge re-reads the persisted
// `instance_settings` row.
const storageBridgeConfigCacheTTL = 30 * time.Second

// storageBridgeCache holds the most recent resolved storage
// backend so the bridge does not re-read `instance_settings` on
// every record save.
var storageBridgeCache = struct {
	sync.Mutex
	store    storage.Storage
	loadedAt time.Time
}{}

// registerStorageBridge subscribes the storage bridge to the
// `patches` and `samples` collections' after-create, after-update,
// and after-delete record hooks. The bridge mirrors each local
// file to the configured remote backend (S3, R2, MinIO) and
// stores the public URL in the record's `remote_url` column.
//
// The hook is a no-op when no remote storage is configured (the
// default and the `DATABASE`/`LOCAL` settings). It runs
// best-effort: a remote-side failure logs a warning but does not
// fail the request, because the local file is the canonical
// source of truth and the next save will retry.
func registerStorageBridge(app core.App) {
	bind := func(e *core.RecordEvent) error {
		defer func() { _ = e.Next() }()
		mirrorRecordToRemote(app, e.Record)
		return nil
	}
	bindDelete := func(e *core.RecordEvent) error {
		defer func() { _ = e.Next() }()
		deleteRemoteForRecord(app, e.Record)
		return nil
	}
	for _, tag := range []string{"patches", "samples"} {
		app.OnRecordAfterCreateSuccess(tag).BindFunc(bind)
		app.OnRecordAfterUpdateSuccess(tag).BindFunc(bind)
		app.OnRecordAfterDeleteSuccess(tag).BindFunc(bindDelete)
	}
}

// mirrorRecordToRemote uploads every file in the record's
// FileField to the configured remote backend, writing the
// returned public URL into the record's `remote_url` column. If
// no remote backend is configured, the call is a no-op.
func mirrorRecordToRemote(app core.App, rec *core.Record) {
	store, err := resolveStorage(app)
	if err != nil {
		storageBridgeLog.Warn("storage-bridge: cannot resolve backend", "err", err.Error())
		return
	}
	if store == nil {
		return
	}
	if !shouldMirrorCollection(rec.Collection().Name) {
		return
	}
	filenames := rec.GetStringSlice(fileFieldName(rec))
	if len(filenames) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), storageBridgeTimeout)
	defer cancel()

	urls := make([]string, 0, len(filenames))
	for _, filename := range filenames {
		if filename == "" {
			continue
		}
		key := filepath.ToSlash(filepath.Join(rec.Collection().Id, rec.Id, filename))
		fsys, err := app.NewFilesystem()
		if err != nil {
			storageBridgeLog.Warn("storage-bridge: cannot open filesystem", "err", err.Error())
			return
		}
		r, err := fsys.GetReader(key)
		if err != nil {
			storageBridgeLog.Warn("storage-bridge: cannot read local file", "key", key, "err", err.Error())
			return
		}
		url, err := store.Put(ctx, key, r, mimeForFile(filename))
		_ = r.Close()
		if err != nil {
			storageBridgeLog.Warn("storage-bridge: upload failed", "key", key, "err", err.Error())
			return
		}
		urls = append(urls, url)
	}
	if len(urls) == 0 {
		return
	}
	// Store as JSON array so we can carry multiple files (samples
	// allows up to 50). For the single-file case, this still
	// round-trips through joinURLs/splitURLs.
	remote := joinURLs(urls)
	rec.Set("remote_url", remote)
	if err := app.Save(rec); err != nil {
		storageBridgeLog.Warn("storage-bridge: save failed", "record", rec.Id, "err", err.Error())
	}
}

// deleteRemoteForRecord reads remote_url and deletes each URL
// from the configured backend. The local file is already removed
// by PocketBase by the time this hook fires.
func deleteRemoteForRecord(app core.App, rec *core.Record) {
	store, err := resolveStorage(app)
	if err != nil || store == nil {
		return
	}
	remote := rec.GetString("remote_url")
	if remote == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storageBridgeTimeout)
	defer cancel()
	for _, u := range splitURLs(remote) {
		key := keyFromURL(u)
		if key == "" {
			continue
		}
		if err := store.Delete(ctx, key); err != nil {
			storageBridgeLog.Warn("storage-bridge: delete failed", "key", key, "err", err.Error())
		}
	}
}

// resolveStorage returns the storage backend for the current
// instance. The Local backend is signalled by a nil storage with
// no error (callers can short-circuit).
func resolveStorage(app core.App) (storage.Storage, error) {
	storageBridgeCache.Lock()
	defer storageBridgeCache.Unlock()
	if storageBridgeCache.store != nil && time.Since(storageBridgeCache.loadedAt) < storageBridgeConfigCacheTTL {
		return storageBridgeCache.store, nil
	}
	if storageBridgeCache.store != nil {
		if c, ok := storageBridgeCache.store.(*storage.S3); ok {
			_ = c.Close()
		}
		storageBridgeCache.store = nil
	}

	cfg, err := readStorageConfig(app)
	if err != nil {
		return nil, err
	}
	if cfg.Backend == "" || cfg.Backend == storage.BackendLocal || cfg.Backend == storage.BackendDatabase {
		return nil, nil
	}
	store, err := storage.New(app.DataDir(), cfg)
	if err != nil {
		return nil, err
	}
	storageBridgeCache.store = store
	storageBridgeCache.loadedAt = time.Now()
	return store, nil
}
