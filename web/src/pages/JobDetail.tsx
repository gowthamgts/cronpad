import { SkipReason } from "@/components/SkipReason";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { Pause, Pencil, Play, RotateCw, Trash2, Zap } from "lucide-react";
import { useState } from "react";
import { api, ApiError, call } from "@/api/client";
import { keys, useInvalidate, useJob, useJobAction, useJobRuns, type Job, type Run } from "@/api/hooks";
import { RevealDialog } from "@/components/RevealDialog";
import { StatusPill } from "@/components/StatusPill";
import { createJobCurl, listRunsCurl, usePublicUrl } from "@/components/curl";
import { EmptyState, ErrorState, Loading, PageHeader, StatStrip } from "@/components/states";
import { DualTime } from "@/components/DualTime";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import Combobox from "@/components/ui/combobox";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatDateTime, formatDuration, relativeTime } from "@/lib/format";
import { usePageTitle } from "@/lib/usePageTitle";
import { usePageShortcuts } from "@/lib/shortcuts";
import { scheduleSummary } from "./Jobs";

export function JobDetailPage({ jobId }: { jobId: string }) {
  const job = useJob(jobId);
  usePageTitle(job.data?.name ?? "Job");
  if (job.isPending) return <Loading label="Loading job" />;
  if (job.isError) return <ErrorState error={job.error} retry={() => void job.refetch()} />;
  return <JobDetail job={job.data} />;
}

function JobDetail({ job }: { job: Job }) {
  const navigate = useNavigate();
  const tab = useSearch({ from: "/_authed/jobs/$jobId" }).tab ?? "overview";
  const setTab = (next: string) => void navigate({ to: "/jobs/$jobId", params: { jobId: job.id }, search: next === "overview" ? {} : { tab: next as "runs" | "audit" }, replace: true });
  usePageShortcuts([
    { keys: ["e"], label: "Edit this job", run: () => void navigate({ to: "/jobs/$jobId/edit", params: { jobId: job.id } }) },
    { keys: ["o"], label: "Overview tab", run: () => setTab("overview") },
    { keys: ["r"], label: "Runs tab", run: () => setTab("runs") },
    { keys: ["u"], label: "Audit tab", run: () => setTab("audit") },
  ]);
  const invalidate = useInvalidate();
  const action = useJobAction();
  const [notice, setNotice] = useState<{ runId: string } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmRotate, setConfirmRotate] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);

  const trigger = useMutation({
    mutationFn: () => call(api.POST("/api/v1/jobs/{id}/trigger", { params: { path: { id: job.id } } })),
    onSuccess: async (t) => {
      setNotice({ runId: t.run_id });
      await invalidate();
    },
  });
  const remove = useMutation({
    mutationFn: () => call(api.DELETE("/api/v1/jobs/{id}", { params: { path: { id: job.id } } })),
    onSuccess: async () => {
      await invalidate();
      await navigate({ to: "/jobs" });
    },
  });
  const rotate = useMutation({
    mutationFn: () => call(api.POST("/api/v1/jobs/{id}/rotate-signing-secret", { params: { path: { id: job.id } } })),
    onSuccess: (r) => {
      setConfirmRotate(false);
      setSecret(r.signing_secret);
    },
  });

  const stateAction = job.state === "active" ? "pause" : job.state === "paused" ? "resume" : job.state === "disabled" ? "enable" : null;

  return (
    <>
      <PageHeader
        title={job.name}
        description={
          <span className="flex flex-wrap items-center gap-2">
            <StatusPill status={job.state} />
            <span>{scheduleSummary(job)}</span>
            <span aria-hidden="true">·</span>
            <span>{job.schedule.timezone}</span>
          </span>
        }
        actions={
          <>
            {stateAction ? (
              <Button variant="outline" disabled={action.isPending} onClick={() => action.mutate({ id: job.id, action: stateAction })}>
                {stateAction === "pause" ? <Pause /> : <Play />}
                {stateAction === "pause" ? "Pause" : stateAction === "resume" ? "Resume" : "Enable"}
              </Button>
            ) : null}
            <Button onClick={() => trigger.mutate()} disabled={trigger.isPending}>
              <Zap /> {trigger.isPending ? "Triggering…" : "Trigger now"}
            </Button>
            <Link to="/jobs/$jobId/edit" params={{ jobId: job.id }} className={buttonVariants({ variant: "outline" })}>
              <Pencil /> Edit
            </Link>
            <Button variant="ghost" size="icon" className="rounded border border-ink/40 hover:bg-bad-soft" aria-label="Delete job" title="Delete job" onClick={() => setConfirmDelete(true)}>
              <Trash2 />
            </Button>
          </>
        }
      />

      {notice ? (
        <p role="status" className="mb-4 rounded-md border border-line bg-ok-soft px-3 py-2 text-sm text-success">
          Run queued.{" "}
          <Link to="/runs/$runId" params={{ runId: notice.runId }} className="font-medium underline">
            View run
          </Link>
        </p>
      ) : null}
      {trigger.isError ? <div className="mb-4"><ErrorState error={trigger.error} /></div> : null}
      {action.isError ? <div className="mb-4"><ErrorState error={action.error} /></div> : null}
      {job.state === "disabled" && job.disabled_reason ? (
        <p role="alert" className="mb-4 rounded-md border border-failure/50 bg-bad-soft px-3 py-2 text-sm text-destructive">
          This job was disabled: {job.disabled_reason}
        </p>
      ) : null}

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList aria-label="Job sections">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="runs">Runs</TabsTrigger>
          <TabsTrigger value="audit">Audit</TabsTrigger>
        </TabsList>
        <TabsContent value="overview">
          <Overview job={job} onRotate={() => setConfirmRotate(true)} />
        </TabsContent>
        <TabsContent value="runs">
          <RunsTab job={job} />
        </TabsContent>
        <TabsContent value="audit">
          <AuditTab jobId={job.id} />
        </TabsContent>
      </Tabs>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {job.name}?</DialogTitle>
            <DialogDescription>The job stops running. Its run history stays available until it is pruned.</DialogDescription>
          </DialogHeader>
          {remove.isError ? <ErrorState error={remove.error} /> : null}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(false)}>Cancel</Button>
            <Button variant="destructive" disabled={remove.isPending} onClick={() => remove.mutate()}>
              Delete job
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmRotate} onOpenChange={setConfirmRotate}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rotate the signing secret?</DialogTitle>
            <DialogDescription>For 24 hours both the old and the new secret sign each request, so you can deploy the new one without dropping requests.</DialogDescription>
          </DialogHeader>
          {rotate.isError ? <ErrorState error={rotate.error} /> : null}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmRotate(false)}>Cancel</Button>
            <Button disabled={rotate.isPending} onClick={() => rotate.mutate()}>
              <RotateCw /> Rotate secret
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <RevealDialog
        value={secret}
        title="New signing secret"
        description="Use this to verify webhook signatures. The previous secret keeps working for 24 hours."
        label="signing secret"
        onClose={() => setSecret(null)}
      />
    </>
  );
}

function Overview({ job, onRotate }: { job: Job; onRotate: () => void }) {
  const stats = job.stats_24h;
  const config = (job.sink.config ?? {}) as Record<string, unknown>;
  return (
    <div className="grid gap-5">
      <StatStrip
        label="Last 24 hours"
        items={[
          { label: "runs (24h)", value: stats?.runs ?? 0 },
          { label: "succeeded", value: stats?.succeeded ?? 0 },
          { label: "failed", value: stats?.failed ?? 0, tone: (stats?.failed ?? 0) > 0 ? "danger" : undefined },
          { label: "p95 start lag", value: formatDuration(stats?.p95_start_lag_ms) },
        ]}
      />
      <div className="grid gap-5 lg:grid-cols-2 lg:items-start">
        <Card>
          <CardHeader>
            <CardTitle tab>schedule</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-3 text-sm">
            <dl className="grid min-w-0 grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-1.5">
              <dt className="text-ink-soft">Expression</dt>
              <dd className="break-all font-mono">{job.schedule.expr}</dd>
              <dt className="text-ink-soft">Timezone</dt><dd>{job.schedule.timezone}</dd>
              <dt className="text-ink-soft">Syntax</dt>
              <dd>{job.schedule.kind}</dd>
              <dt className="text-ink-soft">Misfires</dt>
              <dd>{job.misfire_policy}</dd>
              <dt className="text-ink-soft">Overlap</dt>
              <dd>{job.overlap_policy}</dd>
              <dt className="text-ink-soft">Next run</dt>
              <dd>
                {job.next_run_at ? (
                  <time dateTime={job.next_run_at} title={formatDateTime(job.next_run_at, job.schedule.timezone)}>
                    {relativeTime(job.next_run_at)}
                  </time>
                ) : (
                  "—"
                )}
              </dd>
            </dl>
            {job.next_runs && job.next_runs.length > 0 ? (
              <div className="border-t border-line pt-3">
                <h3 className="mb-1 text-xs font-bold lowercase text-ink-soft">upcoming</h3>
                <ol className="grid gap-0.5 text-xs tabular-nums">
                  {job.next_runs.map((t) => (
                    <li key={t} className="flex flex-wrap justify-between gap-x-3">
                      <span>{formatDateTime(t, job.schedule.timezone)}</span>
                      {job.schedule.timezone !== "UTC" ? <span className="text-ink-soft">{formatDateTime(t, "UTC")}</span> : null}
                    </li>
                  ))}
                </ol>
              </div>
            ) : null}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle tab>action · {job.sink.kind}</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-3 text-sm">
            <dl className="grid min-w-0 grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-1.5">
              {job.sink.kind === "webhook" ? (
                <>
                  <dt className="text-ink-soft">Request</dt>
                  <dd className="break-all font-mono">
                    {String(config.method ?? "POST")} {String(config.url ?? "")}
                  </dd>
                </>
              ) : (
                <>
                  <dt className="text-ink-soft">Command</dt>
                  <dd className="break-all font-mono">{String(config.command ?? "")}</dd>
                </>
              )}
              <dt className="text-ink-soft">Timeout</dt>
              <dd>{job.timeout}</dd>
              <dt className="text-ink-soft">Retries</dt>
              <dd>{job.retry.max_attempts ?? "default"} attempts</dd>
              <dt className="text-ink-soft">Tags</dt>
              <dd>{job.tags.length > 0 ? job.tags.join(", ") : "—"}</dd>
            </dl>
            {job.sink.kind === "webhook" ? (
              <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line pt-3">
                <div className="grid gap-0.5">
                  <h3 className="text-xs font-bold lowercase text-ink-soft">signing secret</h3>
                  <p className="text-xs text-ink-soft">Only shown when it’s created or rotated.</p>
                </div>
                <Button variant="outline" size="sm" onClick={onRotate}>
                  <RotateCw /> Rotate secret
                </Button>
              </div>
            ) : null}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function RunsTab({ job }: { job: Job }) {
  const base = usePublicUrl();
  const [status, setStatus] = useState<Run["status"] | "">("");
  const runs = useJobRuns(job.id, status ? { status } : {});
  const rows = runs.data?.pages.flatMap((p) => p.data) ?? [];
  return (
    <div className="grid gap-4">
      <div className="flex items-center gap-2">
        <Combobox className="w-full sm:w-56" label="Status" value={status} onChange={(value) => setStatus(value as Run["status"] | "")} options={[{value:"",label:"All"}, ...["running", "deferred", "succeeded", "failed", "skipped", "cancelled"].map((value) => ({value}))]} />
      </div>
      {runs.isPending ? <Loading label="Loading runs" /> : null}
      {runs.isError ? <ErrorState error={runs.error} retry={() => void runs.refetch()} /> : null}
      {runs.isSuccess && rows.length === 0 ? (
        <EmptyState
          title="No runs yet"
          description={status ? "No runs have this status." : "Runs appear here as soon as the job fires. Trigger it to see one now."}
          curl={status ? undefined : `${listRunsCurl(base, job.id)}\n\n# create a similar job\n${createJobCurl(base)}`}
          label="list runs command"
        />
      ) : null}
      {rows.length > 0 ? (
        <div className="box min-w-0">
          <Table className="min-w-[36rem]">
            <TableHeader>
              <TableRow>
                <TableHead>Status</TableHead>
                <TableHead>Scheduled</TableHead>
                <TableHead>Trigger</TableHead>
                <TableHead>Start lag</TableHead>
                <TableHead>Duration</TableHead>
                <TableHead>Attempts</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={r.id}>
                  <TableCell>
                    <Link to="/runs/$runId" params={{ runId: r.id }} aria-label={`Run ${r.id}, ${r.status}`}>
                      <StatusPill status={r.status} />
                    </Link>
                    <SkipReason run={r} />
                  </TableCell>
                  <TableCell>
                    <DualTime iso={r.scheduled_at} timeZone={job.schedule.timezone} seconds />
                  </TableCell>
                  <TableCell>{r.trigger}</TableCell>
                  <TableCell className="tabular-nums">
                    {formatDuration(r.start_lag_ms)}
                    {r.sla_breached ? <Badge variant="warning" className="ml-2">late</Badge> : null}
                  </TableCell>
                  <TableCell className="tabular-nums">{formatDuration(r.e2e_ms)}</TableCell>
                  <TableCell className="tabular-nums">{r.attempts}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : null}
      {runs.hasNextPage ? (
        <div className="flex justify-center">
          <Button variant="outline" onClick={() => void runs.fetchNextPage()} disabled={runs.isFetchingNextPage}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function AuditTab({ jobId }: { jobId: string }) {
  // The audit log is workspace-wide; show the entries that target this job.
  const log = useQuery({
    queryKey: [...keys.audit, jobId],
    queryFn: () => call(api.GET("/api/v1/audit-log", { params: { query: { limit: 200 } } })),
  });
  if (log.isPending) return <Loading label="Loading audit log" />;
  if (log.isError) return <ErrorState error={log.error instanceof ApiError ? log.error : new Error("Cannot load audit log")} retry={() => void log.refetch()} />;
  const entries = log.data.data.filter((e) => e.target === jobId);
  if (entries.length === 0) return <p className="rounded-md border border-dashed border-ink-soft p-6 text-center text-sm text-ink-soft">No recorded changes for this job yet.</p>;
  return (
    <div className="box min-w-0">
      <Table className="min-w-[36rem]">
        <TableHeader>
          <TableRow>
            <TableHead>When</TableHead>
            <TableHead>Who</TableHead>
            <TableHead>Action</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {entries.map((e) => (
            <TableRow key={e.id}>
              <TableCell>
                <time dateTime={e.created_at} title={formatDateTime(e.created_at, "UTC", { seconds: true })}>{formatDateTime(e.created_at)}<span className="block text-xs text-ink-soft">{relativeTime(e.created_at)}</span></time>
              </TableCell>
              <TableCell className="font-mono text-xs">{e.actor}</TableCell>
              <TableCell>{e.action}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
