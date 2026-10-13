import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AuthPageLayout from "@/components/AuthPageLayout";
import { InstanceAccessMode } from "@/types/proto/api/v1/instance_service_pb";

const instance = vi.hoisted(() => ({
  instanceUrl: "https://notes.example.com",
  accessMode: 1,
  customProfile: undefined as { title?: string; logoUrl?: string } | undefined,
}));

const generalSetting = vi.hoisted(() => ({
  // Mutable so the logo tests can set per-render values without reloading
  // the module. Read by the mocked InstanceContext below.
  get customProfile() {
    return instance.customProfile;
  },
}));

vi.mock("@/contexts/InstanceContext", () => ({
  useInstance: () => ({ profile: instance, generalSetting }),
}));

vi.mock("@/utils/i18n", () => ({
  useTranslate: () => (key: string) => key,
  loadLocale: vi.fn(),
}));

vi.mock("@/components/AuthFooter", () => ({ default: () => <div data-testid="auth-footer" /> }));

// Pin BASE_PATH=/community so the basename-aware branch of
// `toBasenameAwareUrl` is exercised. The default Vitest environment ships
// BASE_PATH="/" (Vite's default `base: "/"`), which would make every
// assertion vacuously identity and miss the very regression this test
// suite is here to prevent. See the same fixture pattern in
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

const renderLayout = (props?: { hideExplore?: boolean }) =>
  render(
    <MemoryRouter>
      <AuthPageLayout title="Sign in" {...props}>
        <div />
      </AuthPageLayout>
    </MemoryRouter>,
  );

describe("<AuthPageLayout> explore band", () => {
  beforeEach(() => {
    instance.accessMode = InstanceAccessMode.PRIVATE;
  });

  it("links to Explore on public instances", () => {
    instance.accessMode = InstanceAccessMode.PUBLIC;
    renderLayout();

    expect(screen.getByRole("link", { name: /auth\.explore-public-memos/ })).toHaveAttribute("href", "/explore");
  });

  it("omits the band on private instances even when an instance URL is configured", () => {
    renderLayout();

    expect(screen.queryByRole("link", { name: /auth\.explore-public-memos/ })).not.toBeInTheDocument();
  });

  it("omits the band when hideExplore is set (first-run setup)", () => {
    instance.accessMode = InstanceAccessMode.PUBLIC;
    renderLayout({ hideExplore: true });

    expect(screen.queryByRole("link", { name: /auth\.explore-public-memos/ })).not.toBeInTheDocument();
  });
});

// Logo URL is a separate concern from the explore band: it routes through
// the document origin (an <img src>), not the React Router basename, so it
// needs a basename-aware helper. Pin both the default-fallback branch and
// the user-supplied-relative path under a sub-path mount so a future
// regression to a root-relative string can't slip through.
describe("<AuthPageLayout> logo URL under a sub-path mount", () => {
  beforeEach(() => {
    instance.accessMode = InstanceAccessMode.PRIVATE;
    instance.customProfile = undefined;
  });

  it("prefixes the default fallback `/logo.webp` with the SPA mount path", () => {
    const { container } = renderLayout();

    expect(container.querySelector("img")).toHaveAttribute("src", "/community/logo.webp");
  });

  it("prefixes a user-supplied relative logoUrl with the SPA mount path", () => {
    instance.customProfile = { logoUrl: "/uploads/team-logo.png" };

    const { container } = renderLayout();

    expect(container.querySelector("img")).toHaveAttribute("src", "/community/uploads/team-logo.png");
  });

  it("passes an absolute URL through unchanged", () => {
    instance.customProfile = { logoUrl: "https://cdn.example.com/team-logo.png" };

    const { container } = renderLayout();

    expect(container.querySelector("img")).toHaveAttribute("src", "https://cdn.example.com/team-logo.png");
  });
});
