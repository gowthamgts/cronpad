import { useId } from "react";
import { cn } from "@/lib/utils";

interface Option<T extends string> {
  value: T;
  label: string;
}

/**
 * A single-choice control built on native radio inputs, so arrow-key navigation
 * and screen-reader semantics come from the browser rather than ARIA wiring.
 */
export function Segmented<T extends string>({ value, onChange, options, label, className }: { value: T; onChange: (value: T) => void; options: Option<T>[]; label: string; className?: string }) {
  const name = useId();
  return (
    <fieldset className={cn("inline-flex w-fit max-w-full flex-wrap self-start items-center rounded border border-ink/40 bg-sunken p-0.5 text-muted-foreground", className)}>
      <legend className="sr-only">{label}</legend>
      {options.map((o) => (
        <label
          key={o.value}
          className="relative inline-flex cursor-pointer rounded-sm items-center justify-center whitespace-nowrap min-h-10 px-3 py-1 text-sm font-bold text-ink-soft transition-colors hover:text-ink sm:min-h-8 sm:px-2.5 has-[:checked]:bg-accent has-[:checked]:text-accent-ink has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring"
        >
          <input type="radio" name={name} value={o.value} checked={value === o.value} onChange={() => onChange(o.value)} className="peer sr-only" />
          {o.label}
        </label>
      ))}
    </fieldset>
  );
}
