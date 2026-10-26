import type { Stat } from "@/components/states";

// Build-time extension points. A downstream build may replace this whole module with its own
// implementation of the same exports; the defaults here render nothing. Keep each export
// small, stable and free of assumptions about what fills it.

/** Extra overview tiles after the built-in ones (at most one, the strip has five columns). Called on every render. */
export function useOverviewStats(): Stat[] {
  return [];
}

/** Below the overview strip, beside the built-in notices. */
export function OverviewNotices() {
  return null;
}

/** Above "Create your first job" while the workspace has no jobs. */
export function GettingStartedNotices() {
  return null;
}

/** At the top of Settings → Workspace. */
export function WorkspaceSettingsTop() {
  return null;
}
