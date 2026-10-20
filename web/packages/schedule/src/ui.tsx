/**
 * Schedule editor controls shared by the dashboard (React) and the website (Preact via
 * preact/compat). Stateless on purpose: no hooks, so both runtimes behave the same.
 * Styling uses only @cronpad/design tokens and classes; each app must point Tailwind's
 * @source at this folder so the utilities below are generated.
 */
import { convertExpression, MODES, PRESETS, presetExpression, type ScheduleMode, type SchedulePreset, type ScheduleValue } from "./convert";

/** `notice` explains a conversion or preset that could not be applied exactly; null clears it. */
export type ScheduleChange = (value: ScheduleValue, notice: string | null) => void;

/** Syntax picker. Switching converts the expression into the new syntax where it can. */
export function ScheduleSyntaxToggle({ value, onChange, name, className = "" }: { value: ScheduleValue; onChange: ScheduleChange; name: string; className?: string }) {
  const pick = (mode: ScheduleMode) => {
    const converted = convertExpression(value.expr, value.mode, mode);
    onChange({ expr: converted.expr, mode }, converted.warning ?? converted.caveat ?? null);
  };
  return (
    <fieldset className={`inline-flex w-fit max-w-full flex-wrap items-center self-start rounded border border-ink/40 bg-sunken p-0.5 ${className}`}>
      <legend className="sr-only">Schedule syntax</legend>
      {MODES.map((m) => (
        <label
          key={m.value}
          className="relative inline-flex min-h-10 cursor-pointer items-center justify-center whitespace-nowrap rounded-sm px-3 py-1 text-sm font-bold text-ink-soft transition-colors hover:text-ink has-[:checked]:bg-accent has-[:checked]:text-accent-ink has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-focus sm:min-h-8 sm:px-2.5"
        >
          <input type="radio" name={name} value={m.value} checked={value.mode === m.value} onChange={() => pick(m.value)} className="peer sr-only" />
          {m.label}
        </label>
      ))}
    </fieldset>
  );
}

/** One-click schedules, written in the selected syntax. */
export function SchedulePresets({ value, onChange, random, className = "" }: { value: ScheduleValue; onChange: ScheduleChange; random?: () => number; className?: string }) {
  const apply = (preset: SchedulePreset) => {
    const made = presetExpression(preset, value.mode, random);
    onChange({ ...value, expr: made.expr }, made.warning ?? null);
  };
  return (
    <div className={`flex flex-wrap gap-2 ${className}`} role="group" aria-label="Schedule presets">
      {PRESETS.map((p) => (
        <button key={p.label} type="button" className="btn btn-secondary" onClick={() => apply(p)}>
          {p.label}
        </button>
      ))}
    </div>
  );
}

/** Why the last syntax switch or preset wasn't applied exactly. Renders nothing without a message. */
export function ScheduleNotice({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <p role="status" data-testid="schedule-notice" className="flex items-start gap-1.5 border border-ink/40 bg-warn-soft p-2 text-xs font-medium text-ink">
      <span aria-hidden="true">⚠</span>
      {message}
    </p>
  );
}
