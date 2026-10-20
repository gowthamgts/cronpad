import { formatDateTime, relativeTime } from "@/lib/format";
import { normalizeZone } from "@/lib/timezones";
import { cn } from "@/lib/utils";

/**
 * A time in the job's zone with UTC on a quieter second line. The UTC line is
 * left out when the job already runs in UTC, so nothing is said twice.
 */
export function DualTime({ iso, timeZone, relative = false, seconds = false, className }: { iso: string; timeZone?: string; relative?: boolean; seconds?: boolean; className?: string }) {
  const zone = timeZone ?? "UTC";
  const isUtc = normalizeZone(zone) === "UTC" || zone === "Etc/UTC";
  const secondary = [isUtc ? null : formatDateTime(iso, "UTC", { seconds }), relative ? relativeTime(iso) : null].filter(Boolean).join(" · ");
  return (
    <time dateTime={iso} className={cn("tabular-nums", className)}>
      {formatDateTime(iso, zone, { seconds })}
      {secondary ? <span className="block text-xs text-ink-soft">{secondary}</span> : null}
    </time>
  );
}
