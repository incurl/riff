import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import MemosLogo from "@/components/MemosLogo";

// The instance-shape surface MemosLogo actually reads; declared inline so
// the test doesn't reach into a context file for a structural type.
const mockInstance = {
  generalSetting: {} as { customProfile?: { title?: string; logoUrl?: string } },
};

vi.mock("@/contexts/InstanceContext", () => ({
  useInstance: () => mockInstance,
}));

// Pin BASE_PATH=/community so the basename-aware branch is exercised.
// The default Vitest environment ships BASE_PATH="/" (Vite's default
// `base: "/"`), which would make every assertion vacuously identity and
// miss the very regression this test is here to prevent. See the same
// fixture pattern in auth-redirect.test.ts.
vi.mock("@/utils/base-path", async () => {
  const actual = await vi.importActual<typeof import("@/utils/base-path")>("@/utils/base-path");
  return {
    ...actual,
    BASE_PATH: "/community",
    withBasePath: (path: string) => actual.withBasePathWith(path, "/community"),
    toBasenameAwareUrl: (url: string | undefined | null) => actual.toBasenameAwareUrlWith(url, "/community"),
  };
});

describe("<MemosLogo> logo URL under a sub-path mount", () => {
  beforeEach(() => {
    mockInstance.generalSetting = {};
  });

  afterEach(() => {
    mockInstance.generalSetting = {};
  });

  it("prefixes the default fallback `/full-logo.webp` with the SPA mount path", () => {
    // The hardcoded fallback in MemosLogo.tsx resolves at the document
    // origin (it's an <img src>), not the router basename. Without the
    // `toBasenameAwareUrl` wrap, a sub-path mount (Caddyfile.fly.community
    // layout with VITE_BASE_PATH=/community/) would 404 the sidebar brand
    // against the static site that owns the document root.
    const { container } = render(<MemosLogo />);

    expect(container.querySelector("img")).toHaveAttribute("src", "/community/full-logo.webp");
  });

  it("prefixes a user-supplied relative logoUrl with the SPA mount path", () => {
    // A customisation that points at a baked SPA asset (e.g. an admin who
    // dropped `/uploads/team-logo.png` into the public folder) must also
    // resolve under the basename.
    mockInstance.generalSetting = { customProfile: { logoUrl: "/uploads/team-logo.png" } };

    const { container } = render(<MemosLogo />);

    expect(container.querySelector("img")).toHaveAttribute("src", "/community/uploads/team-logo.png");
  });

  it("passes an absolute URL through unchanged", () => {
    // A customisation that points at an external CDN must not be prefixed
    // — `toBasenameAwareUrl` recognises the scheme and returns the input
    // verbatim. Doubling the prefix would silently break every instance
    // that customises its logo with a full URL.
    mockInstance.generalSetting = { customProfile: { logoUrl: "https://cdn.example.com/team-logo.png" } };

    const { container } = render(<MemosLogo />);

    expect(container.querySelector("img")).toHaveAttribute("src", "https://cdn.example.com/team-logo.png");
  });

  it("passes a data URL through unchanged", () => {
    // The form accepts a `data:` URL for an inline image; the helper
    // recognises the `data:` scheme and returns it verbatim. The browser
    // decodes the inline asset without a network request.
    mockInstance.generalSetting = { customProfile: { logoUrl: "data:image/svg+xml;base64,PHN2Zy8+" } };

    const { container } = render(<MemosLogo />);

    expect(container.querySelector("img")).toHaveAttribute("src", "data:image/svg+xml;base64,PHN2Zy8+");
  });
});
