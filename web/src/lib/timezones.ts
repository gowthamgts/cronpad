import type { ComboboxOption } from "@/components/ui/combobox";

const aliases: Record<string, string> = {
  "Asia/Calcutta": "Asia/Kolkata", "Europe/Kiev": "Europe/Kyiv",
  "Asia/Saigon": "Asia/Ho_Chi_Minh", "Asia/Katmandu": "Asia/Kathmandu",
  "Asia/Rangoon": "Asia/Yangon", "Etc/UTC": "UTC", "Etc/GMT": "UTC", GMT: "UTC",
  "America/Godthab": "America/Nuuk", "Pacific/Truk": "Pacific/Chuuk",
  "Pacific/Ponape": "Pacific/Pohnpei", "Pacific/Enderbury": "Pacific/Kanton",
  "US/Eastern": "America/New_York", "US/Central": "America/Chicago",
  "US/Mountain": "America/Denver", "US/Pacific": "America/Los_Angeles",
};
export function normalizeZone(zone: string): string { return aliases[zone] ?? zone; }
export function browserZone(): string {
  try { return normalizeZone(Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"); }
  catch { return "UTC"; }
}
const offsets = new Map<string, string>();
export function zoneOffset(zone: string): string {
  const canonical = normalizeZone(zone);
  const cached = offsets.get(canonical);
  if (cached) return cached;
  try {
    const part = new Intl.DateTimeFormat("en", { timeZone: canonical, timeZoneName: "longOffset" }).formatToParts(new Date()).find((p) => p.type === "timeZoneName")?.value ?? "GMT";
    const hint = part === "GMT" ? "UTC+00:00" : part.replace("GMT", "UTC");
    offsets.set(canonical, hint);
    return hint;
  } catch { return ""; }
}
export function zoneOptions(): ComboboxOption[] {
  let supported: string[];
  try { supported = Intl.supportedValuesOf("timeZone"); } catch { supported = []; }
  const zones = [...new Set(["UTC", ...supported.map(normalizeZone)])].sort((a, b) => a === "UTC" ? -1 : b === "UTC" ? 1 : a.localeCompare(b));
  return zones.map((value) => ({ value, group: value === "UTC" ? "UTC" : value.split("/")[0], hint: zoneOffset(value), keywords: Object.entries(aliases).filter(([, current]) => current === value).map(([legacy]) => legacy).join(" ") }));
}
