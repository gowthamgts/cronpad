import { describe, expect, it } from "vitest";
import { bucketRuns } from "@/components/RunsChart";
import { buildJobInput, emptyForm, validateJobForm } from "./jobForm";
import { isUrlOrTemplate, validateKeyName, validateSecretName, validateSecretValue, validateWorkspace } from "./validation";

describe("job form", () => {
  const valid = () => {
    const form = emptyForm("UTC");
    form.name = "nightly";
    form.schedule = { expr: "every day at 9am", mode: "natural" };
    form.webhook.url = "https://example.com/hook";
    return form;
  };

  it("accepts a minimal valid form", () => {
    expect(validateJobForm(valid(), { scheduleValid: true })).toEqual({});
  });

  it("mirrors the server's field rules", () => {
    const form = valid();
    form.name = "x".repeat(201);
    form.webhook.url = "ftp://example.com";
    form.timeout = "soon";
    form.retry.max_attempts = "0";
    form.retry.jitter = "2";
    const errors = validateJobForm(form, { scheduleValid: false });
    expect(Object.keys(errors).sort()).toEqual(["jitter", "max_attempts", "name", "schedule", "timeout", "url"]);
  });

  it("accepts HTTP URLs with templates and rejects unsupported schemes", () => {
    for (const url of ["https://example.com/{{run.id}}", "https://{{secret.HOST}}/hook", "{{secret.URL}}/hook", "{{ secret.URL }}/hook"]) expect(isUrlOrTemplate(url)).toBe(true);
    for (const url of ["ftp://example.com", "javascript:alert(1)", "{{run.id}}", "example.com"]) expect(isUrlOrTemplate(url)).toBe(false);
  });

  it("checks response code syntax against the webhook server contract", () => {
    const form = valid();
    for (const codes of ["2xx", "200, 202", "100 599", "5xx", "2xx, 301, 4xx"]) {
      form.webhook.successCodes = codes;
      expect(validateJobForm(form, { scheduleValid: true }).successCodes).toBeUndefined();
    }
    for (const codes of ["", "2XX", "6xx", "099", "600", "200-299"]) {
      form.webhook.successCodes = codes;
      expect(validateJobForm(form, { scheduleValid: true }).successCodes).toBeDefined();
    }
    form.webhook.successCodes = "2xx";
    form.webhook.retryOn = "4xx, 5xx, 408, 429, network, timeout";
    expect(validateJobForm(form, { scheduleValid: true })).toEqual({});
    for (const codes of ["2xx", "NETWORK", "600", "later"]) {
      form.webhook.retryOn = codes;
      expect(validateJobForm(form, { scheduleValid: true }).retryOn).toBeDefined();
    }
  });

  it("checks command exit codes without changing request construction", () => {
    const form = valid();
    form.sinkKind = "command";
    form.command.command = "/bin/echo";
    form.command.successExitCodes = "0, 2";
    form.command.retryExitCodes = "75, 255";
    expect(validateJobForm(form, { scheduleValid: true })).toEqual({});
    expect(buildJobInput(form).sink.config).toEqual({ command: "/bin/echo", success_exit_codes: [0, 2], retry_exit_codes: [75, 255] });
    form.command.successExitCodes = "zero";
    form.command.retryExitCodes = "-1, 256";
    expect(validateJobForm(form, { scheduleValid: true })).toMatchObject({ successExitCodes: expect.any(String), retryExitCodes: expect.any(String) });
  });

  it("builds a request that omits defaults", () => {
    const form = valid();
    form.tags = "reports, nightly, reports";
    form.webhook.headers = [{ name: "X-Token", value: "{{secret.T}}" }, { name: "", value: "" }];
    form.webhook.successCodes = "200, 202";
    form.webhook.retryOn = "5xx, 429, network";
    expect(buildJobInput(form)).toEqual({
      name: "nightly",
      tags: ["reports", "nightly"],
      schedule: { expr: "every day at 9am", kind: "natural", timezone: "UTC" },
      sink: {
        kind: "webhook",
        config: { url: "https://example.com/hook", headers: { "X-Token": "{{secret.T}}" }, success_codes: [200, 202], retry_on: ["5xx", 429, "network"] },
      },
    });
  });
});

describe("secret, key and workspace rules", () => {
  it("requires UPPER_SNAKE secret names", () => {
    expect(validateSecretName("REPORTS_TOKEN")).toBeNull();
    for (const bad of ["", "lower", "1ABC", "HAS-DASH", "A".repeat(65)]) expect(validateSecretName(bad)).not.toBeNull();
  });
  it("limits secret values by bytes", () => {
    expect(validateSecretValue("x".repeat(8192))).toBeNull();
    expect(validateSecretValue("é".repeat(4097))).not.toBeNull();
    expect(validateSecretValue("")).not.toBeNull();
  });
  it("checks key and workspace names", () => {
    expect(validateKeyName(" ")).not.toBeNull();
    expect(validateKeyName("ci")).toBeNull();
    expect(validateWorkspace("Acme")).toEqual({});
    expect(validateWorkspace(" ")).toEqual({ name: expect.any(String) });
  });
});

describe("runs chart", () => {
  it("buckets runs into 24 hours ending now", () => {
    const now = Date.parse("2026-11-10T12:30:00Z");
    const buckets = bucketRuns(
      [
        { scheduled_at: "2026-11-10T12:10:00Z", status: "succeeded" },
        { scheduled_at: "2026-11-10T12:20:00Z", status: "failed" },
        { scheduled_at: "2026-11-10T11:59:00Z", status: "running" },
        { scheduled_at: "2026-11-08T00:00:00Z", status: "succeeded" },
      ],
      now,
    );
    expect(buckets).toHaveLength(24);
    expect(buckets[23]).toMatchObject({ succeeded: 1, failed: 1, other: 0 });
    expect(buckets[22]).toMatchObject({ other: 1 });
    expect(buckets.reduce((n, b) => n + b.succeeded + b.failed + b.skipped + b.other, 0)).toBe(3);
  });

  it("uses 6-hour buckets for 7 days and UTC days for 30 days", () => {
    const now = Date.parse("2026-11-10T12:30:00Z");
    const runs: Parameters<typeof bucketRuns>[0] = [
      { scheduled_at: "2026-11-10T12:10:00Z", status: "succeeded" },
      { scheduled_at: "2026-11-10T00:10:00Z", status: "failed" },
      { scheduled_at: "2026-11-04T00:00:00Z", status: "succeeded" },
      { scheduled_at: "2026-10-01T00:00:00Z", status: "succeeded" },
    ];
    const week = bucketRuns(runs, now, "7d");
    expect(week).toHaveLength(28);
    expect(new Date(week[27]!.start).toISOString()).toBe("2026-11-10T12:00:00.000Z");
    expect(week[25]).toMatchObject({ failed: 1 });
    expect(week.reduce((n, b) => n + b.succeeded + b.failed, 0)).toBe(3);
    const month = bucketRuns(runs, now, "30d");
    expect(month).toHaveLength(30);
    expect(new Date(month[29]!.start).toISOString()).toBe("2026-11-10T00:00:00.000Z");
    expect(month[29]).toMatchObject({ succeeded: 1, failed: 1 });
    expect(month.reduce((n, b) => n + b.succeeded + b.failed, 0)).toBe(3);
  });

  it("starts from the server's defaults and always sends the retry list, even when emptied", () => {
    const form = { ...emptyForm("UTC", { success_codes: ["2xx", "301"], retry_on: ["5xx"], success_exit_codes: [0], retry_exit_codes: [75] }), name: "n", schedule: { expr: "every day", mode: "auto" as const } };
    expect(form.webhook.successCodes).toBe("2xx, 301");
    expect(form.command.retryExitCodes).toBe("75");
    form.webhook.url = "https://example.com/hook";
    form.webhook.retryOn = "";
    expect(buildJobInput(form).sink.config).toMatchObject({ success_codes: ["2xx", 301], retry_on: [] });
    form.webhook.successCodes = "";
    expect(validateJobForm(form, { scheduleValid: true }).successCodes).toBeDefined();
  });
});
