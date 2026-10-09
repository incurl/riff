// RateLimit is the per-route middleware this file exposes; callers
// attach it via Route.BindFunc(connect.RateLimit(limiter, scope,
// keyFunc)) and it consumes one unit per request, returning a 429
// with the Memos-shape envelope when the policy refuses. A nil
// limiter is a no-op so operators who have not set RIFF_RATE_LIMIT
// keep the previous unbounded behaviour.
//
// procedureScope maps a Connect procedure path to the ratelimit.Scope
// the registration loop in connect.go should attach at the route
// level. The table is opt-in: procedures with no row fall through to
// the catch-all budgets the authorizer (a future wave) will layer on
// top. Procedures whose key is in the request body (signin_account,
// keyed on the submitted username) call Consume / Refund from the
// handler instead of getting a row here. The package doc comment
// lives in connect.go.
package connect

import (
	"math"
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/ratelimit"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
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

// procedureScope is the *opt-in* override table: procedures that need a
// tighter rule than the catch-all ScopeAuthenticated/ScopeAnonymous
// defaults. Anything not in the table is treated as authenticated
// (anonymous for procedures that may run without a Bearer token) by the
// registration loop in connect.go, so the table only needs the rows that
// deserve their own row in the policy.
//
// Scopes are assigned per docs/design/api-abuse-controls.md. The
// signin_account counter for failed password attempts is intentionally
// not here — it depends on the submitted username and is consumed inside
// the SignIn handler (auth.go) so a successful sign-in refunds it.
var procedureScope = map[string]ratelimit.Scope{
	apiv1connect.AuthServiceSignInProcedure:                             ratelimit.ScopeSignInIP,
	apiv1connect.UserServiceCreateUserProcedure:                         ratelimit.ScopeSignupIP,
	apiv1connect.MemoServiceCreateMemoProcedure:                         ratelimit.ScopeWriteUser,
	apiv1connect.MemoServiceUpdateMemoProcedure:                         ratelimit.ScopeWriteUser,
	apiv1connect.MemoServiceDeleteMemoProcedure:                         ratelimit.ScopeWriteUser,
	apiv1connect.MemoServiceUpsertMemoReactionProcedure:                 ratelimit.ScopeWriteUser,
	apiv1connect.MemoServiceDeleteMemoReactionProcedure:                 ratelimit.ScopeWriteUser,
	apiv1connect.MemoServiceCreateMemoCommentProcedure:                  ratelimit.ScopeWriteUser,
	apiv1connect.MemoServiceSetMemoAttachmentsProcedure:                 ratelimit.ScopeWriteUser,
	apiv1connect.MemoServiceSetMemoRelationsProcedure:                   ratelimit.ScopeWriteUser,
	apiv1connect.MemoServiceCreateMemoShareProcedure:                    ratelimit.ScopeWriteUser,
	apiv1connect.MemoServiceDeleteMemoShareProcedure:                    ratelimit.ScopeWriteUser,
	apiv1connect.AttachmentServiceCreateAttachmentProcedure:             ratelimit.ScopeUploadUser,
	apiv1connect.AttachmentServiceUploadAttachmentProcedure:             ratelimit.ScopeUploadUser,
	apiv1connect.AttachmentServiceUpdateAttachmentProcedure:             ratelimit.ScopeUploadUser,
	apiv1connect.AttachmentServiceDeleteAttachmentProcedure:             ratelimit.ScopeUploadUser,
	apiv1connect.AttachmentServiceBatchDeleteAttachmentsProcedure:       ratelimit.ScopeUploadUser,
	apiv1connect.MemoServiceGetLinkMetadataProcedure:                    ratelimit.ScopeLinkMetadata,
	apiv1connect.MemoServiceBatchGetLinkMetadataProcedure:               ratelimit.ScopeLinkMetadata,
	apiv1connect.AIServiceTranscribeProcedure:                           ratelimit.ScopeTranscribeUser,
	apiv1connect.UserServiceExportMemosProcedure:                        ratelimit.ScopeArchiveUser,
	apiv1connect.UserServiceImportMemosProcedure:                        ratelimit.ScopeArchiveUser,
	apiv1connect.UserServiceCreatePersonalAccessTokenProcedure:          ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceDeletePersonalAccessTokenProcedure:          ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceCreateLinkedIdentityProcedure:               ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceDeleteLinkedIdentityProcedure:               ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceCreateMemoViewProcedure:                     ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceUpdateMemoViewProcedure:                     ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceDeleteMemoViewProcedure:                     ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceUpdateUserNotificationProcedure:             ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceDeleteUserNotificationProcedure:             ratelimit.ScopeWriteUser,
	apiv1connect.SpaceServiceCreateSpaceProcedure:                       ratelimit.ScopeWriteUser,
	apiv1connect.SpaceServiceUpdateSpaceProcedure:                       ratelimit.ScopeWriteUser,
	apiv1connect.SpaceServiceDeleteSpaceProcedure:                       ratelimit.ScopeWriteUser,
	apiv1connect.SpaceServiceCreateSpaceInvitationProcedure:             ratelimit.ScopeWriteUser,
	apiv1connect.SpaceServiceUpdateSpaceMemberProcedure:                 ratelimit.ScopeWriteUser,
	apiv1connect.SpaceServiceDeleteSpaceMemberProcedure:                 ratelimit.ScopeWriteUser,
	apiv1connect.SpaceServiceAcceptSpaceInvitationProcedure:             ratelimit.ScopeWriteUser,
	apiv1connect.SpaceServiceDeclineSpaceInvitationProcedure:            ratelimit.ScopeWriteUser,
	apiv1connect.SpaceServiceDeleteSpaceInvitationProcedure:             ratelimit.ScopeWriteUser,
	apiv1connect.InstanceServiceUpdateInstanceSettingProcedure:          ratelimit.ScopeWriteUser,
	apiv1connect.InstanceServiceTestInstanceEmailSettingProcedure:       ratelimit.ScopeWriteUser,
	apiv1connect.IdentityProviderServiceCreateIdentityProviderProcedure: ratelimit.ScopeWriteUser,
	apiv1connect.IdentityProviderServiceUpdateIdentityProviderProcedure: ratelimit.ScopeWriteUser,
	apiv1connect.IdentityProviderServiceDeleteIdentityProviderProcedure: ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceCreateUserWebhookProcedure:                  ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceUpdateUserWebhookProcedure:                  ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceDeleteUserWebhookProcedure:                  ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceGetUserWebhookSigningSecretProcedure:        ratelimit.ScopeWriteUser,
	apiv1connect.UserServiceUpdateUserSettingProcedure:                  ratelimit.ScopeWriteUser,
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
