// Lighthouse accessibility audit of every dashboard page, run with the installed Google Chrome
// against the single built binary. Exits non-zero if any page scores below the threshold.
import { mkdirSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import lighthouse from "lighthouse";
import { launch } from "chrome-launcher";
import { startStack } from "./harness.mjs";

const THRESHOLD = 100;
const outDir = fileURLToPath(new URL("../.lighthouse/", import.meta.url));

const stack = await startStack();
const chrome = await launch({ chromeFlags: ["--headless=new", "--no-sandbox", "--disable-gpu"] });
let failed = false;

try {
  // Sign in with the dev login and keep the session + CSRF cookies.
  const login = await fetch(`${stack.baseURL}/auth/dev/login`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ name: "lighthouse" }),
  });
  if (login.status !== 204) throw new Error(`dev login failed: ${login.status}`);
  const cookies = login.headers.getSetCookie().map((c) => c.split(";")[0]);
  const cookie = cookies.join("; ");
  const csrf = cookies.find((c) => c.startsWith("cp_csrf="))?.slice("cp_csrf=".length) ?? "";
  const api = async (method, path, body) => {
    const res = await fetch(`${stack.baseURL}${path}`, {
      method,
      headers: { Cookie: cookie, "X-CSRF-Token": csrf, "Content-Type": "application/json" },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
    return res.status === 204 ? null : res.json();
  };

  // Seed one successful and one failed run so the detail pages have real content.
  const ok = await api("POST", "/api/v1/jobs", { name: "lh-ok", schedule: { expr: "every 30 minutes" }, sink: { kind: "webhook", config: { url: `${stack.echoURL}/ok` } }, tags: ["audit"] });
  const bad = await api("POST", "/api/v1/jobs", { name: "lh-failing", schedule: { expr: "every hour" }, sink: { kind: "webhook", config: { url: `${stack.echoURL}/fail` } }, retry: { max_attempts: 1 } });
  const okRun = (await api("POST", `/api/v1/jobs/${ok.id}/trigger`)).run_id;
  const badRun = (await api("POST", `/api/v1/jobs/${bad.id}/trigger`)).run_id;
  await api("PUT", "/api/v1/secrets/AUDIT_TOKEN", { value: "x" });
  for (const [id, want] of [[okRun, "succeeded"], [badRun, "failed"]]) {
    for (let i = 0; i < 100; i += 1) {
      if ((await api("GET", `/api/v1/runs/${id}`)).status === want) break;
      await new Promise((r) => setTimeout(r, 300));
    }
  }

  const pages = [
    ["login", "/login", false],
    ["dashboard", "/", true],
    ["jobs", "/jobs", true],
    ["job-new", "/jobs/new", true],
    ["job-detail", `/jobs/${ok.id}`, true],
    ["job-edit", `/jobs/${ok.id}/edit`, true],
    ["run-ok", `/runs/${okRun}`, true],
    ["run-failed", `/runs/${badRun}`, true],
    ["alerts", "/alerts", true],
    ["settings", "/settings", true],
  ];

  const scores = {};
  for (const [name, path, authed] of pages) {
    const result = await lighthouse(
      `${stack.baseURL}${path}`,
      {
        port: chrome.port,
        output: "json",
        logLevel: "error",
        onlyCategories: ["accessibility"],
        formFactor: "desktop",
        screenEmulation: { mobile: false, width: 1350, height: 940, deviceScaleFactor: 1, disabled: false },
        extraHeaders: authed ? { Cookie: cookie } : undefined,
      },
    );
    const lhr = result.lhr;
    const score = Math.round((lhr.categories.accessibility.score ?? 0) * 100);
    const failing = Object.values(lhr.audits)
      .filter((a) => a.score !== null && a.score < 1 && a.scoreDisplayMode !== "informative" && a.scoreDisplayMode !== "notApplicable")
      .map((a) => a.id);
    scores[name] = { path, score, failing };
    // A redirect to the sign-in page would score well and prove nothing.
    const landed = new URL(lhr.finalDisplayedUrl).pathname;
    if (landed !== path) throw new Error(`${name}: audited ${landed}, expected ${path}`);
    if (score < THRESHOLD) failed = true;
    console.log(`${name.padEnd(12)} ${String(score).padStart(3)}  ${path}${failing.length ? `   (not passing: ${failing.join(", ")})` : ""}`);
  }
  mkdirSync(outDir, { recursive: true });
  writeFileSync(`${outDir}scores.json`, `${JSON.stringify(scores, null, 2)}\n`);
} finally {
  await chrome.kill();
  await stack.stop();
}

if (failed) {
  console.error(`Lighthouse accessibility score below ${THRESHOLD} on at least one page`);
  process.exit(1);
}
