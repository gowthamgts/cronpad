import { Monitor, Moon, Sun } from "lucide-react";
import { useEffect, useState } from "react";
import { applyThemePreference, themeChangeEvent, themePreference, type ThemePreference } from "@/lib/theme";

// Same cycle as the public site: system → light → dark. "system" clears the saved choice and follows the OS.
const nextMode: Record<ThemePreference, ThemePreference> = { system: "light", light: "dark", dark: "system" };
const names: Record<ThemePreference, string> = { system: "System", light: "Light", dark: "Dark" };
const icons: Record<ThemePreference, typeof Sun> = { system: Monitor, light: Sun, dark: Moon };

/** One icon button that shows the current theme and cycles to the next, like the site header. */
export function ThemeToggle() {
  const [preference, setPreference] = useState<ThemePreference>(themePreference);
  useEffect(() => {
    const onChange = (e: Event) => setPreference((e as CustomEvent<ThemePreference>).detail);
    window.addEventListener(themeChangeEvent, onChange);
    return () => window.removeEventListener(themeChangeEvent, onChange);
  }, []);
  const next = nextMode[preference];
  const Icon = icons[preference];
  return (
    <button
      type="button"
      title={`Theme: ${names[preference]}`}
      aria-label={`Theme: ${names[preference]}. Switch to ${names[next]}`}
      onClick={() => {
        applyThemePreference(next);
        setPreference(next);
      }}
      className="inline-flex size-11 shrink-0 items-center justify-center rounded text-ink-soft hover:bg-sunken hover:text-ink sm:size-8"
    >
      <Icon className="size-[18px]" aria-hidden />
    </button>
  );
}
