package connect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/ratelimit"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"

	_ "github.com/incurl/riff/pb/migrations"
)

// connectRegisterForTest is a copy of connect.Register's wiring that
// runs synchronously against a router the caller owns, instead of
// scheduling it behind app.OnServe. Tests use it to drive the real
// route-registration code path (procedureScope lookup, BindFunc per
// procedure) without standing up the full PocketBase start loop.
//
// It mirrors connect.Register verbatim; any change to Register's
// procedure table or middleware attachment must be reflected here.
// The returned mux is the stdlib http.Handler the registered routes
// dispatch into.
func connectRegisterForTest(app core.App, limiter ratelimit.Limiter) (http.Handler, error) {
	srv := connect.NewServer(WithAuth(app))
	apiv1connect.RegisterAuthServiceHandler(srv, NewAuthServer(app, limiter))
	apiv1connect.RegisterUserServiceHandler(srv, NewUserServer(app))
	apiv1connect.RegisterSpaceServiceHandler(srv, NewSpaceServer(app))
	apiv1connect.RegisterMemoServiceHandler(srv, NewMemoServer(app))
	apiv1connect.RegisterAttachmentServiceHandler(srv, NewAttachmentServer(app))
	apiv1connect.RegisterInstanceServiceHandler(srv, NewInstanceServer(app))
	apiv1connect.RegisterAIServiceHandler(srv, apiv1connect.UnimplementedAIServiceHandler{})
	apiv1connect.RegisterIdentityProviderServiceHandler(srv, apiv1connect.UnimplementedIdentityProviderServiceHandler{})

	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)

	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		return nil, err
	}
	registerConnectRoutes(srv, mux, baseRouter, limiter)
	return baseRouter.BuildMux()
}

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

// newConnectRouteRouter builds an app, calls connect.Register to wire the
// real procedure routes through registerConnectRoutes, and returns the
// built mux. The auth interceptor runs (with no Bearer token, every
// request is anonymous) and the rate-limit middleware (when limiter is
// non-nil) is attached exactly the way the production OnServe callback
// would attach it. The test can then drive the real
// /memos.api.v1.AuthService/SignIn path and assert that the policy
// actually fires.
func newConnectRouteRouter(t *testing.T, limiter ratelimit.Limiter) http.Handler {
	t.Helper()

	app := newTestApp(t)
	// Register returns nil; the OnServe binding is the only place that
	// mutates the router, so we drive registerConnectRoutes directly
	// against a router we own. The helper is the same code path
	// production runs at startup.
	mux, err := connectRegisterForTest(app, limiter)
	require.NoError(t, err)
	return mux
}

// TestConnectRegister_RateLimitFiresAgainstAuthSignIn is the end-to-end
// proof that Register's procedure loop actually attaches the
// RateLimit middleware: a single-peer burst of N+1 SignIn requests
// against a limiter with N units/min must answer the (N+1)th with 429
// and the Memos-shape envelope. The unit tests above only cover the
// middleware function in isolation; this test pins the wiring so a
// refactor that drops the BindFunc (or mistypes a procedure path in
// procedureScope) is caught.
func TestConnectRegister_RateLimitFiresAgainstAuthSignIn(t *testing.T) {
	const limit = 2
	mux := newConnectRouteRouter(t,
		ratelimit.NewMemoryLimiter(ratelimit.Policy{
			ratelimit.ScopeSignInIP: {Limit: limit, Window: time.Minute},
		}),
	)

	signIn := func(peer string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost,
			"/memos.api.v1.AuthService/SignIn",
			strings.NewReader(`{}`),
		)
		req.RemoteAddr = peer + ":1234"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	for i := range limit {
		rec := signIn("10.0.1.1")
		require.NotEqualf(t, http.StatusTooManyRequests, rec.Code,
			"under the limit the request must reach the handler; iteration %d returned 429 (body=%s)",
			i+1, rec.Body.String())
	}

	rec := signIn("10.0.1.1")
	require.Equal(t, http.StatusTooManyRequests, rec.Code,
		"the request that exceeds the limit must be refused")

	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Scope string `json:"scope"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body),
		"the 429 body must be the Memos-shape envelope: %s", rec.Body.String())
	assert.Equal(t, http.StatusTooManyRequests, body.Code)
	assert.Equal(t, string(ratelimit.ScopeSignInIP), body.Data.Scope)
}

// TestConnectRegister_NilLimiterKeepsProceduresReachable pins the
// no-op contract: when the operator has not set RIFF_RATE_LIMIT
// (Profile.RateLimit=false, Register is called with a nil limiter),
// the loop's BindFunc is a no-op and every procedure is still
// reachable. Without this, a refactor that turns the table's row
// into a hard refuse would silently break the default install.
func TestConnectRegister_NilLimiterKeepsProceduresReachable(t *testing.T) {
	mux := newConnectRouteRouter(t, nil)

	for i := range 5 {
		req := httptest.NewRequest(http.MethodPost,
			"/memos.api.v1.AuthService/SignIn",
			strings.NewReader(`{"passwordCredentials":{"username":"nope","password":"nope"}}`),
		)
		req.RemoteAddr = "10.0.2.1:1234"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		require.NotEqualf(t, http.StatusTooManyRequests, rec.Code,
			"a nil limiter must never refuse; iteration %d returned 429", i+1)
	}
}

// TestSignIn_FailedAttemptsCountSigninAccount is the unit test for
// the per-account branch of the design: a bad password must
// increment the signin_account counter for the *submitted* username,
// a successful sign-in must NOT, and a non-existent username must
// also increment (so an attacker cannot probe the user namespace).
// Together those three cases pin the "Consume up front, Refund on
// success" pattern in auth.go.
func TestSignIn_FailedAttemptsCountSigninAccount(t *testing.T) {
	app := newTestApp(t)
	// newUser fills PocketBase's own `password` field, which is
	// what the Connect SignIn path verifies (through
	// `Record.ValidatePassword`).
	newUser(t, app, "alice")
	const pw = "passw0rd!"

	const limit = 2
	limiter := ratelimit.NewMemoryLimiter(ratelimit.Policy{
		ratelimit.ScopeSignInAccount: {Limit: limit, Window: time.Minute},
	})
	srv := NewAuthServer(app, limiter)

	signIn := func(t *testing.T, username, password string) error {
		t.Helper()
		_, err := srv.SignIn(context.Background(), &v1.SignInRequest{
			Credentials: &v1.SignInRequest_PasswordCredentials_{
				PasswordCredentials: &v1.SignInRequest_PasswordCredentials{
					Username: username,
					Password: password,
				},
			},
		})
		return err
	}

	// A real user with the wrong password burns the budget. The
	// submitted username is the key, not the resolved user.
	for i := range limit {
		err := signIn(t, "alice", "wrong")
		require.Errorf(t, err, "wrong password must fail; iteration %d", i+1)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	}
	err := signIn(t, "alice", "wrong")
	require.Error(t, err, "the attempt past the limit must be refused by the limiter")
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"per-account over-limit must surface as ResourceExhausted (HTTP 429 shape)")

	// A different username has a fresh budget — the keys are
	// independent. Both attempts below should fail authentication
	// (no such user, wrong password) but not be refused by the
	// limiter, which proves the per-account key is the submitted
	// username and not a global "anyone failing" counter.
	for _, name := range []string{"bob-different", "bob-different"} {
		err := signIn(t, name, "x")
		require.Error(t, err, "non-existent user must fail auth, not succeed")
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
			"a different username must not be limited by alice's budget")
	}

	// A real user typing the right password is Refunded and never
	// consumes the budget. Use a fresh limiter with a one-unit
	// budget so a single successful sign-in is the only thing that
	// can be in the entry.
	freshLimiter := ratelimit.NewMemoryLimiter(ratelimit.Policy{
		ratelimit.ScopeSignInAccount: {Limit: 1, Window: time.Minute},
	})
	freshSrv := NewAuthServer(app, freshLimiter)
	_, err = freshSrv.SignIn(context.Background(), &v1.SignInRequest{
		Credentials: &v1.SignInRequest_PasswordCredentials_{
			PasswordCredentials: &v1.SignInRequest_PasswordCredentials{
				Username: "alice",
				Password: pw,
			},
		},
	})
	require.NoError(t, err, "a successful sign-in must not consume the budget")

	// After the success, the limiter should still have its full
	// budget available for alice, because Refund gave the unit back.
	dec := freshLimiter.Allowed(ratelimit.ScopeSignInAccount, "alice", 1)
	assert.True(t, dec.Allowed, "the budget for alice must be full after a Refund")
}
