// Where the Vite dev server forwards API calls. riff's PocketBase
// serves the Connect/gRPC, `/api`, and `/file/*` endpoints on
// `:8090` (see main.go's PB_PORT handling and the `8090` references
// in fly.toml / scripts/Caddyfile). The Memos-Go upstream listened
// on `:8081`, so this default used to point at a closed port and
// every connect-web call in `pnpm dev` 404'd. Override with
// `DEV_PROXY_SERVER=http://host:port pnpm dev` when running the
// backend in a container or on a different host. The `pnpm dev`
// script doesn't set this, so a clean `pnpm install && pnpm dev`
// Just Works against a riff server bound on the local 8090.
//
// Single source of truth: `web/vite.config.mts` reads
// `process.env.DEV_PROXY_SERVER` at import time and falls back to
// `DEFAULT_DEV_PROXY_SERVER`. `resolveDevProxyServer` is the
// pure resolver; the regression test in
// `tests/vite-dev-proxy.test.ts` pins both halves so a future
// revert to the Memos `:8081` default fails loudly.
export const DEFAULT_DEV_PROXY_SERVER = "http://localhost:8090";

/**
 * Resolve the dev proxy target. Returns the override from
 * `process.env.DEV_PROXY_SERVER` if it is set and non-empty, else
 * the riff default. Pure-ish (reads `process.env`) so it can be
 * exercised from a vitest setup without booting Vite.
 */
export function resolveDevProxyServer(env: NodeJS.ProcessEnv = process.env): string {
  const override = env.DEV_PROXY_SERVER;
  if (typeof override === "string" && override.length > 0) {
    return override;
  }
  return DEFAULT_DEV_PROXY_SERVER;
}
