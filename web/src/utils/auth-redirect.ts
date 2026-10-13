import { clearAccessToken } from "@/auth-state";
import { ROUTES } from "@/router/routes";
import { stripBasePath, withBasePath } from "./base-path";
import { buildAuthRoute, isPublicRoute } from "./redirect-safety";

// Re-export the pure helpers so existing call sites (`@/utils/auth-redirect`)
// keep working without every caller switching to the new module. The side-effectful
// `redirectOnAuthFailure` lives here; pure logic lives in `./redirect-safety`.
export {
  AUTH_REASON_PARAM,
  AUTH_REASON_PROTECTED_MEMO,
  AUTH_REDIRECT_PARAM,
  appendSearchParams,
  buildAuthRoute,
  getSafeRedirectPath,
  isPublicRoute,
  shouldGatePrivateInstance,
} from "./redirect-safety";

/**
 * Imperatively redirects the current document to the auth entry page, preserving
 * the current URL as the `redirect` target. Intended for hard-fail auth paths
 * (e.g. a refresh-token request returning 401 from a non-React context).
 *
 * No-ops when the user is already on an auth page or on a public page that
 * does not require authentication, unless `forceRedirect` is set.
 */
export function redirectOnAuthFailure(
  forceRedirect = false,
  options?: {
    redirect?: string | null;
    reason?: string | null;
  },
): void {
  // `window.location.pathname` carries the reverse-proxy mount prefix (e.g.
  // "/community/setting") while the React Router `basename` strips that same
  // prefix from `useLocation().pathname`. Without stripping, the auth-page
  // check below would never match a basename-mounted deployment, and the
  // encoded `redirect` target would be passed straight back to `navigate()`
  // with the prefix already attached, double-prefixing the destination.
  const currentPath = stripBasePath(window.location.pathname);
  const currentRedirectPath = `${currentPath}${window.location.search}${window.location.hash}`;

  // Already on auth page, nothing to do.
  if (currentPath.startsWith(ROUTES.AUTH)) {
    return;
  }

  // Don't redirect if it's a public route (unless forced, e.g. public visibility is disallowed).
  if (!forceRedirect && isPublicRoute(currentPath)) {
    return;
  }

  // The embedded `redirect` target is intentionally router-level (basename-stripped)
  // because post-auth navigation flows through `navigate()`, which adds the basename
  // automatically. The destination URL itself, however, is handed to
  // `window.location.replace`, which is a raw browser navigation that does NOT
  // consult React Router. The destination must therefore be a full browser URL
  // — i.e. the router-level `/auth?…` produced by `buildAuthRoute` must be
  // prefixed with the basename so a sub-path deployment (Caddyfile.fly.community
  // layout with `VITE_BASE_PATH=/community/`) lands on the SPA's auth page and
  // not on the static site's `file_server`. `withBasePath` is a no-op for
  // root-mounted builds.
  clearAccessToken();
  window.location.replace(
    withBasePath(
      buildAuthRoute({
        ...options,
        redirect: options?.redirect ?? currentRedirectPath,
      }),
    ),
  );
}
