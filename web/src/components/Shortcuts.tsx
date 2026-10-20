import { useNavigate } from "@tanstack/react-router";
import { Fragment, useEffect } from "react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { isMac, shouldIgnore, useRegisteredPageShortcuts, type ShortcutInfo } from "@/lib/shortcuts";

export const GLOBAL_SHORTCUTS: { group: string; items: ShortcutInfo[] }[] = [
  {
    group: "anywhere",
    items: [
      { keys: ["mod", "k"], label: "Open the command palette" },
      { keys: ["/"], label: "Search (jobs list) or open the palette" },
      { keys: ["?"], label: "Show keyboard shortcuts" },
      { keys: ["n"], label: "New job" },
    ],
  },
  {
    group: "go to",
    items: [
      { keys: ["g", "d"], label: "Dashboard" },
      { keys: ["g", "j"], label: "Jobs" },
      { keys: ["g", "a"], label: "Alerts" },
      { keys: ["g", "s"], label: "Settings" },
    ],
  },
];

const GO_TO: Record<string, string> = { d: "/", j: "/jobs", a: "/alerts", s: "/settings" };

export function Kbd({ k }: { k: string }) {
  const text = k === "mod" ? (isMac ? "⌘" : "Ctrl") : k;
  const name = k === "mod" ? (isMac ? "Command" : "Control") : undefined;
  return (
    <kbd className="inline-flex h-6 min-w-6 items-center justify-center rounded-sm border border-ink/30 bg-sunken px-1.5 font-mono text-xs font-bold text-ink" title={name}>
      {text}
    </kbd>
  );
}

function Keys({ keys }: { keys: string[] }) {
  // Two-key chords ("g" then "d") read "then"; modifier combos ("⌘ K") are pressed together.
  const together = keys[0] === "mod";
  return (
    <span className="flex shrink-0 items-center gap-1 text-xs text-ink-soft">
      {keys.map((k, i) => (
        <Fragment key={i}>
          {i > 0 ? <span>{together ? "+" : "then"}</span> : null}
          <Kbd k={k} />
        </Fragment>
      ))}
    </span>
  );
}

/**
 * Global keys: ⌘K/Ctrl+K opens the palette (even while typing), `?` the help,
 * `/` search, `n` a new job, and `g` then a letter jumps between sections.
 */
export function useGlobalShortcuts({ openPalette, openHelp }: { openPalette: () => void; openHelp: () => void }) {
  const navigate = useNavigate();
  useEffect(() => {
    let pending = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        openPalette();
        return;
      }
      if (shouldIgnore(e)) return;
      if (pending) {
        pending = false;
        clearTimeout(timer);
        const to = GO_TO[e.key];
        if (to) {
          e.preventDefault();
          void navigate({ to });
        }
        return;
      }
      if (e.key === "g") {
        pending = true;
        timer = setTimeout(() => (pending = false), 1200);
      } else if (e.key === "?" || (e.key === "/" && e.shiftKey)) {
        e.preventDefault();
        openHelp();
      } else if (e.key === "/") {
        e.preventDefault();
        const search = document.querySelector<HTMLInputElement>("[data-shortcut-search]");
        if (search) search.focus();
        else openPalette();
      } else if (e.key === "n") {
        e.preventDefault();
        void navigate({ to: "/jobs/new" });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      clearTimeout(timer);
    };
  }, [navigate, openPalette, openHelp]);
}

export function ShortcutsDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const page = useRegisteredPageShortcuts();
  const groups = page.length > 0 ? [...GLOBAL_SHORTCUTS, { group: "this page", items: page }] : GLOBAL_SHORTCUTS;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl gap-4">
        <DialogHeader>
          <DialogTitle>keyboard shortcuts</DialogTitle>
          <DialogDescription>Single keys work anywhere except while you type in a field.</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4 sm:grid-cols-2">
          {groups.map((g) => (
            <section key={g.group} aria-label={g.group} className="grid content-start gap-1">
              <h3 className="section-title mb-1">{g.group}</h3>
              <ul className="grid">
                {g.items.map((s) => (
                  <li key={s.keys.join("+")} className="flex items-center justify-between gap-3 border-b border-line py-1.5 text-sm last:border-0">
                    <span>{s.label}</span>
                    <Keys keys={s.keys} />
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
