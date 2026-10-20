import { rules } from "@/api/rules";
import type { Job, JobCreate } from "@/api/hooks";
import type { ScheduleValue } from "@/components/ScheduleInput";
import { isDuration, isUrlOrTemplate } from "./validation";

export type SinkKind = "webhook" | "command";

export interface Header {
  name: string;
  value: string;
}

export interface WebhookValues {
  url: string;
  method: string;
  headers: Header[];
  body: string;
  successCodes: string;
  retryOn: string;
  followRedirects: boolean;
}

export interface CommandValues {
  env: Header[];
  command: string;
  args: string;
  workdir: string;
  shell: boolean;
  successExitCodes: string;
  retryExitCodes: string;
}

export interface JobFormValues {
  name: string;
  description: string;
  tags: string;
  schedule: ScheduleValue;
  timezone: string;
  sinkKind: SinkKind;
  webhook: WebhookValues;
  command: CommandValues;
  retry: { max_attempts: string; backoff: string; initial_interval: string; max_interval: string; multiplier: string; jitter: string };
  timeout: string;
  sla_threshold: string;
  misfire_policy: string;
  overlap_policy: string;
  auto_disable_threshold: string;
}

/** What a new job's code fields start with. The server's config decides; this matches its built-in values for older servers and legacy jobs. */
export interface CodeDefaults {
  success_codes: string[];
  retry_on: string[];
  success_exit_codes: number[];
  retry_exit_codes: number[];
}
export const BUILTIN_CODE_DEFAULTS: CodeDefaults = { success_codes: ["2xx"], retry_on: ["5xx", "408", "429", "network", "timeout"], success_exit_codes: [0], retry_exit_codes: [] };

export const emptyForm = (timezone: string, defaults: CodeDefaults = BUILTIN_CODE_DEFAULTS): JobFormValues => ({
  name: "",
  description: "",
  tags: "",
  schedule: { expr: "", mode: "auto" },
  timezone,
  sinkKind: "webhook",
  webhook: { url: "", method: "POST", headers: [], body: "", successCodes: defaults.success_codes.join(", "), retryOn: defaults.retry_on.join(", "), followRedirects: false },
  command: { env: [], command: "", args: "", workdir: "", shell: false, successExitCodes: defaults.success_exit_codes.join(", "), retryExitCodes: defaults.retry_exit_codes.join(", ") },
  retry: { max_attempts: "", backoff: "", initial_interval: "", max_interval: "", multiplier: "", jitter: "" },
  timeout: "",
  sla_threshold: "",
  misfire_policy: "",
  overlap_policy: "",
  auto_disable_threshold: "",
});

export const splitList = (v: string) =>
  v
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);

/** Status codes stay numbers; classes ("5xx") and keywords ("network") stay strings. */
const codeToken = (t: string): string | number => (/^\d+$/.test(t) ? Number(t) : t);

export function parseTags(v: string): string[] {
  return [...new Set(v.split(",").map((t) => t.trim()).filter(Boolean))];
}

export function buildSinkConfig(v: JobFormValues): Record<string, unknown> {
  if (v.sinkKind === "webhook") {
    const w = v.webhook;
    const config: Record<string, unknown> = { url: w.url.trim() };
    if (w.method && w.method !== "POST") config.method = w.method;
    const headers = Object.fromEntries(w.headers.filter((h) => h.name.trim()).map((h) => [h.name.trim(), h.value]));
    if (Object.keys(headers).length > 0) config.headers = headers;
    if (w.body !== "") config.body = w.body;
    const success = splitList(w.successCodes);
    // A lone class keeps the original string form; anything else is a list mixing codes and classes.
    if (success.length === 1 && success[0] === "2xx") config.success_codes = "2xx";
    else if (success.length > 0) config.success_codes = success.map(codeToken);
    // Always sent, even when empty: an empty list means "never retry", and leaving it out would bring the server defaults back.
    config.retry_on = splitList(w.retryOn).map(codeToken);
    if (w.followRedirects) config.follow_redirects = true;
    return config;
  }
  const c = v.command;
  const config: Record<string, unknown> = { command: c.command.trim() };
  const env = Object.fromEntries(c.env.filter((e) => e.name.trim()).map((e) => [e.name.trim(), e.value]));
  if (Object.keys(env).length > 0) config.env = env;
  const args = splitList(c.args);
  if (args.length > 0) config.args = args;
  if (c.workdir.trim()) config.workdir = c.workdir.trim();
  if (c.shell) config.shell = true;
  const ok = splitList(c.successExitCodes).map(Number);
  if (ok.length > 0) config.success_exit_codes = ok;
  config.retry_exit_codes = splitList(c.retryExitCodes).map(Number);
  return config;
}

/** A create/update body with the fields the form always fills in. */
export type JobBody = JobCreate & Required<Pick<JobCreate, "name" | "schedule" | "sink">>;

export function buildJobInput(v: JobFormValues): JobBody {
  const retry: Record<string, string | number> = {};
  if (v.retry.max_attempts) retry.max_attempts = Number(v.retry.max_attempts);
  if (v.retry.backoff) retry.backoff = v.retry.backoff;
  if (v.retry.initial_interval) retry.initial_interval = v.retry.initial_interval.trim();
  if (v.retry.max_interval) retry.max_interval = v.retry.max_interval.trim();
  if (v.retry.multiplier) retry.multiplier = Number(v.retry.multiplier);
  if (v.retry.jitter) retry.jitter = Number(v.retry.jitter);
  const input: JobBody = {
    name: v.name.trim(),
    schedule: { expr: v.schedule.expr.trim(), kind: v.schedule.mode, timezone: v.timezone },
    sink: { kind: v.sinkKind, config: buildSinkConfig(v) },
    tags: parseTags(v.tags),
  };
  if (v.description.trim()) input.description = v.description.trim();
  if (Object.keys(retry).length > 0) input.retry = retry;
  if (v.timeout.trim()) input.timeout = v.timeout.trim();
  if (v.sla_threshold.trim()) input.sla_threshold = v.sla_threshold.trim();
  if (v.misfire_policy) input.misfire_policy = v.misfire_policy as JobCreate["misfire_policy"];
  if (v.overlap_policy) input.overlap_policy = v.overlap_policy as JobCreate["overlap_policy"];
  if (v.auto_disable_threshold) input.auto_disable_threshold = Number(v.auto_disable_threshold);
  return input;
}

/** A job stores its own codes; one saved before that (key absent) behaves as the built-in default, so the form shows that. */
function codesToText(value: unknown, whenAbsent: readonly (string | number)[] = []): string {
  if (Array.isArray(value)) return value.join(", ");
  if (typeof value === "string") return value;
  return whenAbsent.join(", ");
}

export function formFromJob(job: Job): JobFormValues {
  const base = emptyForm(job.schedule.timezone || "UTC");
  const config = (job.sink.config ?? {}) as Record<string, unknown>;
  const retry = (job.retry ?? {}) as Record<string, unknown>;
  const str = (v: unknown) => (v === undefined || v === null ? "" : String(v));
  return {
    ...base,
    name: job.name,
    description: job.description ?? "",
    tags: job.tags?.join(", ") ?? "",
    // Edit the stored expression in its stored syntax; one-off schedules fall back to auto-detect.
    schedule: { expr: job.schedule.expr, mode: (["natural", "cron", "quartz", "rrule"].includes(job.schedule.kind ?? "") ? job.schedule.kind : "auto") as ScheduleValue["mode"] },
    sinkKind: job.sink.kind,
    webhook:
      job.sink.kind === "webhook"
        ? {
            url: str(config.url),
            method: str(config.method) || "POST",
            headers: Object.entries((config.headers ?? {}) as Record<string, string>).map(([name, value]) => ({ name, value })),
            body: str(config.body),
            successCodes: codesToText(config.success_codes, BUILTIN_CODE_DEFAULTS.success_codes),
            retryOn: codesToText(config.retry_on, BUILTIN_CODE_DEFAULTS.retry_on),
            followRedirects: config.follow_redirects === true,
          }
        : base.webhook,
    command:
      job.sink.kind === "command"
        ? {
            env: Object.entries((config.env ?? {}) as Record<string, string>).map(([name, value]) => ({ name, value })),
            command: str(config.command),
            args: codesToText(config.args),
            workdir: str(config.workdir),
            shell: config.shell === true,
            successExitCodes: codesToText(config.success_exit_codes, BUILTIN_CODE_DEFAULTS.success_exit_codes),
            retryExitCodes: codesToText(config.retry_exit_codes),
          }
        : base.command,
    retry: {
      max_attempts: str(retry.max_attempts),
      backoff: str(retry.backoff),
      initial_interval: str(retry.initial_interval),
      max_interval: str(retry.max_interval),
      multiplier: str(retry.multiplier),
      jitter: str(retry.jitter),
    },
    timeout: str(job.timeout),
    sla_threshold: str(job.sla_threshold),
    misfire_policy: job.misfire_policy,
    overlap_policy: job.overlap_policy,
    auto_disable_threshold: str(job.auto_disable_threshold),
  };
}

export type FormErrors = Record<string, string>;

/** Client-side checks mirroring the server contract (limits come from openapi.yaml). */
export function validateJobForm(v: JobFormValues, opts: { scheduleValid: boolean }): FormErrors {
  const e: FormErrors = {};
  const name = v.name.trim();
  if (name.length < rules.jobNameMin) e.name = "Name is required";
  else if (name.length > rules.jobNameMax) e.name = `Name must be at most ${rules.jobNameMax} characters`;
  if (v.description.length > rules.jobDescriptionMax) e.description = `Description must be at most ${rules.jobDescriptionMax} characters`;
  if (parseTags(v.tags).length > rules.tagsMax) e.tags = `At most ${rules.tagsMax} tags`;
  if (!v.schedule.expr.trim()) e.schedule = "Schedule is required";
  else if (!opts.scheduleValid) e.schedule = "Fix the schedule before saving";
  if (v.sinkKind === "webhook") {
    if (!v.webhook.url.trim()) e.url = "URL is required";
    else if (!isUrlOrTemplate(v.webhook.url)) e.url = "Enter a full http:// or https:// URL";
    if (v.webhook.headers.length > 50) e.headers = "At most 50 headers";
    if (v.webhook.headers.some((h) => h.name.trim() === "" && h.value !== "")) e.headers = "Every header needs a name";
    const success = splitList(v.webhook.successCodes);
    if (success.length === 0) e.successCodes = "Pick at least one success code";
    else if (success.some((t) => !/^[1-5]\d{2}$|^[1-5]xx$/.test(t))) e.successCodes = "Use codes from 100–599 or classes like 2xx, separated by commas (2xx, 301)";
    const retryOn = splitList(v.webhook.retryOn);
    if (retryOn.some((t) => !/^[1-5]\d{2}$|^[45]xx$|^network$|^timeout$/.test(t))) e.retryOn = "Use 100–599, 4xx, 5xx, network or timeout, separated by commas";
  } else {
    if (!v.command.command.trim()) e.command = "Command is required";
    if (splitList(v.command.successExitCodes).length === 0) e.successExitCodes = "Pick at least one success exit code";
    for (const key of ["successExitCodes", "retryExitCodes"] as const) {
      if (splitList(v.command[key]).some((t) => !/^\d+$/.test(t) || Number(t) > 255)) e[key] = "Use whole exit codes from 0–255, separated by commas";
    }
  }
  const n = (k: keyof JobFormValues["retry"]) => v.retry[k];
  if (n("max_attempts") && (!/^\d+$/.test(n("max_attempts")) || Number(n("max_attempts")) < rules.retryAttemptsMin)) e.max_attempts = `Enter a whole number, at least ${rules.retryAttemptsMin}`;
  if (n("initial_interval") && !isDuration(n("initial_interval"))) e.initial_interval = "Use a duration like 10s or 5m";
  if (n("max_interval") && !isDuration(n("max_interval"))) e.max_interval = "Use a duration like 10s or 5m";
  if (n("multiplier") && !(Number(n("multiplier")) >= rules.retryMultiplierMin)) e.multiplier = `Must be at least ${rules.retryMultiplierMin}`;
  if (n("jitter") && !(Number(n("jitter")) >= 0 && Number(n("jitter")) <= rules.retryJitterMax)) e.jitter = `Must be between 0 and ${rules.retryJitterMax}`;
  if (v.timeout && !isDuration(v.timeout)) e.timeout = "Use a duration like 30s or 2m";
  if (v.sla_threshold && !isDuration(v.sla_threshold)) e.sla_threshold = "Use a duration like 1m";
  if (v.auto_disable_threshold && (!/^\d+$/.test(v.auto_disable_threshold) || Number(v.auto_disable_threshold) < rules.autoDisableMin))
    e.auto_disable_threshold = `Enter a whole number, at least ${rules.autoDisableMin}`;
  return e;
}
