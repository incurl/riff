import { describe, expect, it } from "vitest";
import {
  baseUrlWith,
  isAbsoluteUrl,
  normalizeBasePath,
  stripBasePath,
  stripBasePathWith,
  toBasenameAwareUrl,
  toBasenameAwareUrlWith,
  withBasePath,
  withBasePathWith,
} from "@/utils/base-path";

describe("normalizeBasePath", () => {
  it("returns '/' for an unset or empty value", () => {
    expect(normalizeBasePath(undefined)).toBe("/");
    expect(normalizeBasePath("")).toBe("/");
  });

  it("returns '/' for the root value as-is", () => {
    expect(normalizeBasePath("/")).toBe("/");
  });

  it("strips a single trailing slash", () => {
    expect(normalizeBasePath("/community/")).toBe("/community");
  });

  it("strips multiple trailing slashes (defensive)", () => {
    expect(normalizeBasePath("/community///")).toBe("/community");
  });

  it("leaves a no-trailing-slash value unchanged", () => {
    expect(normalizeBasePath("/community")).toBe("/community");
  });
});

describe("stripBasePathWith (pure, prefix-driven)", () => {
  describe("with prefix '/'", () => {
    it("is the identity", () => {
      expect(stripBasePathWith("/auth/signup", "/")).toBe("/auth/signup");
      expect(stripBasePathWith("/", "/")).toBe("/");
      expect(stripBasePathWith("/community/setting", "/")).toBe("/community/setting");
    });
  });

  describe("with prefix '/community'", () => {
    it("strips the prefix from a path that starts with it", () => {
      expect(stripBasePathWith("/community/setting", "/community")).toBe("/setting");
      expect(stripBasePathWith("/community/auth/signup", "/community")).toBe("/auth/signup");
    });

    it("collapses the prefix alone to '/'", () => {
      expect(stripBasePathWith("/community", "/community")).toBe("/");
    });

    it("leaves paths outside the prefix unchanged", () => {
      expect(stripBasePathWith("/setting", "/community")).toBe("/setting");
      expect(stripBasePathWith("/", "/community")).toBe("/");
    });

    it("does not strip a path that merely contains the prefix mid-string", () => {
      // "startsWith" is anchored; "/x/community/..." must NOT lose the prefix.
      expect(stripBasePathWith("/x/community/setting", "/community")).toBe("/x/community/setting");
    });
  });
});

describe("withBasePathWith (pure, prefix-driven)", () => {
  describe("with prefix '/'", () => {
    it("is the identity", () => {
      expect(withBasePathWith("/auth", "/")).toBe("/auth");
      expect(withBasePathWith("/", "/")).toBe("/");
    });
  });

  describe("with prefix '/community'", () => {
    it("prepends the prefix to a router-level path", () => {
      expect(withBasePathWith("/auth", "/community")).toBe("/community/auth");
      expect(withBasePathWith("/auth/signup", "/community")).toBe("/community/auth/signup");
    });

    it("preserves the trailing slash on root-level paths", () => {
      expect(withBasePathWith("/", "/community")).toBe("/community/");
    });
  });
});

describe("stripBasePath and withBasePath (BASE_PATH-driven)", () => {
  // The module-level `BASE_PATH` is whatever Vite baked in at build time.
  // Local dev and the default `pnpm release` both land on "/"; the
  // sub-path branches above cover every other case via the pure functions.
  it("stripBasePath is the identity at the root mount", () => {
    expect(stripBasePath("/auth/signup")).toBe("/auth/signup");
    expect(stripBasePath("/")).toBe("/");
  });

  it("withBasePath is the identity at the root mount", () => {
    expect(withBasePath("/auth")).toBe("/auth");
    expect(withBasePath("/")).toBe("/");
  });
});

describe("isAbsoluteUrl", () => {
  it("detects standard URL schemes", () => {
    expect(isAbsoluteUrl("https://example.com/logo.png")).toBe(true);
    expect(isAbsoluteUrl("http://example.com/logo.png")).toBe(true);
    expect(isAbsoluteUrl("HTTPS://EXAMPLE.COM/logo.png")).toBe(true);
  });

  it("detects non-http schemes used for baked assets", () => {
    expect(isAbsoluteUrl("data:image/png;base64,abc")).toBe(true);
    expect(isAbsoluteUrl("blob:https://example.com/abc")).toBe(true);
    expect(isAbsoluteUrl("file:///etc/hosts")).toBe(true);
  });

  it("detects protocol-relative URLs", () => {
    expect(isAbsoluteUrl("//cdn.example.com/logo.png")).toBe(true);
  });

  it("rejects root-relative and bare paths", () => {
    expect(isAbsoluteUrl("/logo.webp")).toBe(false);
    expect(isAbsoluteUrl("/api/files/users/abc/avatar.png")).toBe(false);
    expect(isAbsoluteUrl("logo.webp")).toBe(false);
    expect(isAbsoluteUrl("")).toBe(false);
  });

  it("rejects scheme-like fragments that lack the ':' separator", () => {
    // `a://` is scheme-like, but `a/b` is not; the regex anchors on ':'.
    expect(isAbsoluteUrl("a/b")).toBe(false);
  });
});

describe("toBasenameAwareUrlWith (pure, prefix-driven)", () => {
  describe("with prefix '/'", () => {
    it("is the identity for relative paths", () => {
      expect(toBasenameAwareUrlWith("/logo.webp", "/")).toBe("/logo.webp");
      expect(toBasenameAwareUrlWith("/api/files/users/abc/avatar.png", "/")).toBe("/api/files/users/abc/avatar.png");
    });

    it("is the identity for absolute URLs (no prefix prepended)", () => {
      expect(toBasenameAwareUrlWith("https://example.com/logo.png", "/")).toBe("https://example.com/logo.png");
      expect(toBasenameAwareUrlWith("data:image/png;base64,abc", "/")).toBe("data:image/png;base64,abc");
    });
  });

  describe("with prefix '/community'", () => {
    it("prepends the prefix to a relative path", () => {
      expect(toBasenameAwareUrlWith("/logo.webp", "/community")).toBe("/community/logo.webp");
      expect(toBasenameAwareUrlWith("/full-logo.webp", "/community")).toBe("/community/full-logo.webp");
    });

    it("does not double-prefix an absolute URL", () => {
      // The whole point of the absolute-URL passthrough: a user-supplied
      // customProfile.logoUrl that points at an external host must NOT
      // become https://example.com/community/logo.png after the helper
      // runs. That would silently break every instance that customised
      // its logo with a full URL.
      expect(toBasenameAwareUrlWith("https://example.com/logo.png", "/community")).toBe("https://example.com/logo.png");
      expect(toBasenameAwareUrlWith("data:image/svg+xml;base64,abc", "/community")).toBe("data:image/svg+xml;base64,abc");
      expect(toBasenameAwareUrlWith("//cdn.example.com/logo.png", "/community")).toBe("//cdn.example.com/logo.png");
    });

    it("returns '' for empty / nullish input", () => {
      expect(toBasenameAwareUrlWith("", "/community")).toBe("");
      expect(toBasenameAwareUrlWith(undefined, "/community")).toBe("");
      expect(toBasenameAwareUrlWith(null, "/community")).toBe("");
    });
  });
});

describe("toBasenameAwareUrl (BASE_PATH-driven)", () => {
  // Module-level `BASE_PATH` is the Vite-baked value. Local dev and the
  // default `pnpm release` both land on "/", so the identity branch is the
  // one exercised by tests that don't override the module. The pure
  // function variants above cover the sub-path branches.
  it("is the identity for a relative path at the root mount", () => {
    expect(toBasenameAwareUrl("/logo.webp")).toBe("/logo.webp");
  });

  it("is the identity for an absolute URL at the root mount", () => {
    expect(toBasenameAwareUrl("https://example.com/logo.png")).toBe("https://example.com/logo.png");
  });
});

describe("baseUrlWith (pure, prefix-driven)", () => {
  const origin = window.location.origin;

  describe("with prefix '/'", () => {
    it("returns '<origin>/' so callers can append '/file/...' without a join bug", () => {
      // The trailing slash is load-bearing: `origin + "/file/..."` would
      // collapse the leading slash of the path segment into the origin and
      // (on hosts that lack one) yield "<origin>file/...". The helper's
      // job is to make the join safe.
      expect(baseUrlWith("/")).toBe(`${origin}/`);
    });
  });

  describe("with prefix '/community'", () => {
    it("prepends the prefix and a trailing slash", () => {
      expect(baseUrlWith("/community")).toBe(`${origin}/community/`);
    });

    it("preserves the trailing slash the caller can append to", () => {
      // The exact shape the attachment URL builders depend on: they
      // concatenate `BASE_URL + "file/..."` and expect to land on
      // `<origin>/community/file/...`, not `<origin>/communityfile/...`.
      expect(`${baseUrlWith("/community")}file/attachments/uid/photo.png`).toBe(`${origin}/community/file/attachments/uid/photo.png`);
    });
  });
});
