import { MODES, type ScheduleMode, type ScheduleValue } from "@cronpad/schedule";
import { ScheduleNotice, SchedulePresets, ScheduleSyntaxToggle, type ScheduleChange } from "@cronpad/schedule/ui";
import cronstrue from "cronstrue";
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { api, ApiError, call, type ScheduleProblem, type Schemas } from "@/api/client";
import { rules } from "@/api/rules";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { formatDateTime, viewerTimeZone } from "@/lib/format";
import { cn } from "@/lib/utils";

export type { ScheduleMode, ScheduleValue };
export type SchedulePreview = Schemas["SchedulePreview"];

export type PreviewFn = (input: { expr: string; kind: ScheduleMode; timezone: string }) => Promise<SchedulePreview>;

export const DEBOUNCE_MS = 200;
const FIRES = 5;

/** Preview through the public API; validation failures arrive as ApiError.problems. */
export const apiPreview: PreviewFn = ({ expr, kind, timezone }) =>
  call(api.POST("/api/v1/schedules/preview", { body: { expr, kind, timezone, count: FIRES } }));

/** The server reports byte offsets; JavaScript strings are indexed in UTF-16 units. */
export function byteOffsetToIndex(text: string, byteOffset: number): number {
  const encoder = new TextEncoder();
  let bytes = 0;
  let index = 0;
  for (const char of text) {
    if (bytes >= byteOffset) break;
    bytes += encoder.encode(char).length;
    index += char.length;
  }
  return index;
}

export function problemRange(expr: string, problem: ScheduleProblem): [number, number] {
  const start = Math.min(byteOffsetToIndex(expr, problem.position ?? 0), expr.length);
  let end = start;
  if (problem.length && problem.length > 0) {
    end = byteOffsetToIndex(expr, (problem.position ?? 0) + problem.length);
  } else {
    // No length from the server: underline the word at the position.
    while (end < expr.length && !/\s/.test(expr.charAt(end))) end += 1;
  }
  if (end <= start) end = Math.min(start + 1, expr.length);
  return [start, Math.max(end, start)];
}

function instantCronDescription(expr: string): string | null {
  if (expr.trim().split(/\s+/).length !== 5) return null;
  try {
    return cronstrue.toString(expr, { verbose: false });
  } catch {
    return null;
  }
}

function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return debounced;
}

type State =
  | { status: "idle" }
  | { status: "ok"; preview: SchedulePreview }
  | { status: "invalid"; problems: ScheduleProblem[]; expr: string }
  | { status: "failed"; message: string };

interface Props {
  value: ScheduleValue;
  onChange: (value: ScheduleValue) => void;
  /** IANA zone the schedule runs in. */
  timezone: string;
  preview?: PreviewFn;
  random?: () => number;
  viewerZone?: string;
  /** Reports validity so forms can block submission. */
  onValidity?: (valid: boolean) => void;
  id?: string;
  error?: string | null;
  /** Extra controls that belong with the schedule (the time zone), shown under the presets. */
  children?: ReactNode;
}

/**
 * Schedule editor with a compact empty state and a reserved result area while
 * a nonempty expression is being checked.
 */
export function ScheduleInput({ value, onChange, timezone, preview = apiPreview, random = Math.random, viewerZone, onValidity, id, error, children }: Props) {
  const generated = useId();
  const inputId = id ?? `${generated}-expr`;
  const statusId = `${generated}-status`;
  const viewer = viewerZone ?? viewerTimeZone();
  const [settled, setSettled] = useState<{ key: string; state: State } | null>(null);
  const request = useRef(0);
  // Why the last mode switch or preset couldn't be carried over; cleared as soon as the user edits.
  const [notice, setNotice] = useState<string | null>(null);

  const change: ScheduleChange = (next, message) => {
    setNotice(message);
    onChange(next);
  };

  const query = useDebounced({ expr: value.expr.trim(), mode: value.mode, timezone }, DEBOUNCE_MS);
  const queryKey = `${query.mode}\u0000${query.timezone}\u0000${query.expr}`;
  const typing = query.expr !== value.expr.trim() || query.mode !== value.mode || query.timezone !== timezone;
  // A result belongs to the query that produced it; anything else is still pending.
  const pending = query.expr !== "" && settled?.key !== queryKey;
  const state: State = value.expr.trim() === "" || query.expr === "" || !settled ? { status: "idle" } : settled.state;

  useEffect(() => {
    if (query.expr === "") return;
    const mine = ++request.current;
    const done = (next: State) => {
      if (mine === request.current) setSettled({ key: queryKey, state: next });
    };
    preview({ expr: query.expr, kind: query.mode, timezone: query.timezone })
      .then((result) => done({ status: "ok", preview: result }))
      .catch((error: unknown) => {
        if (error instanceof ApiError && error.status === 400) {
          const problems = error.problems.length > 0 ? error.problems : [{ message: error.fields.schedule ?? error.message }];
          done({ status: "invalid", problems, expr: query.expr });
        } else {
          done({ status: "failed", message: error instanceof Error ? error.message : "Preview failed" });
        }
      });
  }, [query.expr, query.mode, query.timezone, queryKey, preview]);

  const valid = state.status === "ok" && !typing && !pending;
  useEffect(() => {
    onValidity?.(valid);
  }, [valid, onValidity]);

  const placeholder = MODES.find((m) => m.value === value.mode)?.placeholder;
  const instant = useMemo(() => (value.mode === "cron" || value.mode === "auto" ? instantCronDescription(value.expr.trim()) : null), [value.expr, value.mode]);
  const problem = state.status === "invalid" && !typing ? state.problems[0] : undefined;

  return (
    <div className="grid min-w-0 gap-4 lg:grid-cols-2 lg:items-start">
    <div className="grid min-w-0 grid-cols-1 gap-3">
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-2">
        <Label htmlFor={inputId}>Schedule</Label>
        <div className="w-full min-w-0 overflow-x-auto"><ScheduleSyntaxToggle name={`${generated}-syntax`} value={value} onChange={change} /></div>
      </div>

      <Input
        id={inputId}
        value={value.expr}
        maxLength={rules.scheduleExprMax}
        placeholder={placeholder}
        spellCheck={false}
        autoComplete="off"
        className="font-mono"
        aria-invalid={problem || error ? true : undefined}
        aria-describedby={error ? `${statusId} ${inputId}-error` : statusId}
        onChange={(e) => {
          setNotice(null);
          onChange({ ...value, expr: e.target.value });
        }}
      />

      {error ? <p id={`${inputId}-error`} className="flex items-start gap-1.5 text-xs font-medium text-destructive"><span aria-hidden="true">✕</span>{error}</p> : null}

      <ScheduleNotice message={notice} />

      <SchedulePresets value={value} onChange={change} random={random} />

      <p className="text-xs text-muted-foreground">Hourly and daily presets spread out to avoid the top-of-the-hour rush.</p>
      {children}
    </div>

      <div id={statusId} role="status" aria-live="polite" aria-busy={pending || typing} className={cn("border border-line bg-sunken p-3", value.expr.trim() && "min-h-[15rem]")}>
        {state.status === "idle" ? (
          <p className="text-sm text-muted-foreground">{value.expr.trim() ? "Checking schedule…" : "Choose a preset or type a schedule to preview the next runs."}</p>
        ) : null}

        {state.status === "failed" ? <p className="text-sm text-destructive">Couldn’t check this schedule: {state.message}</p> : null}

        {problem && state.status === "invalid" ? <ProblemView expr={state.expr} problem={problem} onApply={(expr) => onChange({ ...value, expr })} /> : null}

        {state.status === "ok" ? (
          <div className={cn("grid gap-3", (pending || typing) && "opacity-60")}>
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-sm font-medium" data-testid="schedule-description">
                {state.preview.description}
              </p>
              <Badge variant="outline">{state.preview.kind_detected}</Badge>
            </div>
            <p className="break-all font-mono text-xs text-muted-foreground">{state.preview.canonical}</p>
            <div className="min-w-0">
              {viewer === state.preview.timezone ? (
                <FireList title={`In ${viewer} (your time zone)`} zone={viewer} times={state.preview.next} testId="fires-job" />
              ) : (
                <div className="grid gap-4 sm:grid-cols-2">
                  <FireList title={`In ${state.preview.timezone}`} zone={state.preview.timezone} times={state.preview.next} testId="fires-job" />
                  <FireList title={`In your time zone (${viewer})`} zone={viewer} times={state.preview.next} testId="fires-viewer" />
                </div>
              )}
            </div>
          </div>
        ) : null}

        {state.status !== "ok" && !problem && instant ? <p className="mt-2 text-xs text-muted-foreground">{instant}</p> : null}
      </div>
    </div>
  );
}

function ProblemView({ expr, problem, onApply }: { expr: string; problem: ScheduleProblem; onApply: (expr: string) => void }) {
  const [start, end] = problemRange(expr, problem);
  const suggestion = problem.suggestion?.trim();
  return (
    <div className="grid gap-2" data-testid="schedule-problem">
      <p className="overflow-x-auto whitespace-pre font-mono text-sm" aria-hidden="true">
        {expr.slice(0, start)}
        <span data-testid="schedule-underline" className="underline decoration-destructive decoration-wavy decoration-2 underline-offset-4">
          {expr.slice(start, end) || " "}
        </span>
        {expr.slice(end)}
      </p>
      <p className="text-sm font-medium text-destructive"><span aria-hidden="true">✕ </span><span>{problem.message}</span></p>
      {suggestion ? (
        <p className="text-sm">
          Did you mean{" "}
          <Button
            variant="link"
            className="h-auto p-0 font-mono"
            onClick={() => onApply(expr.slice(0, start) + suggestion + expr.slice(end))}
          >
            {suggestion}
          </Button>
          ?
        </p>
      ) : null}
    </div>
  );
}

function FireList({ title, zone, times, testId }: { title: string; zone: string; times: string[]; testId: string }) {
  return (
    <div>
      <h3 className="mb-1 text-xs font-bold uppercase tracking-wide text-muted-foreground">{title}</h3>
      <ol className="grid gap-0.5 text-sm tabular-nums" data-testid={testId}>
        {times.map((t) => (
          <li key={t}><time dateTime={t} title={formatDateTime(t, zone)} aria-label={formatDateTime(t, zone)}>{new Intl.DateTimeFormat("en", { timeZone: zone, month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }).format(new Date(t))}</time><span className="block text-xs text-ink-soft">{formatDateTime(t, "UTC")}</span></li>
        ))}
      </ol>
    </div>
  );
}
