import type { Usage } from "@/api/client";
import { useUsage } from "@/api/hooks";
import { ErrorState, Loading } from "@/components/states";
import { formatDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";

/** Where a workspace stands against its monthly limit, and what to tell people about it. */
export function usageStatus(usage: Usage) {
  const limited = usage.limit !== null && usage.limit > 0;
  const ratio = limited ? usage.runs / usage.limit! : 0;
  const exceeded = ratio >= 1;
  const warning = ratio >= 0.8;
  const message = !warning
    ? null
    : exceeded
      ? usage.over_quota_policy === "block" ? "Monthly limit reached. New occurrences are skipped until usage resets or your limit increases." : "Monthly limit reached. New occurrences are counted as overage."
      : "You’ve used at least 80% of your monthly runs.";
  return { limited, ratio, exceeded, warning, message };
}

/** A thin bar against the monthly limit, labelled for assistive tech. */
export function UsageBar({ usage, className }: { usage: Usage; className?: string }) {
  const { ratio, exceeded } = usageStatus(usage);
  return (
    <div role="progressbar" aria-label="Monthly runs used" aria-valuemin={0} aria-valuemax={usage.limit!} aria-valuenow={Math.min(usage.runs, usage.limit!)} aria-valuetext={`${usage.runs} of ${usage.limit} runs used`} className={cn("h-3 overflow-hidden rounded-sm border border-ink/40 bg-sunken usage-ruler", className)}>
      <div className={cn("h-full", exceeded ? "bg-failure" : "bg-accent")} style={{ width: `${Math.min(ratio * 100, 100)}%` }} />
    </div>
  );
}

export function UsageMeter({ usage }: { usage: Usage }) {
  const { limited, exceeded, warning, message } = usageStatus(usage);
  return (
    <section aria-label="Monthly usage" className={cn("box mb-5 grid gap-2 px-4 py-3 text-sm", exceeded ? "bg-bad-soft" : warning && "bg-warn-soft")}>
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
        <h2 className="text-xs font-bold lowercase">monthly usage</h2>
        <p className="font-mono font-bold tabular-nums">
          {limited ? `${usage.runs.toLocaleString("en")} / ${usage.limit!.toLocaleString("en")} runs this month` : `${usage.runs.toLocaleString("en")} runs this month · unlimited`}
        </p>
        {limited ? <UsageBar usage={usage} className="min-w-32 flex-1" /> : null}
        <p className="text-xs text-ink-soft sm:ml-auto">Usage period {usage.month} · Resets <time dateTime={usage.reset_at}>{formatDateTime(usage.reset_at, "UTC")}</time></p>
      </div>
      {message ? <p className={exceeded ? "font-medium text-destructive" : "font-medium text-ink"}>{message}</p> : null}
    </section>
  );
}

export function WorkspaceUsage() {
  const usage = useUsage();
  if (usage.isPending) return <Loading label="Loading monthly usage" />;
  if (usage.isError) return <div className="mb-5"><ErrorState error={usage.error} retry={() => void usage.refetch()} /></div>;
  return <UsageMeter usage={usage.data} />;
}
