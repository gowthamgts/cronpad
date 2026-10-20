import { defineConfig } from "@playwright/test";

const port = Number(process.env.E2E_APP_PORT ?? 18090);

// Uses the Google Chrome installed on the machine (no browser download).
export default defineConfig({
  testDir: "./e2e",
  testMatch: "*.spec.ts",
  timeout: 90_000,
  expect: { timeout: 10_000 },
  workers: 1,
  reporter: [["list"]],
  use: { baseURL: `http://127.0.0.1:${port}`, channel: "chrome", trace: "retain-on-failure" },
  webServer: {
    command: "node e2e/serve.mjs",
    url: `http://127.0.0.1:${port}/healthz`,
    reuseExistingServer: false,
    timeout: 30_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
