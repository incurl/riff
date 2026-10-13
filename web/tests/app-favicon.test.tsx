import { act, render, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "@/App";

// Mutable per-test so each `it` can swap the customProfile value
// without reloading the module.
const mockInstance = {
  profile: { needsSetup: false },
  profileLoaded: true,
  generalSetting: {
    additionalStyle: "",
    additionalScript: "",
    customProfile: undefined as { title?: string; logoUrl?: string } | undefined,
  } as {
    additionalStyle: string;
    additionalScript: string;
    customProfile: { title?: string; logoUrl?: string } | undefined;
  },
};

vi.mock("@/contexts/InstanceContext", () => ({
  useInstance: () => mockInstance,
}));

vi.mock("@/hooks/useNavigateTo", () => ({ default: () => vi.fn() }));
vi.mock("@/hooks/useUserLocale", () => ({ useUserLocale: vi.fn() }));
vi.mock("@/hooks/useUserTheme", () => ({ useUserTheme: vi.fn() }));
vi.mock("@/utils/oauth", () => ({ cleanupExpiredOAuthState: vi.fn() }));

// Pin BASE_PATH=/community so the basename-aware branch of
// `toBasenameAwareUrl` is exercised. The default Vitest environment
// ships BASE_PATH="/" (Vite's default `base: "/"`), which would make
// every assertion vacuously identity and miss the very regression this
// test is here to prevent. See the same fixture pattern in
// auth-redirect.test.ts.
vi.mock("@/utils/base-path", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/utils/base-path")>();
  return {
    ...actual,
    BASE_PATH: "/community",
    withBasePath: (path: string) => actual.withBasePathWith(path, "/community"),
    toBasenameAwareUrl: (url: string | undefined | null) => actual.toBasenameAwareUrlWith(url, "/community"),
  };
});

const renderApp = () => {
  const router = createMemoryRouter(
    [
      {
        path: "/",
        element: <App />,
        children: [{ index: true, element: <div>routed</div> }],
      },
    ],
    { initialEntries: ["/"] },
  );
  return render(<RouterProvider router={router} />);
};

describe("<App> favicon URL under a sub-path mount", () => {
  let link: HTMLLinkElement;

  beforeEach(() => {
    // The static-HTML scaffold a real <link rel="icon"> tag, which
    // App.tsx's useEffect then overrides. Each test gets a fresh link
    // element with a placeholder href so the override is observable.
    document.head.querySelectorAll("link[rel~='icon']").forEach((node) => node.remove());
    link = document.createElement("link");
    link.rel = "icon";
    link.href = "/placeholder.ico";
    document.head.appendChild(link);
  });

  afterEach(() => {
    document.head.querySelectorAll("link[rel~='icon']").forEach((node) => node.remove());
  });

  it("does not run the customisation effect when customProfile is unset", () => {
    // The early-return guard is the contract that keeps the static
    // HTML's href in place on a fresh install. Pin it so a refactor
    // doesn't silently start writing `/community/logo.webp` on every
    // page load.
    mockInstance.generalSetting.customProfile = undefined;

    renderApp();

    expect(link.href.endsWith("/placeholder.ico")).toBe(true);
  });

  it("prefixes the default fallback `/logo.webp` with the SPA mount path", async () => {
    // The favicon override is a browser-level API: the <link href>
    // resolves against the document origin, not the router basename.
    // Without the `toBasenameAwareUrl` wrap, a sub-path mount would
    // 404 the favicon against the static site that owns the document
    // root.
    mockInstance.generalSetting.customProfile = { title: "Team Notes" };

    renderApp();

    await waitFor(() => {
      expect(link.href).toContain("/community/logo.webp");
    });
  });

  it("prefixes a customisation-supplied relative logoUrl with the SPA mount path", async () => {
    mockInstance.generalSetting.customProfile = { title: "Team Notes", logoUrl: "/uploads/team-favicon.ico" };

    renderApp();

    await waitFor(() => {
      expect(link.href).toContain("/community/uploads/team-favicon.ico");
    });
  });

  it("passes an absolute URL through unchanged", async () => {
    // A customisation that points at an external CDN must not be
    // prefixed — `toBasenameAwareUrl` recognises the scheme and returns
    // the input verbatim.
    mockInstance.generalSetting.customProfile = {
      title: "Team Notes",
      logoUrl: "https://cdn.example.com/team-favicon.ico",
    };

    act(() => {
      renderApp();
    });

    await waitFor(() => {
      expect(link.href).toContain("https://cdn.example.com/team-favicon.ico");
    });
  });
});
