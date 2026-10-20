import { cn } from "@/lib/utils";

/**
 * A strip of recent outcomes, oldest to newest. Failures are taller and use a
 * distinct fill so the strip reads without color vision.
 */
export function Sparkline({ statuses, className }: { statuses: string[]; className?: string }) {
  if (statuses.length === 0) return <span className="text-muted-foreground">—</span>;
  const ok = statuses.filter((s) => s === "succeeded").length;
  const label = `${ok} of ${statuses.length} recent runs succeeded`;
  const width = 4;
  const gap = 2;
  const height = 18;
  return (
    <svg role="img" aria-label={label} width={statuses.length * (width + gap)} height={height} className={cn("inline-block", className)}>
      <title>{label}</title>
      {statuses.map((s, i) => {
        const failed = s === "failed";
        const succeeded = s === "succeeded";
        const h = failed ? height : succeeded ? height * 0.6 : height * 0.35;
        return (
          <rect
            key={i}
            x={i * (width + gap)}
            y={height - h}
            width={width}
            height={h}
            rx={0}
            className={failed ? "fill-chart-failure" : succeeded ? "fill-chart-success" : "fill-chart-other"}
          />
        );
      })}
    </svg>
  );
}
