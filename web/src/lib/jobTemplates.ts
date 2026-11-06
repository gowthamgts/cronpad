import type { JobFormValues } from "./jobForm";

/** Starting points offered to a workspace with no jobs; each prefills /jobs/new so only the target is left to fill in. */
export const JOB_TEMPLATES = {
  uptime: {
    title: "Check a URL is up",
    description: "GET a URL every 5 minutes; anything but 2xx is a failure.",
    apply: (v: JobFormValues): JobFormValues => ({ ...v, schedule: { expr: "every 5 minutes", mode: "auto" }, sinkKind: "webhook", webhook: { ...v.webhook, method: "GET" } }),
  },
  daily: {
    title: "Call a webhook daily",
    description: "POST a JSON payload every morning at 9am.",
    apply: (v: JobFormValues): JobFormValues => ({ ...v, schedule: { expr: "every day at 9am", mode: "auto" }, sinkKind: "webhook", webhook: { ...v.webhook, method: "POST" } }),
  },
  command: {
    title: "Run a command hourly",
    description: "Run a script or binary on this server every hour.",
    apply: (v: JobFormValues): JobFormValues => ({ ...v, schedule: { expr: "every hour", mode: "auto" }, sinkKind: "command" }),
  },
} as const;

export type JobTemplateKey = keyof typeof JOB_TEMPLATES;

export const isJobTemplate = (v: unknown): v is JobTemplateKey => typeof v === "string" && Object.hasOwn(JOB_TEMPLATES, v);
