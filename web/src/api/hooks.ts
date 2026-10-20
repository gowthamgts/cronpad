import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, call, getWorkspace, type Schemas } from "./client";

export type Job = Schemas["Job"];
export type JobCreate = Schemas["JobCreate"];
export type JobInput = Schemas["JobInput"];
export type Run = Schemas["Run"] & { skip_reason?: string | null };
export type RunDetail = Schemas["RunDetail"] & { skip_reason?: string | null };
export type Stats = Schemas["Stats"];

const ws = () => getWorkspace() ?? "personal";

export const keys = {
  features: ["features"] as const,
  me: ["me"] as const,
  usage: () => [ws(), "usage"] as const,
  stats: (window: string) => [ws(), "stats", window] as const,
  jobs: (filters: object) => [ws(), "jobs", filters] as const,
  job: (id: string) => [ws(), "job", id] as const,
  jobRuns: (id: string, filters: object) => [ws(), "job-runs", id, filters] as const,
  runs: (filters: object) => [ws(), "runs", filters] as const,
  run: (id: string) => [ws(), "run", id] as const,
  audit: [ws(), "audit"] as const,
  apiKeys: () => [ws(), "api-keys"] as const,
  secrets: () => [ws(), "secrets"] as const,
  alertChannels: () => [ws(), "alert-channels"] as const,
  alertRules: () => [ws(), "alert-rules"] as const,
};

export function useFeatures() {
  return useQuery({
    queryKey: keys.features,
    queryFn: () => call(api.GET("/api/v1/features")),
    staleTime: 60_000,
    retry: false,
  });
}

export function useMe() {
  return useQuery({
    queryKey: keys.me,
    queryFn: () => call(api.GET("/api/v1/me")),
    retry: false,
    staleTime: 30_000,
  });
}

export function useStats(window = "24h" as Stats["window"]) {
  return useQuery({
    queryKey: keys.stats(window),
    queryFn: () => call(api.GET("/api/v1/stats/overview", { params: { query: { window } } })),
    refetchInterval: 30_000,
    // Switching the dashboard range keeps the old numbers on screen until the new ones arrive.
    placeholderData: keepPreviousData,
  });
}

export function useUsage() {
  return useQuery({
    queryKey: keys.usage(),
    queryFn: () => call(api.GET("/api/v1/usage")),
    refetchInterval: 30_000,
  });
}

export interface JobFilters {
  state?: Job["state"];
  tag?: string;
  q?: string;
}

export function useJobs(filters: JobFilters, limit = 50) {
  return useInfiniteQuery({
    queryKey: keys.jobs({ ...filters, limit }),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      call(api.GET("/api/v1/jobs", { params: { query: { ...filters, limit, cursor: pageParam } } })),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}

export function useJob(id: string) {
  return useQuery({ queryKey: keys.job(id), queryFn: () => call(api.GET("/api/v1/jobs/{id}", { params: { path: { id } } })) });
}

export interface RunFilters {
  status?: Run["status"];
  from?: string;
  job_id?: string;
}

export function useRuns(filters: RunFilters, limit = 50, opts: { enabled?: boolean; refetchInterval?: number } = {}) {
  return useInfiniteQuery({
    queryKey: keys.runs({ ...filters, limit }),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => call(api.GET("/api/v1/runs", { params: { query: { ...filters, limit, cursor: pageParam } } })),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled: opts.enabled,
    refetchInterval: opts.refetchInterval,
    placeholderData: keepPreviousData,
  });
}

export function useJobRuns(id: string, filters: { status?: Run["status"] }, limit = 25) {
  return useInfiniteQuery({
    queryKey: keys.jobRuns(id, { ...filters, limit }),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      call(api.GET("/api/v1/jobs/{id}/runs", { params: { path: { id }, query: { ...filters, limit, cursor: pageParam } } })),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    refetchInterval: 5_000,
  });
}

export function useRun(id: string) {
  return useQuery({
    queryKey: keys.run(id),
    queryFn: () => call(api.GET("/api/v1/runs/{id}", { params: { path: { id } } })),
    refetchInterval: (query) => (query.state.data && ["running", "deferred"].includes(query.state.data.status) ? 2_000 : false),
  });
}

/** Invalidates everything workspace-scoped after a mutation. */
export function useInvalidate() {
  const client = useQueryClient();
  return () => client.invalidateQueries({ predicate: (q) => q.queryKey[0] !== "features" && q.queryKey[0] !== "me" });
}

export function useJobAction() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: ({ id, action }: { id: string; action: "pause" | "resume" | "enable" }) => {
      const path = `/api/v1/jobs/{id}/${action}` as const;
      return call(api.POST(path, { params: { path: { id } } }));
    },
    onSuccess: invalidate,
  });
}

/** Active jobs, newest first; the dashboard filters these to the next hour. */
export function useActiveJobs() {
  return useQuery({
    queryKey: [ws(), "active-jobs"],
    queryFn: () => call(api.GET("/api/v1/jobs", { params: { query: { state: "active", limit: 200 } } })),
    refetchInterval: 30_000,
  });
}
