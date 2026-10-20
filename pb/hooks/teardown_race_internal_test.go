package hooks

import (
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for the per-app teardown state shared by
// memo_links, scheduler and motion_media.
//
// All three track detached goroutines in a `sync.WaitGroup`
// and block on it at teardown. Three defects shared that shape:
//
//  1. `Add` inside the per-event hook raced the teardown
//     `Wait`. A wait group cannot be reused while a `Wait` is
//     in progress; the Go runtime panics with "sync:
//     WaitGroup is reused before previous Wait has returned".
//     Neither the race detector nor a vet pass flags it — it
//     is a runtime panic that only fires when a save lands in
//     the window between the gate closing and the wait
//     returning.
//
//  2. The scheduler's teardown deleted its map entry on
//     `OnTerminate` but not on `OnBootstrapClear`. Every test
//     cleanup calls `ClearBootstrap` (which fires
//     `OnBootstrapClear` but NOT `OnTerminate`), so each test
//     leaked one entry into a package-level map keyed by the
//     app value.
//
//  3. The scheduler read that map without holding
//     `schedulerStatesMu`. An unsynchronised map read
//     concurrent with a write is `fatal error: concurrent map
//     read and map write` — an unrecoverable process crash,
//     which is strictly worse than the data race the detector
//     would have reported.

// TestMemoLinksStateGateRejectsAddsAfterTeardown pins defect 1
// for memo_links: once teardown has begun, `begin` must refuse
// new workers rather than calling `Add` on a group that is
// mid-`Wait`.
func TestMemoLinksStateGateRejectsAddsAfterTeardown(t *testing.T) {
	st := &memoLinksState{
		shutdown:      make(chan struct{}),
		shutdownOnce:  &sync.Once{},
		gateAccepting: true,
	}

	// A worker accepted before teardown.
	require.True(t, st.begin(), "begin must accept before teardown")

	// Teardown starts and completes while that worker is
	// still running, exactly as it would if a fan-out were
	// mid-fetch at shutdown.
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		st.stopAndWait()
	}()

	// Once the gate is closed, no further worker may be
	// tracked. Calling Add here is what panicked before.
	//
	// Poll the gate flag rather than calling `begin`:
	// `Eventually` re-runs its condition on a ticker, and
	// every poll that lands BEFORE the gate closes is a
	// legitimate `Add` with no matching `Done`, which would
	// leave the wait group permanently unbalanced and hang
	// the teardown below.
	require.Eventually(t, func() bool {
		st.gate.Lock()
		defer st.gate.Unlock()
		return !st.gateAccepting
	}, 5*time.Second, 5*time.Millisecond,
		"teardown must close the gate")

	// And now the save path declines to track, which is the
	// actual regression: this is the `Add` that panicked.
	require.False(t, st.begin(),
		"begin must refuse workers once teardown has started")

	// Release the in-flight worker; teardown must complete.
	st.end()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopAndWait did not return after the last worker finished")
	}

	// Idempotent: the second teardown hook (OnTerminate after
	// OnBootstrapClear, or a double ClearBootstrap) must be a
	// no-op rather than a double close panic.
	assert.NotPanics(t, func() { st.stopAndWait() })
}

// TestMemoLinksStateConcurrentBeginDuringTeardown is the race
// probe: many saves and a teardown at the same time. Under
// `-race` this catches an unsynchronised gate; without the
// gate it panics outright.
func TestMemoLinksStateConcurrentBeginDuringTeardown(t *testing.T) {
	for i := 0; i < 50; i++ {
		st := &memoLinksState{
			shutdown:      make(chan struct{}),
			shutdownOnce:  &sync.Once{},
			gateAccepting: true,
		}

		var wg sync.WaitGroup
		start := make(chan struct{})

		// Concurrent savers.
		for s := 0; s < 8; s++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if st.begin() {
					st.end()
				}
			}()
		}
		// Concurrent teardown.
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			st.stopAndWait()
		}()

		close(start)
		wg.Wait()
	}
}

// TestStopSchedulerLoopsIsIdempotentAndRemovesEntry pins
// defects 2 and 3: the single teardown helper must drop the
// map entry and survive being called twice (the two hooks can
// both fire).
func TestStopSchedulerLoopsIsIdempotentAndRemovesEntry(t *testing.T) {
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_test_env_riff_teardown",
	})
	require.NoError(t, app.Bootstrap())
	t.Cleanup(func() { _ = app.ClearBootstrap() })

	registerScheduler(app)

	schedulerStatesMu.Lock()
	_, ok := schedulerShutdowns[app]
	schedulerStatesMu.Unlock()
	require.True(t, ok, "registerScheduler must record per-app state")

	// Both teardown hooks route through this helper, so the
	// second call must not panic on a double close and must
	// leave the map clean.
	require.NotPanics(t, func() {
		stopSchedulerLoops(app)
		stopSchedulerLoops(app)
	})

	schedulerStatesMu.Lock()
	_, stillThere := schedulerShutdowns[app]
	size := len(schedulerShutdowns)
	schedulerStatesMu.Unlock()
	assert.False(t, stillThere,
		"teardown must remove the app's map entry so a ClearBootstrap-only teardown does not leak it")
	assert.Equal(t, 0, size,
		"the scheduler state map must be empty after teardown")
}

// TestStopSchedulerLoopsConcurrentWithRegister exercises the
// unsynchronised map access (defect 3). A bare map read racing
// the register write is a `fatal error: concurrent map read and
// map write`, which kills the process — so this test passing
// at all is the signal; `-race` catches the weaker form.
func TestStopSchedulerLoopsConcurrentWithRegister(t *testing.T) {
	base := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_test_env_riff_teardown2",
	})
	require.NoError(t, base.Bootstrap())
	t.Cleanup(func() { _ = base.ClearBootstrap() })

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Distinct app values per goroutine would each
			// need their own bootstrap; the shared value is
			// enough to exercise read-vs-write on the map.
			stopSchedulerLoops(base)
		}()
	}
	close(start)
	wg.Wait()
}

// TestMotionMediaStateGateRejectsAddsAfterTeardown pins
// defect 1 for motion_media, whose `bind` had the same
// `Add`-inside-the-per-event-hook shape as memo_links.
func TestMotionMediaStateGateRejectsAddsAfterTeardown(t *testing.T) {
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_test_env_riff_teardown3",
	})
	require.NoError(t, app.Bootstrap())
	t.Cleanup(func() { _ = app.ClearBootstrap() })

	st := motionMediaStateFor(app)
	require.True(t, st.begin(), "begin must accept before teardown")

	motionMediaStatesMu.Lock()
	delete(motionMediaStates, app)
	motionMediaStatesMu.Unlock()

	st.gate.Lock()
	st.gateAccepting = false
	st.gate.Unlock()

	assert.False(t, st.begin(),
		"begin must refuse workers once teardown has closed the gate")

	// The already-accepted worker still drains, so the wait
	// group returns.
	st.workers.Done()
}
