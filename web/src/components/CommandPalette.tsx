import * as DialogPrimitive from "@radix-ui/react-dialog";
import { useNavigate } from "@tanstack/react-router";
import { ArrowRight, Bell, Keyboard, LayoutDashboard, ListChecks, LogOut, Monitor, Moon, Plus, Search, Settings, Sun, Workflow, type LucideIcon } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useJobs } from "@/api/hooks";
import { StatusPill } from "@/components/StatusPill";
import { Kbd } from "@/components/Shortcuts";
import { applyThemePreference } from "@/lib/theme";

interface Command {
  id: string;
  group: "jobs" | "go to" | "actions";
  label: string;
  hint?: React.ReactNode;
  keywords?: string;
  icon: LucideIcon;
  run: () => void;
}

const normalize = (s: string) => s.toLowerCase().replace(/[^a-z0-9+:.\- ]/g, " ");
// Words that pick a job's tab rather than its name: "nightly runs" searches jobs for "nightly".
const TAB_WORDS = new Set(["runs", "audit", "edit"]);
const jobSearchText = (query: string) => query.trim().split(/\s+/).filter((w) => w && !TAB_WORDS.has(w.toLowerCase())).join(" ");

/**
 * ⌘K / Ctrl+K: a small floating command window. Type to filter pages, actions
 * and jobs (searched on the server by name); arrows move, Enter runs, Escape closes.
 */
export function CommandPalette({ open, onOpenChange, onSignOut, onShowShortcuts }: { open: boolean; onOpenChange: (open: boolean) => void; onSignOut: () => void; onShowShortcuts: () => void }) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-ink/30" />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className="box fixed left-1/2 top-[12vh] z-50 grid w-[calc(100%-2rem)] max-w-xl -translate-x-1/2 overflow-hidden shadow-[0_16px_48px_hsl(var(--ink)/0.22)]"
        >
          <DialogPrimitive.Title className="sr-only">Command palette</DialogPrimitive.Title>
          {/* Remounting on open starts each visit with an empty query. */}
          {open ? <Palette close={() => onOpenChange(false)} onSignOut={onSignOut} onShowShortcuts={onShowShortcuts} /> : null}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

function Palette({ close, onSignOut, onShowShortcuts }: { close: () => void; onSignOut: () => void; onShowShortcuts: () => void }) {
  const navigate = useNavigate();
  const uid = useId().replace(/:/g, "");
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [active, setActive] = useState(0);
  const list = useRef<HTMLUListElement>(null);

  useEffect(() => {
    const t = setTimeout(() => setDebounced(jobSearchText(query)), 150);
    return () => clearTimeout(t);
  }, [query]);
  const jobs = useJobs(debounced ? { q: debounced } : {}, 6);

  const commands = useMemo<Command[]>(() => {
    const go = (to: string, search?: Record<string, string>) => () => void navigate({ to, search: search ?? {} });
    const statics: Command[] = [
      { id: "new-job", group: "actions", label: "New job", icon: Plus, keywords: "create add schedule", hint: <Kbd k="n" />, run: go("/jobs/new") },
      { id: "dash", group: "go to", label: "Dashboard", icon: LayoutDashboard, keywords: "home overview stats", run: go("/") },
      { id: "dash-7d", group: "go to", label: "Dashboard · last 7 days", icon: LayoutDashboard, keywords: "week range", run: go("/", { range: "7d" }) },
      { id: "dash-30d", group: "go to", label: "Dashboard · last 30 days", icon: LayoutDashboard, keywords: "month range", run: go("/", { range: "30d" }) },
      { id: "jobs", group: "go to", label: "Jobs", icon: ListChecks, keywords: "list schedules", run: go("/jobs") },
      ...(["active", "paused", "disabled", "completed"] as const).map((state): Command => ({ id: `jobs-${state}`, group: "go to", label: `Jobs · ${state}`, icon: ListChecks, keywords: `filter state${state === "disabled" ? " failing auto" : ""}`, run: go("/jobs", { state }) })),
      { id: "alerts", group: "go to", label: "Alerts · channels", icon: Bell, keywords: "notifications webhook email slack discord", run: go("/alerts") },
      { id: "alerts-rules", group: "go to", label: "Alerts · rules", icon: Bell, keywords: "notifications events conditions", run: go("/alerts", { tab: "rules" }) },
      { id: "settings", group: "go to", label: "Settings · API keys", icon: Settings, keywords: "tokens access cli", run: go("/settings") },
      { id: "settings-secrets", group: "go to", label: "Settings · secrets", icon: Settings, keywords: "env variables tokens", run: go("/settings", { tab: "secrets" }) },
      { id: "settings-workspace", group: "go to", label: "Settings · workspace", icon: Settings, keywords: "name slug usage monthly limit", run: go("/settings", { tab: "workspace" }) },
      { id: "theme-light", group: "actions", label: "Theme: light", icon: Sun, keywords: "appearance mode", run: () => applyThemePreference("light") },
      { id: "theme-dark", group: "actions", label: "Theme: dark", icon: Moon, keywords: "appearance mode night", run: () => applyThemePreference("dark") },
      { id: "theme-system", group: "actions", label: "Theme: system", icon: Monitor, keywords: "appearance mode auto", run: () => applyThemePreference("system") },
      { id: "shortcuts", group: "actions", label: "Keyboard shortcuts", icon: Keyboard, keywords: "help keys hotkeys", hint: <Kbd k="?" />, run: onShowShortcuts },
      { id: "sign-out", group: "actions", label: "Sign out", icon: LogOut, keywords: "logout log out", run: onSignOut },
    ];
    const terms = normalize(query).split(" ").filter(Boolean);
    const matches = (c: Command) => terms.every((t) => normalize(`${c.label} ${c.keywords ?? ""} ${c.group}`).includes(t));
    // Only list jobs fetched for exactly what's typed now, so a fast Enter never opens a stale match.
    const jobsCurrent = !jobs.isPlaceholderData && debounced === jobSearchText(query);
    const jobCommands: Command[] = (jobsCurrent ? (jobs.data?.pages.flatMap((p) => p.data) ?? []) : []).flatMap((j) => {
      const open = (tab?: "runs" | "audit") => () => void navigate({ to: "/jobs/$jobId", params: { jobId: j.id }, search: tab ? { tab } : {} });
      const main: Command = { id: `job-${j.id}`, group: "jobs", label: j.name, icon: Workflow, hint: <StatusPill status={j.state} />, run: open() };
      // Each job tab gets its own entry once you search, so "nightly runs" lands on the runs tab.
      if (!terms.length) return [main];
      return [
        main,
        { id: `job-${j.id}-runs`, group: "jobs", label: `${j.name} · runs`, icon: Workflow, run: open("runs") },
        { id: `job-${j.id}-audit`, group: "jobs", label: `${j.name} · audit`, icon: Workflow, run: open("audit") },
        { id: `job-${j.id}-edit`, group: "jobs", label: `${j.name} · edit`, icon: Workflow, run: () => void navigate({ to: "/jobs/$jobId/edit", params: { jobId: j.id } }) },
      ];
    });
    // The server matched job names; tab words ("runs", "audit", "edit") are matched here.
    const filteredStatics = statics.filter(matches);
    const tabTerms = terms.filter((t) => TAB_WORDS.has(t));
    const filteredJobs = jobCommands.filter((c) => tabTerms.every((t) => normalize(c.label).includes(t)));
    return terms.length ? [...filteredJobs, ...filteredStatics] : [...filteredStatics.filter((c) => c.group !== "actions" || c.id === "new-job"), ...jobCommands];
  }, [query, debounced, jobs.data, jobs.isPlaceholderData, navigate, onSignOut, onShowShortcuts]);

  const current = Math.min(active, Math.max(commands.length - 1, 0));
  useEffect(() => {
    list.current?.querySelector(`[data-index="${current}"]`)?.scrollIntoView({ block: "nearest" });
  }, [current]);

  function runCommand(c: Command | undefined) {
    if (!c) return;
    close();
    c.run();
  }

  function onKeyDown(e: React.KeyboardEvent) {
    const last = commands.length - 1;
    const moves: Record<string, number> = { ArrowDown: Math.min(current + 1, last), ArrowUp: Math.max(current - 1, 0), Home: 0, End: last, PageDown: Math.min(current + 5, last), PageUp: Math.max(current - 5, 0) };
    if (e.key in moves) {
      e.preventDefault();
      setActive(moves[e.key]!);
    } else if (e.key === "Enter") {
      e.preventDefault();
      runCommand(commands[current]);
    }
  }

  const groups = ["jobs", "go to", "actions"] as const;
  let index = -1;
  const listId = `${uid}-list`;
  return (
    <div className="grid">
      <div className="flex items-center gap-2 border-b border-line px-3 focus-within:border-ink">
        <Search aria-hidden="true" className="size-4 shrink-0 text-ink-soft" />
        <input
          role="combobox"
          aria-expanded="true"
          aria-controls={listId}
          aria-autocomplete="list"
          aria-label="Type a command or search jobs"
          aria-activedescendant={commands.length ? `${uid}-opt-${current}` : undefined}
          placeholder="Type a command or search jobs…"
          className="min-h-12 w-full bg-transparent sm:min-h-11 text-base outline-hidden placeholder:text-ink-soft focus-visible:ring-0 focus-visible:ring-offset-0 sm:text-sm"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
          }}
          onKeyDown={onKeyDown}
        />
      </div>
      <ul ref={list} id={listId} role="listbox" aria-label="Commands" className="max-h-[min(24rem,60dvh)] overflow-y-auto overscroll-contain py-1">
        {groups.map((group) => {
          const items = commands.filter((c) => c.group === group);
          if (!items.length) return null;
          return (
            <li key={group} role="presentation">
              <p aria-hidden="true" className="px-3 pb-1 pt-2 text-[11px] font-bold uppercase tracking-widest text-ink-soft">{group}</p>
              <ul role="group" aria-label={group}>
                {items.map((c) => {
                  index += 1;
                  const i = index;
                  const isActive = i === current;
                  const Icon = c.icon;
                  return (
                    <li
                      key={c.id}
                      id={`${uid}-opt-${i}`}
                      data-index={i}
                      role="option"
                      aria-selected={isActive}
                      onMouseMove={() => setActive(i)}
                      onMouseDown={(e) => e.preventDefault()}
                      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); runCommand(c); } }}
                      onClick={() => runCommand(c)}
                      className={`mx-1 flex min-h-10 cursor-pointer items-center gap-3 rounded-sm px-2 text-sm sm:min-h-9 ${isActive ? "bg-accent text-accent-ink" : ""}`}
                    >
                      <Icon aria-hidden="true" className="size-4 shrink-0" />
                      <span className="min-w-0 flex-1 truncate">{c.label}</span>
                      {c.hint ? <span className="shrink-0">{c.hint}</span> : null}
                      {isActive ? <ArrowRight aria-hidden="true" className="size-4 shrink-0" /> : null}
                    </li>
                  );
                })}
              </ul>
            </li>
          );
        })}
        {commands.length === 0 ? <li className="px-3 py-6 text-center text-sm text-ink-soft">{jobs.isFetching ? "Searching…" : "No matching commands or jobs."}</li> : null}
      </ul>
      <p className="sr-only" aria-live="polite">{commands.length} {commands.length === 1 ? "result" : "results"}</p>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-line bg-sunken/60 px-3 py-2 text-xs text-ink-soft">
        <span className="flex items-center gap-1"><Kbd k="↑" /><Kbd k="↓" /> move</span>
        <span className="flex items-center gap-1"><Kbd k="↵" /> open</span>
        <span className="flex items-center gap-1"><Kbd k="esc" /> close</span>
        <span className="ml-auto">type “shortcuts” for the full list</span>
      </div>
    </div>
  );
}
