export type ThemePreference = "light" | "dark" | "system";

// Shared with the public site and public/theme-init.js; an absent key means "system".
const storageKey = "cronpad-theme";
export const themeChangeEvent = "cronpad-theme-change";
let memoryPreference: ThemePreference = "system";

/*
 * In hosted mode (app.cronpad.com) the server puts the shared parent domain in <meta name="cronpad-cookie-domain"> and the choice is
 * also kept in a `cronpad-theme` cookie on that domain, which the public site reads and writes too. The
 * cookie always holds a value ("system" included) so it can override this origin's localStorage.
 */
function cookieDomain(): string {
  return document.querySelector<HTMLMetaElement>('meta[name="cronpad-cookie-domain"]')?.content ?? "";
}

function parse(value: string | null | undefined): ThemePreference | null {
  return value === "light" || value === "dark" || value === "system" ? value : null;
}

function readCookie(): ThemePreference | null {
  return parse(/(?:^|;\s*)cronpad-theme=([^;]*)/.exec(document.cookie)?.[1]);
}

function writeCookie(preference: ThemePreference) {
  const domain = cookieDomain();
  if (!domain) return;
  const secure = location.protocol === "https:" ? "; Secure" : "";
  document.cookie = `${storageKey}=${preference}; Domain=${domain}; Path=/; Max-Age=31536000; SameSite=Lax${secure}`;
}

function systemQuery(): MediaQueryList | null {
  return typeof window.matchMedia === "function" ? window.matchMedia("(prefers-color-scheme: dark)") : null;
}

function render(preference: ThemePreference) {
  const dark = preference === "system" ? (systemQuery()?.matches ?? false) : preference === "dark";
  document.documentElement.classList.toggle("dark", dark);
}

export function themePreference(): ThemePreference {
  const shared = cookieDomain() ? readCookie() : null;
  if (shared) return shared;
  try {
    const saved = localStorage.getItem(storageKey);
    return saved === "light" || saved === "dark" ? saved : "system";
  } catch {
    return memoryPreference;
  }
}

export function applyThemePreference(preference: ThemePreference) {
  memoryPreference = preference;
  try {
    if (preference === "system") localStorage.removeItem(storageKey);
    else localStorage.setItem(storageKey, preference);
  } catch {
    /* storage unavailable: the choice lasts for this page view */
  }
  writeCookie(preference);
  render(preference);
  // Lets every theme control (header toggle, command palette) show the same choice.
  window.dispatchEvent(new CustomEvent(themeChangeEvent, { detail: preference }));
}

/** Re-renders when the OS scheme changes while the preference is "system". */
export function followSystemTheme(): () => void {
  const query = systemQuery();
  if (!query) return () => {};
  const onChange = () => {
    if (themePreference() === "system") render("system");
  };
  query.addEventListener("change", onChange);
  return () => query.removeEventListener("change", onChange);
}

/** Picks up a theme changed on the public site (shared cookie) when this tab is shown again. */
export function followSharedTheme(): () => void {
  const onVisible = () => {
    if (document.visibilityState !== "visible" || !cookieDomain()) return;
    const preference = themePreference();
    render(preference);
    window.dispatchEvent(new CustomEvent(themeChangeEvent, { detail: preference }));
  };
  document.addEventListener("visibilitychange", onVisible);
  return () => document.removeEventListener("visibilitychange", onVisible);
}
