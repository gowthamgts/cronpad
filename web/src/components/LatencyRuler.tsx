import { formatDuration } from "@/lib/format";
export function LatencyRuler({ values, measured = true, span = "24 hours" }: { values: { p50?: number | null; p95?: number | null; p99?: number | null }; measured?: boolean; span?: string }) {
  if (!measured) return <p className="border border-dashed border-line px-3 py-6 text-center text-sm text-ink-soft">No runs measured in the last {span} yet.</p>;
  const ticks = (["p50", "p95", "p99"] as const).map((key) => ({ key, value: values[key] }));
  const max = Math.max(0, ...ticks.map((t) => t.value ?? 0));
  const scale = Math.max(1, Math.ceil(max * 1.25));
  return <div className="grid gap-3">
    <div role="img" aria-label={`Start lag over the last ${span}: ${ticks.map((t) => `${t.key} ${formatDuration(t.value)}`).join(", ")}. Scale 0 to ${scale} ms.`} className="pt-10">
      <div className="relative h-6 border-b-2 border-ink bg-sunken">
        <div className="absolute bottom-0 h-2 bg-accent" style={{ width: `${max / scale * 100}%` }} />
        {ticks.map((t, i) => t.value != null ? <span key={t.key} className="absolute bottom-0 border-l border-ink" style={{left:`${t.value / scale * 100}%`, height: `${18 + i * 14}px`}}><span className="absolute -top-4 whitespace-nowrap pl-0.5 text-[10px]">{t.key}</span></span> : null)}
      </div>
      <div className="mt-1 flex justify-between text-xs tabular-nums text-ink-soft"><span>0 ms</span><span>{scale} ms</span></div>
    </div>
    <dl className="grid grid-cols-3 gap-3 text-xs">
      <div><dt className="text-ink-soft">median (p50)</dt><dd className="text-base font-bold leading-tight tabular-nums">{formatDuration(values.p50)}</dd></div>
      <div><dt className="text-ink-soft">p95</dt><dd className="text-base font-bold leading-tight tabular-nums">{formatDuration(values.p95)}</dd></div>
      <div><dt className="text-ink-soft">p99</dt><dd className="text-base font-bold leading-tight tabular-nums">{formatDuration(values.p99)}</dd></div>
    </dl>
  </div>;
}

/** How start lag is measured, for the info tip next to the ruler. */
export function LatencyMethod({ span = "24 hours" }: { span?: string }) {
  return (
    <>
      <span className="mb-1 block font-bold">how this is measured</span>
      Start lag is the time from a run’s scheduled time to when its request was sent. p50, p95 and p99 cover the last {span}; runs without a recorded start are excluded. p50 is the median: half of runs start sooner. p95: 95 in 100 start sooner. p99: only 1 in 100 is slower. No target line is drawn.
    </>
  );
}
