import { useLayoutEffect, useRef, useState } from "react";
import { formatDateTime } from "@/lib/format";
import type { Run } from "@/api/hooks";

const HOUR_MS = 3_600_000;

export type RangeKey = "24h" | "7d" | "30d";

/** How each dashboard range is split into bars. */
export const RANGES: Record<RangeKey, { label: string; span: string; count: number; size: number; unit: string; tick: number }> = {
  "24h": { label: "24h", span: "24 hours", count: 24, size: HOUR_MS, unit: "hour", tick: 6 },
  "7d": { label: "7d", span: "7 days", count: 28, size: 6 * HOUR_MS, unit: "6 hours", tick: 4 },
  "30d": { label: "30d", span: "30 days", count: 30, size: 24 * HOUR_MS, unit: "day", tick: 5 },
};

export interface Bucket {
  start: number;
  succeeded: number;
  failed: number;
  skipped: number;
  other: number;
  /** Skipped runs by reason label (e.g. "missed scheduled time"). */
  skipDetail: Record<string, number>;
}

const SKIP_LABELS: Record<string, string> = {
  quota_exceeded: "quota exceeded",
  workspace_suspended: "workspace suspended",
  previous_run_in_progress: "previous run in progress",
  misfire: "missed scheduled time",
};

/** Groups runs into the range's buckets; the last one is the period (hour, 6 hours or UTC day) containing `now`. */
export function bucketRuns(runs: (Pick<Run, "scheduled_at" | "status"> & { skip_reason?: string | null })[], now = Date.now(), range: RangeKey = "24h"): Bucket[] {
  const { count, size } = RANGES[range];
  const end = Math.floor(now / size) * size + size;
  const buckets = Array.from({ length: count }, (_, i): Bucket => ({ start: end - (count - i) * size, succeeded: 0, failed: 0, skipped: 0, other: 0, skipDetail: {} }));
  for (const run of runs) {
    const t = new Date(run.scheduled_at).getTime();
    const bucket = buckets[Math.floor((t - buckets[0]!.start) / size)];
    if (!bucket) continue;
    if (run.status === "succeeded") bucket.succeeded += 1;
    else if (run.status === "failed") bucket.failed += 1;
    else if (run.status === "skipped") {
      bucket.skipped += 1;
      const label = run.skip_reason ? (SKIP_LABELS[run.skip_reason] ?? run.skip_reason) : "no reason recorded";
      bucket.skipDetail[label] = (bucket.skipDetail[label] ?? 0) + 1;
    } else bucket.other += 1;
  }
  return buckets;
}

function tickLabel(start: number, range: RangeKey): string {
  const d = new Date(start);
  if (range === "24h") return d.toLocaleTimeString("en", { hour: "numeric" });
  // 6-hour and daily buckets are UTC-aligned, so label them in UTC too.
  if (range === "7d") return d.toLocaleDateString("en", { weekday: "short", timeZone: "UTC" });
  return d.toLocaleDateString("en", { month: "short", day: "numeric", timeZone: "UTC" });
}

/**
 * Stacked bars, failures on top so they stand out; a table mirrors the data for
 * assistive tech. Buckets older than `loadedFrom` weren't fetched, so they are
 * shaded as "not loaded" rather than drawn as zero.
 */
export function RunsChart({ buckets, range = "24h", loadedFrom }: { buckets: Bucket[]; range?: RangeKey; loadedFrom?: number }) {
  const { span, unit, tick, size } = RANGES[range];
  const [hover, setHover] = useState<number | null>(null);
  // Drawn at the container's real pixel size so the chart fills whatever height the card has.
  const box = useRef<HTMLDivElement>(null);
  const [dims, setDims] = useState({ w: 720, h: 200 });
  useLayoutEffect(() => {
    const el = box.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const measure = () => {
      const { width: w, height: h } = el.getBoundingClientRect();
      if (w > 0 && h > 0) setDims((prev) => (prev.w === Math.round(w) && prev.h === Math.round(h) ? prev : { w: Math.round(w), h: Math.round(h) }));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const width = dims.w;
  const height = dims.h;
  const pad = { top: 8, bottom: 22, left: 28, right: 4 };
  const max = Math.max(1, ...buckets.map((b) => b.succeeded + b.failed + b.skipped + b.other));
  const slot = (width - pad.left - pad.right) / buckets.length;
  const barWidth = Math.max(2, slot - 3);
  const y = (n: number) => (n / max) * (height - pad.top - pad.bottom);
  const total = buckets.reduce((s, b) => s + b.succeeded + b.failed + b.skipped + b.other, 0);
  const failed = buckets.reduce((s, b) => s + b.failed, 0);
  return (
    <figure className="relative min-w-0">
      <div ref={box} className="relative h-56 sm:h-64">
      <svg viewBox={`0 0 ${width} ${height}`} width={width} height={height} className="absolute inset-0 size-full" role="img" aria-label={`Runs per ${unit} over the last ${span}: ${total} runs, ${failed} failed`}>
        {[0.25, 0.5, 0.75, 1].map((fraction) => (
          <line key={fraction} x1={pad.left} x2={width - pad.right} y1={height - pad.bottom - y(max * fraction)} y2={height - pad.bottom - y(max * fraction)} className="stroke-border/60" strokeDasharray="3 4" />
        ))}
        <line x1={pad.left} x2={width - pad.right} y1={height - pad.bottom} y2={height - pad.bottom} className="stroke-ink" />
        <text x={pad.left - 4} y={pad.top + 8} textAnchor="end" className="fill-ink-soft text-[10px]">
          {max}
        </text>
        <text x={pad.left - 4} y={height - pad.bottom} textAnchor="end" className="fill-ink-soft text-[10px]">
          0
        </text>
        {buckets.map((b, i) => {
          const x = pad.left + i * slot + (slot - barWidth) / 2;
          const base = height - pad.bottom;
          const hs = y(b.succeeded);
          const hk = y(b.skipped);
          const ho = y(b.other);
          const hf = y(b.failed);
          const unloaded = loadedFrom !== undefined && b.start + size <= loadedFrom;
          return (
            <g key={b.start}>
              {unloaded ? <rect x={pad.left + i * slot} y={pad.top} width={slot} height={base - pad.top} className="fill-sunken" /> : null}
              <rect x={x} y={base - hs} width={barWidth} height={hs} className="fill-chart-success" />
              <rect x={x} y={base - hs - hk} width={barWidth} height={hk} className="fill-chart-skipped" />
              <rect x={x} y={base - hs - hk - ho} width={barWidth} height={ho} className="fill-chart-other" />
              <rect x={x} y={base - hs - hk - ho - hf} width={barWidth} height={hf} className="fill-chart-failure" />
              <rect x={pad.left + i * slot} y={0} width={slot} height={height - pad.bottom} fill="transparent" onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)} />
              {i % tick === 0 ? (
                <text x={x + barWidth / 2} y={height - 6} textAnchor="middle" className="fill-ink-soft text-[10px]">
                  {tickLabel(b.start, range)}
                </text>
              ) : null}
            </g>
          );
        })}
      </svg>
      </div>
      {hover !== null && buckets[hover] ? <ChartTooltip bucket={buckets[hover]} left={pad.left + (hover + 0.5) * slot} flip={hover >= buckets.length / 2} /> : null}
      <figcaption className="mt-2 flex flex-wrap gap-4 text-xs text-muted-foreground">
        <span className="flex items-center gap-1.5"><span className="inline-block size-2.5 bg-chart-success" aria-hidden="true" />Succeeded</span>
        <span className="flex items-center gap-1.5"><span className="inline-block size-2.5 bg-chart-failure" aria-hidden="true" />Failed</span>
        <span className="flex items-center gap-1.5"><span className="inline-block size-2.5 bg-chart-skipped" aria-hidden="true" />Skipped</span>
        <span className="flex items-center gap-1.5"><span className="inline-block size-2.5 bg-chart-other" aria-hidden="true" />Other</span>
        {loadedFrom !== undefined ? <span className="flex items-center gap-1.5"><span className="inline-block size-2.5 border border-line bg-sunken" aria-hidden="true" />Older runs not loaded — totals above still count them</span> : null}
      </figcaption>
      <div className="sr-only"><table>
        <caption>{`Runs per ${unit}, last ${span}`}</caption>
        <thead>
          <tr><th scope="col">Period starting</th><th scope="col">Succeeded</th><th scope="col">Failed</th><th scope="col">Skipped</th><th scope="col">Other</th></tr>
        </thead>
        <tbody>
          {buckets.map((b) => (
            <tr key={b.start}>
              <th scope="row">{formatDateTime(new Date(b.start).toISOString())}</th>
              <td>{b.succeeded}</td><td>{b.failed}</td><td>{b.skipped}</td><td>{b.other}</td>
            </tr>
          ))}
        </tbody>
      </table></div>
    </figure>
  );
}

function ChartTooltip({ bucket: b, left, flip }: { bucket: Bucket; left: number; flip: boolean }) {
  const rows: [string, number][] = [
    ["succeeded", b.succeeded],
    ["failed", b.failed],
    ["skipped", b.skipped],
    ["other", b.other],
  ];
  return (
    <div
      aria-hidden="true"
      className="pointer-events-none absolute top-0 z-10 grid min-w-44 gap-0.5 rounded border border-line bg-surface px-3 py-2 text-xs shadow-md"
      style={{ left: `${left}px`, transform: `translateX(${flip ? "calc(-100% - 12px)" : "12px"})` }}
    >
      <p className="font-bold">{formatDateTime(new Date(b.start).toISOString())}</p>
      {rows.filter(([, n]) => n > 0).map(([label, n]) => (
        <p key={label} className="flex justify-between gap-4"><span className="text-ink-soft">{label}</span><span>{n}</span></p>
      ))}
      {Object.entries(b.skipDetail).map(([label, n]) => (
        <p key={label} className="flex justify-between gap-4 pl-3 text-ink-soft"><span>{label}</span><span>{n}</span></p>
      ))}
      {b.succeeded + b.failed + b.skipped + b.other === 0 ? <p className="text-ink-soft">no runs</p> : null}
    </div>
  );
}
