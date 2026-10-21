import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useMemoActionHandlers } from "@/components/MemoActionMenu/hooks";
import { type Memo, MemoSchema } from "@/types/proto/api/v1/memo_service_pb";

const state = vi.hoisted(() => ({
  profile: { instanceUrl: "" } as { instanceUrl: string },
  copy: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("copy-to-clipboard", () => ({ default: state.copy }));
vi.mock("react-hot-toast", () => ({ default: { success: state.toastSuccess, error: vi.fn() } }));
vi.mock("@/contexts/InstanceContext", () => ({ useInstance: () => ({ profile: state.profile }) }));
vi.mock("@/hooks/useMemoQueries", () => ({
  memoKeys: { detail: (name: string) => ["memo", name] },
  useUpdateMemo: () => ({ mutateAsync: vi.fn() }),
  useDeleteMemo: () => ({ mutateAsync: vi.fn() }),
}));
vi.mock("@/hooks/useUserQueries", () => ({ userKeys: { stats: () => ["stats"] } }));
vi.mock("@/hooks/useNavigateTo", () => ({ default: () => vi.fn() }));
vi.mock("@/utils/i18n", () => ({ useTranslate: () => (key: string) => key }));

const wrapper = ({ children }: { children: ReactNode }) => (
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <MemoryRouter initialEntries={["/explore"]}>{children}</MemoryRouter>
  </QueryClientProvider>
);

const memoOf = (fields: Parameters<typeof create<typeof MemoSchema>>[1] = {}) => create(MemoSchema, { name: "memos/abc123", ...fields });

const copyLinkFor = (memo: Memo) => {
  const { result } = renderHook(() => useMemoActionHandlers({ memo, setDeleteDialogOpen: vi.fn() }), { wrapper });
  result.current.handleCopyLink();
  expect(state.toastSuccess).toHaveBeenCalledWith("message.succeed-copy-link");
  expect(state.copy).toHaveBeenCalledOnce();
  return state.copy.mock.calls[0][0] as string;
};

describe("MemoActionMenu copy link", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    state.profile = { instanceUrl: "" };
  });

  // Regression: with `instanceUrl` set the router path was concatenated
  // onto the host without its leading slash, so the copied URL was
  // `https://example.commemos/abc123` — not a URL at all.
  it("joins the configured instance URL and the memo path with a slash", () => {
    state.profile = { instanceUrl: "https://example.com/community" };
    expect(copyLinkFor(memoOf())).toBe("https://example.com/community/memos/abc123");
  });

  it("strips a trailing slash from the configured instance URL", () => {
    state.profile = { instanceUrl: "https://example.com/community///" };
    expect(copyLinkFor(memoOf())).toBe("https://example.com/community/memos/abc123");
  });

  it("uses the SPA origin and base path when no instance URL is configured", () => {
    expect(copyLinkFor(memoOf())).toBe(`${window.location.origin}/memos/abc123`);
  });

  // A comment's link points at the comment inside its parent memo.
  it("anchors a comment link on its parent memo", () => {
    state.profile = { instanceUrl: "https://example.com" };
    expect(copyLinkFor(memoOf({ name: "memos/comment1", parent: "memos/parent1" }))).toBe("https://example.com/memos/parent1#comment1");
  });
});
