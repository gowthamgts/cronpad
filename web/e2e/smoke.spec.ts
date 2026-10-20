import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

const ECHO = `http://127.0.0.1:${Number(process.env.E2E_ECHO_PORT ?? 18091)}/hook`;

/** Zero serious or critical axe violations on the current page. */
// The header theme button cycles system → light → dark; click until it shows the wanted one.
async function setTheme(page: Page, name: "System" | "Light" | "Dark") {
  const toggle = page.getByRole("button", { name: /^Theme: / });
  for (let i = 0; i < 3 && !(await toggle.getAttribute("aria-label"))?.startsWith(`Theme: ${name}.`); i++) await toggle.click();
  await expect(toggle).toHaveAttribute("aria-label", new RegExp(`^Theme: ${name}\\.`));
}

async function expectAccessible(page: Page, label: string) {
  // Let any theme or layout change paint before sampling colours.
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"]).analyze();
  const bad = results.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  const summary = bad.map((v) => `${v.id} (${v.impact}): ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`);
  expect(summary, `axe violations on ${label}`).toEqual([]);
}

test("log in, create a job from plain English, trigger it and see the run", async ({ page }) => {
  // Reduced motion also stops colour transitions, which would otherwise make axe sample half-faded colours.
  await page.emulateMedia({ reducedMotion: "reduce" });
  const problems: string[] = [];
  let signedIn = false;
  page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    // The anonymous session probe returns 401 before login; typo previews return 400.
    const expectedStatus = /^Failed to load resource: the server responded with a status of 400/.test(m.text()) || (!signedIn && /^Failed to load resource: the server responded with a status of 401/.test(m.text()));
    if ((m.type() === "error" && !expectedStatus) || /content security policy/i.test(m.text())) problems.push(`console: ${m.text()}`);
  });

  // The binary serves the dashboard directly at the root.
  const rootResponse = await page.request.get("/", { maxRedirects: 0 });
  expect(rootResponse.status()).toBe(200);
  expect(rootResponse.headers()["content-type"]).toContain("text/html");
  // A fresh server has no users, so sign-in starts with onboarding, even from /login.
  await page.goto("/");
  await expect(page).toHaveURL(/\/setup/);
  await page.goto("/login");
  await expect(page).toHaveURL(/\/setup/);
  await expect(page.getByRole("heading", { name: "Set up cronpad" })).toBeVisible();
  await expectAccessible(page, "setup");

  await page.getByLabel("Name").fill("e2e-user");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Create your first job" })).toBeVisible();
  signedIn = true;
  await expect(page.getByText("curl -X POST").first()).toBeVisible();
  await expectAccessible(page, "dashboard (empty)");

  // Create a job with a natural-language schedule and watch the live preview.
  await page.getByRole("link", { name: "New job" }).first().click();
  await expect(page.getByRole("heading", { name: "New job" })).toBeVisible();
  await expectAccessible(page, "new job");
  await page.getByRole("button", { name: "Create job", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("Check these fields");
  await expect(page.getByLabel("Name", { exact: true })).toBeFocused();
  await page.getByRole("link", { name: "Schedule: Schedule is required", exact: true }).click();
  await expect(page.getByLabel("Schedule", { exact: true })).toBeFocused();
  await expectAccessible(page, "new job with errors");
  await page.getByLabel("Name", { exact: true }).fill("e2e-heartbeat");
  await page.getByLabel("Schedule", { exact: true }).fill("every 5 minutes");
  await expect(page.getByTestId("schedule-description")).toContainText(/5 minutes/i);
  await expect(page.getByTestId("fires-job").locator("li")).toHaveCount(5);

  // Timezone search accepts the legacy name while storing the current IANA name.
  await page.getByRole("button", { name: /^Time zone/ }).click();
  await page.getByRole("combobox", { name: "Search time zone" }).fill("Calcutta");
  await expect(page.getByRole("option", { name: /Asia\/Kolkata/ })).toBeVisible();
  await expectAccessible(page, "timezone combobox open");
  await page.getByRole("option", { name: /Asia\/Kolkata/ }).click();
  await expect(page.getByRole("button", { name: /^Time zone/ })).toContainText("Asia/Kolkata");

  // A typo gets an underline and a suggestion instead of a generic failure.
  await page.getByLabel("Schedule", { exact: true }).fill("every tuesdy at 9am");
  await expect(page.getByTestId("schedule-underline")).toHaveText("tuesdy");
  await expectAccessible(page, "new job with schedule error");
  await page.getByLabel("Schedule", { exact: true }).fill("every 5 minutes");
  await expect(page.getByTestId("schedule-description")).toContainText(/5 minutes/i);

  // Switching syntax converts the expression, and the server accepts every converted form.
  const schedule = page.getByLabel("Schedule", { exact: true });
  const syntax = (name: string) => page.getByRole("radio", { name, exact: true }).check({ force: true });
  await page.getByRole("button", { name: "weekdays 9am", exact: true }).click();
  for (const [mode, expr] of [
    ["Cron", "0 9 * * 1-5"],
    ["Quartz", "0 0 9 ? * MON-FRI"],
    ["RRULE", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0"],
    ["Natural", "every weekday at 09:00"],
  ] as const) {
    await syntax(mode);
    await expect(schedule).toHaveValue(expr);
    await expect(page.getByTestId("schedule-description")).toBeVisible();
    await expect(page.getByTestId("schedule-notice")).toHaveCount(0);
  }
  // Presets follow the selected syntax; Natural can't pick a minute, so the hourly preset warns.
  await page.getByRole("button", { name: "hourly", exact: true }).click();
  await expect(schedule).toHaveValue("every hour");
  await expect(page.getByTestId("schedule-notice")).toContainText("top of the hour");
  await syntax("Cron");
  await page.getByRole("button", { name: "hourly", exact: true }).click();
  await expect(schedule).toHaveValue(/^\d+ \* \* \* \*$/);
  await expect(page.getByTestId("schedule-notice")).toHaveCount(0);
  // An expression the target can't express is kept as typed, with a warning.
  await syntax("Natural");
  await expect(schedule).toHaveValue(/^\d+ \* \* \* \*$/);
  await expect(page.getByTestId("schedule-notice")).toContainText("Couldn't convert");
  await expect(page.locator('[aria-busy="false"]')).toBeVisible(); // preview settled, not dimmed mid-refresh
  await expectAccessible(page, "new job with conversion warning");
  await syntax("Auto");
  await schedule.fill("every 5 minutes");
  await expect(page.getByTestId("schedule-notice")).toHaveCount(0);
  await expect(page.getByTestId("schedule-description")).toContainText(/5 minutes/i);

  await page.getByLabel("URL").fill("ftp://example.com");
  await expect(page.getByRole("alert")).toContainText("http:// or https://");
  await page.getByRole("button", { name: /^Method/ }).click();
  await page.getByRole("option", {name:"GET",exact:true}).click();
  await expect(page.getByLabel("Body", { exact: true })).toBeDisabled();
  await page.getByRole("button", { name: /^Method/ }).click();
  await page.getByRole("option", {name:"POST",exact:true}).click();
  await page.getByRole("button", { name: "+ JSON content type", exact: true }).click();
  await expect(page.getByLabel("Header 1 value", { exact: true })).toHaveValue("application/json");
  await page.getByLabel("URL").fill(ECHO);
  await page.getByRole("button", { name: "Send test" }).click();
  await expect(page.getByText("Test succeeded")).toBeVisible();

  await page.getByRole("button", { name: "Create job" }).click();
  const secret = page.getByRole("dialog", { name: "Signing secret" });
  await expect(secret).toBeVisible();
  await expect(secret.getByText(/whsec_/)).toBeVisible();
  await expectAccessible(page, "signing secret dialog");
  await secret.getByRole("button", { name: "I’ve saved it" }).click();

  // Job detail: trigger it and follow the run.
  await expect(page.getByRole("heading", { name: "e2e-heartbeat" })).toBeVisible();
  await expectAccessible(page, "job detail");
  await page.getByRole("link", { name: "Edit", exact: true }).click();
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("e2e-heartbeat");
  await expect(page.getByLabel("URL", { exact: true })).toHaveValue(ECHO);
  await expect(page.getByTestId("schedule-description")).toBeVisible();
  await expectAccessible(page, "edit webhook");
  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await setTheme(page, "Dark");
  await expectAccessible(page, "edit webhook (phone, dark)");
  await setTheme(page, "Light");
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.getByLabel("Description (optional)").fill("Edited from the dashboard");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(page.getByRole("heading", { name: /^(\/\/ )?e2e-heartbeat$/ })).toBeVisible();
  await page.getByRole("button", { name: "Trigger now" }).click();
  await expect(page.getByText("Run queued.")).toBeVisible();
  await page.getByRole("tab", { name: "Runs" }).click();
  await expect(page.getByRole("link", { name: /^Run run_.*succeeded/ }).first()).toBeVisible({ timeout: 30_000 });
  await expectAccessible(page, "job runs");
  await page.getByRole("link", { name: /^Run run_.*succeeded/ }).first().click();

  await expect(page.getByRole("heading", { name: /^(\/\/ )?run run_/i })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Attempt 1" })).toBeVisible();
  await expect(page.getByText("HTTP 200")).toBeVisible();
  await expectAccessible(page, "run detail");

  // A failing endpoint: the run detail shows the error, the request and the response body.
  const csrf = (await page.context().cookies()).find((c) => c.name === "cp_csrf")?.value ?? "";
  const channelResponse = await page.request.post("/api/v1/alert-channels", {
    headers: { "X-CSRF-Token": csrf },
    data: { kind: "webhook", name: "e2e alerts", config: { url: ECHO.replace("/hook", "/alert"), format: "generic" } },
  });
  expect(channelResponse.status(), await channelResponse.text()).toBe(201);
  const alertChannel = await channelResponse.json() as { id: string; signing_secret: string };
  expect(alertChannel.signing_secret).toMatch(/^whsec_/);
  const ruleResponse = await page.request.post("/api/v1/alert-rules", {
    headers: { "X-CSRF-Token": csrf },
    data: { name: "e2e failures", job_selector: { all: true }, events: ["run.failed"], channel_ids: [alertChannel.id], cooldown: "15m" },
  });
  expect(ruleResponse.status(), await ruleResponse.text()).toBe(201);
  const created = await page.request.post("/api/v1/jobs", {
    headers: { "X-CSRF-Token": csrf },
    data: { name: "e2e-failing", schedule: { expr: "every 30 minutes" }, sink: { kind: "webhook", config: { url: `${ECHO.replace("/hook", "")}/fail` } }, retry: { max_attempts: 1 } },
  });
  expect(created.ok(), await created.text()).toBeTruthy();
  const failingJob = (await created.json()) as { id: string };
  const trigger = await page.request.post(`/api/v1/jobs/${failingJob.id}/trigger`, { headers: { "X-CSRF-Token": csrf } });
  const { run_id: failedRun } = (await trigger.json()) as { run_id: string };
  await expect
    .poll(async () => ((await (await page.request.get(`/api/v1/runs/${failedRun}`)).json()) as { status: string }).status, { timeout: 30_000 })
    .toBe("failed");
  await expect.poll(async () => {
    const response = await page.request.get(ECHO.replace("/hook", "/alerts"));
    const alerts = await response.json() as { body: { event: string; run: { id: string } } }[];
    return alerts.some((a) => a.body.event === "run.failed" && a.body.run.id === failedRun);
  }, { timeout: 5000 }).toBe(true);
  await page.goto(`/runs/${failedRun}`);
  await expect(page.getByRole("heading", { name: "Attempt 1" })).toBeVisible();
  await expect(page.getByText("HTTP 500")).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry run" })).toBeVisible();
  await page.getByRole("button", { name: "Response body" }).click();
  await expect(page.getByLabel("Attempt 1 response body")).toContainText("boom");
  await expectAccessible(page, "failed run detail");

  // The other pages.
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  const row = page.getByRole("row", { name: /e2e-heartbeat/ });
  await expect(row).toBeVisible();
  await expectAccessible(page, "jobs");
  await row.getByRole("switch").click();
  await expect(row.getByText("paused", { exact: true })).toBeVisible();
  await page.getByRole("searchbox", { name: "Search" }).fill("nothing-matches");
  await expect(page.getByText("No jobs match these filters.")).toBeVisible();
  await page.getByRole("searchbox", { name: "Search" }).fill("");

  await page.getByRole("link", { name: "Dashboard" }).click();
  await expect(page.getByRole("heading", { name: "runs per hour" })).toBeVisible();
  await expectAccessible(page, "dashboard");

  await page.getByRole("link", { name: "Alerts" }).click();
  await expect(page.getByRole("heading", { name: "e2e alerts" })).toBeVisible();
  await page.getByRole("tab", { name: "Rules", exact: true }).click();
  await expect(page.getByRole("heading", { name: "e2e failures" })).toBeVisible();
  await expectAccessible(page, "alerts");

  await page.getByRole("link", { name: "Settings" }).click();
  await expect(page.getByRole("heading", { name: "API keys", exact: true })).toBeVisible();
  await expectAccessible(page, "settings");
  await page.getByRole("button", { name: "New key" }).click();
  await page.getByRole("dialog").getByLabel("Name").fill("ci");
  await page.getByRole("dialog").getByRole("button", { name: "Create key" }).click();
  await expect(page.getByRole("dialog", { name: "Your new API key" }).getByText(/cp_[A-Za-z0-9]{8}_/)).toBeVisible();
  await page.getByRole("button", { name: "I’ve saved it" }).click();
  await expect(page.getByRole("row", { name: /ci/ })).toBeVisible();

  await page.getByRole("tab", { name: "Secrets" }).click();
  await page.getByLabel("Name", { exact: true }).fill("lowercase");
  await page.getByLabel("Value").fill("hunter2");
  await page.getByRole("button", { name: "Save secret" }).click();
  await expect(page.getByText(/UPPER_SNAKE_CASE/)).toBeVisible();
  await page.getByLabel("Name", { exact: true }).fill("REPORTS_TOKEN");
  await page.getByRole("button", { name: "Save secret" }).click();
  await expect(page.getByRole("cell", { name: "REPORTS_TOKEN", exact: true })).toBeVisible();
  await expectAccessible(page, "settings secrets");

  await page.getByRole("tab", { name: "Workspace" }).click();
  await page.getByLabel("Name", { exact: true }).fill("Renamed workspace");
  await page.getByRole("button", { name: "Save workspace" }).click();
  await expect(page.getByText("Saved.")).toBeVisible();

  // Dark mode keeps contrast.
  await setTheme(page, "Dark");
  await expect(page.locator("html")).toHaveClass(/dark/);
  await expectAccessible(page, "settings (dark)");

  expect(problems).toEqual([]);
});

test("the dashboard serves SPA routes and security headers", async ({ request }) => {
  const response = await request.get("/jobs/job_does_not_matter");
  expect(response.status()).toBe(200);
  expect(response.headers()["cache-control"]).toBe("no-cache");
  expect(response.headers()["content-security-policy"]).toContain("script-src 'self'");
  const asset = /\/assets\/[^"]+\.js/.exec(await (await request.get("/")).text());
  expect(asset).not.toBeNull();
  expect((await request.get(asset![0])).headers()["cache-control"]).toContain("immutable");
  const legacy = await request.get("/app/jobs?state=paused", { maxRedirects: 0 });
  expect(legacy.status()).toBe(308);
  expect(legacy.headers().location).toBe("/jobs?state=paused");
  const missingAPI = await request.get("/api/unknown");
  expect(missingAPI.status()).toBe(404);
  expect(await missingAPI.json()).toMatchObject({ error: { code: "not_found" } });
  expect((await request.get("/healthz")).status()).toBe(200);
  expect((await request.get("/version")).headers()["content-type"]).toContain("application/json");
});

test("self-hosted command editor tests, creates and runs a command", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  // A user exists now, so setup hands over to the ordinary sign-in page.
  await page.goto("/setup");
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expectAccessible(page, "login");
  await page.getByLabel("Name").fill("e2e-command");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByRole("link", { name: "New job" }).first().click();
  await page.getByLabel("Name", { exact: true }).fill("e2e-command");
  await page.getByLabel("Schedule", { exact: true }).fill("every hour");
  await page.getByRole("radio", { name: "Command", exact: true }).press("Space");
  await page.getByRole("textbox", { name: "Command", exact: true }).fill("/bin/echo");
  await page.getByLabel("Arguments", { exact: true }).fill("hello-command");
  await page.getByRole("button", { name: "Add variable" }).click();
  await page.getByLabel("Variable 1 name", { exact: true }).fill("EXAMPLE");
  await page.getByLabel("Variable 1 value", { exact: true }).fill("value");
  await expectAccessible(page, "command editor");
  await page.getByRole("button", { name: "Send test" }).click();
  await expect(page.getByText("Test succeeded")).toBeVisible();
  await page.getByRole("button", { name: "Create job" }).click();
  await expect(page.getByRole("heading", { name: "e2e-command" })).toBeVisible();
  await page.getByRole("link", { name: "Edit", exact: true }).click();
  await expect(page.getByRole("textbox", { name: "Command", exact: true })).toHaveValue("/bin/echo");
  await expect(page.getByLabel("Arguments", { exact: true })).toHaveValue("hello-command");
  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await expectAccessible(page, "edit command (phone)");
  await page.getByRole("link", { name: "Cancel", exact: true }).click();
  await page.getByRole("button", { name: "Trigger now" }).click();
  await page.getByRole("tab", { name: "Runs", exact: true }).click();
  await expect(page.getByRole("link", { name: /^Run run_.*succeeded/ }).first()).toBeVisible({ timeout: 10_000 });
});

test("monthly usage states and suspension remain accessible across navigation", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  let usage = { month: "2026-10", runs: 80, limit: 100 as number | null, over_quota_policy: "block", reset_at: "2026-11-01T00:00:00Z" };
  await page.route("**/api/v1/usage", (route) => route.fulfill({ json: usage }));
  await page.goto("/login");
  await page.getByLabel("Name").fill("e2e-usage");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByText("80 / 100 runs this month")).toBeVisible();
  await expect(page.getByText(/at least 80%/)).toBeVisible();
  await expectAccessible(page, "usage warning");

  usage = { ...usage, runs: 100 };
  await page.reload();
  await expect(page.getByText(/New occurrences are skipped/)).toBeVisible();
  await expectAccessible(page, "usage blocked");

  usage = { ...usage, runs: 120, over_quota_policy: "allow" };
  await page.reload();
  await expect(page.getByText(/New occurrences are counted as overage/)).toBeVisible();
  await expectAccessible(page, "usage overage");

  usage = { ...usage, limit: null };
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await page.reload();
  await page.getByRole("tab", { name: "Workspace" }).click();
  await expect(page.getByText("120 runs this month · unlimited")).toBeVisible();
  await expect(page.getByRole("progressbar")).toHaveCount(0);
  await expectAccessible(page, "usage unlimited");

  await page.route("**/api/v1/api-keys", (route) => route.request().method() === "POST"
    ? route.fulfill({ status: 403, json: { error: { code: "workspace_suspended", message: "Workspace suspended", fields: { reason: "Operator review" } } } })
    : route.continue());
  await page.getByRole("tab", { name: "API keys" }).click();
  await page.getByRole("button", { name: "New key" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("blocked-key");
  await dialog.getByRole("button", { name: "Create key" }).click();
  await expect(dialog.getByRole("alert")).toContainText("This workspace is suspended: Operator review");
  await expectAccessible(page, "suspended workspace form");
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("This workspace is suspended: Operator review");
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  await page.reload();
  await expect(page.getByRole("heading", { name: /^(\/\/ )?jobs$/ })).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("This workspace is suspended: Operator review");
  await expectAccessible(page, "persistent suspension banner");
});


test("sign-in returns to the requested root dashboard route", async ({ page }) => {
  await page.goto("/jobs?state=paused");
  await expect(page).toHaveURL(/\/login\?redirect=/);
  await page.getByLabel("Name").fill("e2e-deep-link");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/jobs\?state=paused$/);
  await expect(page.getByRole("heading", { name: /^(\/\/ )?jobs$/ })).toBeVisible();
});
