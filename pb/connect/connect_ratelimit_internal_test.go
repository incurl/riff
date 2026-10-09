package connect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/ratelimit"

	_ "github.com/incurl/riff/pb/migrations"
)

// newRateLimitedRouter builds an app with a single GET /probe route
// guarded by connect.RateLimit(limiter, scope, IPKey). It returns the
// mux that the test can drive with httptest.NewRecorder so each
// scenario is a black-box test of the real PocketBase router +
// middleware chain, not a unit test of the middleware function in
// isolation.
func newRateLimitedRouter(t *testing.T, limiter ratelimit.Limiter, scope ratelimit.Scope) http.Handler {
	t.Helper()

	app := newTestApp(t)
	baseRouter, err := apis.NewRouter(app)
	require.NoError(t, err)

	probe := func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, map[string]any{"ok": true})
	}

	baseRouter.GET("/probe", probe).
		BindFunc(RateLimit(limiter, scope, IPKey))

	mux, err := baseRouter.BuildMux()
	require.NoError(t, err)
	return mux
}

// rateLimitedGet issues a GET against mux from the given peer,
// returning the recorded response. The peer is set via RemoteAddr on
// the request; the middleware reads it through core.RequestEvent.RealIP.
func rateLimitedGet(t *testing.T, mux http.Handler, peer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.RemoteAddr = peer + ":1234"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestRateLimit_AllowsUntilLimitThenRefusesWithEnvelope is the happy
// path. With a policy of 3 per minute, the first three requests from
// one peer pass and the fourth gets a 429. The 429 body matches the
// Memos-shape envelope the React client expects, and the headers carry
// the data a polite client needs to back off.
func TestRateLimit_AllowsUntilLimitThenRefusesWithEnvelope(t *testing.T) {
	mux := newRateLimitedRouter(t,
		ratelimit.NewMemoryLimiter(ratelimit.Policy{
			ratelimit.ScopeSignInIP: {Limit: 3, Window: time.Minute},
		}),
		ratelimit.ScopeSignInIP,
	)

	for i := range 3 {
		rec := rateLimitedGet(t, mux, "10.0.0.1")
		require.Equalf(t, http.StatusOK, rec.Code,
			"request #%d (under the limit) must pass; body=%s", i+1, rec.Body.String())
	}

	rec := rateLimitedGet(t, mux, "10.0.0.1")
	require.Equal(t, http.StatusTooManyRequests, rec.Code,
		"the request that exceeds the limit must be refused")

	retryAfter := rec.Header().Get("Retry-After")
	require.NotEmpty(t, retryAfter, "Retry-After header must be set")
	seconds, err := strconv.Atoi(retryAfter)
	require.NoError(t, err, "Retry-After must be a whole number of seconds")
	require.GreaterOrEqual(t, seconds, 1)
	require.LessOrEqual(t, seconds, 60, "retry delay cannot exceed the window")

	require.Equal(t, "0", rec.Header().Get("Riff-RateLimit-Remaining"),
		"Remaining must be 0 at refusal time")

	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Scope        string `json:"scope"`
			RetryAfterMS int64  `json:"retry_after_ms"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body),
		"the 429 body must be a Memos-shape envelope: %s", rec.Body.String())
	assert.Equal(t, http.StatusTooManyRequests, body.Code)
	assert.Equal(t, "rate limited", body.Message)
	assert.Equal(t, string(ratelimit.ScopeSignInIP), body.Data.Scope)
	assert.Positive(t, body.Data.RetryAfterMS, "retry_after_ms must be positive")
}

// TestRateLimit_NilLimiterIsNoop proves the property the rest of the
// sequencing relies on: attaching RateLimit(nil, ...) must not change
// behaviour, so Phase 1 can land the middleware without the operator
// needing to configure a limiter.
func TestRateLimit_NilLimiterIsNoop(t *testing.T) {
	mux := newRateLimitedRouter(t, nil, ratelimit.ScopeSignInIP)

	for i := range 50 {
		rec := rateLimitedGet(t, mux, "10.0.0.2")
		require.Equalf(t, http.StatusOK, rec.Code,
			"a nil limiter must never refuse; iteration %d failed", i+1)
	}
}

// TestRateLimit_IndependentKeysPerPeer guards the per-key bucketing:
// two different client addresses must not share a budget, so a peer
// that has been throttled cannot affect a peer's allowed request
// count.
func TestRateLimit_IndependentKeysPerPeer(t *testing.T) {
	mux := newRateLimitedRouter(t,
		ratelimit.NewMemoryLimiter(ratelimit.Policy{
			ratelimit.ScopeSignInIP: {Limit: 2, Window: time.Minute},
		}),
		ratelimit.ScopeSignInIP,
	)

	// Peer A exhausts its budget.
	for range 2 {
		rec := rateLimitedGet(t, mux, "10.0.0.3")
		require.Equal(t, http.StatusOK, rec.Code)
	}
	refused := rateLimitedGet(t, mux, "10.0.0.3")
	require.Equal(t, http.StatusTooManyRequests, refused.Code)

	// Peer B starts fresh.
	for i := range 2 {
		rec := rateLimitedGet(t, mux, "10.0.0.4")
		require.Equalf(t, http.StatusOK, rec.Code,
			"a different peer must not share Peer A's budget; iteration %d failed", i+1)
	}
}

// TestRateLimit_UnknownScopeIsUnlimited is the property the in-memory
// limiter guarantees for every scope, and the middleware must respect
// it: a policy that omits the scope is equivalent to no limit.
func TestRateLimit_UnknownScopeIsUnlimited(t *testing.T) {
	mux := newRateLimitedRouter(t,
		// No rule for ScopeSignInIP — the limiter is configured but
		// does not know about the scope we ask about.
		ratelimit.NewMemoryLimiter(ratelimit.Policy{
			ratelimit.ScopeLinkMetadata: {Limit: 1, Window: time.Minute},
		}),
		ratelimit.ScopeSignInIP,
	)

	for i := range 20 {
		rec := rateLimitedGet(t, mux, "10.0.0.5")
		require.Equalf(t, http.StatusOK, rec.Code,
			"an unknown scope must let every request through; iteration %d failed", i+1)
	}
}
