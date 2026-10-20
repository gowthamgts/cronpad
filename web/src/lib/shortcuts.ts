import { useEffect, useRef, useSyncExternalStore } from "react";

export interface ShortcutInfo {
  /** Keys pressed in order, e.g. ["g", "j"]; "mod" means ⌘ on macOS and Ctrl elsewhere. */
  keys: string[];
  label: string;
}

export interface PageShortcut extends ShortcutInfo {
  run: () => void;
}

export const isMac = typeof navigator !== "undefined" && /mac|iphone|ipad/i.test(navigator.platform || navigator.userAgent);

/** Single-key shortcuts stay out of the way while someone types or a dialog is open. */
export function shouldIgnore(e: KeyboardEvent): boolean {
  if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return true;
  const target = e.target as HTMLElement | null;
  if (target && (target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) || target.closest("[role=listbox],[role=combobox]"))) return true;
  return document.querySelector("[role=dialog],[role=alertdialog]") !== null;
}

// The current page's shortcuts, so the help dialog can list them next to the global ones.
let pageShortcuts: ShortcutInfo[] = [];
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};
const setPageShortcuts = (next: ShortcutInfo[]) => {
  pageShortcuts = next;
  listeners.forEach((l) => l());
};

export function useRegisteredPageShortcuts(): ShortcutInfo[] {
  return useSyncExternalStore(subscribe, () => pageShortcuts);
}

/** Binds single-key shortcuts for the page that calls it and lists them in the `?` dialog. */
export function usePageShortcuts(shortcuts: PageShortcut[]) {
  const latest = useRef(shortcuts);
  useEffect(() => {
    latest.current = shortcuts;
  });
  const signature = shortcuts.map((s) => `${s.keys.join("+")}:${s.label}`).join("|");
  useEffect(() => {
    setPageShortcuts(latest.current.map(({ keys, label }) => ({ keys, label })));
    const onKey = (e: KeyboardEvent) => {
      if (shouldIgnore(e)) return;
      const match = latest.current.find((s) => s.keys.length === 1 && s.keys[0] === e.key);
      if (!match) return;
      e.preventDefault();
      match.run();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      setPageShortcuts([]);
    };
  }, [signature]);
}
