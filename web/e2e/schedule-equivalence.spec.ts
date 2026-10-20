import { convertExpression } from "@cronpad/schedule";
import { CRON_TO_NATURAL, MEANINGS, NATURAL_TO_CRON, QUARTZ_TO_CRON, RRULE_TO_CRON, SYNTAXES, type Syntax } from "@cronpad/schedule/fixtures";
import { expect, test, type APIRequestContext } from "@playwright/test";

/**
 * The real schedule parser is the oracle: a converted expression is only correct if it fires at
 * exactly the same instants as the one it came from. Interval rules count elapsed time and are
 * checked in UTC; calendar rules are checked across a daylight-saving change in New York.
 */
const AFTER = "2026-02-25T00:00:00Z";
const COUNT = 60;
const PACE_MS = 120; // stay under the preview endpoint's 600 requests a minute

interface Reply {
  status: number;
  kind?: string;
  next?: string[];
}

const cache = new Map<string, Reply>();
let last = 0;

async function preview(request: APIRequestContext, expr: string, kind: string, timezone: string, after = AFTER): Promise<Reply> {
  const key = `${kind}\u0000${timezone}\u0000${after}\u0000${expr}`;
  const hit = cache.get(key);
  if (hit) return hit;
  const wait = last + PACE_MS - Date.now();
  if (wait > 0) await new Promise((resolve) => setTimeout(resolve, wait));
  last = Date.now();
  const response = await request.post("/api/v1/schedules/preview", { data: { expr, kind, timezone, after, count: COUNT } });
  if (response.status() === 429) throw new Error("preview endpoint rate limited the test");
  const body = response.status() === 200 ? await response.json() : {};
  const reply = { status: response.status(), kind: body.kind_detected, next: body.next };
  cache.set(key, reply);
  return reply;
}

const isInterval = (m: { rrule: string | null }) => /FREQ=(MINUTELY|HOURLY)/.test(m.rrule ?? "");
const zoneFor = (m: { rrule: string | null }) => (isInterval(m) ? "UTC" : "America/New_York");

test.describe.configure({ mode: "serial", timeout: 300_000 });

test("every shared example is valid, detected as its own syntax, and read the same under auto", async ({ request }) => {
  for (const m of MEANINGS) {
    const zone = zoneFor(m);
    for (const syntax of SYNTAXES) {
      const expr = m[syntax];
      if (expr === null) continue;
      const explicit = await preview(request, expr, syntax, zone);
      expect(explicit.status, `${m.name} as ${syntax}: ${expr}`).toBe(200);
      expect(explicit.next, `${m.name} as ${syntax}`).toHaveLength(COUNT);
      const auto = await preview(request, expr, "auto", zone);
      expect(auto.status, `${m.name} auto: ${expr}`).toBe(200);
      expect(auto.kind, `server detected ${expr}`).toBe(syntax);
      expect(auto.next, `${m.name} auto vs ${syntax}`).toEqual(explicit.next);
    }
  }
});

test("all four spellings of every example fire at identical times", async ({ request }) => {
  for (const m of MEANINGS) {
    const zone = zoneFor(m);
    const forms = SYNTAXES.filter((s) => m[s] !== null);
    const reference = await preview(request, m[forms[0] as Syntax] as string, forms[0] as string, zone);
    for (const syntax of forms) {
      const reply = await preview(request, m[syntax] as string, syntax, zone);
      expect(reply.next, `${m.name}: ${syntax} vs ${forms[0]}`).toEqual(reference.next);
    }
  }
});

test("converting any example to any syntax yields an expression that fires at the same times", async ({ request }) => {
  let compared = 0;
  for (const m of MEANINGS) {
    const zone = zoneFor(m);
    for (const from of SYNTAXES) {
      const source = m[from];
      if (source === null) continue;
      const original = await preview(request, source, from, zone);
      for (const to of SYNTAXES) {
        if (to === from) continue;
        const converted = convertExpression(source, from, to);
        if (m[to] === null) {
          expect(converted.warning, `${m.name}: ${from} → ${to} must be refused`).toBeDefined();
          continue;
        }
        expect(converted.warning).toBeUndefined();
        const after = await preview(request, converted.expr, to, zone);
        expect(after.status, `${m.name}: ${source} → ${converted.expr} must be valid ${to}`).toBe(200);
        expect(after.next, `${m.name}: ${from} ${source} → ${to} ${converted.expr}`).toEqual(original.next);
        compared++;
      }
    }
  }
  expect(compared).toBeGreaterThan(250);
});

test("converting from auto reads the typed text the way the server does", async ({ request }) => {
  for (const m of MEANINGS) {
    const zone = zoneFor(m);
    for (const from of SYNTAXES) {
      const source = m[from];
      if (source === null) continue;
      const original = await preview(request, source, "auto", zone);
      for (const to of SYNTAXES) {
        if (to === from || m[to] === null) continue;
        const converted = convertExpression(source, "auto", to);
        const after = await preview(request, converted.expr, to, zone);
        expect(after.next, `${m.name}: auto ${source} → ${to} ${converted.expr}`).toEqual(original.next);
      }
    }
  }
});

for (const [name, table, from] of [
  ["cron", CRON_TO_NATURAL, "cron"],
  ["quartz", QUARTZ_TO_CRON, "quartz"],
  ["rrule", RRULE_TO_CRON, "rrule"],
  ["natural", NATURAL_TO_CRON, "natural"],
] as const) {
  test(`every ${name} edge input the server accepts converts to expressions with the same fire times`, async ({ request }) => {
    let compared = 0;
    for (const [source] of table) {
      for (const zone of ["America/New_York", "UTC"]) {
        const original = await preview(request, source, from, zone);
        // The converter is deliberately lenient about spellings the server would reject (day 7, say).
        if (original.status !== 200) continue;
        for (const to of SYNTAXES) {
          if (to === from) continue;
          const converted = convertExpression(source, from, to);
          if (converted.warning) continue;
          const after = await preview(request, converted.expr, to, zone);
          expect(after.status, `${source} → ${converted.expr} must be valid ${to}`).toBe(200);
          // The converter flags steps that cross between clock and interval syntaxes. Those only line up
          // in UTC, with the start aligned to the grid; the caveat test below shows they drift otherwise.
          if (converted.caveat && zone !== "UTC") continue;
          expect(after.next, `${zone}: ${from} ${source} → ${to} ${converted.expr}`).toEqual(original.next);
          compared++;
        }
      }
    }
    expect(compared).toBeGreaterThan(table.length);
  });
}

test("the caveat is justified: interval and clock schedules drift apart unless the start is aligned", async ({ request }) => {
  const drifted = "2026-02-25T00:03:00Z";
  const interval = await preview(request, "every 5 minutes", "natural", "UTC", drifted);
  const clock = await preview(request, "*/5 * * * *", "cron", "UTC", drifted);
  expect(clock.next).not.toEqual(interval.next);
  expect(clock.next?.[0]).toBe("2026-02-25T00:05:00Z"); // on the clock
  expect(interval.next?.[0]).toBe("2026-02-25T00:08:00Z"); // counted from the start
  // In a zone with a whole-hour offset the 6-hour clock grid sits on local midnight, not the UTC-aligned start.
  const ny = await preview(request, "0 */6 * * *", "cron", "America/New_York");
  const elapsed = await preview(request, "every 6 hours", "natural", "America/New_York");
  expect(ny.next).not.toEqual(elapsed.next);
  expect(convertExpression("every 5 minutes", "natural", "cron").caveat).toBeDefined();
  expect(convertExpression("every 6 hours", "natural", "cron").caveat).toBeDefined();
  // Calendar schedules carry no caveat because they match exactly, daylight saving included.
  expect(convertExpression("every day at 02:30", "natural", "cron").caveat).toBeUndefined();
});

test("the refused conversions really are different schedules", async ({ request }) => {
  // The "obvious" cron spelling of an uneven step restarts each hour or day, so it would fire at other times.
  const minutes = await preview(request, "every 7 minutes", "natural", "UTC");
  const cronMinutes = await preview(request, "*/7 * * * *", "cron", "UTC");
  expect(cronMinutes.next).not.toEqual(minutes.next);
  const hours = await preview(request, "every 5 hours", "natural", "UTC");
  const cronHours = await preview(request, "0 */5 * * *", "cron", "UTC");
  expect(cronHours.next).not.toEqual(hours.next);
  // And the converter refuses exactly those.
  expect(convertExpression("every 7 minutes", "natural", "cron").warning).toBeDefined();
  expect(convertExpression("every 5 hours", "natural", "cron").warning).toBeDefined();
});
