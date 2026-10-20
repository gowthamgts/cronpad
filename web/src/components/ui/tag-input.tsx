import { useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { cn } from "@/lib/utils";

export interface TagOption {
  value: string;
  /** Short description shown beside the value and matched while typing, e.g. "Not Found". */
  label?: string;
  /** Not offered while this other value is selected, e.g. "503" under "5xx". */
  coveredBy?: string;
  /** Can't be combined: picking it replaces the others, and while it is selected nothing else is offered. */
  exclusive?: boolean;
}

interface TagInputProps {
  id?: string;
  value: string[];
  onChange: (tags: string[]) => void;
  placeholder?: string;
  /** When set, only these values can be added: the list autocompletes as the user types. Order is popularity: the first ones show before anything is typed. */
  options?: TagOption[];
  /** Values that came from the server's defaults: their chips carry a small "default" mark. They are ordinary chips otherwise. */
  defaults?: string[];
  /** Cap on suggestions shown at once; the rest are reached by typing. */
  maxSuggestions?: number;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
}

const norm = (s: string) => s.toLowerCase().trim();

/**
 * Text input that turns each entry into a removable chip: Enter, comma or Tab adds, Backspace on empty removes the last.
 * With `options` it becomes a picker: suggestions are filtered as the user types and free text is not accepted.
 */
export function TagInput({ id, value, onChange, placeholder, options, defaults, maxSuggestions = 8, ...aria }: TagInputProps) {
  const [draft, setDraft] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [armed, setArmed] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const listId = useId();

  // What may still be added: not selected, not covered by a selected class, and nothing at all next to an exclusive value.
  const available = useMemo(() => {
    if (!options) return [];
    if (options.some((o) => o.exclusive && value.includes(o.value))) return [];
    return options.filter((o) => !value.includes(o.value) && !(o.coveredBy && value.includes(o.coveredBy)));
  }, [options, value]);
  const matches = useMemo(() => {
    const q = norm(draft);
    if (!q) return available;
    const starts = available.filter((o) => norm(o.value).startsWith(q));
    const rest = available.filter((o) => !norm(o.value).startsWith(q) && norm(`${o.value} ${o.label ?? ""}`).includes(q));
    return [...starts, ...rest];
  }, [available, draft]);
  const lockedBy = options?.find((o) => o.exclusive && value.includes(o.value));
  const shown = matches.slice(0, maxSuggestions);
  const hidden = matches.length - shown.length;
  const listOpen = Boolean(options) && open;

  const add = (tags: string[]) => {
    setDraft("");
    setActive(0);
    setArmed(false);
    const picked = tags.map((t) => options?.find((o) => o.value === t));
    // An exclusive pick replaces everything; a class like "5xx" absorbs the codes it covers.
    const base = picked.some((o) => o?.exclusive) ? [] : value.filter((v) => !picked.some((o) => o && options?.find((x) => x.value === v)?.coveredBy === o.value));
    const next = [...new Set([...base, ...tags])];
    if (next.length !== value.length || next.some((t, i) => t !== value[i])) onChange(next);
  };

  /** Free text commits as typed; in picker mode only an exact option value counts. */
  const commitDraft = () => {
    const parts = draft.split(",").map((t) => t.trim()).filter(Boolean);
    if (!options) return add(parts);
    add(parts.flatMap((p) => available.find((o) => norm(o.value) === norm(p))?.value ?? []));
  };

  /** Picker mode: the highlighted suggestion. With nothing typed it only counts once the user arrowed to it. */
  const commitPick = () => {
    const pick = shown[active];
    if (pick && (draft.trim() !== "" || armed)) return add([pick.value]);
    commitDraft();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const hasDraft = draft.trim() !== "";
    if (options && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      setOpen(true);
      setArmed(true);
      if (shown.length > 0) setActive((a) => (a + (e.key === "ArrowDown" ? 1 : -1) + shown.length) % shown.length);
    } else if (e.key === "Escape" && listOpen) {
      e.preventDefault();
      setOpen(false);
    } else if (e.key === "Enter" || e.key === "," || (e.key === "Tab" && !e.shiftKey && hasDraft)) {
      // Enter must not submit the form; Tab keeps focus so more tags can follow.
      e.preventDefault();
      if (options) commitPick();
      else commitDraft();
    } else if (e.key === "Backspace" && draft === "" && value.length > 0) {
      onChange(value.slice(0, -1));
    }
  };

  const remove = (tag: string) => {
    onChange(value.filter((t) => t !== tag));
    input.current?.focus();
  };

  return (
    <div className="relative">
      {/* Clicking the padding focuses the input; keyboard users already reach the input directly. */}
      {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions */}
      <div
        onClick={() => input.current?.focus()}
        aria-invalid={aria["aria-invalid"]}
        className="flex min-h-11 sm:min-h-9 w-full flex-wrap items-center gap-1.5 rounded border border-ink/40 bg-paper px-2 py-1 hover:border-ink/70 focus-within:border-focus focus-within:ring-1 focus-within:ring-focus aria-[invalid=true]:border-danger aria-[invalid=true]:bg-bad-soft"
      >
        {value.map((tag) => (
          <span key={tag} className="inline-flex items-center gap-1 rounded border border-line bg-sunken py-0.5 pl-2 pr-1 font-mono text-xs text-ink">
            {tag}
            {defaults?.includes(tag) ? <span className="rounded-sm bg-ink/10 px-1 text-[10px] uppercase leading-4 tracking-wide text-ink-soft">default</span> : null}
            <button
              type="button"
              aria-label={`Remove tag ${tag}`}
              onClick={(e) => {
                e.stopPropagation();
                remove(tag);
              }}
              className="inline-flex size-4 items-center justify-center rounded-sm text-ink-soft hover:bg-ink/10 hover:text-ink focus-visible:outline-2 focus-visible:outline-focus"
            >
              <svg viewBox="0 0 12 12" className="size-2.5" aria-hidden="true"><path d="M2 2l8 8M10 2l-8 8" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" /></svg>
            </button>
          </span>
        ))}
        <input
          ref={input}
          id={id}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
            setActive(0);
            setArmed(false);
            setOpen(true);
          }}
          onKeyDown={onKeyDown}
          onFocus={() => setOpen(true)}
          onBlur={() => {
            setOpen(false);
            commitDraft();
          }}
          placeholder={value.length === 0 ? placeholder : undefined}
          role={options ? "combobox" : undefined}
          aria-expanded={options ? listOpen && shown.length > 0 : undefined}
          aria-controls={options ? listId : undefined}
          aria-autocomplete={options ? "list" : undefined}
          aria-activedescendant={listOpen && shown[active] ? `${listId}-${active}` : undefined}
          aria-describedby={aria["aria-describedby"]}
          autoComplete="off"
          className={cn("min-w-24 flex-1 bg-transparent py-1 font-mono text-base outline-hidden placeholder:text-ink-soft sm:text-sm")}
        />
      </div>
      {listOpen && (shown.length > 0 || draft.trim() !== "" || lockedBy) ? (
        <ul
          id={listId}
          role="listbox"
          aria-label="Suggestions"
          className="absolute left-0 top-full z-40 mt-1 max-h-64 w-full overflow-auto rounded-md border border-line bg-surface py-1 shadow-[0_8px_24px_hsl(var(--ink)/0.14)] sm:min-w-56"
        >
          {shown.map((o, i) => (
            <li
              key={o.value}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={i === active}
              // Keep focus in the input so blur doesn't fire before the pick.
              onMouseDown={(e) => {
                e.preventDefault();
                add([o.value]);
              }}
              onMouseMove={() => {
                setActive(i);
                setArmed(true);
              }}
              className={cn("flex cursor-pointer items-baseline gap-2 px-3 py-1.5 font-mono text-sm", i === active && "bg-accent text-accent-ink")}
            >
              <span className="font-bold">{o.value}</span>
              {o.label ? <span className={cn("truncate text-xs", i === active ? "text-accent-ink" : "text-ink-soft")}>{o.label}</span> : null}
            </li>
          ))}
          {shown.length === 0 ? <li role="presentation" className="px-3 py-1.5 text-sm text-ink-soft">{lockedBy ? `${lockedBy.value} can't be combined with other options. Remove it to pick specific ones.` : "No matching option"}</li> : null}
          {hidden > 0 ? <li role="presentation" className="px-3 py-1 text-xs text-ink-soft">{hidden} more — keep typing to narrow</li> : null}
        </ul>
      ) : null}
    </div>
  );
}
