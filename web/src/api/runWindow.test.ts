import { describe, expect, it } from "vitest";
import type { Run } from "./hooks";
import { mergeWindow, planFetch, windowStart, type RunWindow } from "./runWindow";

const H = 3_600_000;
const shape = { count: 24, size: H };
const T0 = Date.UTC(2026, 10, 6, 0, 0, 0);

function run(id: string, at: number, status: Run["status"] = "succeeded"): Run {
  return { id, job_id: "job_1", status, scheduled_at: new Date(at).toISOString() } as Run;
}

function load(prev: RunWindow | undefined, now: number, server: Run[], cap = Infinity) {
  const plan = planFetch(prev, now, shape);
  const matching = server.filter((r) => new Date(r.scheduled_at).getTime() >= plan.from).sort((a, b) => b.scheduled_at.localeCompare(a.scheduled_at));
  return { plan, next: mergeWindow(prev, matching.slice(0, cap), matching.length > cap, plan, now, shape) };
}

describe("run window cache", () => {
  it("fetches the whole window first, then only from the first open bucket", () => {
    const now = T0 + 30 * H + 20 * 60_000; // 06:20 on day 2
    const server = [run("a", now - 10 * H), run("b", now - 2 * H), run("c", now - 60_000)];
    const first = load(undefined, now, server);
    expect(first.plan).toEqual({ from: windowStart(now, shape), reuse: false });
    expect(first.next.runs.map((r) => r.id)).toEqual(["c", "b", "a"]);
    // 06:20 - 5 min settles everything before 06:00.
    expect(first.next.frozenUntil).toBe(T0 + 30 * H);

    const later = now + 60_000;
    const second = load(first.next, later, [...server, run("d", later - 1000)]);
    expect(second.plan).toEqual({ from: T0 + 30 * H, reuse: true });
    expect(second.next.runs.map((r) => r.id)).toEqual(["d", "c", "b", "a"]);
  });

  it("does not freeze a bucket that still has an unfinished run", () => {
    const now = T0 + 30 * H + 20 * 60_000;
    const { next } = load(undefined, now, [run("a", now - 3 * H, "deferred"), run("b", now - H)]);
    expect(next.frozenUntil).toBe(T0 + 27 * H);
  });

  it("does not freeze a bucket that closed less than the settle time ago", () => {
    const now = T0 + 30 * H + 2 * 60_000;
    const { next } = load(undefined, now, []);
    expect(next.frozenUntil).toBe(T0 + 29 * H);
  });

  it("drops runs that slid out of the window", () => {
    const now = T0 + 30 * H + 20 * 60_000;
    const first = load(undefined, now, [run("old", windowStart(now, shape)), run("new", now - H)]);
    const second = load(first.next, now + H, [run("old", windowStart(now, shape)), run("new", now - H)]);
    expect(second.next.runs.map((r) => r.id)).toEqual(["new"]);
  });

  it("refetches everything once the cache is old", () => {
    const now = T0 + 30 * H + 20 * 60_000;
    const first = load(undefined, now, []);
    expect(planFetch(first.next, now + 16 * 60_000, shape).reuse).toBe(false);
  });

  it("marks where a capped fetch stopped and does not mix in older cached runs", () => {
    const now = T0 + 30 * H + 20 * 60_000;
    const server = [run("a", now - 5 * H), run("b", now - 4 * H), run("c", now - 3 * H)];
    const first = load(undefined, now, server, 2);
    expect(first.next.loadedFrom).toBe(now - 4 * H);
    expect(first.next.runs.map((r) => r.id)).toEqual(["c", "b"]);
    const second = load(first.next, now + 60_000, server);
    expect(second.next.loadedFrom).toBe(now - 4 * H);
    const capped = load(second.next, now + 2 * 60_000, [...server, run("x", now), run("y", now + 1000)], 1);
    expect(capped.next.runs.map((r) => r.id)).toEqual(["y"]);
    expect(capped.next.loadedFrom).toBe(now + 1000);
  });
});
