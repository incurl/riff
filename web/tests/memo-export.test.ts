import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { exportMemos, stageMemoImport } from "@/lib/memo-export";

const { exportRequest, importRequest, download } = vi.hoisted(() => ({
  exportRequest: vi.fn(),
  importRequest: vi.fn(),
  download: vi.fn(),
}));

vi.mock("@/connect", () => ({ userServiceClient: { exportMemos: exportRequest, importMemos: importRequest } }));
vi.mock("@/lib/browser", () => ({ downloadFileFromUrl: download }));

// Deterministic, non-trivial bytes per case so the upload pipeline
// is exercised with multi-chunk writes (mock maxChunkSize is 1024).
// The values are arbitrary; stageMemoImport only depends on file.size
// and forwards slices verbatim, so the test verifies chunking
// behaviour, not the byte values themselves.
function makeBytes(seed: number, length: number): Uint8Array {
  const out = new Uint8Array(length);
  for (let i = 0; i < length; i++) {
    out[i] = (seed * 31 + i * 7) & 0xff;
  }
  return out;
}

describe("memo export files", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("downloads a ZIP with the export name and releases its object URL", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-20T08:30:00Z"));
    const createURL = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:export");
    const revokeURL = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    exportRequest.mockResolvedValue({ contentType: "application/vnd.usememos.export+zip", data: new Uint8Array([80, 75]) });

    await exportMemos("users/alice", "alice");

    expect(exportRequest).toHaveBeenCalledWith({ name: "users/alice" });
    expect(createURL.mock.calls[0][0]).toMatchObject({ type: "application/vnd.usememos.export+zip", size: 2 });
    expect(download).toHaveBeenCalledWith("blob:export", "memos-export-alice-20260920T083000Z.zip");
    expect(revokeURL).not.toHaveBeenCalled();
    await vi.runAllTimersAsync();
    expect(revokeURL).toHaveBeenCalledWith("blob:export");
  });

  it.each([
    [1, 4096, "memos-export-alice.zip", "application/vnd.usememos.export+zip"],
    [2, 3072, "memos-archive-alice.zip", "application/vnd.usememos.archive+zip"],
    [2, 3072, "my-notes.zip", "application/zip"],
  ])("uploads %i bytes as %s without filtering by the filename or MIME type", async (seed, length, name, type) => {
    const bytes = Buffer.from(makeBytes(seed, length));
    const file = new File([bytes], name, { type });
    const plan = { memos: 3 };
    importRequest.mockImplementation(async (request) => ({
      uploadId: "upload-id",
      maxChunkSize: 1024,
      committedSize: request.data ? request.writeOffset + BigInt(request.data.length) : 0n,
      result: request.finishWrite ? { case: "plan", value: plan } : { case: undefined },
    }));

    await expect(stageMemoImport("users/alice", file)).resolves.toEqual({ uploadId: "upload-id", size: bytes.length, plan });

    const chunks = importRequest.mock.calls.slice(1).map(([request]) => request.data);
    expect(Buffer.concat(chunks)).toEqual(bytes);
    expect(importRequest.mock.lastCall?.[0]).toMatchObject({ finishWrite: true, validateOnly: true });
  });
});
