// Regression coverage for the session cookie that lets an `<img>` read a
// private attachment (see pb/api/file_authz.go).
//
// The SPA keeps its PocketBase token in localStorage, but an `<img src>` can
// present neither localStorage nor an Authorization header, so the browser
// fetched attachment bytes with no credential and every image on a PRIVATE
// memo — the editor's default — came back 404. `syncAuthCookie` mirrors the
// token into a cookie the browser attaches automatically.
//
// These tests pin the cookie's *security* properties, not just that a cookie
// appears. The interesting failures are all "the cookie is broader or
// longer-lived than the session it mirrors".
import { beforeEach, describe, expect, it, vi } from "vitest";

// `auth-state` reads `BASE_PATH` at call time to scope the cookie. The vitest
// environment uses Vite's default root base, so the sub-path case (the
// Caddyfile.fly.community deployment — the one this bug was reported from)
// needs an explicit override.
vi.mock("@/utils/base-path", async () => {
  const actual = await vi.importActual<typeof import("@/utils/base-path")>("@/utils/base-path");
  return { ...actual, BASE_PATH: "/community" };
});

const { clearAccessToken, setAccessToken, syncAuthCookie } = await import("@/auth-state");

// Assert on the *written* cookie string rather than on `document.cookie`.
//
// jsdom serves the document from `http://localhost:3000/`, and its cookie jar
// only surfaces cookies whose path prefixes that URL — so a cookie correctly
// scoped to `/community/` is invisible to a read-back, and a `Secure` cookie
// may also be dropped outright. Reading the jar would therefore test jsdom's
// cookie implementation instead of riff's, and would fail on exactly the
// deployment layout this fixes. The written string is the contract riff
// controls: name, value, path, expiry, SameSite, and Secure.
const lastCookieWrite = (written: { mock: { calls: unknown[][] } }): string => (written.mock.calls.at(-1)?.[0] as string | undefined) ?? "";

// jsdom does not implement `isSecureContext`, so it reads `undefined`.
const setSecureContext = (value: boolean | undefined) => {
  Object.defineProperty(window, "isSecureContext", { value, configurable: true, writable: true });
};

describe("syncAuthCookie", () => {
  beforeEach(() => {
    localStorage.clear();
    setSecureContext(true);
  });

  it("mirrors the token so an <img> GET can present a credential", () => {
    const written = vi.spyOn(document, "cookie", "set");

    syncAuthCookie("tok-123", new Date(Date.now() + 60_000));

    expect(lastCookieWrite(written)).toContain("riff_auth=tok-123");
  });

  it("scopes the cookie to the SPA mount path so the static site never sees the token", () => {
    const written = vi.spyOn(document, "cookie", "set");

    syncAuthCookie("tok-456", new Date(Date.now() + 60_000));

    // Without the prefix the token would be attached to every request to the
    // host, including the static site that shares it in this layout.
    expect(lastCookieWrite(written)).toContain("path=/community/");
  });

  it("binds the cookie to the token's own lifetime", () => {
    const expiresAt = new Date(Date.now() + 45_000);
    const written = vi.spyOn(document, "cookie", "set");

    syncAuthCookie("tok-123", expiresAt);

    // A cookie outliving the token would keep a dead session looking
    // authenticated to the file server until it expired on its own.
    expect(lastCookieWrite(written)).toContain(`expires=${expiresAt.toUTCString()}`);
  });

  it("clears the cookie under the same path it was written with", () => {
    const written = vi.spyOn(document, "cookie", "set");

    syncAuthCookie("tok-123", new Date(Date.now() + 60_000));
    syncAuthCookie(null);
    const cleared = lastCookieWrite(written);

    // A mismatched path would leave the original cookie in place, so the
    // session would outlive the sign-out that was supposed to end it.
    expect(cleared).toContain("path=/community/");
    expect(cleared).toContain("expires=Thu, 01 Jan 1970");
    expect(cleared).toContain("riff_auth=;");
  });

  it("omits Secure on a cleartext origin so local dev over http still works", () => {
    // A `Secure` cookie is rejected outright by a browser on a plain-HTTP
    // origin, which would make the fix appear broken locally and work in
    // production — the worst order in which to discover a transport rule.
    setSecureContext(false);
    const written = vi.spyOn(document, "cookie", "set");

    syncAuthCookie("tok-123", new Date(Date.now() + 60_000));

    expect(lastCookieWrite(written)).not.toContain("Secure");
  });

  it("always marks the cookie SameSite=Strict", () => {
    const written = vi.spyOn(document, "cookie", "set");

    syncAuthCookie("tok-123", new Date(Date.now() + 60_000));

    // Strict keeps the session off cross-site requests, so a third-party page
    // embedding the SPA cannot ride it.
    expect(lastCookieWrite(written)).toContain("SameSite=Strict");
  });

  it("URL-encodes the token", () => {
    const written = vi.spyOn(document, "cookie", "set");

    syncAuthCookie("tok with space;and=semi", new Date(Date.now() + 60_000));

    // An unencoded value would truncate the cookie at the first separator and
    // silently degrade the session to anonymous.
    expect(lastCookieWrite(written)).toContain(`riff_auth=${encodeURIComponent("tok with space;and=semi")}`);
  });
});

describe("token lifecycle", () => {
  beforeEach(() => {
    localStorage.clear();
    setSecureContext(true);
  });

  it("sets the cookie when a session is established", () => {
    const written = vi.spyOn(document, "cookie", "set");

    setAccessToken("tok-new", new Date(Date.now() + 60_000));

    expect(lastCookieWrite(written)).toContain("riff_auth=tok-new");
  });

  it("removes the cookie on sign-out", () => {
    const written = vi.spyOn(document, "cookie", "set");

    setAccessToken("tok-new", new Date(Date.now() + 60_000));
    const afterSignIn = written.mock.calls.length;
    clearAccessToken();

    // A leftover cookie would keep serving file reads for a session the user
    // just ended, because the server resolves it to a live user record.
    expect(written.mock.calls.length).toBeGreaterThan(afterSignIn);
    expect(lastCookieWrite(written)).toContain("expires=Thu, 01 Jan 1970");
  });

  it("re-asserts the cookie on page load so a reload keeps rendering private images", async () => {
    // Seed a stored session the way a previous tab left it, then import a
    // fresh module instance — the module-scoped token is null there, which is
    // exactly the state a page reload starts from.
    localStorage.setItem("memos_access_token", "tok-stored");
    localStorage.setItem("memos_token_expires_at", new Date(Date.now() + 60_000).toISOString());
    vi.resetModules();
    const written = vi.spyOn(document, "cookie", "set");

    const fresh = await import("@/auth-state");
    fresh.getAccessToken();

    // Without this the cookie would only ever be written at sign-in, and a
    // reload inside a long-lived session would render every private image
    // anonymously.
    expect(lastCookieWrite(written)).toContain("riff_auth=tok-stored");
  });
});
