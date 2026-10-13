import { render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import About from "@/pages/About";

const mockInstance = {
  profile: {
    version: "0.25.0",
    commit: "0123456789abcdef0123456789abcdef01234567",
    instanceUrl: "",
    demo: false,
    admin: undefined as { username: string; displayName: string } | undefined,
  },
  generalSetting: {} as { customProfile?: { title: string; description: string; logoUrl: string } },
};

vi.mock("@/contexts/InstanceContext", () => ({
  useInstance: () => mockInstance,
}));

vi.mock("@/utils/i18n", () => ({
  useTranslate: () => (key: string) =>
    (
      ({
        "common.version": "Version",
        "about.powered-by": "Powered by Memos",
        "about.sponsor": "Sponsor Memos",
      }) as Record<string, string>
    )[key] ?? key,
}));

// Pin BASE_PATH=/community so the basename-aware branch of
// `toBasenameAwareUrl` is exercised. The default Vitest environment ships
// BASE_PATH="/" (Vite's default `base: "/"`), which would make every
// assertion vacuously identity and miss the very regression this test
// suite is here to prevent. See the same fixture pattern in
// auth-redirect.test.ts.
//
// `vi.mock` is hoisted by Vitest to the top of the file before any test
// runs, so it applies to every `describe` block in this module. The
// tests in the first `<About>` block do not exercise the logo URL and
// are unaffected; the logo-URL assertion has been moved into the new
// `<About> logo URL under a sub-path mount` block below.
vi.mock("@/utils/base-path", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/utils/base-path")>();
  return {
    ...actual,
    BASE_PATH: "/community",
    withBasePath: (path: string) => actual.withBasePathWith(path, "/community"),
    toBasenameAwareUrl: (url: string | undefined | null) => actual.toBasenameAwareUrlWith(url, "/community"),
  };
});

const renderAbout = () => render(<About />);

describe("<About>", () => {
  beforeEach(() => {
    mockInstance.profile = {
      version: "0.25.0",
      commit: "0123456789abcdef0123456789abcdef01234567",
      instanceUrl: "https://notes.example.com",
      demo: false,
      admin: { username: "steven", displayName: "Steven" },
    };
    mockInstance.generalSetting = {};
  });

  afterEach(() => {
    document.documentElement.removeAttribute("data-theme");
  });

  it("renders the identity hero with linked version and commit chips", () => {
    renderAbout();

    expect(screen.getByRole("heading", { name: "Memos" })).toBeInTheDocument();
    expect(screen.getByText(/Your thoughts, your data/i)).toBeInTheDocument();
    expect(screen.getByText("about.description")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Version\s*v0\.25\.0/ })).toHaveAttribute(
      "href",
      "https://github.com/usememos/memos/releases/tag/v0.25.0",
    );
    expect(screen.getByRole("heading", { name: "Memos" }).parentElement).toContainElement(
      screen.getByRole("link", { name: /Version\s*v0\.25\.0/ }),
    );
    expect(screen.getByRole("link", { name: "0123456" })).toHaveAttribute(
      "href",
      "https://github.com/usememos/memos/commit/0123456789abcdef0123456789abcdef01234567",
    );
    expect(screen.queryByRole("link", { name: "MIT" })).not.toBeInTheDocument();
  });

  it("links to the project homepage, docs, API docs, GitHub, and Web Clipper", () => {
    renderAbout();

    expect(screen.getByRole("link", { name: /about\.official-website/ })).toHaveAttribute("href", "https://usememos.com/");
    expect(screen.getByRole("link", { name: /about\.documents/ })).toHaveAttribute("href", "https://usememos.com/docs");
    expect(screen.getByRole("link", { name: /about\.api-docs/ })).toHaveAttribute("href", "https://usememos.com/docs/api");
    expect(screen.getByRole("link", { name: /about\.web-clipper/ })).toHaveAttribute("href", "https://github.com/usememos/web-clipper");
    expect(screen.getByRole("link", { name: /about\.github-repository/ })).toHaveAttribute("href", "https://github.com/usememos/memos");
  });

  it("does not surface the instance URL, administrator, or birds", () => {
    renderAbout();

    expect(screen.queryByText("https://notes.example.com")).not.toBeInTheDocument();
    expect(screen.queryByText("Administrator")).not.toBeInTheDocument();
    expect(screen.queryByText("Steven")).not.toBeInTheDocument();
    expect(screen.queryByText("Birds")).not.toBeInTheDocument();
    expect(screen.queryByTestId("about-bird-sprite")).not.toBeInTheDocument();
  });

  it("offers a single sponsorship link to the Memos project, including on custom instances", () => {
    mockInstance.generalSetting = {
      customProfile: { title: "Team Notes", description: "Our shared scratchpad.", logoUrl: "/custom-logo.png" },
    };

    renderAbout();

    const links = screen.getAllByRole("link", { name: /Sponsor Memos/ });
    expect(links).toHaveLength(1);
    expect(within(screen.getByRole("navigation", { name: "about.project-links" })).getByRole("link", { name: /Sponsor Memos/ })).toBe(
      links[0],
    );
    expect(links[0]).toHaveAttribute("href", "https://github.com/sponsors/usememos");
    expect(links[0]).toHaveAttribute("target", "_blank");
    expect(links[0]).toHaveAttribute("rel", "noreferrer");
  });

  it("shows a plain version chip and no commit chip on dev builds", () => {
    mockInstance.profile.version = "dev";
    mockInstance.profile.commit = "unknown";

    renderAbout();

    expect(screen.getByText("dev")).toBeInTheDocument();
    expect(screen.queryByText("vdev")).not.toBeInTheDocument();
    expect(screen.queryByText(/unknown/)).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "about.build" })).not.toBeInTheDocument();
  });

  it("shows the demo badge on demo instances", () => {
    mockInstance.profile.demo = true;

    renderAbout();

    expect(screen.getByText("about.demo")).toBeInTheDocument();
  });

  it("renders as a page without nested mobile padding", () => {
    const { container } = renderAbout();

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    const contentWrapper = container.querySelector("section > div");
    expect(contentWrapper).toHaveClass("w-full");
    expect(contentWrapper).not.toHaveClass("px-4");
  });
});

// The default-LOGO constant in About.tsx is `/logo.webp`, and the
// `customProfile.logoUrl` slot is documented as accepting a path relative
// to the SPA root. Both flow into an <img src>, which resolves against the
// document origin — not the router basename. On a sub-path mount, a
// root-relative URL 404s against the static site that owns the document
// root. These tests pin both branches under a sub-path mount so a future
// regression to a root-relative string can't slip through.
describe("<About> logo URL under a sub-path mount", () => {
  beforeEach(() => {
    mockInstance.profile = {
      version: "0.25.0",
      commit: "0123456789abcdef0123456789abcdef01234567",
      instanceUrl: "https://notes.example.com",
      demo: false,
      admin: { username: "steven", displayName: "Steven" },
    };
    mockInstance.generalSetting = {};
  });

  it("uses custom branding for the identity hero and credits Memos", () => {
    // The "Team Notes" customisation also exercises the logo-URL path,
    // so it lives in this describe block: the top-of-file `vi.mock`
    // pins BASE_PATH=/community, and `toBasenameAwareUrl` is expected
    // to prefix the relative logoUrl. The pre-existing assertion
    // (originally written under the root-mount default) is updated
    // here to match the prefixed URL.
    mockInstance.generalSetting = {
      customProfile: { title: "Team Notes", description: "Our shared scratchpad.", logoUrl: "/custom-logo.png" },
    };

    const { container } = renderAbout();

    expect(screen.getByRole("heading", { name: "Team Notes" })).toBeInTheDocument();
    expect(screen.getByText("Our shared scratchpad.")).toBeInTheDocument();
    expect(screen.queryByText(/Your thoughts, your data/i)).not.toBeInTheDocument();
    expect(container.querySelector("img")).toHaveAttribute("src", "/community/custom-logo.png");
    expect(screen.getByText("Powered by Memos")).toBeInTheDocument();
  });

  it("prefixes the default fallback `/logo.webp` with the SPA mount path", () => {
    const { container } = renderAbout();

    expect(container.querySelector("img")).toHaveAttribute("src", "/community/logo.webp");
  });

  it("prefixes a customisation-supplied relative logoUrl with the SPA mount path", () => {
    mockInstance.generalSetting = {
      customProfile: { title: "Team Notes", description: "Our shared scratchpad.", logoUrl: "/custom-logo.png" },
    };

    const { container } = renderAbout();

    expect(container.querySelector("img")).toHaveAttribute("src", "/community/custom-logo.png");
  });

  it("passes an absolute URL through unchanged", () => {
    mockInstance.generalSetting = {
      customProfile: {
        title: "Team Notes",
        description: "Our shared scratchpad.",
        logoUrl: "https://cdn.example.com/team-logo.png",
      },
    };

    const { container } = renderAbout();

    expect(container.querySelector("img")).toHaveAttribute("src", "https://cdn.example.com/team-logo.png");
  });
});
