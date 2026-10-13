import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/auth-state", () => ({
  clearAccessToken: vi.fn(),
}));

// Simulate a sub-path mount (Caddyfile.fly.community layout with
// VITE_BASE_PATH=/community/). The vitest environment uses Vite's default
// `base: "/"`, so without this override `BASE_PATH === "/"` and the
// basename-aware branches in `redirectOnAuthFailure` would never be
// exercised. We mock the module that derives the value so the production
// code under test sees a non-root prefix. The same fixture is reused for
// every test in this file: contract assertions about the destination URL
// prefix and the embedded `redirect` value both rely on `BASE_PATH ===
// "/community"`.
vi.mock("@/utils/base-path", async () => {
  const actual = await vi.importActual<typeof import("@/utils/base-path")>("@/utils/base-path");
  return {
    ...actual,
    BASE_PATH: "/community",
    stripBasePath: (path: string) => actual.stripBasePathWith(path, "/community"),
    withBasePath: (path: string) => actual.withBasePathWith(path, "/community"),
  };
});

import { clearAccessToken } from "@/auth-state";
import { redirectOnAuthFailure } from "@/utils/auth-redirect";

const mockedClearAccessToken = vi.mocked(clearAccessToken);

type NavigationStub = { replace: ReturnType<typeof vi.fn>; href: string };

function installLocation(href: string): NavigationStub {
  const url = new URL(href);
  const replace = vi.fn((next: string) => {
    // Mirror real navigation: update the mutable href on subsequent inspection.
    location.href = new URL(next, url).toString();
  });
  const location: NavigationStub = { replace, href: url.toString() };

  Object.defineProperty(window, "location", {
    configurable: true,
    value: {
      get href() {
        return location.href;
      },
      set href(value: string) {
        location.href = value;
      },
      pathname: url.pathname,
      search: url.search,
      hash: url.hash,
      origin: url.origin,
      replace,
    },
  });

  return location;
}

/**
 * Parses the destination URL handed to `window.location.replace` and reports
 * the embedded `redirect` query parameter, mirroring the auth page's own
 * decoding step. Returning `undefined` for the absent-redirect case keeps
 * the assertions concise.
 */
function embeddedRedirect(destination: string): string | undefined {
  const queryIndex = destination.indexOf("?");
  if (queryIndex < 0) {
    return undefined;
  }
  const params = new URLSearchParams(destination.slice(queryIndex + 1));
  return params.get("redirect") ?? undefined;
}

describe("redirectOnAuthFailure (sub-path mount: BASE_PATH=/community)", () => {
  let originalLocation: Location;

  beforeEach(() => {
    originalLocation = window.location;
  });

  afterEach(() => {
    Object.defineProperty(window, "location", {
      configurable: true,
      value: originalLocation,
    });
  });

  it("does nothing when the user is already on an /auth page", () => {
    // The auth-page check uses the basename-stripped pathname, so a literal
    // `/auth?foo=bar` URL (with no mount prefix) still matches the check.
    const nav = installLocation("http://localhost/auth?foo=bar");

    redirectOnAuthFailure();

    expect(nav.replace).not.toHaveBeenCalled();
    expect(mockedClearAccessToken).not.toHaveBeenCalled();
  });

  it("does nothing on a public route by default", () => {
    const nav = installLocation("http://localhost/explore");

    redirectOnAuthFailure();

    expect(nav.replace).not.toHaveBeenCalled();
    expect(mockedClearAccessToken).not.toHaveBeenCalled();
  });

  it("prefixes the destination with the basename while keeping the embedded redirect target router-level", () => {
    // The user is on a basename-stripped URL (`/home`), but in a real sub-path
    // build the browser would have shown them `/community/home`. The destination
    // must be a full browser URL (`/community/auth?...`) because
    // `window.location.replace` is a raw navigation that does not consult
    // React Router's basename. The embedded `redirect` value, however, flows
    // back through `navigate()` after auth and must be basename-stripped
    // (router-level) to avoid double-prefixing the destination.
    const nav = installLocation("http://localhost/home?tab=pins#latest");

    redirectOnAuthFailure();

    expect(mockedClearAccessToken).toHaveBeenCalledTimes(1);
    expect(nav.replace).toHaveBeenCalledWith("/community/auth?redirect=%2Fhome%3Ftab%3Dpins%23latest");
  });

  it("honours forceRedirect even on a public route", () => {
    const nav = installLocation("http://localhost/explore");

    redirectOnAuthFailure(true);

    expect(mockedClearAccessToken).toHaveBeenCalledTimes(1);
    expect(nav.replace).toHaveBeenCalledWith("/community/auth?redirect=%2Fexplore");
  });

  it("embeds the reason parameter when provided", () => {
    const nav = installLocation("http://localhost/home");

    redirectOnAuthFailure(false, { reason: "protected-memo" });

    expect(nav.replace).toHaveBeenCalledWith("/community/auth?redirect=%2Fhome&reason=protected-memo");
  });

  it("prefers an explicitly provided redirect target over the current location", () => {
    // An explicit `redirect` is a router-level path supplied by the caller.
    // It is embedded as-is and the destination URL is the only thing that
    // gets the basename prefix.
    const nav = installLocation("http://localhost/home");

    redirectOnAuthFailure(false, { redirect: "/setting" });

    expect(nav.replace).toHaveBeenCalledWith("/community/auth?redirect=%2Fsetting");
  });

  it("drops an unsafe redirect target silently", () => {
    const nav = installLocation("http://localhost/home");

    redirectOnAuthFailure(false, { redirect: "//evil.example/phish" });

    expect(nav.replace).toHaveBeenCalledWith("/community/auth");
  });

  it("strips the reverse-proxy mount prefix before checking and embedding the redirect target", () => {
    // Under a sub-path mount (e.g. Caddyfile.fly.community layout with
    // VITE_BASE_PATH=/community/), `window.location.pathname` carries the
    // prefix the proxy strips. The auth-page check must still fire when the
    // user is on `/community/auth/...`, the encoded `redirect` value must be
    // the router-level path so a later `navigate()` call does not
    // double-prefix the destination, and the destination itself must be a
    // full browser URL with the basename prefix.
    const nav = installLocation("http://localhost/community/home?tab=pins#latest");

    redirectOnAuthFailure();

    expect(mockedClearAccessToken).toHaveBeenCalledTimes(1);
    expect(nav.replace).toHaveBeenCalledWith("/community/auth?redirect=%2Fhome%3Ftab%3Dpins%23latest");
  });

  it("does nothing when the user is already on a basename-mounted /auth page", () => {
    const nav = installLocation("http://localhost/community/auth?foo=bar");

    redirectOnAuthFailure();

    expect(nav.replace).not.toHaveBeenCalled();
    expect(mockedClearAccessToken).not.toHaveBeenCalled();
  });

  it("keeps the destination prefix and the embedded redirect on opposite sides of the basename boundary", () => {
    // Defence-in-depth assertion against a regression that would let the
    // basename prefix leak into the `redirect` value (which would later
    // double-prefix the post-auth destination) or vanish from the
    // destination URL (which would 404 against the static site's
    // `file_server` on a sub-path deployment).
    const nav = installLocation("http://localhost/community/setting/profile?tab=2#bio");

    redirectOnAuthFailure();

    expect(nav.replace).toHaveBeenCalledTimes(1);
    const destination = nav.replace.mock.calls[0][0] as string;
    expect(destination.startsWith("/community/auth?")).toBe(true);
    expect(embeddedRedirect(destination)).toBe("/setting/profile?tab=2#bio");
  });
});
