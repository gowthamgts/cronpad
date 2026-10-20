/**
 * Converts a schedule between syntaxes without a server round trip.
 *
 * Every expression is parsed into a small plan (every N minutes, every N hours,
 * or a time of day on some days). A plan is rendered into any syntax that can
 * say the same thing. Anything outside that vocabulary is not converted: the
 * caller keeps the original text and shows the returned warning.
 */

export type ScheduleMode = "auto" | "natural" | "cron" | "quartz" | "rrule";
type Kind = Exclude<ScheduleMode, "auto">;

/** What a schedule editor holds: the text and the syntax it is read as. */
export interface ScheduleValue {
  expr: string;
  mode: ScheduleMode;
}

export const MODES: { value: ScheduleMode; label: string; placeholder: string }[] = [
  { value: "auto", label: "Auto", placeholder: "every weekday at 9am, */5 * * * *, or FREQ=DAILY" },
  { value: "natural", label: "Natural", placeholder: "every weekday at 9am" },
  { value: "cron", label: "Cron", placeholder: "*/5 * * * *" },
  { value: "quartz", label: "Quartz", placeholder: "0 0 9 ? * MON-FRI" },
  { value: "rrule", label: "RRULE", placeholder: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9" },
];

export const modeLabel = (mode: ScheduleMode) => MODES.find((m) => m.value === mode)?.label ?? mode;

/** Day of week, 0 = Sunday. Day of month, 1-31. */
type Days = { weekdays?: number[]; monthDays?: number[] };
type Plan =
  | { type: "minutes"; every: number }
  | { type: "hours"; every: number; minute: number }
  | { type: "time"; hour: number; minute: number; days: Days };

export interface Conversion {
  expr: string;
  /** Set when the expression could not be carried over. `expr` is then unchanged (or, for presets, approximated). */
  warning?: string;
  /** Set when `expr` is a faithful translation whose run times can still differ from the original's. */
  caveat?: string;
}

class Unsupported extends Error {}
const no = (reason: string): never => {
  throw new Unsupported(reason);
};

const DAY_NAMES = ["SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"];
const DAY_WORDS = ["sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"];
const RRULE_DAYS = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"];
const WEEKDAYS = [1, 2, 3, 4, 5];
const WEEKEND = [0, 6];
const same = (a: number[], b: number[]) => a.length === b.length && a.every((v, i) => v === b[i]);
const pad = (n: number) => String(n).padStart(2, "0");

/**
 * Guess which syntax a typed expression is written in. Mirrors the server's detection
 * (internal/schedule/schedule.go) so "auto" converts from the syntax the server will read.
 */
export function detectKind(expr: string): Kind {
  const text = expr.trim();
  if (/FREQ=/i.test(text)) return "rrule";
  const fields = text.split(/\s+/);
  if (fields.length >= 5 && fields.length <= 7 && /^[0-9*,\-/?]+$/.test(fields[0] ?? "")) {
    let quartz = fields.length === 7;
    // Six fields are cron with a leading seconds field unless a day field uses a Quartz-only form.
    if (fields.length === 6) {
      for (const i of [3, 5]) {
        const f = (fields[i] ?? "").toUpperCase();
        if (/[?#]/.test(f) || f === "L" || f === "LW" || f.startsWith("L-") || f.endsWith("W") || f.endsWith("L")) quartz = true;
      }
    }
    return quartz ? "quartz" : "cron";
  }
  return text.startsWith("@") ? "cron" : "natural";
}

// ---------------------------------------------------------------- parsing

function int(text: string, min: number, max: number, what: string): number {
  if (!/^\d+$/.test(text)) return no(`${what} “${text}” isn't a plain number`);
  const n = Number(text);
  return n < min || n > max ? no(`${what} ${n} is out of range`) : n;
}

function weekdayValue(token: string): number {
  const named = DAY_NAMES.indexOf(token.toUpperCase());
  if (named >= 0) return named;
  const n = int(token, 0, 7, "day of week");
  return n === 7 ? 0 : n;
}

function expandList(field: string, value: (t: string) => number): number[] {
  const out = new Set<number>();
  for (const part of field.split(",")) {
    if (part.includes("/") || /#|^L|\d[LW]$/i.test(part)) no(`“${part}” uses a step or special form`);
    const range = part.split("-");
    if (range.length > 2) no(`“${part}” isn't a simple range`);
    const lo = value(range[0] ?? "");
    const hi = range.length === 2 ? value(range[1] ?? "") : lo;
    if (hi < lo) no(`range “${part}” wraps around`);
    for (let v = lo; v <= hi; v++) out.add(v);
  }
  return [...out].sort((a, b) => a - b);
}

/** Cron day-of-week, with names or 0-7 numbers. */
const cronWeekdays = (field: string) => expandList(field, weekdayValue);

function planFromFields(f: { minute: string; hour: string; dom: string; month: string; dow: string }, dowOf: (field: string) => number[]): Plan {
  const any = (s: string) => s === "*" || s === "?";
  if (!any(f.month)) no("it is limited to certain months");
  const step = (s: string) => {
    if (s === "*") return 1;
    // Quartz writes "every 5 from 0" as 0/5, the same as */5.
    const m = /^(?:\*|0)\/(\d+)$/.exec(s);
    return m ? int(m[1] ?? "", 1, 59, "step") : 0;
  };
  const everyMinutes = step(f.minute);
  if (everyMinutes) {
    if (!any(f.hour) || !any(f.dom) || !any(f.dow)) no("it mixes a minute step with other limits");
    if (60 % everyMinutes !== 0) no(`every ${everyMinutes} minutes restarts each hour, which other syntaxes can't say the same way`);
    return { type: "minutes", every: everyMinutes };
  }
  const minute = int(f.minute, 0, 59, "minute");
  const everyHours = step(f.hour);
  if (everyHours) {
    if (!any(f.dom) || !any(f.dow)) no("it mixes an hourly step with day limits");
    if (24 % everyHours !== 0) no(`every ${everyHours} hours restarts each day, which other syntaxes can't say the same way`);
    return { type: "hours", every: everyHours, minute };
  }
  const hour = int(f.hour, 0, 23, "hour");
  const days: Days = {};
  if (!any(f.dom) && !any(f.dow)) no("it limits both day of month and day of week");
  if (!any(f.dow)) days.weekdays = dowOf(f.dow);
  if (!any(f.dom)) days.monthDays = expandList(f.dom, (t) => int(t, 1, 31, "day of month"));
  return { type: "time", hour, minute, days };
}

function parseCron(expr: string): Plan {
  const text = expr.trim();
  const alias = { "@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *" }[text.toLowerCase()];
  let fields = (alias ?? text).split(/\s+/);
  // Cron also accepts a leading seconds field; only second 0 has an equivalent elsewhere.
  if (fields.length === 6) {
    if (fields[0] !== "0") no("it fires on a second other than 0");
    fields = fields.slice(1);
  }
  if (fields.length !== 5) return no("it isn't a five- or six-field cron expression");
  const [minute = "", hour = "", dom = "", month = "", dow = ""] = fields;
  return planFromFields({ minute, hour, dom, month, dow }, cronWeekdays);
}

function parseQuartz(expr: string): Plan {
  const fields = expr.trim().split(/\s+/);
  if (fields.length !== 6 && fields.length !== 7) return no("it isn't a Quartz expression");
  const [second = "", minute = "", hour = "", dom = "", month = "", dow = "", year = "*"] = fields;
  if (second !== "0") no("it fires on a second other than 0");
  if (year !== "*") no("it is limited to certain years");
  // Quartz numbers days 1-7 from Sunday; names mean the same in both syntaxes.
  const quartzDays = (field: string) => expandList(field, (t) => (/^\d+$/.test(t) ? int(t, 1, 7, "day of week") - 1 : weekdayValue(t)));
  return planFromFields({ minute, hour, dom, month, dow }, quartzDays);
}

function parseRRule(expr: string): Plan {
  const parts = new Map<string, string>();
  for (const part of expr.trim().replace(/^RRULE:/i, "").split(";")) {
    const [k, v] = part.split("=");
    if (!k || v === undefined) return no("it isn't a plain RRULE");
    parts.set(k.toUpperCase(), v.toUpperCase());
  }
  const freq = parts.get("FREQ");
  const interval = parts.has("INTERVAL") ? int(parts.get("INTERVAL") ?? "", 1, 1000, "interval") : 1;
  const allowed = new Set(["FREQ", "INTERVAL", "BYHOUR", "BYMINUTE", "BYSECOND", "BYDAY", "BYMONTHDAY"]);
  for (const key of parts.keys()) if (!allowed.has(key)) no(`${key} has no equivalent here`);
  if (parts.has("BYSECOND") && parts.get("BYSECOND") !== "0") no("it fires on a second other than 0");
  const single = (key: string, max: number) => {
    const v = parts.get(key);
    return v === undefined ? undefined : int(v, 0, max, key.toLowerCase());
  };
  if (freq === "MINUTELY") {
    if (["BYHOUR", "BYMINUTE", "BYDAY", "BYMONTHDAY"].some((k) => parts.has(k))) no("it narrows a minutely rule");
    return { type: "minutes", every: interval };
  }
  const minute = single("BYMINUTE", 59);
  if (minute === undefined) return no("it has no fixed minute, so it depends on when the schedule is created");
  if (freq === "HOURLY") {
    if (["BYHOUR", "BYDAY", "BYMONTHDAY"].some((k) => parts.has(k))) no("it narrows an hourly rule");
    return { type: "hours", every: interval, minute };
  }
  const hour = single("BYHOUR", 23);
  if (hour === undefined) return no("it has no fixed hour");
  if (interval !== 1) no("it repeats every few days, weeks or months");
  const days: Days = {};
  if (freq === "DAILY" && !parts.has("BYDAY") && !parts.has("BYMONTHDAY")) return { type: "time", hour, minute, days };
  if (freq === "WEEKLY" && parts.has("BYDAY") && !parts.has("BYMONTHDAY")) {
    days.weekdays = expandList(parts.get("BYDAY") ?? "", (t) => {
      const i = RRULE_DAYS.indexOf(t);
      return i >= 0 ? i : no(`day “${t}” isn't a plain weekday`);
    });
    return { type: "time", hour, minute, days };
  }
  if (freq === "MONTHLY" && parts.has("BYMONTHDAY") && !parts.has("BYDAY")) {
    days.monthDays = expandList(parts.get("BYMONTHDAY") ?? "", (t) => int(t, 1, 31, "day of month"));
    return { type: "time", hour, minute, days };
  }
  return no("this combination of rule parts has no equivalent");
}

function parseTime(text: string): { hour: number; minute: number } {
  if (text === "noon") return { hour: 12, minute: 0 };
  if (text === "midnight") return { hour: 0, minute: 0 };
  const m = /^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$/.exec(text);
  if (!m) return no(`“${text}” isn't a plain time`);
  // A bare number could be an hour or a count; require a colon or am/pm.
  if (!m[2] && !m[3]) return no(`“${text}” needs a colon or am/pm to read as a time`);
  let hour = Number(m[1]);
  const minute = m[2] ? Number(m[2]) : 0;
  if (m[3]) {
    if (hour < 1 || hour > 12) no("the hour is out of range");
    hour = (hour % 12) + (m[3] === "pm" ? 12 : 0);
  }
  if (hour > 23 || minute > 59) no("the time is out of range");
  return { hour, minute };
}

function parseNatural(expr: string): Plan {
  const text = expr.trim().toLowerCase().replace(/\s+/g, " ");
  let m = /^every (?:(\d+) )?minutes?$/.exec(text);
  if (m) return { type: "minutes", every: m[1] ? int(m[1], 1, 1000, "interval") : 1 };
  m = /^(?:every (?:(\d+) )?hours?|hourly)$/.exec(text);
  if (m) return { type: "hours", every: m[1] ? int(m[1], 1, 1000, "interval") : 1, minute: 0 };
  m = /^(?:every day|daily|every weekdays?|every weekends?|every ([a-z, ]+?)) at (.+)$/.exec(text);
  if (m) {
    const { hour, minute } = parseTime(m[2] ?? "");
    const days: Days = {};
    if (/^every weekday/.test(text)) days.weekdays = WEEKDAYS;
    else if (/^every weekend/.test(text)) days.weekdays = WEEKEND;
    else if (m[1] !== undefined) {
      const words = m[1].split(/,| and /).map((w) => w.trim()).filter(Boolean);
      days.weekdays = [...new Set(words.map((w) => (DAY_WORDS.includes(w) ? DAY_WORDS.indexOf(w) : no(`“${w}” isn't a weekday name`))))].sort((a, b) => a - b);
      if (days.weekdays.length === 0) no("no weekdays given");
    }
    return { type: "time", hour, minute, days };
  }
  m = /^on the ((?:\d+(?:st|nd|rd|th)(?:,? and |, | and )?)+) of every month at (.+)$/.exec(text);
  if (m) {
    const { hour, minute } = parseTime(m[2] ?? "");
    const monthDays = [...(m[1] ?? "").matchAll(/(\d+)(?:st|nd|rd|th)/g)].map((d) => int(d[1] ?? "", 1, 31, "day of month"));
    return { type: "time", hour, minute, days: { monthDays: [...new Set(monthDays)].sort((a, b) => a - b) } };
  }
  return no("it uses wording this converter doesn't cover");
}

function parse(expr: string, kind: Kind): Plan {
  return { natural: parseNatural, cron: parseCron, quartz: parseQuartz, rrule: parseRRule }[kind](expr);
}

// -------------------------------------------------------------- rendering

const ordinal = (n: number) => `${n}${n % 100 >= 11 && n % 100 <= 13 ? "th" : (["th", "st", "nd", "rd"][n % 10] ?? "th")}`;

function quartzDays(days: number[]): string {
  if (same(days, WEEKDAYS)) return "MON-FRI";
  if (same(days, WEEKEND)) return "SAT,SUN";
  return days.map((d) => DAY_NAMES[d]).join(",");
}

function render(plan: Plan, kind: Kind): string {
  /** Minute and hour steps, with the field syntax cron and Quartz share. */
  const stepped = (second: string, tail: string) => {
    if (plan.type === "minutes") {
      if (60 % plan.every !== 0) no(`every ${plan.every} minutes restarts each hour, so ${modeLabel(kind)} can't say it`);
      return `${second}${plan.every === 1 ? "*" : `*/${plan.every}`} * * *${tail}`;
    }
    if (plan.type === "hours") {
      if (24 % plan.every !== 0) no(`every ${plan.every} hours restarts each day, so ${modeLabel(kind)} can't say it`);
      return `${second}${plan.minute} ${plan.every === 1 ? "*" : `*/${plan.every}`} * *${tail}`;
    }
    return "";
  };
  switch (kind) {
    case "cron": {
      if (plan.type !== "time") return stepped("", " *");
      const { weekdays, monthDays } = plan.days;
      return `${plan.minute} ${plan.hour} ${monthDays?.join(",") ?? "*"} * ${weekdays ? (same(weekdays, WEEKDAYS) ? "1-5" : weekdays.join(",")) : "*"}`;
    }
    case "quartz": {
      if (plan.type !== "time") return stepped("0 ", " ?");
      const { weekdays, monthDays } = plan.days;
      const dom = monthDays?.join(",") ?? (weekdays ? "?" : "*");
      const dow = weekdays ? quartzDays(weekdays) : "?";
      return `0 ${plan.minute} ${plan.hour} ${dom} * ${dow}`;
    }
    case "rrule": {
      if (plan.type === "minutes") return `FREQ=MINUTELY;INTERVAL=${plan.every}`;
      if (plan.type === "hours") return `FREQ=HOURLY;INTERVAL=${plan.every};BYMINUTE=${plan.minute}`;
      const at = `BYHOUR=${plan.hour};BYMINUTE=${plan.minute}`;
      const { weekdays, monthDays } = plan.days;
      if (weekdays) return `FREQ=WEEKLY;BYDAY=${weekdays.map((d) => RRULE_DAYS[d]).join(",")};${at}`;
      if (monthDays) return `FREQ=MONTHLY;BYMONTHDAY=${monthDays.join(",")};${at}`;
      return `FREQ=DAILY;${at}`;
    }
    case "natural": {
      if (plan.type === "minutes") return plan.every === 1 ? "every minute" : `every ${plan.every} minutes`;
      if (plan.type === "hours") {
        if (plan.minute !== 0) no("Natural has no way to pick the minute past the hour");
        return plan.every === 1 ? "every hour" : `every ${plan.every} hours`;
      }
      const at = ` at ${pad(plan.hour)}:${pad(plan.minute)}`;
      const { weekdays, monthDays } = plan.days;
      if (monthDays) return `on the ${monthDays.map(ordinal).join(" and ")} of every month${at}`;
      if (!weekdays) return `every day${at}`;
      if (same(weekdays, WEEKDAYS)) return `every weekday${at}`;
      if (same(weekdays, WEEKEND)) return `every weekend${at}`;
      return `every ${weekdays.map((d) => DAY_WORDS[d]).join(" and ")}${at}`;
    }
  }
}

// ----------------------------------------------------------------- public

/**
 * Re-express `expr` (written in `from`) in `to`. Blank input, an unchanged
 * syntax, or a target of "auto" (which accepts anything) are returned as is.
 */
export function convertExpression(expr: string, from: ScheduleMode, to: ScheduleMode): Conversion {
  if (!expr.trim() || from === to || to === "auto") return { expr };
  const source = from === "auto" ? detectKind(expr) : from;
  if (source === to) return { expr };
  try {
    const plan = parse(expr, source);
    const converted = render(plan, to);
    // Calendar schedules (a time of day) mean the same everywhere. Steps do not: cron and Quartz follow
    // the clock in the job's time zone, while natural and RRULE intervals count from creation.
    const clockBased = (k: Kind) => k === "cron" || k === "quartz";
    return plan.type !== "time" && clockBased(source) !== clockBased(to) ? { expr: converted, caveat: INTERVAL_CAVEAT } : { expr: converted };
  } catch (error) {
    if (!(error instanceof Unsupported)) throw error;
    return { expr, warning: `Couldn't convert this to ${modeLabel(to)}: ${error.message}. Your ${source === from ? modeLabel(from) : `detected ${modeLabel(source)}`} expression was kept as typed.` };
  }
}

const INTERVAL_CAVEAT = "Cron and Quartz steps follow the clock in the job's time zone; Natural and RRULE intervals count from when the job is created. The run times can shift.";

export const PRESETS = [
  { label: "every 5 min", plan: { type: "minutes", every: 5 } },
  { label: "hourly", plan: { type: "hours", every: 1, minute: 0 }, spread: true },
  { label: "weekdays 9am", plan: { type: "time", hour: 9, minute: 0, days: { weekdays: WEEKDAYS } } },
  { label: "daily", plan: { type: "time", hour: 9, minute: 0, days: {} }, spread: true },
  { label: "1st of month", plan: { type: "time", hour: 9, minute: 0, days: { monthDays: [1] } } },
] as const satisfies readonly { label: string; plan: Plan; spread?: boolean }[];

export type SchedulePreset = (typeof PRESETS)[number];

/**
 * The expression a preset button writes in the selected syntax. Hourly and daily
 * spread their minute at random to avoid the top-of-the-hour rush; where the
 * syntax can't say a minute (Natural), the preset runs on the hour and warns.
 * Auto picks cron for the spread presets and natural for the rest.
 */
export function presetExpression(preset: SchedulePreset, mode: ScheduleMode, random: () => number = Math.random): Conversion {
  const sample = Math.floor(random() * 60);
  const minute = Number.isFinite(sample) ? Math.min(59, Math.max(0, sample)) : 0;
  const spread: Plan = "spread" in preset ? { ...preset.plan, minute } : preset.plan;
  const kind: Kind = mode === "auto" ? ("spread" in preset ? "cron" : "natural") : mode;
  try {
    return { expr: render(spread, kind) };
  } catch (error) {
    if (!(error instanceof Unsupported)) throw error;
    const fallback = render(preset.plan, kind);
    return { expr: fallback, warning: `${error.message}, so “${preset.label}” runs at the top of the hour. Switch to Cron or RRULE to spread it out.` };
  }
}
