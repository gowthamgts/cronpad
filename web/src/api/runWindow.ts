import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, call, getWorkspace } from "./client";
import type { Run } from "./hooks";

/**
 * The dashboard chart needs every run in its range, and there is no time-series
 * endpoint, so it pages through /runs. Buckets that have closed and hold only
 * finished runs no longer change, so they are kept in memory ("frozen") and
 * each refetch only asks for runs from the first open bucket onwards.
 */

const PAGE_SIZE = 200;
/** Caps one fetch so a busy 30d range stays light; older buckets then show as "not loaded". */
const MAX_PAGES = 10;
/** A bucket is only frozen this long after it closes, so late-dispatched runs still land in it. */
const SETTLE_MS = 5 * 60_000;
/** Frozen buckets are refetched in full after this, to pick up backfilled misfires, deleted jobs and pruned history. */
const FULL_REFRESH_MS = 15 * 60_000;
const TERMINAL = new Set<Run["status"]>(["succeeded", "failed", "skipped", "cancelled"]);

export interface RunWindow {
  /** Runs in the window, newest first. */
  runs: Run[];
  /** Runs scheduled before this bucket boundary are final and are not fetched again. */
  frozenUntil: number;
  /** Set when the page cap stopped a fetch: runs scheduled before this are unknown, not zero. */
  loadedFrom?: number;
  /** When the window was last fetched in full. */
  fullAt: number;
}

export interface WindowShape {
  count: number;
  size: number;
}

const at = (r: Pick<Run, "scheduled_at">) => new Date(r.scheduled_at).getTime();

/** First instant of the window: `count` buckets ending with the one that contains `now`. */
export function windowStart(now: number, { count, size }: WindowShape): number {
  return Math.floor(now / size) * size + size - count * size;
}

/** Where the next fetch starts: the frozen boundary when the cache is reusable, else the window start. */
export function planFetch(prev: RunWindow | undefined, now: number, shape: WindowShape): { from: number; reuse: boolean } {
  const start = windowStart(now, shape);
  const reuse = prev !== undefined && now - prev.fullAt < FULL_REFRESH_MS && prev.frozenUntil > start;
  return { from: reuse ? prev.frozenUntil : start, reuse };
}

/** Combines freshly fetched runs (scheduled at or after `from`) with the frozen part of `prev`. */
export function mergeWindow(prev: RunWindow | undefined, fresh: Run[], truncated: boolean, plan: { from: number; reuse: boolean }, now: number, shape: WindowShape): RunWindow {
  const start = windowStart(now, shape);
  // A truncated incremental fetch leaves a gap before the frozen runs, so start over from what was fetched.
  const keep = plan.reuse && !truncated && prev ? prev.runs.filter((r) => at(r) < prev.frozenUntil && at(r) >= start) : [];
  const runs = [...fresh, ...keep];
  const loadedFrom = truncated ? Math.min(...fresh.map(at)) : plan.reuse ? prev?.loadedFrom : undefined;
  // Freeze up to the last bucket that closed a while ago and holds no unfinished run.
  let frozenUntil = Math.floor((now - SETTLE_MS) / shape.size) * shape.size;
  for (const r of runs) if (!TERMINAL.has(r.status)) frozenUntil = Math.min(frozenUntil, Math.floor(at(r) / shape.size) * shape.size);
  return { runs, frozenUntil: Math.max(frozenUntil, plan.from), loadedFrom, fullAt: plan.reuse && !truncated && prev ? prev.fullAt : now };
}

async function fetchSince(from: number): Promise<{ runs: Run[]; truncated: boolean }> {
  const runs: Run[] = [];
  let cursor: string | undefined;
  for (let page = 1; ; page++) {
    const res = await call(api.GET("/api/v1/runs", { params: { query: { from: new Date(from).toISOString(), limit: PAGE_SIZE, cursor } } }));
    runs.push(...res.data);
    if (!res.next_cursor) return { runs, truncated: false };
    if (page >= MAX_PAGES) return { runs, truncated: true };
    cursor = res.next_cursor;
  }
}

/** Runs for a dashboard chart range, refetching only the buckets that can still change. */
export function useRunWindow(range: string, shape: WindowShape) {
  const client = useQueryClient();
  return useQuery({
    queryKey: [getWorkspace() ?? "personal", "run-window", range] as const,
    queryFn: async ({ queryKey }) => {
      const now = Date.now();
      const prev = client.getQueryData<RunWindow>(queryKey);
      const plan = planFetch(prev, now, shape);
      const { runs, truncated } = await fetchSince(plan.from);
      return mergeWindow(prev, runs, truncated, plan, now, shape);
    },
    refetchInterval: 30_000,
    // Keep the frozen buckets as long as they are trusted, even while the dashboard isn't open.
    gcTime: FULL_REFRESH_MS,
    placeholderData: keepPreviousData,
  });
}
