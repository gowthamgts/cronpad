import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The dashboard is served by the Go binary at /; during `just dev` Vite proxies
// API and auth calls to the Go server.
export default defineConfig({
  base: "/",
  plugins: [
    react(),
    tailwindcss(),
    {
      // In production Go adds this meta tag to index.html (hosted mode); `just dev-hosted` sets the variable
      // so the Vite-served page shares its theme cookie with the local site the same way.
      name: "dev-cookie-domain",
      apply: "serve",
      transformIndexHtml(html) {
        const domain = process.env.DEV_COOKIE_DOMAIN;
        return domain ? html.replace("<head>", `<head>\n    <meta name="cronpad-cookie-domain" content="${domain}" />`) : html;
      },
    },
    {
      // emptyOutDir removes dist/.gitkeep, which keeps `go:embed all:dist` compiling without a web build.
      name: "dist-support-files",
      apply: "build",
      generateBundle() {
        this.emitFile({
          type: "asset",
          fileName: "licenses/maple-mono-OFL.txt",
          source: readFileSync(new URL("./packages/design/fonts/OFL.txt", import.meta.url), "utf8"),
        });
      },
      closeBundle() {
        writeFileSync(path.resolve(import.meta.dirname, "dist/.gitkeep"), "");
      },
    },
  ],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "src") } },
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 600 },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
      "/auth": "http://localhost:8080",
      "/mcp": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
      "/readyz": "http://localhost:8080",
      "/metrics": "http://localhost:8080",
      "/llms.txt": "http://localhost:8080",
      "/robots.txt": "http://localhost:8080",
      "/version": "http://localhost:8080",
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
