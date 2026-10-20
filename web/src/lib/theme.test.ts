import { afterEach, describe, expect, it, vi } from "vitest";
import { applyThemePreference, followSystemTheme, themePreference } from "./theme";

function mockSystem(dark: boolean) {
  const listeners = new Set<() => void>();
  const query = {
    matches: dark,
    addEventListener: (_: string, fn: () => void) => listeners.add(fn),
    removeEventListener: (_: string, fn: () => void) => listeners.delete(fn),
  };
  vi.stubGlobal("matchMedia", () => query);
  return {
    set(next: boolean) {
      query.matches = next;
      listeners.forEach((fn) => fn());
    },
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
  document.querySelector('meta[name="cronpad-cookie-domain"]')?.remove();
  document.cookie = "cronpad-theme=; Path=/; Max-Age=0";
  document.documentElement.classList.remove("dark");
});

describe("theme", () => {
  it("defaults to system and stores only explicit choices", () => {
    expect(themePreference()).toBe("system");
    applyThemePreference("dark");
    expect(localStorage.getItem("cronpad-theme")).toBe("dark");
    expect(document.documentElement).toHaveClass("dark");
    applyThemePreference("system");
    expect(localStorage.getItem("cronpad-theme")).toBeNull();
    expect(themePreference()).toBe("system");
  });

  it("follows the OS scheme only while the preference is system", () => {
    const system = mockSystem(false);
    const stop = followSystemTheme();
    applyThemePreference("system");
    expect(document.documentElement).not.toHaveClass("dark");
    system.set(true);
    expect(document.documentElement).toHaveClass("dark");
    applyThemePreference("light");
    system.set(true);
    expect(document.documentElement).not.toHaveClass("dark");
    stop();
  });

  it("shares the choice through a cookie when a cookie domain is configured", () => {
    const writes: string[] = [];
    const jar = new Map<string, string>();
    vi.spyOn(document, "cookie", "get").mockImplementation(() => [...jar].map(([k, v]) => `${k}=${v}`).join("; "));
    vi.spyOn(document, "cookie", "set").mockImplementation((value: string) => {
      writes.push(value);
      const [pair = ""] = value.split(";");
      const [k = "", v = ""] = pair.split("=");
      jar.set(k, v);
    });
    const meta = Object.assign(document.createElement("meta"), { name: "cronpad-cookie-domain", content: "example.com" });
    document.head.append(meta);

    applyThemePreference("dark");
    expect(writes.at(-1)).toMatch(/^cronpad-theme=dark; Domain=example.com; Path=\/; Max-Age=31536000; SameSite=Lax/);
    // "system" is stored explicitly so it overrides a stale localStorage value.
    applyThemePreference("system");
    expect(writes.at(-1)).toMatch(/^cronpad-theme=system; Domain=example.com/);
    localStorage.setItem("cronpad-theme", "light");
    expect(themePreference()).toBe("system");
    // A choice made on the public site wins over this origin's localStorage.
    jar.set("cronpad-theme", "dark");
    expect(themePreference()).toBe("dark");
    vi.restoreAllMocks();
  });

  it("ignores the cookie and writes none without a cookie domain", () => {
    const set = vi.spyOn(document, "cookie", "set");
    applyThemePreference("dark");
    expect(set).not.toHaveBeenCalled();
    vi.restoreAllMocks();
  });
});
