import { convertExpression as convertRaw, detectKind, MODES, modeLabel, PRESETS, presetExpression, type ScheduleMode } from "@cronpad/schedule";
import { CRON_TO_NATURAL, MEANINGS, NATURAL_TO_CRON, QUARTZ_TO_CRON, RRULE_TO_CRON, SYNTAXES, type Syntax } from "@cronpad/schedule/fixtures";

const ok = (expr: string) => ({ expr });
/** The conversion without its caveat, which has its own tests below. */
const convert: typeof convertRaw = (expr, from, to) => {
  const result = { ...convertRaw(expr, from, to) };
  delete result.caveat;
  return result;
};
const refused = (expr: string, from: ScheduleMode, to: ScheduleMode) => {
  const result = convert(expr, from, to);
  expect(result.expr, `${expr} must be kept as typed`).toBe(expr);
  expect(result.warning, `${expr} → ${to} must warn`).toMatch(/Couldn't convert this to/);
  return result.warning as string;
};

describe("conversion matrix", () => {
  // Every meaning × every ordered pair of syntaxes.
  const pairs = MEANINGS.flatMap((m) => SYNTAXES.flatMap((from) => SYNTAXES.filter((to) => to !== from).map((to) => ({ m, from, to }))));
  it.each(pairs.filter(({ m, from }) => m[from] !== null))("$m.name: $from → $to", ({ m, from, to }) => {
    const source = m[from] as string;
    if (m[to] === null) refused(source, from, to);
    else expect(convert(source, from, to)).toEqual(ok(m[to] as string));
  });

  it.each(MEANINGS.filter((m) => SYNTAXES.every((s) => m[s] !== null)))("$name survives a full natural → cron → quartz → rrule → natural loop", (m) => {
    let expr = m.natural as string;
    let mode: Syntax = "natural";
    for (const next of ["cron", "quartz", "rrule", "natural"] as const) {
      expr = convert(expr, mode, next).expr;
      mode = next;
      expect(expr).toBe(m[next]);
    }
  });

  it.each(MEANINGS.filter((m) => m.cron !== null && m.rrule !== null))("$name converts the same from auto as from the explicit syntax", (m) => {
    for (const from of SYNTAXES) {
      const source = m[from];
      if (source === null) continue;
      for (const to of SYNTAXES) {
        if (to === from) continue;
        const auto = convert(source, "auto", to);
        const explicit = convert(source, from, to);
        expect(auto.expr, `${source} auto → ${to}`).toBe(explicit.expr);
        expect(Boolean(auto.warning), `${source} auto → ${to} warning`).toBe(Boolean(explicit.warning));
      }
    }
  });
});

describe("detectKind mirrors the server", () => {
  it.each([
    ["*/5 * * * *", "cron"],
    ["0 9 * * 1-5", "cron"],
    ["  0   9 * * *  ", "cron"],
    ["0 0 9 * * *", "cron"], // six fields are cron with seconds
    ["0 0 9 * * 1-5", "cron"],
    ["@daily", "cron"],
    ["@every 5m", "cron"],
    ["0 0 9 ? * MON-FRI", "quartz"],
    ["0 0 9 * * ?", "quartz"],
    ["0 0 9 L * ?", "quartz"],
    ["0 0 9 LW * ?", "quartz"],
    ["0 0 9 L-3 * ?", "quartz"],
    ["0 0 9 15W * *", "quartz"],
    ["0 0 9 * * 6L", "quartz"],
    ["0 0 9 ? * 2#1", "quartz"],
    ["0 0 9 * * ? 2027", "quartz"], // seven fields
    ["FREQ=DAILY", "rrule"],
    ["freq=daily;byhour=9", "rrule"],
    ["RRULE:FREQ=WEEKLY;BYDAY=MO", "rrule"],
    ["every 5 minutes", "natural"],
    ["every 2 weeks on friday", "natural"], // five words, but the first isn't numeric
    ["9am", "natural"],
    ["MON * * * *", "natural"],
    ["", "natural"],
  ] as const)("%j is %s", (expr, kind) => expect(detectKind(expr)).toBe(kind));
});

describe("convertExpression guards", () => {
  it.each(["", "   ", "\n\t"])("leaves blank input %j alone, with no warning", (blank) => {
    for (const to of ["natural", "cron", "quartz", "rrule"] as const) expect(convert(blank, "auto", to)).toEqual(ok(blank));
  });

  it("returns the expression untouched when the syntax does not change", () => {
    expect(convert("  0 9 * * *  ", "cron", "cron")).toEqual(ok("  0 9 * * *  "));
    expect(convert("garbage in", "natural", "natural")).toEqual(ok("garbage in"));
  });

  it("never converts into auto, which accepts any syntax", () => {
    for (const from of ["natural", "cron", "quartz", "rrule"] as const) expect(convert("0 9 * * *", from, "auto")).toEqual(ok("0 9 * * *"));
    expect(convert("garbage", "auto", "auto")).toEqual(ok("garbage"));
  });

  it("does nothing when auto detects the target syntax already", () => {
    expect(convert("0 9 * * *", "auto", "cron")).toEqual(ok("0 9 * * *"));
    expect(convert("every day at 9am", "auto", "natural")).toEqual(ok("every day at 9am"));
    expect(convert("FREQ=DAILY;BYHOUR=9;BYMINUTE=0", "auto", "rrule")).toEqual(ok("FREQ=DAILY;BYHOUR=9;BYMINUTE=0"));
  });

  it("does not trim or normalise text it cannot convert", () => {
    expect(convert("  every   odd   thing  ", "natural", "cron").expr).toBe("  every   odd   thing  ");
  });

  it("names the target, and the detected source when converting from auto", () => {
    expect(refused("17 * * * *", "cron", "natural")).toMatch(/to Natural: Natural has no way to pick the minute/);
    expect(refused("17 * * * *", "cron", "natural")).toMatch(/Your Cron expression was kept as typed/);
    expect(refused("17 * * * *", "auto", "natural")).toMatch(/detected Cron expression/);
  });

  it("labels every mode", () => {
    expect(MODES.map((m) => m.value)).toEqual(["auto", "natural", "cron", "quartz", "rrule"]);
    expect(MODES.map((m) => modeLabel(m.value))).toEqual(["Auto", "Natural", "Cron", "Quartz", "RRULE"]);
    for (const m of MODES) expect(m.placeholder).not.toBe("");
  });
});

describe("cron input", () => {
  it.each(CRON_TO_NATURAL)("%s → natural", (expr, expected) => expect(convert(expr, "cron", "natural")).toEqual(ok(expected)));

  it.each([
    "60 * * * *",
    "-1 * * * *",
    "0 24 * * *",
    "0 9 32 * *",
    "0 9 0 * *",
    "0 9 * * 8",
    "0 9 * 6 *", // month-limited
    "0 9 * JAN *",
    "0 9 15 * 1", // day of month and day of week together
    "0 9,17 * * *", // lists in minute/hour
    "0,30 9 * * *",
    "0-30 9 * * *",
    "0 9-17 * * *",
    "0 9-17/2 * * *",
    "10/5 * * * *",
    "*/7 * * * *", // restarts every hour
    "*/45 * * * *",
    "*/0 * * * *",
    "*/60 * * * *",
    "0 */5 * * *", // restarts every day
    "0 */7 * * *",
    "*/5 9 * * *", // minute step with an hour limit
    "*/5 * * * 1",
    "0 */2 * * 1",
    "0 9 * * 5-1", // wraps
    "0 9 * * 6-7", // 7 is Sunday, so this wraps too
    "0 9 * * */2",
    "0 9 */2 * *",
    "0 9 L * *",
    "0 9 * * MON#2",
    "30 0 9 * * *", // second other than 0
    "@yearly",
    "@annually",
    "@every 5m",
    "@reboot",
    "* * * *",
    "* * * * * * *",
    "every minute",
    "",
  ])("%j cannot become natural, cron-style fields stay as typed", (expr) => {
    if (expr === "") return expect(convert(expr, "cron", "natural")).toEqual(ok(""));
    const result = convert(expr, "cron", "rrule");
    expect(result.expr).toBe(expr);
    expect(result.warning).toBeDefined();
  });

  it("sends day-of-week ranges and lists into quartz as names", () => {
    expect(convert("0 9 * * 1-5", "cron", "quartz").expr).toBe("0 0 9 ? * MON-FRI");
    expect(convert("0 9 * * 0,6", "cron", "quartz").expr).toBe("0 0 9 ? * SAT,SUN");
    expect(convert("0 9 * * 1,3", "cron", "quartz").expr).toBe("0 0 9 ? * MON,WED");
    expect(convert("0 9 * * 0", "cron", "quartz").expr).toBe("0 0 9 ? * SUN");
    expect(convert("0 9 * * 6,0", "cron", "quartz").expr).toBe("0 0 9 ? * SAT,SUN");
  });

  it("puts ? in whichever of day-of-month or day-of-week is unused", () => {
    expect(convert("0 9 * * *", "cron", "quartz").expr).toBe("0 0 9 * * ?");
    expect(convert("0 9 1 * *", "cron", "quartz").expr).toBe("0 0 9 1 * ?");
    expect(convert("0 9 * * 1", "cron", "quartz").expr).toBe("0 0 9 ? * MON");
    expect(convert("*/5 * * * *", "cron", "quartz").expr).toBe("0 */5 * * * ?");
    expect(convert("0 */2 * * *", "cron", "quartz").expr).toBe("0 0 */2 * * ?");
  });
});

describe("quartz input", () => {
  it.each(QUARTZ_TO_CRON)("%s → cron %s", (expr, expected) => expect(convert(expr, "quartz", "cron")).toEqual(ok(expected)));

  it.each([
    "0 0 9 L * ?",
    "0 0 9 L-3 * ?",
    "0 0 9 LW * ?",
    "0 0 9 15W * ?",
    "0 0 9 ? * 6L",
    "0 0 9 ? * 2#1",
    "0 0 9 ? * 0", // 0 is not a Quartz day
    "0 0 9 ? * 8",
    "0 0 9 ? * 5-2",
    "30 0 9 * * ?", // second other than 0
    "0 0 9 * * ? 2027", // limited to a year
    "0 0 9 ? 6 *", // limited to a month
    "0 0 9 15 * 2", // both day fields
    "0 0,30 9 * * ?",
    "0 0 9,17 * * ?",
    "0 5/10 * * * ?", // offset step
    "0 */7 * * * ?",
    "0 0 */5 * * ?",
    "0 0 9 *",
    "0 0 9 * * ? 2027 1",
  ])("%j cannot be converted to cron and is kept", (expr) => {
    const result = convert(expr, "quartz", "cron");
    expect(result.expr).toBe(expr);
    expect(result.warning).toBeDefined();
  });
});

describe("rrule input", () => {
  it.each(RRULE_TO_CRON)("%s → cron %s", (expr, expected) => expect(convert(expr, "rrule", "cron")).toEqual(ok(expected)));

  it.each([
    "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29;BYHOUR=0;BYMINUTE=0",
    "FREQ=DAILY", // fixed hour and minute come from the creation time
    "FREQ=DAILY;BYHOUR=9",
    "FREQ=DAILY;BYMINUTE=0",
    "FREQ=DAILY;BYHOUR=9,17;BYMINUTE=0",
    "FREQ=DAILY;BYHOUR=9;BYMINUTE=0,30",
    "FREQ=DAILY;INTERVAL=2;BYHOUR=9;BYMINUTE=0",
    "FREQ=DAILY;BYHOUR=9;BYMINUTE=0;BYSECOND=30",
    "FREQ=DAILY;BYHOUR=9;BYMINUTE=0;COUNT=10",
    "FREQ=DAILY;BYHOUR=9;BYMINUTE=0;UNTIL=20271231T000000Z",
    "FREQ=DAILY;BYHOUR=24;BYMINUTE=0",
    "FREQ=DAILY;BYHOUR=9;BYMINUTE=60",
    "FREQ=DAILY;INTERVAL=0;BYHOUR=9;BYMINUTE=0",
    "FREQ=DAILY;BYDAY=MO;BYHOUR=9;BYMINUTE=0", // daily narrowed by day
    "FREQ=WEEKLY;BYHOUR=9;BYMINUTE=0", // no BYDAY
    "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO;BYHOUR=9;BYMINUTE=0",
    "FREQ=WEEKLY;BYDAY=1MO;BYHOUR=9;BYMINUTE=0",
    "FREQ=WEEKLY;BYDAY=XX;BYHOUR=9;BYMINUTE=0",
    "FREQ=WEEKLY;BYDAY=MO;BYMONTHDAY=1;BYHOUR=9;BYMINUTE=0",
    "FREQ=MONTHLY;BYDAY=2TU;BYHOUR=9;BYMINUTE=0",
    "FREQ=MONTHLY;BYMONTHDAY=1;BYSETPOS=1;BYHOUR=9;BYMINUTE=0",
    "FREQ=MONTHLY;BYHOUR=9;BYMINUTE=0",
    "FREQ=MONTHLY;BYMONTHDAY=-1;BYHOUR=9;BYMINUTE=0",
    "FREQ=MINUTELY;INTERVAL=5;BYHOUR=9",
    "FREQ=HOURLY", // no fixed minute
    "FREQ=HOURLY;BYHOUR=9;BYMINUTE=0",
    "FREQ=SECONDLY;INTERVAL=30",
    "FREQ=DAILY;",
    "FREQ=DAILY;;BYHOUR=9;BYMINUTE=0",
    "FREQ=DAILY;BYHOUR;BYMINUTE=0",
    "FREQ=DAILY;BYHOUR=nine;BYMINUTE=0",
    "FREQ=DAILY;BYHOUR=9;BYMINUTE=0;WKST=MO",
  ])("%j cannot be converted and is kept", (expr) => {
    const result = convert(expr, "rrule", "cron");
    expect(result.expr).toBe(expr);
    expect(result.warning).toBeDefined();
  });
});

describe("natural input", () => {
  it.each(NATURAL_TO_CRON)("%j → cron %s", (expr, expected) => expect(convert(expr, "natural", "cron")).toEqual(ok(expected)));

  it.each([
    "every day", // no time
    "every weekday",
    "daily",
    "every day at",
    "every day at nine",
    "every day at 9",
    "every day at 9 o'clock",
    "every day at 0am",
    "every day at 13am",
    "every day at 13pm",
    "every day at 24:00",
    "every day at 9:60",
    "every day at 9:5",
    "every day at 9am and 6pm",
    "every day at 9am 10 times",
    "every day at 9am until 2027-01-01",
    "every funday at 9am",
    "every monday and funday at 9am",
    "every month at 9am",
    "every 0 minutes",
    "every 5 seconds",
    "every 30 seconds",
    "every 2 weeks on friday",
    "every 2 days at 9am",
    "every hour between 9am and 5pm on weekdays",
    "every 10 minutes for 2 hours",
    "second tuesday of every month at 9am",
    "last friday of the month",
    "last day of every month at 23:59",
    "quarterly",
    "yearly",
    "every year on march 31",
    "in 20 minutes",
    "tomorrow at 8am",
    "at 2026-12-25 07:00",
    "on the 0th of every month at 9am",
    "on the 32nd of every month at 9am",
    "on the 1st of every month",
    "on the first of every month at 9am",
    "on the 1st of every year at 9am",
    "every 90",
    "now",
    "?!",
    "0 9 * * *",
    "FREQ=DAILY",
  ])("%j has no cron equivalent and is kept", (expr) => {
    const result = convert(expr, "natural", "cron");
    expect(result.expr).toBe(expr);
    expect(result.warning).toBeDefined();
  });

  it("converts non-word whitespace like newlines and tabs between words", () => {
    expect(convert("every\tday\nat 9am", "natural", "cron").expr).toBe("0 9 * * *");
  });

  it("carries unicode and very long text safely", () => {
    expect(convert("every dаy at 9am", "natural", "cron").warning).toBeDefined(); // Cyrillic а
    const long = `every ${"9".repeat(5000)} minutes`;
    expect(convert(long, "natural", "cron").expr).toBe(long);
  });
});

describe("natural output", () => {
  it("writes whole-hour and minute intervals", () => {
    expect(convert("*/5 * * * *", "cron", "natural").expr).toBe("every 5 minutes");
    expect(convert("0 */6 * * *", "cron", "natural").expr).toBe("every 6 hours");
    expect(convert("FREQ=MINUTELY;INTERVAL=1", "rrule", "natural").expr).toBe("every minute");
    expect(convert("FREQ=HOURLY;INTERVAL=1;BYMINUTE=0", "rrule", "natural").expr).toBe("every hour");
  });

  it("refuses a minute past the hour, whatever it is", () => {
    for (const minute of [1, 15, 30, 59]) {
      const w = refused(`${minute} * * * *`, "cron", "natural");
      expect(w).toMatch(/minute past the hour/);
      refused(`FREQ=HOURLY;INTERVAL=2;BYMINUTE=${minute}`, "rrule", "natural");
    }
  });

  it("writes times as zero-padded 24-hour clock", () => {
    expect(convert("5 0 * * *", "cron", "natural").expr).toBe("every day at 00:05");
    expect(convert("0 13 * * *", "cron", "natural").expr).toBe("every day at 13:00");
  });

  it("orders days Sunday-first and joins them with and", () => {
    expect(convert("0 9 * * 5,1", "cron", "natural").expr).toBe("every monday and friday at 09:00");
    expect(convert("0 9 * * 6,0,3", "cron", "natural").expr).toBe("every sunday and wednesday and saturday at 09:00");
  });

  it("uses the right ordinal suffix for every day of the month", () => {
    const expected = (n: number) => `${n}${n % 100 >= 11 && n % 100 <= 13 ? "th" : (["th", "st", "nd", "rd"][n % 10] ?? "th")}`;
    for (let day = 1; day <= 31; day++) {
      const natural = convert(`0 9 ${day} * *`, "cron", "natural").expr;
      expect(natural).toBe(`on the ${expected(day)} of every month at 09:00`);
      expect(convert(natural, "natural", "cron").expr).toBe(`0 9 ${day} * *`);
    }
  });
});

describe("minute and hour steps", () => {
  it.each([1, 2, 3, 4, 5, 6, 10, 12, 15, 20, 30])("every %i minutes is exact in cron, quartz and rrule", (n) => {
    const cron = n === 1 ? "* * * * *" : `*/${n} * * * *`;
    expect(convert(`every ${n} minutes`, "natural", "cron").expr).toBe(n === 1 ? "* * * * *" : cron);
    expect(convert(cron, "cron", "rrule").expr).toBe(`FREQ=MINUTELY;INTERVAL=${n}`);
    expect(convert(cron, "cron", "quartz").expr).toBe(`0 ${n === 1 ? "*" : `*/${n}`} * * * ?`);
  });

  it.each([7, 8, 9, 11, 13, 14, 16, 17, 18, 25, 40, 45, 59])("every %i minutes restarts each hour in cron, so cron and quartz refuse it", (n) => {
    expect(refused(`*/${n} * * * *`, "cron", "natural")).toMatch(/restarts each hour/);
    const w = refused(`every ${n} minutes`, "natural", "cron");
    expect(w).toMatch(/restarts each hour/);
    refused(`every ${n} minutes`, "natural", "quartz");
    expect(convert(`every ${n} minutes`, "natural", "rrule").expr).toBe(`FREQ=MINUTELY;INTERVAL=${n}`);
  });

  it.each([1, 2, 3, 4, 6, 8, 12])("every %i hours is exact in every syntax", (n) => {
    const cron = n === 1 ? "0 * * * *" : `0 */${n} * * *`;
    expect(convert(`every ${n} hours`, "natural", "cron").expr).toBe(cron);
    expect(convert(cron, "cron", "natural").expr).toBe(n === 1 ? "every hour" : `every ${n} hours`);
    expect(convert(cron, "cron", "rrule").expr).toBe(`FREQ=HOURLY;INTERVAL=${n};BYMINUTE=0`);
  });

  it.each([5, 7, 9, 10, 11, 13, 16, 18])("every %i hours restarts each day in cron, so cron and quartz refuse it", (n) => {
    expect(refused(`every ${n} hours`, "natural", "cron")).toMatch(/restarts each day/);
    refused(`every ${n} hours`, "natural", "quartz");
    expect(convert(`every ${n} hours`, "natural", "rrule").expr).toBe(`FREQ=HOURLY;INTERVAL=${n};BYMINUTE=0`);
  });
});

describe("presets", () => {
  const preset = (label: string) => {
    const found = PRESETS.find((p) => p.label === label);
    if (!found) throw new Error(`no preset ${label}`);
    return found;
  };

  it("offers the five presets in order", () => {
    expect(PRESETS.map((p) => p.label)).toEqual(["every 5 min", "hourly", "weekdays 9am", "daily", "1st of month"]);
  });

  it.each([
    ["every 5 min", { natural: "every 5 minutes", cron: "*/5 * * * *", quartz: "0 */5 * * * ?", rrule: "FREQ=MINUTELY;INTERVAL=5" }],
    ["weekdays 9am", { natural: "every weekday at 09:00", cron: "0 9 * * 1-5", quartz: "0 0 9 ? * MON-FRI", rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0" }],
    ["1st of month", { natural: "on the 1st of every month at 09:00", cron: "0 9 1 * *", quartz: "0 0 9 1 * ?", rrule: "FREQ=MONTHLY;BYMONTHDAY=1;BYHOUR=9;BYMINUTE=0" }],
  ] as const)("%s is written in the selected syntax, never warns and ignores randomness", (label, forms) => {
    for (const mode of SYNTAXES) {
      for (const random of [() => 0, () => 0.5, () => 0.999999]) expect(presetExpression(preset(label), mode, random)).toEqual(ok(forms[mode]));
    }
  });

  it.each([
    [0, 0],
    [0.39, 23],
    [0.5, 30],
    [0.999999, 59],
  ])("hourly and daily spread their minute (random %f → minute %i) where the syntax can say it", (sample, minute) => {
    const random = () => sample;
    expect(presetExpression(preset("hourly"), "cron", random)).toEqual(ok(`${minute} * * * *`));
    expect(presetExpression(preset("hourly"), "quartz", random)).toEqual(ok(`0 ${minute} * * * ?`));
    expect(presetExpression(preset("hourly"), "rrule", random)).toEqual(ok(`FREQ=HOURLY;INTERVAL=1;BYMINUTE=${minute}`));
    expect(presetExpression(preset("daily"), "cron", random)).toEqual(ok(`${minute} 9 * * *`));
    expect(presetExpression(preset("daily"), "quartz", random)).toEqual(ok(`0 ${minute} 9 * * ?`));
    expect(presetExpression(preset("daily"), "rrule", random)).toEqual(ok(`FREQ=DAILY;BYHOUR=9;BYMINUTE=${minute}`));
    expect(presetExpression(preset("daily"), "natural", random)).toEqual(ok(`every day at 09:${String(minute).padStart(2, "0")}`));
  });

  it("falls back to the top of the hour with a warning for hourly in Natural", () => {
    for (const sample of [0, 0.39, 0.999999]) {
      const result = presetExpression(preset("hourly"), "natural", () => sample);
      expect(result.expr).toBe("every hour");
      // A random minute of 0 already is the top of the hour, so there is nothing to warn about.
      if (sample === 0) return expect(result.warning).toBeUndefined();
      expect(result.warning).toMatch(/Natural has no way to pick the minute past the hour, so “hourly” runs at the top of the hour\. Switch to Cron or RRULE/);
    }
  });

  it("uses cron for the spread presets and natural for the rest under auto", () => {
    expect(presetExpression(preset("hourly"), "auto", () => 0.39)).toEqual(ok("23 * * * *"));
    expect(presetExpression(preset("daily"), "auto", () => 0.39)).toEqual(ok("23 9 * * *"));
    expect(presetExpression(preset("every 5 min"), "auto")).toEqual(ok("every 5 minutes"));
    expect(presetExpression(preset("weekdays 9am"), "auto")).toEqual(ok("every weekday at 09:00"));
    expect(presetExpression(preset("1st of month"), "auto")).toEqual(ok("on the 1st of every month at 09:00"));
  });

  it.each([-1, 1, 2, 100, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY])("clamps an out-of-range random value (%f) to a real minute", (sample) => {
    const { expr } = presetExpression(preset("hourly"), "cron", () => sample);
    const minute = Number(expr.split(" ")[0]);
    expect(Number.isInteger(minute) && minute >= 0 && minute <= 59, expr).toBe(true);
  });

  it("calls the random source once per click and defaults to Math.random", () => {
    const random = vi.fn(() => 0.1);
    presetExpression(preset("daily"), "cron", random);
    expect(random).toHaveBeenCalledTimes(1);
    const spy = vi.spyOn(Math, "random").mockReturnValue(0.5);
    expect(presetExpression(preset("hourly"), "cron").expr).toBe("30 * * * *");
    spy.mockRestore();
  });

  it("produces output that converts onward to every other syntax it can", () => {
    for (const p of PRESETS) {
      for (const from of SYNTAXES) {
        const made = presetExpression(p, from, () => 0.39).expr;
        for (const to of SYNTAXES) {
          if (to === from) continue;
          const converted = convert(made, from, to);
          // Natural is the only target that can refuse a preset (minute offset on hourly).
          if (converted.warning) expect(to === "natural" || from === "natural", `${p.label} ${from}→${to}: ${converted.warning}`).toBe(true);
        }
      }
    }
  });
});

describe("switching syntax through every path", () => {
  const syntaxes = ["natural", "cron", "quartz", "rrule"] as const;
  it("a supported schedule survives any sequence of switches including auto", () => {
    const start = "every weekday at 09:00";
    const expected = { natural: start, cron: "0 9 * * 1-5", quartz: "0 0 9 ? * MON-FRI", rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0" } as const;
    for (const a of syntaxes)
      for (const b of syntaxes)
        for (const c of syntaxes) {
          let value = { expr: start, mode: "natural" as ScheduleMode };
          for (const mode of [a, "auto", b, "auto", c] as const) {
            const converted = convert(value.expr, value.mode, mode);
            expect(converted.warning).toBeUndefined();
            value = { expr: converted.expr, mode };
          }
          expect(value.expr, `${a}→${b}→${c}`).toBe(expected[c]);
        }
  });

  it("an unsupported schedule keeps its text on every attempt and recovers once the syntax can say it", () => {
    let value = { expr: "17 * * * *", mode: "cron" as ScheduleMode };
    const toNatural = convert(value.expr, value.mode, "natural");
    expect(toNatural.warning).toBeDefined();
    value = { expr: toNatural.expr, mode: "natural" };
    // Now read as natural: converting onward still can't parse the cron text, and keeps it.
    expect(convert(value.expr, value.mode, "quartz").expr).toBe("17 * * * *");
    // Going back to cron the text is already cron, so it converts cleanly.
    expect(convert(value.expr, value.mode, "rrule").warning).toBeDefined();
    expect(convert(value.expr, "auto", "quartz")).toEqual(ok("0 17 * * * ?"));
  });
});

describe("interval caveat", () => {
  const family = { natural: "interval", rrule: "interval", cron: "clock", quartz: "clock" } as const;
  const intervals = MEANINGS.filter((m) => /FREQ=(MINUTELY|HOURLY)/.test(m.rrule ?? ""));
  const calendar = MEANINGS.filter((m) => !/FREQ=(MINUTELY|HOURLY)/.test(m.rrule ?? ""));

  it("covers both kinds of example", () => {
    expect(intervals.length).toBeGreaterThan(8);
    expect(calendar.length).toBeGreaterThan(10);
  });

  it.each(intervals.map((m) => [m.name, m] as const))("%s: carries a caveat only when crossing between clock and interval syntaxes", (_, m) => {
    for (const from of SYNTAXES) {
      for (const to of SYNTAXES) {
        const source = m[from];
        if (source === null || from === to || m[to] === null) continue;
        const result = convertRaw(source, from, to);
        expect(result.expr).toBe(m[to]);
        expect(result.warning).toBeUndefined();
        if (family[from] === family[to]) expect(result.caveat, `${from} → ${to}`).toBeUndefined();
        else expect(result.caveat, `${from} → ${to}`).toMatch(/follow the clock in the job's time zone.*count from when the job is created/);
      }
    }
  });

  it.each(calendar.map((m) => [m.name, m] as const))("%s: calendar schedules never carry a caveat", (_, m) => {
    for (const from of SYNTAXES)
      for (const to of SYNTAXES) {
        const source = m[from];
        if (source === null || from === to || m[to] === null) continue;
        expect(convertRaw(source, from, to).caveat, `${from} → ${to}`).toBeUndefined();
      }
  });

  it("converts from auto with the same caveat", () => {
    expect(convertRaw("*/5 * * * *", "auto", "natural").caveat).toBeDefined();
    expect(convertRaw("every 5 minutes", "auto", "cron").caveat).toBeDefined();
    expect(convertRaw("0 9 * * *", "auto", "natural").caveat).toBeUndefined();
  });

  it("a refusal is a warning without a caveat, and an exact conversion has no warning", () => {
    const refusal = convertRaw("*/7 * * * *", "cron", "quartz");
    expect(refusal.warning).toBeDefined();
    expect(refusal.caveat).toBeUndefined();
    expect(convertRaw("*/5 * * * *", "cron", "natural").warning).toBeUndefined();
  });

  it("presets are written, not converted, so they never carry a caveat", () => {
    for (const mode of ["auto", "natural", "cron", "quartz", "rrule"] as const) for (const p of PRESETS) expect(presetExpression(p, mode, () => 0.39).caveat).toBeUndefined();
  });
});

