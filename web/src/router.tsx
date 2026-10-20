import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, createRoute, createRouter, Link, Outlet, redirect } from "@tanstack/react-router";
import { ApiError, call, api } from "@/api/client";
import { keys, type Job } from "@/api/hooks";
import { AppShell } from "@/components/AppShell";
import { buttonVariants } from "@/components/ui/button";
import { AlertsPage } from "@/pages/Alerts";
import { DashboardPage } from "@/pages/Dashboard";
import { EditJobPage, NewJobPage } from "@/pages/JobForm";
import { JobDetailPage } from "@/pages/JobDetail";
import { JobsPage } from "@/pages/Jobs";
import { LoginPage } from "@/pages/Login";
import { RunDetailPage } from "@/pages/RunDetail";
import { SettingsPage } from "@/pages/Settings";

interface RouterContext {
  queryClient: QueryClient;
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: () => <Outlet />,
  notFoundComponent: () => (
    <main className="grid min-h-screen place-items-center px-4 text-center">
      <div className="grid gap-3">
        <h1 className="text-2xl font-bold">Page not found</h1>
        <Link to="/" className={buttonVariants()}>
          Back to the dashboard
        </Link>
      </div>
    </main>
  ),
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (s: Record<string, unknown>): { redirect?: string; error?: string } => ({
    ...(typeof s.redirect === "string" ? { redirect: s.redirect } : {}),
    ...(typeof s.error === "string" ? { error: s.error } : {}),
  }),
  component: function LoginRoute() {
    const { redirect, error } = loginRoute.useSearch();
    return <LoginPage redirect={redirect} error={error} />;
  },
});

// Onboarding for an empty server; it hands over to /login once the first account exists.
const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/setup",
  validateSearch: (s: Record<string, unknown>): { redirect?: string } => (typeof s.redirect === "string" ? { redirect: s.redirect } : {}),
  component: function SetupRoute() {
    return <LoginPage setup redirect={setupRoute.useSearch().redirect} />;
  },
});

// Every page except login needs a session; an unauthenticated call sends the user to sign in.
const authedRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "_authed",
  beforeLoad: async ({ context, location }) => {
    try {
      await context.queryClient.fetchQuery({ queryKey: keys.me, queryFn: () => call(api.GET("/api/v1/me")), staleTime: 30_000 });
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) throw redirect({ to: "/login", search: { redirect: location.href } });
      throw e;
    }
  },
  component: AppShell,
});

/** `?tab=` on pages with tabs, so every tab has its own link; the first tab is the default and stays out of the URL. */
function tabSearch<T extends string>(tabs: readonly T[]) {
  return (s: Record<string, unknown>): { tab?: T } => (tabs.includes(s.tab as T) ? { tab: s.tab as T } : {});
}
export const JOB_TABS = ["runs", "audit"] as const;
export const ALERT_TABS = ["rules"] as const;
export const SETTINGS_TABS = ["secrets", "workspace"] as const;

const dashboardRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: "/",
  component: DashboardPage,
  validateSearch: (s: Record<string, unknown>): { range?: "7d" | "30d" } => (s.range === "7d" || s.range === "30d" ? { range: s.range } : {}),
});

const jobsRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: "/jobs",
  validateSearch: (s: Record<string, unknown>): { q?: string; state?: Job["state"]; tag?: string } => ({
    q: typeof s.q === "string" && s.q ? s.q : undefined,
    state: ["active", "paused", "disabled", "completed"].includes(String(s.state)) ? (s.state as Job["state"]) : undefined,
    tag: typeof s.tag === "string" && s.tag ? s.tag : undefined,
  }),
  component: JobsPage,
});
const newJobRoute = createRoute({ getParentRoute: () => authedRoute, path: "/jobs/new", component: NewJobPage });
const jobRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: "/jobs/$jobId",
  validateSearch: tabSearch(JOB_TABS),
  component: function JobRoute() {
    return <JobDetailPage jobId={jobRoute.useParams().jobId} />;
  },
});
const editJobRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: "/jobs/$jobId/edit",
  component: function EditRoute() {
    return <EditJobPage jobId={editJobRoute.useParams().jobId} />;
  },
});
const runRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: "/runs/$runId",
  component: function RunRoute() {
    return <RunDetailPage runId={runRoute.useParams().runId} />;
  },
});
const alertsRoute = createRoute({ getParentRoute: () => authedRoute, path: "/alerts", validateSearch: tabSearch(ALERT_TABS), component: AlertsPage });
const settingsRoute = createRoute({ getParentRoute: () => authedRoute, path: "/settings", validateSearch: tabSearch(SETTINGS_TABS), component: SettingsPage });

const routeTree = rootRoute.addChildren([
  loginRoute,
  setupRoute,
  authedRoute.addChildren([dashboardRoute, jobsRoute, newJobRoute, jobRoute, editJobRoute, runRoute, alertsRoute, settingsRoute]),
]);

export function createAppRouter(queryClient: QueryClient) {
  return createRouter({ routeTree, basepath: "/", context: { queryClient }, defaultPreload: "intent" });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
