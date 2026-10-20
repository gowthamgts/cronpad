import { normalizeZone } from "./timezones";

const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
  ["second", 1],
];

/** "in 5 minutes", "2 hours ago". */
export function relativeTime(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "—";
  const diff = Math.round((new Date(iso).getTime() - now) / 1000);
  for (const [unit, seconds] of UNITS) {
    if (Math.abs(diff) >= seconds || unit === "second") {
      return rtf.format(Math.round(diff / seconds), unit);
    }
  }
  return "—";
}

/**
 * "Oct 18, 2:55 PM GMT+5:30". The year appears only when it isn't the current
 * one; pass `seconds` where exact instants matter (run milestones, attempts).
 */
export function formatDateTime(iso: string | null | undefined, timeZone?: string, { seconds = false, now = Date.now() }: { seconds?: boolean; now?: number } = {}): string {
  if (!iso) return "—";
  try {
    const date = new Date(iso);
    // dateStyle/timeStyle cannot be combined with timeZoneName, so spell the fields out.
    return new Intl.DateTimeFormat("en", {
      year: date.getUTCFullYear() === new Date(now).getUTCFullYear() ? undefined : "numeric",
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
      second: seconds ? "2-digit" : undefined,
      timeZone,
      timeZoneName: "short",
    }).format(date);
  } catch {
    return iso;
  }
}

export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return "—";
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`;
  const s = Math.round(ms / 1000);
  return `${Math.floor(s / 60)} m ${s % 60} s`;
}

export function viewerTimeZone(): string {
  try {
    return normalizeZone(Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC");
  } catch {
    return "UTC";
  }
}

export function timeZones(): string[] {
  try {
    const zones = Intl.supportedValuesOf("timeZone");
    return zones.includes("UTC") ? zones : ["UTC", ...zones];
  } catch {
    return ["UTC"];
  }
}

export function percent(part: number, total: number): string {
  return total === 0 ? "—" : `${Math.round((part / total) * 100)}%`;
}
