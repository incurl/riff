import babel from "@rolldown/plugin-babel";
import tailwindcss from "@tailwindcss/vite";
import react, { reactCompilerPreset } from "@vitejs/plugin-react";
import { resolve } from "path";
import { defineConfig } from "vite";
import { resolveDevProxyServer } from "./src/lib/dev-proxy-server";

const devProxyServer = resolveDevProxyServer();
if (process.env.DEV_PROXY_SERVER && process.env.DEV_PROXY_SERVER.length > 0) {
  console.log("Use devProxyServer from environment: ", process.env.DEV_PROXY_SERVER);
}

// https://vite.dev/config/
export default defineConfig({
  // The base path Vite injects into index.html and into
  // `import.meta.env.BASE_URL`. Defaults to "/" so local dev and a
  // root-mounted deployment work unchanged; override with
  // VITE_BASE_PATH=/community/ pnpm release (see scripts/Dockerfile,
  // which does NOT pass the env var by default). The companion
  // Connect transport in web/src/connect.ts reads BASE_URL at
  // runtime and concatenates it to window.location.origin, so the
  // Vite `base` and the connect-web `baseUrl` stay in lockstep.
  base: process.env.VITE_BASE_PATH || "/",
  plugins: [react(), babel({ presets: [reactCompilerPreset()] }), tailwindcss()],
  server: {
    host: "0.0.0.0",
    port: 3001,
    proxy: {
      "^/api/v1/sse": {
        target: devProxyServer,
        xfwd: true,
        // SSE requires no response buffering and longer timeout.
        timeout: 0,
      },
      "^/api": {
        target: devProxyServer,
        xfwd: true,
      },
      "^/memos.api.v1": {
        target: devProxyServer,
        xfwd: true,
      },
      "^/file": {
        target: devProxyServer,
        xfwd: true,
      },
    },
  },
  resolve: {
    alias: {
      "@/": `${resolve(__dirname, "src")}/`,
    },
  },
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              name: "utils-vendor",
              test: /node_modules[\\/](dayjs|lodash-es)([\\/]|$)/,
            },
            {
              name: "leaflet-vendor",
              test: /node_modules[\\/]leaflet([\\/]|$)/,
            },
          ],
        },
      },
    },
  },
});
