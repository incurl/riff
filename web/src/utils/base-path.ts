/**
 * Single source of truth for the SPA's mount path.
 *
 * Vite exposes the build-time `base` value at `import.meta.env.BASE_URL`. The
 * Vite `base` rewrites the asset URLs in `index.html` and the same value is
 * read by `web/src/connect.ts` to build the Connect `baseUrl`. React Router
 * needs the same path passed as the `basename` option so that `<Link to>`,
 * `<Navigate to>`, and `useNavigate()` calls all line up with the prefix the
 * reverse proxy strips.
 *
 * Vite's convention is to include a trailing slash (`"/"` or `"/community/"`).
 * React Router's `basename` convention is the opposite: a leading slash, no
 * trailing slash (`"/"` or `"/community"`). The two cases are reconciled here
 * so the rest of the app never has to think about it.
 *
 * The root-mounted deployment (`base: "/"`) is the identity case and stays
 * `"."`-equivalent for callers — `BASE_PATH === "/"` and `stripBasePath`
 * returns its input unchanged.
 */

/**
 * Normalises a raw Vite `base` value to the React Router basename form:
 * a leading slash, no trailing slash, with the root case preserved as `"/"`.
 *
 * Exported for unit tests so the module-level constant does not need
 * `import.meta.env` overrides to exercise.
 */
export const normalizeBasePath = (raw: string | undefined): "/" | `/${string}` => {
  if (!raw || raw === "/") {
    return "/";
  }
  return raw.replace(/\/+$/, "") as `/${string}`;
};

/**
 * React Router basename: a leading-slash path with no trailing slash.
 * Equals `"/"` for root-mounted builds, `"/community"` for sub-path builds.
 */
export const BASE_PATH: "/" | `/${string}` = normalizeBasePath(import.meta.env.BASE_URL);

/**
 * Removes a given SPA mount prefix from a browser-supplied path so the
 * result is the React Router-level pathname (i.e. the path the router's
 * `path` patterns expect).
 *
 * Pass `BASE_PATH` in production; tests pass an explicit value to cover the
 * sub-path branches.
 *
 * Examples (with prefix `/community`):
 *   stripBasePathWith("/community/setting", "/community")  === "/setting"
 *   stripBasePathWith("/community",        "/community")  === "/"
 *   stripBasePathWith("/setting",          "/community")  === "/setting"  // not under prefix
 *   stripBasePathWith("/",                 "/community")  === "/"
 *
 * With a `/` prefix the function is the identity (no prefix to strip).
 */
export const stripBasePathWith = (path: string, prefix: "/" | `/${string}`): string => {
  if (prefix === "/") {
    return path;
  }
  if (path === prefix) {
    return "/";
  }
  if (path.startsWith(`${prefix}/`)) {
    return path.slice(prefix.length);
  }
  return path;
};

/**
 * Removes the SPA mount prefix (`BASE_PATH`) from a browser-supplied path.
 *
 * Use this for anything derived from `window.location.pathname` that
 * subsequently needs to flow back through the router (return-to URLs, the
 * `currentPath` check in `redirectOnAuthFailure`, etc.). Reading
 * `useLocation().pathname` already returns the basename-stripped value, so
 * the hook path does not need this helper.
 */
export const stripBasePath = (path: string): string => stripBasePathWith(path, BASE_PATH);

/**
 * Prepends a given SPA mount prefix to a router-level path so the result
 * is a browser URL fragment. Pass `BASE_PATH` in production; tests pass an
 * explicit value to cover the sub-path branches.
 *
 * Examples (with prefix `/community`):
 *   withBasePathWith("/auth", "/community")             === "/community/auth"
 *   withBasePathWith("/",    "/community")             === "/community/"
 *
 * With a `/` prefix the function is the identity.
 */
export const withBasePathWith = (path: string, prefix: "/" | `/${string}`): string => {
  if (prefix === "/") {
    return path;
  }
  // `path` is always router-level, so it always starts with "/".
  return `${prefix}${path}`;
};

/**
 * Prepends the SPA mount prefix (`BASE_PATH`) to a router-level path so the
 * result is a browser URL fragment.
 *
 * Use this for the few places that need to hand a full path to a non-router
 * API (e.g. `window.location.replace` for a hard sign-out redirect), since
 * React Router's own `<Link to>`, `<Navigate to>`, and `navigate()` calls
 * already add the prefix via `basename`. The function is the identity when
 * the SPA is root-mounted.
 */
export const withBasePath = (path: string): string => withBasePathWith(path, BASE_PATH);

/**
 * Reports whether a URL addresses a different origin or a baked asset, and
 * therefore must NOT be basename-prefixed.
 *
 * Matches anything with a URL scheme (`https:`, `data:`, `blob:`, `file:`,
 * `mailto:`, `javascript:`, …) and protocol-relative URLs (`//host/path`).
 * The scheme regex is the WHATWG definition (letter, then letters / digits
 * / `+` / `-` / `.`, then `:`), which is what `new URL(input)` itself would
 * recognise.
 */
export const isAbsoluteUrl = (url: string): boolean => /^[a-z][a-z0-9+.-]*:/i.test(url) || url.startsWith("//");

/**
 * Normalises an arbitrary URL — typically a `customProfile.logoUrl` (or its
 * hardcoded default) — to a browser URL that resolves against the SPA's
 * mount point.
 *
 * Absolute URLs are returned unchanged: they already address a fixed host
 * or a baked data/blob asset, and the basename must not be prepended.
 * Anything else is treated as a router-level path and passed through
 * `withBasePathWith`, so a hardcoded `/logo.webp` default becomes
 * `/community/logo.webp` on a sub-path mount (and `/logo.webp` on the root
 * mount).
 *
 * This is the read-side counterpart to `stripBasePath`: the typical use is
 * feeding a string into an `<img src>` / `<link href>`, where the browser
 * resolves the URL against the document origin rather than the router's
 * basename. Empty / nullish input returns `""` so the call site can stay a
 * single expression (`src={toBasenameAwareUrlWith(profile.logoUrl || "/logo.webp", "/community")}`).
 *
 * Pass `BASE_PATH` in production; tests pass an explicit value to cover the
 * sub-path branches.
 *
 * Note: this is intentionally NOT a general-purpose URL normaliser. It
 * does not re-prefix an already-prefixed path (so a customProfile value
 * that was hand-prefixed for one deployment will double-prefix after a
 * redeploy to a different mount point), and it does not rebase a relative
 * path that lacks a leading slash. Both cases are the caller's problem;
 * the form's `logoUrl` field is documented as accepting a full URL or a
 * path relative to the SPA root.
 */
export const toBasenameAwareUrlWith = (url: string | undefined | null, prefix: "/" | `/${string}`): string => {
  if (!url) {
    return "";
  }
  if (isAbsoluteUrl(url)) {
    return url;
  }
  return withBasePathWith(url, prefix);
};

/**
 * Read-side counterpart to `stripBasePath` (see `toBasenameAwareUrlWith`
 * for the contract). Defaults the prefix to `BASE_PATH`, so production
 * code rarely needs to think about the basename; the local `BASE_PATH`
 * is whatever Vite baked at build time.
 */
export const toBasenameAwareUrl = (url: string | undefined | null): string => toBasenameAwareUrlWith(url, BASE_PATH);

/**
 * Builds the absolute base URL for *server* endpoints — i.e. anything the
 * browser fetches with `fetch()` or sets as `<img src>` / `<a href>` where
 * the reverse-proxy's prefix is part of the path the request must hit.
 *
 * `BASE_PATH` is the React Router basename (leading slash, no trailing
 * slash). URL composition needs a trailing slash so the caller can simply
 * append a path segment without worrying about the root case
 * (`"https://host" + "/file/..."` is one segment too few). This is the
 * single helper that does the join:
 *
 *   baseUrlWith("/")        === "https://host/"            // root mount
 *   baseUrlWith("/community") === "https://host/community/"  // sub-path mount
 *
 * Exposed as `*With` so tests can pass an explicit prefix; the
 * module-level `BASE_URL` reads `window.location.origin` at load time
 * (Vite is always browser-targeted for the SPA, so `window` is defined).
 *
 * The helper intentionally reads `window.location.origin` instead of
 * accepting it as a parameter: the origin is "where the SPA is mounted",
 * not a server-controlled configuration knob, and centralising the read
 * here is what makes the four attachment/share callers behave identically.
 *
 * Pass `BASE_PATH` in production; tests pass an explicit value to cover
 * the sub-path branches.
 */
export const baseUrlWith = (prefix: "/" | `/${string}`): string => {
  const origin = window.location.origin;
  return prefix === "/" ? `${origin}/` : `${origin}${prefix}/`;
};

/**
 * Module-level server base URL, e.g. `"https://ravine.fly.dev/community/"`.
 * Equals `"https://<host>/"` on the root mount.
 *
 * Use this when the result is consumed by `fetch()`, `<img src>`, or any
 * other browser-side API that resolves URLs against the document origin
 * rather than the React Router basename — i.e. the inverse situation
 * `toBasenameAwareUrl` exists for.
 */
export const BASE_URL: string = baseUrlWith(BASE_PATH);
