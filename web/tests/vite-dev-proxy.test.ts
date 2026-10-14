import { describe, expect, it } from "vitest";
import { DEFAULT_DEV_PROXY_SERVER, resolveDevProxyServer } from "@/lib/dev-proxy-server";

// These tests guard the Vite dev server's `server.proxy` block, which
// forwards /api, /api/v1/sse, /memos.api.v1, and /file to whatever
// `devProxyServer` resolves to. The repro for the 404 on
// `memos.api.v1.InstanceService/GetInstanceSetting` is straightforward:
// the upstream Memos project served on :8081, so the vite config
// defaulted to `http://localhost:8081`. riff's PocketBase serves on
// :8090, so a fresh `pnpm install && pnpm dev` against a riff backend
// forwarded every connect-web POST to a closed port and the browser
// saw a 404. This file pins the default to 8090 and the override
// semantics for the future.
//
// We exercise `resolveDevProxyServer` directly instead of importing
// `web/vite.config.mts` because the config is a `.mts` outside the
// tsconfig include path, and the resolver is the single source of
// truth — the config just calls it once at import time.

describe("web/src/lib/dev-proxy-server", () => {
  it("defaults to PocketBase's :8090 so a fresh dev install Just Works", () => {
    expect(DEFAULT_DEV_PROXY_SERVER).toBe("http://localhost:8090");
  });

  it("returns the default when DEV_PROXY_SERVER is unset", () => {
    expect(resolveDevProxyServer({})).toBe(DEFAULT_DEV_PROXY_SERVER);
  });

  it("returns the default when DEV_PROXY_SERVER is an empty string", () => {
    expect(resolveDevProxyServer({ DEV_PROXY_SERVER: "" })).toBe(DEFAULT_DEV_PROXY_SERVER);
  });

  it("returns the override when DEV_PROXY_SERVER is set (container / remote backend)", () => {
    expect(resolveDevProxyServer({ DEV_PROXY_SERVER: "http://riff.internal:9000" })).toBe("http://riff.internal:9000");
  });

  it("falls back to the default for non-string DEV_PROXY_SERVER values (defensive)", () => {
    // process.env coerces to string at the Node level, but a hostile
    // injection via `pnpm cross-env DEV_PROXY_SERVER=...` could land
    // a non-string; the resolver should still produce a string.
    expect(resolveDevProxyServer({ DEV_PROXY_SERVER: 42 as unknown as string | undefined })).toBe(DEFAULT_DEV_PROXY_SERVER);
  });
});
