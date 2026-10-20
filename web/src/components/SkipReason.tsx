const LABELS: Record<string, string> = {
  quota_exceeded: "Monthly run quota exceeded",
  workspace_suspended: "Workspace suspended",
  previous_run_in_progress: "Previous run still in progress",
  misfire: "Missed scheduled time",
};

export function SkipReason({ run }: { run: { status: string; skip_reason?: string | null } }) {
  if (run.status !== "skipped" || !run.skip_reason) return null;
  return <span className="block text-sm text-muted-foreground">Skipped: {LABELS[run.skip_reason] ?? run.skip_reason}</span>;
}
