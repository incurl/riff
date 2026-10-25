// Package connect's import-session manager owns the
// in-memory state of every in-flight Memos import. The
// state is keyed on an opaque upload id returned to the
// client from the first call; subsequent writeOffset
// calls reference the same id. The shape mirrors the
// upload manager (see upload_session.go) because the
// proto spec for ImportMemos and the proto spec for
// UploadAttachment are deliberately parallel:
//
//	spec        → stage file opened, session inserted
//	writeOffset → bytes appended at offset
//	finishWrite → archive validated, plan or report
//	               returned, stage file kept or removed
//	expiry      → stage file deleted, session removed
//
// The manager is a package-level singleton, matching the
// pattern set by the upload manager, the storage-bridge
// cache, and the webhook-drain cache. The OnServe hook
// in connect.go calls installImportManager, which starts
// a sweeper goroutine that reaps sessions that have
// been idle for more than importSessionTTL (30 minutes).
//
// Sessions do not survive a server restart. The manager
// is in-memory by design.
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

// importSessionTTL is the inactivity window after which
// a staged import archive is reaped.
const importSessionTTL = 30 * time.Minute

// importSessionSweepInterval is how often the sweeper
// goroutine wakes up.
const importSessionSweepInterval = 5 * time.Minute

// importMaxChunkSize is the upper bound on the number of
// bytes a single ImportMemos writeOffset call may carry.
// Matches the upload manager so the SPA uses the same
// chunk-size value for both protocols.
const importMaxChunkSize = 2 * 1024 * 1024

// importMaxUnfinishedPerUser caps the number of staged
// imports a single user may have in flight at once.
const importMaxUnfinishedPerUser = 2

// importMaxTotalSize is the per-archive hard ceiling.
const importMaxTotalSize int64 = 256 * 1024 * 1024

// importStageDir is the on-disk directory where staged
// archives are written.
const importStageDir = "imports"

// importSessionState tracks the lifecycle of a single
// import.
type importSessionState int

const (
	// importSessionActive is the in-flight state.
	importSessionActive importSessionState = iota
	// importSessionValidated means finishWrite with
	// validate_only has run and the plan has been
	// returned.
	importSessionValidated
	// importSessionImported means the import has
	// completed.
	importSessionImported
	// importSessionAbandoned means finishWrite failed
	// and the archive is no longer valid.
	importSessionAbandoned
)

// importSession is the in-memory state for one in-flight
// import.
type importSession struct {
	id            string
	userID        string
	username      string
	expectedSize  int64
	bytesReceived int64
	tmpPath       string
	tmpFile       *os.File
	createdAt     time.Time
	lastTouchedAt time.Time
	state         importSessionState

	// planJSON is the protojson encoding of the most
	// recent MemoImportPlan.
	planJSON string

	// reportJSON is the protojson encoding of the most
	// recent MemoImportReport.
	reportJSON string
}

// importManager owns the map of in-flight import
// sessions and the sweeper goroutine.
type importManager struct {
	mu       sync.Mutex
	sessions map[string]*importSession
	dir      string
	stopCh   chan struct{}
	stopOnce sync.Once
}

var (
	importManagerOnce sync.Once
	importManagerInst *importManager
)

// importManagerGet returns the package-level import
// manager.
func importManagerGet() *importManager {
	importManagerOnce.Do(func() {
		importManagerInst = &importManager{
			sessions: map[string]*importSession{},
			dir:      importStageDir,
		}
	})
	return importManagerInst
}

// installImportManager starts the sweeper goroutine.
func installImportManager(app core.App) {
	// The state lives on the app's own store, so it is per app by
	// construction and dies with the app instance.
	state := importSweepStateFor(app)
	mgr := importManagerGet()
	mgr.setDir(filepath.Join(app.DataDir(), "imports"))
	stopCh := make(chan struct{})
	mgr.stopCh = stopCh
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		mgr.stopOnce.Do(func() { close(stopCh) })
		// Wait for the sweep loop to unwind. The stop
		// signal makes it return at its next tick, but
		// without the wait the app can tear its data
		// directory down while the sweep is still
		// removing a staged file from it.
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

// importSweepStates holds the per-app sweep state.
//
// The wait group must be PER APP, not package-level: a
// package-level `sync.WaitGroup` cannot be waited on
// twice concurrently, and two apps tearing down at the
// same time would panic with "sync: WaitGroup is
// reused before previous Wait has returned".
type importSweepState struct {
	workers sync.WaitGroup
}

// importSweepStateStoreKey is the app.Store() key the per-app sweep
// state lives under.
const importSweepStateStoreKey = "riff.import_sweep_state"

// importSweepStateFor returns the per-app sweep state, creating it on
// first use.
//
// It used to be a package-level `map[core.App]*importSweepState`
// guarded by its own mutex. The app's own store replaces it: the store
// is already per app and concurrency-safe, so both the map and its
// mutex were redundant — and the map's entry was never deleted, so it
// outlived the app it belonged to.
func importSweepStateFor(app core.App) *importSweepState {
	raw := app.Store().GetOrSet(importSweepStateStoreKey, func() any {
		return &importSweepState{}
	})
	state, ok := raw.(*importSweepState)
	if !ok {
		// Another component claimed the key. Fall back to a fresh
		// state rather than panicking on the type assertion.
		return &importSweepState{}
	}
	return state
}

// setDir overrides the staging directory.
func (m *importManager) setDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dir = dir
}

// sweepLoop reaps expired sessions.
func (m *importManager) sweepLoop(stopCh chan struct{}) {
	t := time.NewTicker(importSessionSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-t.C:
			m.reapExpired()
		}
	}
}

// reapExpired removes sessions whose lastTouchedAt is
// older than importSessionTTL.
func (m *importManager) reapExpired() {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		if now.Sub(s.lastTouchedAt) < importSessionTTL {
			continue
		}
		s.closeAndCleanup()
		delete(m.sessions, id)
	}
}

// closeAndCleanup closes the stage file and removes it
// from disk.
func (s *importSession) closeAndCleanup() {
	if s.tmpFile != nil {
		_ = s.tmpFile.Close()
		s.tmpFile = nil
	}
	if s.tmpPath != "" {
		_ = os.Remove(s.tmpPath)
	}
}

// countUnfinishedForUser returns the number of sessions
// in the active or validated state owned by userID.
func (m *importManager) countUnfinishedForUser(userID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sessions {
		if s.userID != userID {
			continue
		}
		if s.state == importSessionActive || s.state == importSessionValidated {
			n++
		}
	}
	return n
}

// create makes a new session, allocates a stage file,
// and inserts the row.
func (m *importManager) create(userID, username string, expectedSize int64) (*importSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, err := random.String(24)
	if err != nil {
		return nil, fmt.Errorf("mint upload id: %w", err)
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create stage dir: %w", err)
	}
	tmpPath := filepath.Join(m.dir, id+".zip")
	f, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open stage file: %w", err)
	}
	now := time.Now()
	s := &importSession{
		id:            id,
		userID:        userID,
		username:      username,
		expectedSize:  expectedSize,
		bytesReceived: 0,
		tmpPath:       tmpPath,
		tmpFile:       f,
		createdAt:     now,
		lastTouchedAt: now,
		state:         importSessionActive,
	}
	m.sessions[id] = s
	return s, nil
}

// get returns the session by id and touches it.
func (m *importManager) get(id string) (*importSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	s.lastTouchedAt = time.Now()
	return s, true
}

// abort closes the stage file, removes it from disk,
// and drops the session from the map.
func (m *importManager) abort(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return
	}
	s.closeAndCleanup()
	delete(m.sessions, id)
}

// finalize marks the session as imported and stores the
// report. The stage file is kept on disk so a
// subsequent validate-only call could, in principle,
// re-read it; today the design says the file is dropped
// after import, so the helper removes it.
func (m *importManager) finalize(id, reportJSON string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return errors.New("session not found")
	}
	s.state = importSessionImported
	s.reportJSON = reportJSON
	s.closeAndCleanup()
	return nil
}

// validate marks the session as validated and stores
// the plan. The stage file is kept on disk so a later
// finishing call can import it.
func (m *importManager) validate(id, planJSON string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return errors.New("session not found")
	}
	s.state = importSessionValidated
	s.planJSON = planJSON
	return nil
}

// abandon marks the session as abandoned. The stage
// file is removed and any later finishWrite returns
// FailedPrecondition.
func (m *importManager) abandon(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return
	}
	s.state = importSessionAbandoned
	s.closeAndCleanup()
}
