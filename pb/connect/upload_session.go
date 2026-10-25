// Package connect's upload-session manager owns the in-memory
// state of every in-flight chunked attachment upload. The
// state is keyed on an opaque upload id returned to the client
// from the first call; subsequent calls reference the same id.
//
// The manager is a package-level singleton, matching the
// pattern set by the storage-bridge and webhook-drain caches:
// the OnServe hook in connect.go calls installUploadManager,
// which starts a sweeper goroutine that reaps sessions that
// have been idle for more than uploadSessionTTL (30 minutes).
// Sessions that have been finalised (finishWrite) keep their
// record id so a retried call returns the same attachment.
//
// The sweeper also closes the temporary file and removes the
// staging path on disk; the lifecycle is:
//
//	spec         → stage file opened, session inserted
//	writeOffset  → bytes appended at offset
//	finishWrite  → record created in `attachments`, file
//	                uploaded to PocketBase's filesystem, stage
//	                file deleted
//	expiry       → stage file deleted, session removed
//
// Sessions do not survive a server restart. The manager is
// in-memory by design — the proto spec calls out "do not
// survive a restart" as a property of the upload protocol
// (see proto/api/v1/attachment_service.proto).
package connect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/random"
)

// uploadSessionTTL is the inactivity window after which a
// staged file is reaped. The proto spec says "expire after
// 30 minutes of inactivity"; the value is the only place
// this number lives, so a future change to the policy is a
// one-liner here.
const uploadSessionTTL = 30 * time.Minute

// uploadSessionSweepInterval is how often the sweeper
// goroutine wakes up. Five minutes is a fine trade-off
// between promptness on the 30-minute TTL and the cost of
// walking the in-memory map; the map is keyed by random
// ids and is expected to hold at most a few dozen rows in
// normal use.
const uploadSessionSweepInterval = 5 * time.Minute

// uploadMaxChunkSize is the upper bound on the number of
// bytes a single UploadAttachment writeOffset call may
// carry. The SPA caps its own request size at 2 MiB
// (DEFAULT_CHUNK_SIZE in web/src/components/MemoEditor/services/uploadService.ts)
// and treats the server's max_chunk_size as a ceiling, so
// the two values are coordinated here: a 2 MiB server cap
// keeps the request under Connect's default 4 MiB envelope
// limit while still letting a 100 MB upload finish in ~50
// round-trips on a healthy network.
const uploadMaxChunkSize = 2 * 1024 * 1024

// uploadMaxUnfinishedPerUser caps the number of staged
// uploads a single user may have in flight at once. The
// SPA's uploadService uses MAX_CONCURRENT_UPLOADS = 4
// (plus a few retries on the same upload_id), so 8 leaves
// comfortable headroom for the parallel worker pool.
const uploadMaxUnfinishedPerUser = 8

// uploadMaxTotalSize is the per-file hard ceiling, set
// comfortably above the unified `attachments` collection's
// blob FileField MaxSize. A request above this is rejected
// at the spec stage so a client cannot even stage a 4 GB
// blob to disk. The number is a defence in depth:
// PocketBase's own FileField validator is the authoritative
// size check at finalisation.
const uploadMaxTotalSize int64 = 64 * 1024 * 1024

// uploadStageDir is the on-disk directory where staged
// files are written. The directory is created lazily by
// the manager and removed on shutdown.
const uploadStageDir = "uploads"

// uploadSession is the in-memory state for one in-flight
// upload. A session is created on spec, mutated by
// writeOffset / finishWrite, and reaped on expiry. The
// destination collection is now always the unified
// `attachments` collection, so the field is no longer
// needed on the session.
// writeOffset, and closed on finishWrite or expiry.
type uploadSession struct {
	id              string
	userID          string
	filename        string
	mimeType        string
	memoID          string
	motionMediaJSON string
	mediaMetadata   string
	expectedSize    int64
	bytesReceived   int64
	tmpPath         string
	tmpFile         *os.File
	createdAt       time.Time
	lastTouchedAt   time.Time
	recordID        string // set after finishWrite; empty while in flight
	attachmentName  string // set after finishWrite
}

// uploadManager owns the map of in-flight sessions and the
// sweeper goroutine. The zero value is not usable; the
// package-level uploadManagerInstance is wired up by
// installUploadManager at OnServe.
type uploadManager struct {
	mu       sync.Mutex
	sessions map[string]*uploadSession
	dir      string
	stopCh   chan struct{}
	stopOnce sync.Once
}

var (
	uploadManagerOnce sync.Once
	uploadManagerInst *uploadManager
)

// uploadManagerGet returns the package-level upload manager.
// The first call lazily initialises the map; the sweeper
// goroutine is started separately by installUploadManager
// so the unit tests can drive the manager without spinning
// up the real OnServe lifecycle.
func uploadManagerGet() *uploadManager {
	uploadManagerOnce.Do(func() {
		uploadManagerInst = &uploadManager{
			sessions: map[string]*uploadSession{},
			dir:      uploadStageDir,
		}
	})
	return uploadManagerInst
}

// installUploadManager starts the sweeper goroutine that
// reaps expired sessions. The directory layout matches
// PocketBase's data dir (`<DataDir>/uploads`), so a unit
// test's temp data dir is honoured without extra wiring
// and a production server lands its stages under the
// existing data dir.
func installUploadManager(app core.App) {
	// The state lives on the app's own store, so it is per app by
	// construction and dies with the app instance.
	state := uploadSweepStateFor(app)
	mgr := uploadManagerGet()
	mgr.setDir(filepath.Join(app.DataDir(), "uploads"))
	// OnTerminate stops the sweeper so a unit test's app
	// teardown does not leak a goroutine waiting on the
	// ticker.
	stopCh := make(chan struct{})
	mgr.stopCh = stopCh
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		mgr.stopOnce.Do(func() { close(stopCh) })
		// Wait for the sweep loop to unwind. The stop
		// signal makes it return at its next tick, but
		// without the wait the app can tear its data
		// directory down while the sweep is still
		// removing a staged upload from it.
		state.workers.Wait()
		return e.Next()
	})
	// The same wait on `OnBootstrapClear`, which is the
	// hook a test's `ClearBootstrap` cleanup triggers
	// (`OnTerminate` does NOT fire there).
	//
	// The stop signal is sent FIRST: the wait blocks
	// until the sweep loop returns, and the loop only
	// returns at its next tick — which it reaches only
	// after the stop channel is closed. Waiting without
	// closing it would deadlock the teardown.
	app.OnBootstrapClear().BindFunc(func(e *core.BootstrapEvent) error {
		mgr.stopOnce.Do(func() { close(stopCh) })
		state.workers.Wait()
		return e.Next()
	})
	state.workers.Add(1)
	go func() {
		defer state.workers.Done()
		mgr.sweepLoop(stopCh)
	}()
}

// uploadSweepState holds the per-app sweep state.
//
// The wait group must be PER APP, not package-level: a
// package-level `sync.WaitGroup` cannot be waited on
// twice concurrently, and two apps tearing down at the
// same time would panic with "sync: WaitGroup is
// reused before previous Wait has returned".
type uploadSweepState struct {
	workers sync.WaitGroup
}

// uploadSweepStateStoreKey is the app.Store() key the per-app sweep
// state lives under.
const uploadSweepStateStoreKey = "riff.upload_sweep_state"

// uploadSweepStateFor returns the per-app sweep state, creating it on
// first use.
//
// It used to be a package-level `map[core.App]*uploadSweepState`
// guarded by its own mutex. The app's own store replaces it: the store
// is already per app and concurrency-safe, so both the map and its
// mutex were redundant — and the map's entry was never deleted, so it
// outlived the app it belonged to.
func uploadSweepStateFor(app core.App) *uploadSweepState {
	raw := app.Store().GetOrSet(uploadSweepStateStoreKey, func() any {
		return &uploadSweepState{}
	})
	state, ok := raw.(*uploadSweepState)
	if !ok {
		// Another component claimed the key. Fall back to a fresh
		// state rather than panicking on the type assertion.
		return &uploadSweepState{}
	}
	return state
}

// uploadManagerSetDir overrides the staging directory.
// Intended for tests; the manager must have no live
// sessions when this is called.
func (m *uploadManager) setDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dir = dir
}

// sweepLoop runs until stopCh is closed. Every
// uploadSessionSweepInterval it walks the map, deletes any
// session whose lastTouchedAt is older than
// uploadSessionTTL, and closes the underlying file
// handle. A session that has already been finalised (has a
// recordID) is also subject to reaping once the
// 30-minute idle window elapses, because the response was
// already sent and the staged file no longer exists.
func (m *uploadManager) sweepLoop(stopCh chan struct{}) {
	ticker := time.NewTicker(uploadSessionSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			m.reapExpired()
		}
	}
}

// reapExpired removes idle sessions. Called from the
// sweeper loop; safe to call from tests directly.
func (m *uploadManager) reapExpired() {
	cutoff := time.Now().Add(-uploadSessionTTL)
	var stale []*uploadSession
	m.mu.Lock()
	for id, s := range m.sessions {
		if s.lastTouchedAt.Before(cutoff) {
			stale = append(stale, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range stale {
		s.closeAndCleanup()
	}
}

// closeAndCleanup closes the open file handle and removes
// the staged file from disk. It is a no-op for sessions
// that never opened a file (shouldn't happen — the spec
// always opens one — but defensive against test fixtures).
func (s *uploadSession) closeAndCleanup() {
	if s.tmpFile != nil {
		_ = s.tmpFile.Close()
		s.tmpFile = nil
	}
	if s.tmpPath != "" {
		_ = os.Remove(s.tmpPath)
	}
}

// countUnfinishedForUser returns the number of sessions
// for the given user that have not yet been finalised. A
// finalised session keeps its entry in the map until it
// expires, but does not count against the per-user cap.
func (m *uploadManager) countUnfinishedForUser(userID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sessions {
		if s.userID == userID && s.recordID == "" {
			n++
		}
	}
	return n
}

// create allocates a new session and returns it. The
// caller is responsible for populating the rest of the
// fields and calling insert to publish the session. The
// staging file is opened here so a failure to obtain a
// temp file surfaces before the session enters the map.
func (m *uploadManager) create(userID string) (*uploadSession, error) {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create stage dir: %w", err)
	}
	f, err := os.CreateTemp(m.dir, "riff-upload-*.part")
	if err != nil {
		return nil, fmt.Errorf("create stage file: %w", err)
	}
	id, err := random.String(24)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("generate upload id: %w", err)
	}
	now := time.Now()
	return &uploadSession{
		id:            id,
		userID:        userID,
		tmpPath:       f.Name(),
		tmpFile:       f,
		createdAt:     now,
		lastTouchedAt: now,
	}, nil
}

// insert publishes a freshly-created session into the map.
func (m *uploadManager) insert(s *uploadSession) {
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
}

// get returns the session for id, or (nil, false) when no
// such session exists. Cross-user lookups are not
// enforced here; the handler compares the session's userID
// to the caller and returns PermissionDenied on mismatch.
func (m *uploadManager) get(id string) (*uploadSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// touch updates lastTouchedAt so a session that is being
// actively written does not get reaped mid-flight.
func (s *uploadSession) touch() {
	s.lastTouchedAt = time.Now()
}

// abort removes the session from the map and deletes the
// staged file. The handler uses this when the spec
// validation fails after the session was created (e.g. the
// caller is over the per-user cap).
func (m *uploadManager) abort(id string) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if ok {
		s.closeAndCleanup()
	}
}

// errUploadOversize is returned by the spec handler when a
// client tries to stage a file larger than uploadMaxTotalSize.
// The proto's total_size is checked at spec time so a hostile
// client cannot even start writing bytes to disk.
var errUploadOversize = errors.New("upload exceeds maximum size")

// dirForFilenames builds the PocketBase filesystem key for
// the staged file: <collectionId>/<recordId>/<filename>.
// The path is built with filepath.Join and converted to a
// forward-slash form because the PB filesystem treats
// forward slashes as the canonical separator on every
// backend (local, S3).
func dirForFilenames(collectionID, recordID, filename string) string {
	return filepath.ToSlash(filepath.Join(collectionID, recordID, filename))
}
