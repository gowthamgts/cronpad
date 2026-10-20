// React port of cronpad-web/src/components/ui/Combobox.tsx (Preact). Keep behaviour in sync.
import { Fragment, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";

export type ComboboxOption = {
  value: string;
  /** Text shown in the list and on the trigger. Defaults to value. */
  label?: string;
  /** Options with the same group are listed under one heading, in the order groups first appear. */
  group?: string;
  /** Short right-aligned hint, e.g. a UTC offset. Also searchable. */
  hint?: string;
  /** Extra search terms that are not displayed. */
  keywords?: string;
};

type Props = {
  id?: string;
  hideLabel?: boolean;
  invalid?: boolean;
  describedBy?: string;
  label: string;
  options: ComboboxOption[];
  value: string;
  onChange: (value: string) => void;
  searchPlaceholder?: string;
  emptyText?: string;
  className?: string;
};

const normalize = (s: string) => s.toLowerCase().replace(/[_/]+/g, " ").replace(/\s+/g, " ").trim();

/**
 * Searchable select (WAI-ARIA combobox + listbox). A trigger button opens a panel with a search
 * field; arrows, Home/End and PageUp/PageDown move, Enter picks, Escape closes and returns focus.
 */
export default function Combobox({ id, hideLabel = false, invalid, describedBy, label, options, value, onChange, searchPlaceholder = "Search…", emptyText = "No matches", className = "" }: Props) {
  const uid = `cb${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
  const ids = { label: `${uid}-label`, trigger: id ?? `${uid}-trigger`, input: `${uid}-search`, list: `${uid}-list` };
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  // The popup is wider than narrow triggers; open it toward whichever side has room.
  const [alignRight, setAlignRight] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);

  const selected = options.find((o) => o.value === value);
  const filtered = useMemo(() => {
    const terms = normalize(query).split(" ").filter(Boolean);
    if (!terms.length) return options;
    return options.filter((o) => {
      const haystack = normalize(`${o.label ?? o.value} ${o.value} ${o.group ?? ""} ${o.hint ?? ""} ${o.keywords ?? ""}`);
      return terms.every((t) => haystack.includes(t));
    });
  }, [options, query]);

  const optionId = useCallback((i: number) => `${uid}-opt-${i}`, [uid]);

  const show = (seed = "") => {
    setQuery(seed);
    const i = options.findIndex((o) => o.value === value);
    setActive(seed || i < 0 ? 0 : i);
    const box = root.current?.getBoundingClientRect();
    setAlignRight(Boolean(box && box.left + 320 > window.innerWidth - 16));
    setOpen(true);
  };
  const close = (refocus = true) => {
    setOpen(false);
    if (refocus) trigger.current?.focus();
  };
  const pick = (o: ComboboxOption | undefined) => {
    if (!o) return;
    onChange(o.value);
    close();
  };

  // Focus the search field before paint so no keystroke is lost.
  useLayoutEffect(() => {
    if (open) input.current?.focus();
  }, [open]);

  // Keep the highlighted option visible.
  useEffect(() => {
    if (!open) return;
    list.current?.querySelector<HTMLElement>(`#${CSS.escape(optionId(active))}`)?.scrollIntoView({ block: "nearest" });
  }, [active, open, filtered, optionId]);

  // Click or focus outside closes without stealing focus.
  useEffect(() => {
    if (!open) return;
    const away = (e: Event) => {
      if (!root.current?.contains(e.target as Node)) close(false);
    };
    document.addEventListener("pointerdown", away);
    document.addEventListener("focusin", away);
    return () => {
      document.removeEventListener("pointerdown", away);
      document.removeEventListener("focusin", away);
    };
  }, [open]);

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    const last = filtered.length - 1;
    const page = 8;
    const moves: Record<string, () => number> = {
      ArrowDown: () => Math.min(last, active + 1),
      ArrowUp: () => Math.max(0, active - 1),
      PageDown: () => Math.min(last, active + page),
      PageUp: () => Math.max(0, active - page),
      Home: () => 0,
      End: () => last,
    };
    const move = moves[e.key];
    if (move) {
      e.preventDefault();
      setActive(move());
    } else if (e.key === "Enter") {
      e.preventDefault();
      pick(filtered[active]);
    } else if (e.key === "Escape") {
      e.preventDefault();
      close();
    } else if (e.key === "Tab") {
      close(false);
    }
  };

  // Render with group headings; option indexes stay flat for keyboard movement.
  let lastGroup: string | undefined;
  const rows = filtered.map((o, i) => {
    const heading = o.group && o.group !== lastGroup ? o.group : null;
    lastGroup = o.group;
    const isActive = i === active;
    const isSelected = o.value === value;
    return (
      <Fragment key={o.value}>
        {heading && (
          <li role="presentation" className="sticky top-0 z-10 border-b border-line bg-sunken px-3 py-1 text-[11px] font-bold tracking-widest uppercase text-ink-soft">
            {heading}
          </li>
        )}
        <li
          id={optionId(i)}
          role="option"
          aria-selected={isSelected}
          className={`mx-1 flex min-h-10 cursor-pointer items-center gap-2 rounded-sm px-2 text-sm sm:min-h-9 ${isActive ? "bg-accent text-accent-ink" : "hover:bg-sunken"}`}
          onPointerMove={() => setActive(i)}
          onMouseDown={(e) => e.preventDefault()}
          onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); pick(o); } }}
          onClick={() => pick(o)}
        >
          <span aria-hidden="true" className="w-4 shrink-0 font-bold">{isSelected ? "✓" : ""}</span>
          <span className="min-w-0 flex-1 truncate">{o.label ?? o.value}</span>
          {o.hint && <span className={`shrink-0 text-xs tabular-nums ${isActive ? "" : "text-ink-soft"}`}>{o.hint}</span>}
        </li>
      </Fragment>
    );
  });

  return (
    <div ref={root} className={`relative grid content-start gap-1.5 ${className}`}>
      <span id={ids.label} className={hideLabel ? "sr-only" : "flex min-h-5 items-center text-xs font-bold"}>{label}</span>
      <button
        ref={trigger}
        id={ids.trigger}
        type="button"
        data-invalid={invalid || undefined}
        aria-describedby={describedBy}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-labelledby={`${ids.label} ${ids.trigger}`}
        className="flex min-h-11 w-full items-center sm:min-h-9 gap-2 rounded border border-ink/40 bg-paper px-3 text-left hover:border-ink/70 focus-visible:border-focus focus-visible:ring-1 focus-visible:ring-focus focus-visible:ring-offset-0 text-base sm:text-sm data-[invalid=true]:border-danger data-[invalid=true]:bg-bad-soft"
        onClick={() => (open ? close() : show())}
        onKeyDown={(e) => {
          if (["ArrowDown", "ArrowUp", "Enter", " "].includes(e.key)) {
            e.preventDefault();
            show();
          } else if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
            // Type-ahead: typing on the closed trigger opens it with the search started.
            e.preventDefault();
            show(e.key);
          }
        }}
      >
        <span className="min-w-0 flex-1 truncate">{selected?.label ?? value}</span>
        {selected?.hint && <span className="shrink-0 text-xs text-ink-soft tabular-nums">{selected.hint}</span>}
        <svg aria-hidden="true" viewBox="0 0 24 24" className={`size-4 shrink-0 transition-transform ${open ? "rotate-180" : ""}`}>
          <path d="m6 9 6 6 6-6" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="square" />
        </svg>
      </button>

      {open && (
        <div className={`absolute ${alignRight ? "right-0" : "left-0"} top-full z-40 mt-1 w-full overflow-hidden rounded-md border border-line bg-surface shadow-[0_8px_24px_hsl(var(--ink)/0.14)] sm:w-80 sm:min-w-full`}>
          {/* The search row shows focus with its own underline; the global 2px ring + offset is too heavy inside a popup. */}
          <div className="flex items-center gap-2 border-b border-line px-3 focus-within:border-ink">
            <svg aria-hidden="true" viewBox="0 0 24 24" className="size-4 shrink-0 text-ink-soft"><circle cx="11" cy="11" r="6.5" fill="none" stroke="currentColor" strokeWidth="2" /><path d="m16 16 4.5 4.5" stroke="currentColor" strokeWidth="2" strokeLinecap="square" /></svg>
            <input
              ref={input}
              id={ids.input}
              role="combobox"
              aria-invalid={invalid || undefined}
              aria-describedby={describedBy}
              aria-expanded="true"
              aria-controls={ids.list}
              aria-autocomplete="list"
              aria-activedescendant={filtered.length ? optionId(active) : undefined}
              aria-label={`Search ${label.toLowerCase()}`}
              className="min-h-10 w-full bg-transparent text-base outline-hidden placeholder:text-ink-soft focus-visible:ring-0 focus-visible:ring-offset-0 sm:text-sm"
              placeholder={searchPlaceholder}
              value={query}
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              onChange={(e) => {
                setQuery(e.currentTarget.value);
                setActive(0);
              }}
              onKeyDown={onKey}
            />
          </div>
          <ul ref={list} id={ids.list} role="listbox" aria-label={label} className="max-h-[min(20rem,50dvh)] overflow-y-auto overscroll-contain py-1">
            {filtered.length ? rows : <li className="px-3 py-3 text-sm text-ink-soft">{emptyText}</li>}
          </ul>
          <p className="sr-only" aria-live="polite">{filtered.length} {filtered.length === 1 ? "result" : "results"}</p>
        </div>
      )}
    </div>
  );
}
