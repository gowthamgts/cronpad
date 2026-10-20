import { DualTime } from "@/components/DualTime";
import { SkipReason } from "@/components/SkipReason";
import { useMutation } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { RotateCw } from "lucide-react";
import { api, call, type Schemas } from "@/api/client";
import { useInvalidate, useJob, useRun, type RunDetail } from "@/api/hooks";
import { ScrollPre } from "@/components/ScrollPre";
import { StatusPill } from "@/components/StatusPill";
import { ErrorState, Loading, PageHeader } from "@/components/states";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { formatDuration } from "@/lib/format";
import { usePageTitle } from "@/lib/usePageTitle";

type Attempt = Schemas["Attempt"];

export function RunDetailPage({ runId }: { runId: string }) {
  usePageTitle("Run");
  const run = useRun(runId);
  if (run.isPending) return <Loading label="Loading run" />;
  if (run.isError) return <ErrorState error={run.error} retry={() => void run.refetch()} />;
  return <RunView run={run.data} />;
}

export interface Stage {
  label: string;
  at: string;
}

/** Scheduled → claimed → started → finished, skipping stages the run never reached. */
export function lagStages(run: Pick<RunDetail, "scheduled_at" | "claimed_at" | "started_at" | "finished_at">): Stage[] {
  const stages: { label: string; at: string | null | undefined }[] = [
    { label: "Scheduled", at: run.scheduled_at },
    { label: "Claimed", at: run.claimed_at },
    { label: "Started", at: run.started_at },
    { label: "Finished", at: run.finished_at },
  ];
  return stages.filter((s): s is Stage => Boolean(s.at));
}

function LagBreakdown({ run, timezone }: { run: RunDetail; timezone?: string }) {
  const stages = lagStages(run);
  const segments = stages.slice(1).map((s, i) => ({ from: stages[i]!, to: s, ms: Math.max(0, new Date(s.at).getTime() - new Date(stages[i]!.at).getTime()) }));
  const total = segments.reduce((a, s) => a + s.ms, 0);
  return (
    <Card>
      <CardHeader>
        <CardTitle tab>timing</CardTitle>
        <CardDescription>
          Start lag {formatDuration(run.start_lag_ms)} · total {formatDuration(run.e2e_ms)}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid min-w-0 content-start gap-3">
        <ol className="grid gap-1 text-sm tabular-nums" aria-label="Run milestones">
          {stages.map((s) => (
            <li key={s.label} className="flex flex-wrap justify-between gap-x-4 gap-y-1 border-b border-line py-1.5 first:pt-0 last:border-0">
              <span className="font-bold lowercase">{s.label}</span>
              <DualTime iso={s.at} timeZone={timezone} seconds className="text-right" />
            </li>
          ))}
        </ol>
        {segments.length > 0 && total > 0 ? (
          <div role="img" aria-label={segments.map((s) => `${s.from.label} to ${s.to.label}: ${formatDuration(s.ms)}`).join("; ")}>
            <svg viewBox="0 0 600 70" className="h-20 w-full" aria-hidden="true">
              <line x1="20" x2="580" y1="30" y2="30" className="stroke-ink" strokeWidth="2" />
              {stages.map((stage, i) => <g key={stage.label}><line x1={20 + i * 560 / (stages.length - 1)} x2={20 + i * 560 / (stages.length - 1)} y1="20" y2="40" className="stroke-ink" strokeWidth="2" /><text x={20 + i * 560 / (stages.length - 1)} y="58" textAnchor={i === 0 ? "start" : i === stages.length - 1 ? "end" : "middle"} className="fill-ink text-[12px]">{stage.label.toLowerCase()}</text></g>)}
              {segments.map((segment, i) => <text key={segment.to.label} x={20 + (i + 0.5) * 560 / (stages.length - 1)} y="18" textAnchor="middle" className="fill-ink-soft text-[12px]">{formatDuration(segment.ms)}</text>)}
            </svg>
            <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
              {segments.map((s) => (
                <li key={s.to.label}>
                  {s.from.label} → {s.to.label}: {formatDuration(s.ms)}
                </li>
              ))}
            </ul>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

function AttemptView({ attempt, timezone }: { attempt: Attempt; timezone?: string }) {
  const ok = attempt.outcome === "success";
  return (
    <li className="box grid min-w-0 grid-cols-1 gap-3 p-4">
      <div className="flex flex-wrap items-center gap-2">
        {/* The API numbers attempts from 0, like the Cronpad-Attempt header; people count from 1. */}
        <h3 className="font-bold">Attempt {attempt.n + 1}</h3>
        <Badge variant={ok ? "success" : attempt.outcome === "retryable" || attempt.outcome === "timeout" ? "warning" : attempt.outcome ? "danger" : "default"}>{attempt.outcome ?? "in progress"}</Badge>
        {attempt.status_code != null ? <span className={`px-1.5 text-xs font-bold ${(attempt.status_code === 0 || attempt.status_code >= 200 && attempt.status_code < 300) ? "bg-accent text-accent-ink" : attempt.status_code >= 300 && attempt.status_code < 400 ? "bg-redirect text-redirect-ink" : "bg-failure text-failure-ink"}`}>{attempt.status_code >= 100 ? "HTTP" : "Exit"} {attempt.status_code}</span> : null}
        <span className="text-sm text-muted-foreground">{formatDuration(attempt.duration_ms)}</span>
        <DualTime iso={attempt.started_at} timeZone={timezone} seconds className="ml-auto max-w-full text-right text-xs text-ink-soft" />
      </div>
      {attempt.request_summary ? (
        <div>
          <h4 className="mb-1 text-xs font-bold lowercase text-ink-soft">request</h4>
          <ScrollPre aria-label={`Attempt ${attempt.n + 1} request`}>{attempt.request_summary}</ScrollPre>
        </div>
      ) : null}
      {attempt.error ? <p className="text-sm font-medium text-destructive">{attempt.error}</p> : null}
      {attempt.response_body ? (
        <Collapsible>
          <CollapsibleTrigger className="rounded text-sm font-medium text-link underline-offset-4 hover:underline data-[state=open]:mb-2">
            Response body
          </CollapsibleTrigger>
          <CollapsibleContent>
            <ScrollPre className="max-h-72 whitespace-pre-wrap break-all" aria-label={`Attempt ${attempt.n + 1} response body`}>
              {attempt.response_body}
            </ScrollPre>
          </CollapsibleContent>
        </Collapsible>
      ) : null}
    </li>
  );
}

function RunView({ run }: { run: RunDetail }) {
  const job = useJob(run.job_id);
  const navigate = useNavigate();
  const invalidate = useInvalidate();
  const retry = useMutation({
    mutationFn: () => call(api.POST("/api/v1/runs/{id}/retry", { params: { path: { id: run.id } } })),
    onSuccess: async (next) => {
      await invalidate();
      await navigate({ to: "/runs/$runId", params: { runId: next.id } });
    },
  });
  return (
    <>
      <PageHeader
        title={`run ${run.id}`}
        description={
          <span className="flex flex-wrap items-center gap-2">
            <StatusPill status={run.status} />
            <SkipReason run={run} />
            <Badge variant="outline">{run.trigger}</Badge>
            {run.sla_breached ? <Badge variant="warning">late start</Badge> : null}
            <Link to="/jobs/$jobId" params={{ jobId: run.job_id }} className="font-medium text-link underline-offset-4 hover:underline">
              View job
            </Link>
          </span>
        }
        actions={
          run.status === "failed" ? (
            <Button variant="outline" disabled={retry.isPending} onClick={() => retry.mutate()}>
              <RotateCw /> Retry run
            </Button>
          ) : null
        }
      />
      {retry.isError ? <div className="mb-4"><ErrorState error={retry.error} /></div> : null}
      {run.last_error ? (
        <p role="alert" className="mb-4 rounded-md border border-failure/50 bg-bad-soft px-3 py-2 text-sm text-destructive">
          {run.last_error}
        </p>
      ) : null}
      <div className="grid grid-cols-1 gap-5 lg:grid-cols-[2fr_3fr] lg:items-start">
        <LagBreakdown run={run} timezone={job.data?.schedule.timezone} />
        <section aria-labelledby="attempts-heading" className="grid min-w-0 content-start gap-3">
          <h2 id="attempts-heading" className="section-title pt-4">
            attempts <span className="font-normal text-ink-soft">({run.attempts.length})</span>
          </h2>
          {run.attempts.length === 0 ? (
            <p className="rounded-md border border-dashed border-ink-soft p-6 text-center text-sm text-ink-soft">No attempts have started yet.</p>
          ) : (
            <ol className="grid min-w-0 content-start gap-3">
              {run.attempts.map((a) => (
                <AttemptView key={a.n} attempt={a} timezone={job.data?.schedule.timezone} />
              ))}
            </ol>
          )}
        </section>
      </div>
    </>
  );
}
