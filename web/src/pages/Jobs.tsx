import { useNavigate, useSearch, Link } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";
import cronstrue from "cronstrue";
import { Plus } from "lucide-react";
import { useJobAction, useJobs, useRuns, type Job } from "@/api/hooks";
import { Field } from "@/components/Field";
import { Sparkline } from "@/components/Sparkline";
import { StatusPill } from "@/components/StatusPill";
import { createJobCurl, usePublicUrl } from "@/components/curl";
import { EmptyState, ErrorState, Loading, PageHeader } from "@/components/states";
import { Button, buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import Combobox from "@/components/ui/combobox";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDateTime, percent, relativeTime } from "@/lib/format";
import { usePageTitle } from "@/lib/usePageTitle";

export function scheduleSummary(job: Job): string {
  if (job.schedule.description) return job.schedule.description;
  if (job.schedule.kind === "cron") {
    try {
      return cronstrue.toString(job.schedule.expr);
    } catch {
      /* fall through to the raw expression */
    }
  }
  return job.schedule.expr;
}

function StateToggle({ job }: { job: Job }) {
  const action = useJobAction();
  if (job.state === "completed") return <span className="text-xs text-ink-soft">completed</span>;
  const on = job.state === "active";
  const next = on ? "pause" : job.state === "disabled" ? "enable" : "resume";
  return (
    <div className="flex items-center gap-2">
      <Switch
        checked={on}
        disabled={action.isPending}
        aria-label={`${job.name} is ${job.state}. ${on ? "Pause" : next === "enable" ? "Enable" : "Resume"} job`}
        onCheckedChange={() => action.mutate({ id: job.id, action: next })}
      />
      <span className="text-xs text-ink-soft" aria-hidden="true">
        {job.state}
      </span>
    </div>
  );
}

export function JobsPage() {
  usePageTitle("Jobs");
  const base = usePublicUrl();
  const navigate = useNavigate();
  const search = useSearch({ from: "/_authed/jobs" });
  const [q, setQ] = useState(search.q ?? "");
  const searchRef = useRef<HTMLInputElement>(null);

  // Keep the URL (and so the query) in step with the debounced search box.
  useEffect(() => {
    const t = setTimeout(() => {
      if ((search.q ?? "") !== q.trim()) void navigate({ to: "/jobs", search: (prev) => ({ ...prev, q: q.trim() || undefined }), replace: true });
    }, 300);
    return () => clearTimeout(t);
  }, [q, search.q, navigate]);

  const jobs = useJobs({ state: search.state, tag: search.tag, q: search.q });
  const recent = useRuns({}, 200);

  const history = useMemo(() => {
    const byJob = new Map<string, { at: string; status: string }[]>();
    for (const run of recent.data?.pages.flatMap((p) => p.data) ?? []) {
      const list = byJob.get(run.job_id) ?? [];
      list.push({ at: run.scheduled_at, status: run.status });
      byJob.set(run.job_id, list);
    }
    return new Map([...byJob].map(([id, list]) => [id, list.sort((a, b) => a.at.localeCompare(b.at)).slice(-20).map((r) => r.status)]));
  }, [recent.data]);

  const rows = jobs.data?.pages.flatMap((p) => p.data) ?? [];
  const filtered = Boolean(search.q || search.state || search.tag);

  return (
    <>
      <PageHeader
        title="jobs"
        description="Manage schedules and keep an eye on every delivery."
        actions={
          <Link to="/jobs/new" className={buttonVariants()}>
            <Plus /> New job
          </Link>
        }
      />

      <form role="search" aria-label="Filter jobs" className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-[minmax(0,1fr)_11rem_11rem]" onSubmit={(e) => e.preventDefault()}>
        <Field label="Search" className="col-span-2 sm:col-span-1">
          {(p) => <Input {...p} ref={searchRef} data-shortcut-search type="search" placeholder="Search by name (press / to focus)" value={q} onChange={(e) => setQ(e.target.value)} />}
        </Field>
        <Combobox label="State" value={search.state ?? ""} options={[{value:"",label:"All states"}, ...["active", "paused", "disabled", "completed"].map((value) => ({value}))]} onChange={(value) => void navigate({ to: "/jobs", search: (prev) => ({ ...prev, state: (value || undefined) as Job["state"] | undefined }) })} />
        <Field label="Tag">
          {(p) => <Input {...p} placeholder="any tag" defaultValue={search.tag ?? ""} onBlur={(e) => void navigate({ to: "/jobs", search: (prev) => ({ ...prev, tag: e.target.value.trim() || undefined }) })} onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()} />}
        </Field>
      </form>

      {jobs.isPending ? <Loading label="Loading jobs" /> : null}
      {jobs.isError ? <ErrorState error={jobs.error} retry={() => void jobs.refetch()} /> : null}
      {jobs.isSuccess && rows.length === 0 ? (
        filtered ? (
          <p className="rounded-md border border-dashed border-ink-soft p-6 text-center text-sm text-ink-soft">No jobs match these filters.</p>
        ) : (
          <EmptyState
            title="No jobs yet"
            description="Create a job to run a webhook on a schedule."
            action={
              <Link to="/jobs/new" className={buttonVariants()}>
                New job
              </Link>
            }
            curl={createJobCurl(base)}
            label="create job command"
          />
        )
      ) : null}

      {rows.length > 0 ? (
        <div className="box min-w-0">
          <Table className="min-w-[56rem]">
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Schedule</TableHead>
                <TableHead>Next run</TableHead>
                <TableHead>Last run</TableHead>
                <TableHead>Recent runs</TableHead>
                <TableHead>State</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((job) => {
                const statuses = history.get(job.id) ?? [];
                const ok = statuses.filter((s) => s === "succeeded").length;
                return (
                  <TableRow key={job.id}>
                    <TableCell className="min-w-44 max-w-72">
                      <Link to="/jobs/$jobId" params={{ jobId: job.id }} className="block truncate font-bold text-link underline-offset-4 hover:underline" title={job.name}>
                        {job.name}
                      </Link>
                      {job.tags.length > 0 ? <p className="truncate text-xs text-muted-foreground">{job.tags.join(", ")}</p> : null}
                    </TableCell>
                    <TableCell className="min-w-44 max-w-64">
                      <span className="block truncate" title={job.schedule.expr}>{scheduleSummary(job)}</span>
                      <p className="text-xs text-muted-foreground">{job.schedule.timezone}</p>
                    </TableCell>
                    <TableCell>
                      {job.state === "active" && job.next_run_at ? (
                        <time dateTime={job.next_run_at} title={formatDateTime(job.next_run_at, "UTC")} className="whitespace-nowrap tabular-nums">
                          {formatDateTime(job.next_run_at, job.schedule.timezone)}<span className="block text-xs text-ink-soft">{relativeTime(job.next_run_at)}</span>
                        </time>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell>
                      <StatusPill status={job.last_run_status} />
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <Sparkline statuses={statuses} />
                        {statuses.length > 0 ? <span className="text-xs tabular-nums text-muted-foreground">{percent(ok, statuses.length)}</span> : null}
                      </div>
                    </TableCell>
                    <TableCell>
                      <StateToggle job={job} />
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      ) : null}

      {jobs.hasNextPage ? (
        <div className="mt-4 flex justify-center">
          <Button variant="outline" onClick={() => void jobs.fetchNextPage()} disabled={jobs.isFetchingNextPage}>
            {jobs.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      ) : null}
    </>
  );
}
