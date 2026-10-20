// Starts the single built binary (bin/cronpad) with a temp SQLite directory, dev login and a local
// echo server for webhook targets.
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import http from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const APP_PORT = Number(process.env.E2E_APP_PORT ?? 18090);
export const ECHO_PORT = Number(process.env.E2E_ECHO_PORT ?? 18091);

const binary = fileURLToPath(new URL("../../bin/cronpad", import.meta.url));

export async function startStack() {
  if (!existsSync(binary)) throw new Error(`${binary} not found: run \`just build\` first`);
  const dir = mkdtempSync(path.join(tmpdir(), "cronpad-e2e-"));
  const alerts = [];
  const echo = http.createServer((req, res) => {
    if (req.method === "GET" && req.url === "/alerts") {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify(alerts));
      return;
    }
    let body = "";
    req.on("data", (chunk) => { body += chunk.toString(); });
    req.on("end", () => {
      if (req.url === "/alert") alerts.push({ body: JSON.parse(body), id: req.headers["webhook-id"] });
      const failing = req.url?.startsWith("/fail");
      res.writeHead(failing ? 500 : 200, { "Content-Type": "application/json" });
      res.end(failing ? JSON.stringify({ error: "boom" }) : JSON.stringify({ ok: true, path: req.url }));
    });
  });
  await new Promise((resolve) => echo.listen(ECHO_PORT, "127.0.0.1", resolve));

  // CRONPAD_* variables are server configuration; keep the test-only ones out of the child's environment.
  const inherited = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("CRONPAD_TEST_")));
  const child = spawn(binary, ["serve"], {
    env: {
      ...inherited,
      CRONPAD_SERVER__ADDR: `127.0.0.1:${APP_PORT}`,
      CRONPAD_SERVER__PUBLIC_URL: `http://127.0.0.1:${APP_PORT}`,
      CRONPAD_DATABASE__PATH: path.join(dir, "control.db"),
      CRONPAD_DATABASE__HISTORY_PATH: path.join(dir, "history.db"),
      CRONPAD_QUEUE__PATH: path.join(dir, "queue.db"),
      CRONPAD_AUTH__DEV_LOGIN: "true",
      CRONPAD_SECRETS__ENCRYPTION_KEY: randomBytes(32).toString("base64"),
      CRONPAD_LOGGING__FORMAT: "logfmt",
      // The echo server is on loopback; allow it even if a future default tightens this.
      CRONPAD_SINKS__WEBHOOK__ALLOW_PRIVATE_NETWORKS: "true",
      CRONPAD_TELEMETRY__PROMETHEUS_ADDR: `127.0.0.1:${APP_PORT + 100}`,
    },
    stdio: ["ignore", "inherit", "inherit"],
  });
  let stopped = false;
  child.on("exit", (code) => {
    if (!stopped) console.error(`cronpad exited unexpectedly with code ${code}`);
  });

  const baseURL = `http://127.0.0.1:${APP_PORT}`;
  let readiness = "server never listened";
  let ready = false;
  const startupDeadline = Date.now() + 10_000;
  for (let i = 0; i < 100 && Date.now() < startupDeadline; i += 1) {
    if (child.exitCode !== null) break;
    try {
      const response = await fetch(`${baseURL}/readyz`, { signal: AbortSignal.timeout(1500) });
      readiness = await response.text();
      if (response.ok && JSON.parse(readiness).dispatcher === "active") { ready = true; break; }
    } catch (error) { readiness = error.message; }
    await new Promise((r) => setTimeout(r, 100));
  }
  if (!ready) {
    stopped = true;
    child.kill("SIGTERM");
    if (child.exitCode === null && child.signalCode === null) await new Promise((resolve) => child.once("exit", resolve));
    await new Promise((resolve) => echo.close(resolve));
    rmSync(dir, { recursive: true, force: true });
    throw new Error(`E2E startup failed: /readyz never reported an active dispatcher. Last readiness: ${readiness}`);
  }

  return {
    baseURL,
    echoURL: `http://127.0.0.1:${ECHO_PORT}`,
    async stop() {
      stopped = true;
      child.kill("SIGTERM");
      if (child.exitCode === null && child.signalCode === null) await new Promise((resolve) => child.once("exit", resolve));
      await new Promise((resolve) => echo.close(resolve));
      rmSync(dir, { recursive: true, force: true });
    },
  };
}
