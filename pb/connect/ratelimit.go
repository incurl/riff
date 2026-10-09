package connect

import (
	"math"
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/ratelimit"
)

// KeyFunc chooses the rate-limit bucket for a request. The returned key is
// passed straight to ratelimit.Limiter.Consume; an empty key makes the
// in-memory limiter treat the request as unlimited.
//
// Common choices are the resolved client address (sign-in, sign-up,
// anonymous fetches) and the authenticated user id (uploads,
// transcriptions). The caller decides; the middleware does not infer an
// identity, because at the point it runs the request has not yet been
// authenticated.
type KeyFunc func(e *core.RequestEvent) string

// IPKey buckets by the resolved client address. It uses
// core.RequestEvent.RealIP, which already honours the operator-configured
// trusted-proxy headers, so an instance reached through a reverse proxy
// sees the real client and not the proxy.
func IPKey(e *core.RequestEvent) string {
	return e.RealIP()
}

// RateLimit returns a PocketBase-compatible middleware that consults
// limiter for scope before letting the request through. The default
// policy for scope applies when limiter is a *ratelimit.MemoryLimiter;
// an arbitrary Limiter can be used by callers that wire their own
// backend.
//
// When limiter is nil the middleware is a no-op, so an operator that
// has not configured a policy keeps the previous behaviour. This is the
// property the sequencing of feature work relies on: the middleware can
// be attached to a route before any feature requires it, and nothing
// changes for callers that pass nil.
//
// A refused request is answered with 429, the Memos-shape envelope
//
//	{"code": 429, "message": "rate limited", "data": {"scope": "...", "retry_after_ms": ...}}
//
// and a Retry-After header in seconds (RFC 7231 §7.1.3) plus a
// Riff-RateLimit-Remaining header so clients can back off without
// re-parsing the body.
//
// Usage:
//
//	limiter := ratelimit.NewMemoryLimiter(ratelimit.DefaultPolicy())
//	e.Router.POST("/api/po33/auth/signin", signInHandler).
//	    BindFunc(connect.RateLimit(limiter, ratelimit.ScopeSignInIP, connect.IPKey))
func RateLimit(limiter ratelimit.Limiter, scope ratelimit.Scope, key KeyFunc) func(e *core.RequestEvent) error {
	if key == nil {
		key = IPKey
	}
	return func(e *core.RequestEvent) error {
		if limiter == nil {
			return e.Next()
		}
		decision := limiter.Consume(scope, key(e), 1)
		if decision.Allowed {
			return e.Next()
		}
		retrySeconds := int(math.Ceil(decision.RetryAfter.Seconds()))
		if retrySeconds < 1 {
			retrySeconds = 1
		}
		e.Response.Header().Set("Retry-After", strconv.Itoa(retrySeconds))
		e.Response.Header().Set("Riff-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
		return e.JSON(http.StatusTooManyRequests, map[string]any{
			"code":    http.StatusTooManyRequests,
			"message": "rate limited",
			"data": map[string]any{
				"scope":          string(scope),
				"retry_after_ms": decision.RetryAfter.Milliseconds(),
			},
		})
	}
}
