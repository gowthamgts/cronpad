import { Info } from "lucide-react";
import { useEffect, useId, useState, type ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * A small "i" button that shows extra detail on hover, focus or tap. The panel
 * stays open while the pointer is over it and closes on Escape, so it meets the
 * hover-content rules (WCAG 1.4.13).
 */
export function InfoTip({ label, children, className }: { label: string; children: ReactNode; className?: string }) {
  const [open, setOpen] = useState(false);
  const id = useId();

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  return (
    <span className={cn("relative inline-flex", className)} onMouseEnter={() => setOpen(true)} onMouseLeave={() => setOpen(false)}>
      <button
        type="button"
        aria-label={label}
        aria-describedby={id}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onClick={() => setOpen((o) => !o)}
        className="inline-flex size-11 rounded items-center justify-center text-ink-soft hover:text-ink sm:size-8"
      >
        <Info aria-hidden="true" className="size-4" />
      </button>
      <span
        role="tooltip"
        id={id}
        hidden={!open}
        className="absolute right-0 top-full z-30 w-72 max-w-[calc(100vw-2rem)] rounded-md border border-line bg-surface p-3 text-xs leading-relaxed text-ink shadow-[0_8px_24px_hsl(var(--ink)/0.14)]"
      >
        {children}
      </span>
    </span>
  );
}
