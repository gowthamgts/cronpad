import { useMutation } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ChevronDown, CircleAlert, Send } from "lucide-react";
import { useCallback, useState, type FormEvent } from "react";
import { api, ApiError, call, type Schemas } from "@/api/client";
import { useFeatures, useInvalidate, useJob, type Job } from "@/api/hooks";
import { rules } from "@/api/rules";
import { Field } from "@/components/Field";
import { RevealDialog } from "@/components/RevealDialog";
import { ScheduleInput } from "@/components/ScheduleInput";
import { SinkEditor } from "@/components/SinkEditor";
import { TestResultView } from "@/components/TestResultView";
import { ErrorState, Loading, PageHeader } from "@/components/states";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input, Textarea } from "@/components/ui/input";
import { TagInput } from "@/components/ui/tag-input";
import { buildJobInput, parseTags, emptyForm, formFromJob, validateJobForm, type FormErrors, type JobFormValues } from "@/lib/jobForm";
import Combobox from "@/components/ui/combobox";
import { browserZone, normalizeZone, zoneOptions } from "@/lib/timezones";
import { usePageTitle } from "@/lib/usePageTitle";

const ZONES = zoneOptions();
const advancedFields = new Set(["max_attempts", "multiplier", "initial_interval", "max_interval", "jitter", "timeout", "sla_threshold", "auto_disable_threshold"]);
const fieldLabels: Record<string, string> = { name: "Name", description: "Description", tags: "Tags", schedule: "Schedule", url: "URL", headers: "Headers", command: "Command", successCodes: "Success codes", retryOn: "Retry on", successExitCodes: "Success exit codes", retryExitCodes: "Retry exit codes", max_attempts: "Max attempts", multiplier: "Multiplier", initial_interval: "Initial interval", max_interval: "Max interval", jitter: "Jitter", timeout: "Timeout", sla_threshold: "SLA threshold", auto_disable_threshold: "Auto-disable after" };

export function NewJobPage() {
  usePageTitle("New job");
  const features = useFeatures();
  // The server decides what a new job's code fields start with, so wait for it (a failed lookup falls back to the built-ins).
  if (features.isPending) return <Loading label="Loading job form" />;
  return <JobForm initial={emptyForm(browserZone(), features.data?.defaults)} />;
}

export function EditJobPage({ jobId }: { jobId: string }) {
  usePageTitle("Edit job");
  const job = useJob(jobId);
  if (job.isPending) return <Loading label="Loading job" />;
  if (job.isError) return <ErrorState error={job.error} retry={() => void job.refetch()} />;
  return <JobForm key={job.data.id} initial={formFromJob(job.data)} job={job.data} />;
}

function JobForm({ initial, job }: { initial: JobFormValues; job?: Job }) {
  const navigate = useNavigate();
  const invalidate = useInvalidate();
  const features = useFeatures();
  const [values, setValues] = useState({ ...initial, timezone: normalizeZone(initial.timezone) });
  const [scheduleValid, setScheduleValid] = useState(false);
  const [submitted, setSubmitted] = useState(false);
  const [serverError, setServerError] = useState<ApiError | null>(null);
  const [secret, setSecret] = useState<{ value: string; jobId: string } | null>(null);
  const [testError, setTestError] = useState<string | null>(null);
  const [testErrors, setTestErrors] = useState<FormErrors>({});
  const [testResult, setTestResult] = useState<Schemas["TestResult"] | null>(null);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const onValidity = useCallback((valid: boolean) => setScheduleValid(valid), []);

  const currentErrors = validateJobForm(values, { scheduleValid });
  const errors: FormErrors = submitted ? currentErrors : Object.fromEntries(Object.entries(currentErrors).filter(([key]) => key in testErrors));
  const set = <K extends keyof JobFormValues>(key: K, value: JobFormValues[K]) => setValues((v) => ({ ...v, [key]: value }));
  const setRetry = (key: keyof JobFormValues["retry"], value: string) => setValues((v) => ({ ...v, retry: { ...v.retry, [key]: value } }));
  const fieldError = (name: string) => errors[name] ?? serverError?.fields[name] ?? serverError?.fields[`${name}.expr`] ?? null;

  const save = useMutation({
    mutationFn: async () => {
      const input = buildJobInput(values);
      if (job) return call(api.PATCH("/api/v1/jobs/{id}", { params: { path: { id: job.id } }, body: input }));
      return call(api.POST("/api/v1/jobs", { body: input }));
    },
    onSuccess: async (saved) => {
      await invalidate();
      if (!job && saved.signing_secret) {
        setSecret({ value: saved.signing_secret, jobId: saved.id });
        return;
      }
      await navigate({ to: "/jobs/$jobId", params: { jobId: saved.id } });
    },
    onError: (e) => setServerError(e instanceof ApiError ? e : new ApiError(0, "internal", String(e))),
  });

  const test = useMutation({
    mutationFn: () => {
      // The test sends the form as written, so edits are tested before they are saved.
      const input = buildJobInput(values);
      if (!input.name) input.name = "send-test";
      if (!input.schedule.expr) input.schedule = { ...input.schedule, expr: "every day at 9am" };
      return call(api.POST("/api/v1/jobs/test", { body: input }));
    },
    onSuccess: setTestResult,
    onError: (e) => setTestError(e instanceof Error ? e.message : String(e)),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    setSubmitted(true);
    setServerError(null);
    const problems = validateJobForm(values, { scheduleValid });
    if (Object.keys(problems).some((key) => advancedFields.has(key))) setAdvancedOpen(true);
    if (Object.keys(problems).length > 0) {
      requestAnimationFrame(() => document.querySelector<HTMLElement>("[aria-invalid='true']")?.focus());
      return;
    }
    save.mutate();
  }

  function sendTest() {
    setTestError(null);
    setTestResult(null);
    const sinkErrors = validateJobForm(values, { scheduleValid: true });
    delete sinkErrors.name;
    delete sinkErrors.description;
    delete sinkErrors.tags;
    delete sinkErrors.schedule;
    setTestErrors(sinkErrors);
    if (Object.keys(sinkErrors).length) {
      if (Object.keys(sinkErrors).some((key) => advancedFields.has(key))) setAdvancedOpen(true);
      requestAnimationFrame(() => document.querySelector<HTMLElement>("[aria-invalid='true']")?.focus());
      return;
    }
    test.mutate();
  }

  const commandAllowed = features.data?.sinks.command ?? false;

  return (
    <>
      <PageHeader
        title={job ? `edit ${job.name}` : "new job"}
        description={job ? "Changes take effect on the next scheduling pass." : "Choose when it runs and what it calls."}
      />
      <form onSubmit={submit} noValidate className="relative grid min-w-0 gap-5 pb-2" aria-label={job ? "Edit job" : "Create job"}>
        {Object.keys(errors).length > 0 ? (
          <div role="alert" className="rounded-md border border-failure/50 bg-bad-soft p-3 text-sm">
            <p className="flex items-center gap-2 font-bold"><CircleAlert aria-hidden="true" className="size-4 text-destructive" />Check these fields before continuing</p>
            <ul className="mt-2 grid list-disc gap-1 pl-5">
              {Object.entries(errors).map(([key, message]) => <li key={key}><a className="rounded-sm underline underline-offset-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring" href={`#job-${key}`} onClick={(e) => { e.preventDefault(); if (advancedFields.has(key)) setAdvancedOpen(true); requestAnimationFrame(() => document.getElementById(`job-${key}`)?.focus()); }}>{fieldLabels[key] ?? key}: {message}</a></li>)}
            </ul>
          </div>
        ) : null}
        {serverError ? (
          <div role="alert" className="rounded-md border border-failure/50 bg-bad-soft p-3 text-sm">
            <p className="font-medium text-destructive">{serverError.message}</p>
            {Object.entries(serverError.fields).map(([k, v]) => (
              <p key={k} className="text-destructive">
                {k}: {v}
              </p>
            ))}
          </div>
        ) : null}

        <Card className="min-w-0">
          <CardHeader>
            <CardTitle tab>basics</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-4">
            <Field id="job-name" label="Name" error={fieldError("name")} labelAside={values.name.trim().length >= rules.jobNameMax - 40 ? `${values.name.trim().length}/${rules.jobNameMax}` : undefined}>
              {(p) => <Input {...p} maxLength={rules.jobNameMax + 20} autoComplete="off" value={values.name} onChange={(e) => set("name", e.target.value)} />}
            </Field>
            <Field id="job-description" label="Description (optional)" error={fieldError("description")}>
              {(p) => <Textarea {...p} value={values.description} onChange={(e) => set("description", e.target.value)} />}
            </Field>
            <Field id="job-tags" label="Tags (optional)" error={fieldError("tags")} hint="Press Enter to add a tag, Backspace to remove the last">
              {(p) => <TagInput {...p} placeholder="reports" value={parseTags(values.tags)} onChange={(tags) => set("tags", tags.join(", "))} />}
            </Field>
          </CardContent>
        </Card>

        <Card className="min-w-0">
          <CardHeader>
            <CardTitle tab>schedule</CardTitle>
            <CardDescription>Write it in plain English, as cron, as Quartz, or as an RRULE.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-4">
            <ScheduleInput id="job-schedule" error={fieldError("schedule")} value={values.schedule} onChange={(s) => set("schedule", s)} timezone={values.timezone} onValidity={onValidity}>
              <Combobox label="Time zone" value={values.timezone} options={ZONES.some((z) => z.value === values.timezone) ? ZONES : [{value: values.timezone}, ...ZONES]} onChange={(zone) => set("timezone", zone)} searchPlaceholder="City, region or UTC offset" emptyText="No matching timezone" />
              <p className="text-xs text-ink-soft">Daily schedules follow this zone, including daylight saving.</p>
            </ScheduleInput>
          </CardContent>
        </Card>

        <Card className="min-w-0">
          <CardHeader>
            <CardTitle tab>target</CardTitle>
            <CardDescription>Choose what happens each time this job runs.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-4">

            <div className="grid gap-3 rounded-md border border-line bg-sunken/60 p-3">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div><p className="text-sm font-bold">{values.sinkKind === "webhook" ? "Send a test request now" : "Run a test command now"}</p><p className="text-xs text-muted-foreground">Makes a real {values.sinkKind === "webhook" ? "request" : "execution"} — doesn’t create or save the job.</p></div>
                <Button variant="outline" onClick={sendTest} disabled={test.isPending}>
                  <Send /> {test.isPending ? "Sending…" : "Send test"}
                </Button>
              </div>
              {testError ? <p role="alert" className="text-sm text-destructive">{testError}</p> : null}
              <div aria-live="polite">{testResult ? <TestResultView result={testResult} /> : null}</div>
            </div>
            <SinkEditor values={values} onChange={(next) => { setValues(next); setTestErrors({}); setTestResult(null); setTestError(null); }} errors={errors} commandAllowed={commandAllowed} defaults={features.data?.defaults} />
          </CardContent>
        </Card>

        <Collapsible className="min-w-0" open={advancedOpen} onOpenChange={setAdvancedOpen}>
          <Card>
            <CardHeader className="py-2 [[data-state=open]_&]:pb-3">
              <CollapsibleTrigger className="flex min-h-10 w-full items-center justify-between gap-3 border-0 bg-transparent px-0 text-left hover:bg-transparent data-[state=open]:no-underline">
                <span className="grid gap-0.5">
                  <span className="section-title">retries &amp; advanced</span>
                  <span className="text-xs text-ink-soft">Defaults apply to anything left empty.</span>
                </span>
                <ChevronDown aria-hidden="true" className={advancedOpen ? "rotate-180 transition-transform" : "transition-transform"} />
              </CollapsibleTrigger>
            </CardHeader>
            <CollapsibleContent className="border-0 bg-transparent p-0">
              <CardContent className="grid gap-4">
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field id="job-max_attempts" label="Max attempts" error={errors.max_attempts} hint="Default: 3 (including the first attempt).">
                    {(p) => <Input {...p} inputMode="numeric" value={values.retry.max_attempts} onChange={(e) => setRetry("max_attempts", e.target.value)} />}
                  </Field>
                  <Combobox label="Backoff" value={values.retry.backoff} onChange={(value) => setRetry("backoff", value)} options={[{value:"",label:"Default (exponential)"},{value:"exponential"},{value:"fixed"}]} />
                  <Field id="job-multiplier" label="Multiplier" error={errors.multiplier} hint="Default: 2.">
                    {(p) => <Input {...p} inputMode="decimal" value={values.retry.multiplier} onChange={(e) => setRetry("multiplier", e.target.value)} />}
                  </Field>
                  <Field id="job-initial_interval" label="Initial interval" error={errors.initial_interval} hint="Default: 10s.">
                    {(p) => <Input {...p} value={values.retry.initial_interval} onChange={(e) => setRetry("initial_interval", e.target.value)} />}
                  </Field>
                  <Field id="job-max_interval" label="Max interval" error={errors.max_interval} hint="Default: 10m.">
                    {(p) => <Input {...p} value={values.retry.max_interval} onChange={(e) => setRetry("max_interval", e.target.value)} />}
                  </Field>
                  <Field id="job-jitter" label="Jitter" error={errors.jitter} hint="Default: 0.2. Range: 0 to 1.">
                    {(p) => <Input {...p} inputMode="decimal" value={values.retry.jitter} onChange={(e) => setRetry("jitter", e.target.value)} />}
                  </Field>
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field id="job-timeout" label="Timeout" error={errors.timeout} hint="Default: 30s per attempt.">
                    {(p) => <Input {...p} value={values.timeout} onChange={(e) => set("timeout", e.target.value)} />}
                  </Field>
                  <Field id="job-sla_threshold" label="SLA threshold" error={errors.sla_threshold} hint="Default: server setting. Late-start limit (e.g. 1m).">
                    {(p) => <Input {...p} value={values.sla_threshold} onChange={(e) => set("sla_threshold", e.target.value)} />}
                  </Field>
                  <Field id="job-auto_disable_threshold" label="Auto-disable after" error={errors.auto_disable_threshold} hint="Default: server setting. Number of consecutive failures.">
                    {(p) => <Input {...p} inputMode="numeric" value={values.auto_disable_threshold} onChange={(e) => set("auto_disable_threshold", e.target.value)} />}
                  </Field>
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Combobox label="If runs were missed" value={values.misfire_policy} onChange={(value) => set("misfire_policy", value)} options={[{value:"",label:"Default"}, ...rules.misfirePolicies.map((value) => ({value}))]} />
                  <Combobox label="If a run is still going" value={values.overlap_policy} onChange={(value) => set("overlap_policy", value)} options={[{value:"",label:"Default"}, ...rules.overlapPolicies.map((value) => ({value}))]} />
                </div>
              </CardContent>
            </CollapsibleContent>
          </Card>
        </Collapsible>

        <div className="sticky bottom-0 z-10 -mx-4 flex flex-wrap items-center gap-3 border-t border-line bg-paper/95 px-4 py-3 backdrop-blur-xs">
          <Button type="submit" disabled={save.isPending}>
            {save.isPending ? "Saving…" : job ? "Save changes" : "Create job"}
          </Button>
          <Link to={job ? "/jobs/$jobId" : "/jobs"} params={job ? { jobId: job.id } : undefined} className={buttonVariants({ variant: "ghost" })}>
            Cancel
          </Link>
        </div>
      </form>

      <RevealDialog
        value={secret?.value ?? null}
        title="Signing secret"
        description="cronpad signs every webhook request with this secret. Use it to verify requests really come from your job."
        label="signing secret"
        onClose={() => {
          const id = secret?.jobId;
          setSecret(null);
          if (id) void navigate({ to: "/jobs/$jobId", params: { jobId: id } });
        }}
      />
    </>
  );
}
