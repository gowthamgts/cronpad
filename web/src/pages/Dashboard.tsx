import { IntegrateCard } from "@/components/IntegrateCard";
import { LatencyMethod, LatencyRuler } from "@/components/LatencyRuler";
import { InfoTip } from "@/components/ui/info-tip";
import { GettingStartedNotices, OverviewNotices, useOverviewStats } from "@/ext/slots";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import { useActiveJobs, useRuns, useStats } from "@/api/hooks";
import { RANGES, RunsChart, bucketRuns, type RangeKey } from "@/components/RunsChart";
import { EmptyState, ErrorState, Loading, PageHeader, StatStrip, type Stat } from "@/components/states";
import { Segmented } from "@/components/ui/segmented";
import { DualTime } from "@/components/DualTime";
import { createJobCurl, usePublicUrl } from "@/components/curl";
import { buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { formatDuration, percent } from "@/lib/format";
import { usePageTitle } from "@/lib/usePageTitle";
import { usePageShortcuts } from "@/lib/shortcuts";

// The chart pages through runs (no time-series endpoint yet); cap the fetch so 30d stays light.
const MAX_RUN_PAGES = 10;
const UNIT_TITLE: Record<RangeKey, string> = { "24h": "runs per hour", "7d": "runs per 6 hours", "30d": "runs per day" };

export function DashboardPage() {
  usePageTitle("Dashboard");
  const base = usePublicUrl();
  const navigate = useNavigate();
  const range: RangeKey = useSearch({ from: "/_authed/" }).range ?? "24h";
  const setRange = (value: RangeKey) => void navigate({ to: "/", search: value === "24h" ? {} : { range: value }, replace: true });
  usePageShortcuts([
    { keys: ["1"], label: "Show the last 24 hours", run: () => setRange("24h") },
    { keys: ["2"], label: "Show the last 7 days", run: () => setRange("7d") },
    { keys: ["3"], label: "Show the last 30 days", run: () => setRange("30d") },
  ]);
  const { span } = RANGES[range];
  const stats = useStats(range);
  const extraStats = useOverviewStats();
  const upcoming = useActiveJobs();
  // The chart needs bucketed counts, which the stats endpoint doesn't provide: page through the range's runs.
  const [anchor] = useState(() => Date.now());
  const since = new Date(anchor - RANGES[range].count * RANGES[range].size).toISOString();
  const runs = useRuns({ from: since }, 200, { refetchInterval: 30_000 });
  const pages = runs.data?.pages.length ?? 0;
  useEffect(() => {
    if (runs.hasNextPage && !runs.isFetchingNextPage && !runs.isPlaceholderData && pages < MAX_RUN_PAGES) void runs.fetchNextPage();
  }, [runs, pages]);
  const loaded = runs.data?.pages.flatMap((p) => p.data) ?? [];
  // When the cap stops paging, everything older than the oldest fetched run is unknown, not zero.
  const loadedFrom = runs.hasNextPage && pages >= MAX_RUN_PAGES && loaded.length > 0 ? Math.min(...loaded.map((r) => new Date(r.scheduled_at).getTime())) : undefined;

  const buckets = useMemo(() => bucketRuns(runs.data?.pages.flatMap((p) => p.data) ?? [], undefined, range), [runs.data, range]);

  if (stats.isPending) return <Loading label="Loading dashboard" />;
  if (stats.isError) return <ErrorState error={stats.error} retry={() => void stats.refetch()} />;
  const s = stats.data;
  const totalJobs = Object.values(s.jobs).reduce((a, b) => a + b, 0);

  if (totalJobs === 0) {
    return (
      <>
        <PageHeader title="dashboard" description="Your schedules and measured runs." />
        <GettingStartedNotices />
        <EmptyState
          title="Create your first job"
          description="Pick a schedule and point it at a webhook to record your first run."
          action={
            <Link to="/jobs/new" className={buttonVariants()}>
              New job
            </Link>
          }
          curl={createJobCurl(base)}
          label="create job command"
        />
      </>
    );
  }

  // Measured from when the job list was fetched, so rendering stays pure.
  const horizon = upcoming.dataUpdatedAt + 3_600_000;
  const soon = (upcoming.data?.data ?? [])
    .filter((j) => j.next_run_at && new Date(j.next_run_at).getTime() <= horizon)
    .sort((a, b) => new Date(a.next_run_at!).getTime() - new Date(b.next_run_at!).getTime());

  const kpis: Stat[] = [
    { label: `runs · ${range}`, value: s.runs.total.toLocaleString("en"), hint: s.runs.sla_breached > 0 ? `${s.runs.sla_breached.toLocaleString("en")} started late` : "none started late" },
    { label: "success rate", value: percent(s.runs.succeeded, s.runs.total), hint: `${s.runs.succeeded.toLocaleString("en")} succeeded` },
    { label: "failed", value: s.runs.failed.toLocaleString("en"), tone: s.runs.failed > 0 ? "danger" : undefined, hint: s.failing_jobs.length > 0 ? `${s.failing_jobs.length} ${s.failing_jobs.length === 1 ? "job" : "jobs"} failing now` : "no job failing now" },
    { label: "p95 start lag", value: s.runs.total > 0 ? formatDuration(s.start_lag_ms.p95) : "—", hint: s.runs.total > 0 ? `typical ${formatDuration(s.start_lag_ms.p50)}` : "no runs measured" },
    ...extraStats,
  ];

  return (
    <>
      <PageHeader
        title="dashboard"
        description={`The last ${span} across this workspace.`}
        actions={
          <>
            <Segmented
              label="Time range"
              className="h-10 self-center p-[2px] shadow-[3px_3px_0_hsl(var(--ink)/0.3)] sm:h-8 dark:shadow-[3px_3px_0_hsl(var(--ink)/0.35)] [&>label]:h-full [&>label]:min-h-0"
              value={range}
              options={(Object.keys(RANGES) as RangeKey[]).map((value) => ({ value, label: RANGES[value].label }))}
              onChange={setRange}
            />
            <Link to="/jobs/new" className={buttonVariants()}>
              New job
            </Link>
          </>
        }
      />

      <section aria-labelledby="overview-heading" className="mb-5 grid gap-2">
        <h2 id="overview-heading" className="sr-only">
          Overview, last {span}
        </h2>
        <StatStrip label={`Overview, last ${span}`} items={kpis} columns={kpis.length > 4 ? 5 : 4} />
        <OverviewNotices />
        {s.jobs.disabled > 0 ? (
          <p role="status" className="text-sm text-ink">
            <Link to="/jobs" search={{ state: "disabled" }} className="font-medium text-link underline-offset-4 hover:underline">
              {s.jobs.disabled} {s.jobs.disabled === 1 ? "job is" : "jobs are"} disabled
            </Link>{" "}
            after repeated failures.
          </p>
        ) : null}
      </section>

      <section aria-labelledby="runs-heading" className="mb-5 grid gap-5 lg:grid-cols-3">
        <h2 id="runs-heading" className="sr-only">
          Runs and latency
        </h2>
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle tab>{UNIT_TITLE[range]}</CardTitle>
            <CardDescription>
              {s.runs.total === 0 ? `No runs in the last ${span}.` : <>{s.runs.total.toLocaleString("en")} runs · {percent(s.runs.succeeded, s.runs.total)} succeeded · {s.runs.failed.toLocaleString("en")} failed</>}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <RunsChart buckets={buckets} range={range} loadedFrom={loadedFrom} />
          </CardContent>
        </Card>
        <IntegrateCard />
      </section>

      <div className="grid gap-5 lg:grid-cols-3">
        <Card>
          <InfoTip label="How start lag is measured" className="absolute right-1 top-1">
            <LatencyMethod span={span} />
          </InfoTip>
          <CardHeader className="pr-12">
            <CardTitle tab>start lag</CardTitle>
            <CardDescription>Scheduled to started · {span}</CardDescription>
          </CardHeader>
          <CardContent>
            <LatencyRuler values={s.start_lag_ms} measured={s.runs.total > 0} span={span} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle tab>failing jobs</CardTitle>
            <CardDescription>Jobs whose latest runs failed</CardDescription>
          </CardHeader>
          <CardContent>
            {s.failing_jobs.length === 0 ? (
              <div className="grid gap-2">
                <p className="marker">none</p>
                <p className="rounded border border-line bg-ok-soft px-3 py-2 text-sm text-success">Nothing is failing.</p>
              </div>
            ) : (
              // Focusable so keyboard users can scroll the list (axe: scrollable-region-focusable).
              // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
              <div tabIndex={0} role="region" aria-label="Failing jobs" className="max-h-64 overflow-y-auto focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring">
              <ul className="grid">
                {s.failing_jobs.map((j) => (
                  <li key={j.id} className={`grid gap-0.5 border-b border-line py-2 first:pt-0 ${s.failing_jobs.length > 1 ? "last:border-0 last:pb-0" : ""}`}>
                    <Link to="/jobs/$jobId" params={{ jobId: j.id }} className="font-medium text-link underline-offset-4 hover:underline">
                      {j.name}
                    </Link>
                    <span className="text-sm text-ink-soft">{j.consecutive_failures} {j.consecutive_failures === 1 ? "failure" : "failures"} in a row
                      {(() => { const failure = runs.data?.pages.flatMap((p) => p.data).find((r) => r.job_id === j.id && r.status === "failed"); const timezone = upcoming.data?.data.find((job) => job.id === j.id)?.schedule.timezone; return failure ? <DualTime className="block text-xs" iso={failure.scheduled_at} timeZone={timezone} /> : null; })()}
                    </span>
                  </li>
                ))}
              </ul>
              </div>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle tab>upcoming in the next hour</CardTitle>
            <CardDescription>Next fire of each active job</CardDescription>
          </CardHeader>
          <CardContent>
            {upcoming.isPending ? <Loading /> : null}
            {upcoming.isSuccess && soon.length === 0 ? <div className="grid gap-2"><p className="marker">none</p><p className="text-sm text-ink-soft">Nothing is scheduled in the next hour.</p></div> : null}
            {/* Focusable so keyboard users can scroll the list (axe: scrollable-region-focusable). */}
            {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
            <div tabIndex={0} role="region" aria-label="Upcoming jobs" className="max-h-64 overflow-y-auto focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring">
            <ol className="grid">
              {soon.map((j) => (
                <li key={j.id} className={`grid gap-0.5 border-b border-line py-2 first:pt-0 ${soon.length > 1 ? "last:border-0 last:pb-0" : ""}`}>
                  <Link to="/jobs/$jobId" params={{ jobId: j.id }} className="font-medium text-link underline-offset-4 hover:underline">
                    {j.name}
                  </Link>
                  <DualTime iso={j.next_run_at!} timeZone={j.schedule.timezone} relative className="text-sm" />
                </li>
              ))}
            </ol>
            </div>
          </CardContent>
        </Card>
      </div>
    </>
  );
}
