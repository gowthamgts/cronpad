import { SuspensionBanner } from "@/components/SuspensionBanner";
import { useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate } from "@tanstack/react-router";
import { LogOut, Menu, Search, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { getWorkspace, logout, setWorkspace } from "@/api/client";
import { useMe } from "@/api/hooks";
import { ThemeToggle } from "@/components/ThemeToggle";
import { Button } from "@/components/ui/button";
import Combobox from "@/components/ui/combobox";
import { Logo } from "@/components/Logo";
import { CommandPalette } from "@/components/CommandPalette";
import { Kbd, ShortcutsDialog, useGlobalShortcuts } from "@/components/Shortcuts";

const NAV = [
  { to: "/", label: "Dashboard" },
  { to: "/jobs", label: "Jobs" },
  { to: "/alerts", label: "Alerts" },
  { to: "/settings", label: "Settings" },
] as const;

export function AppShell() {
  const me = useMe();
  const client = useQueryClient();
  const navigate = useNavigate();
  const [menuOpen, setMenuOpen] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const openPalette = useCallback(() => {
    setHelpOpen(false);
    setPaletteOpen(true);
  }, []);
  const openHelp = useCallback(() => setHelpOpen(true), []);
  useGlobalShortcuts({ openPalette, openHelp });
  const menuButton = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!menuOpen) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) {
        setMenuOpen(false);
        menuButton.current?.focus();
      }
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [menuOpen]);

  const workspaces = me.data?.workspaces ?? [];
  const active = getWorkspace() ?? workspaces[0]?.id ?? "";

  async function signOut() {
    await logout().catch(() => undefined);
    setWorkspace(null);
    client.clear();
    await navigate({ to: "/login", search: {} });
  }

  const workspaceControl = (id: string) => workspaces.length > 1 ? (
    <Combobox id={id} hideLabel label="Workspace" className="min-w-0 w-full lg:w-40" options={workspaces.map((w) => ({ value: w.id, label: w.name }))} value={active} onChange={(value) => {
      setWorkspace(value);
      client.clear();
      void navigate({ to: "/" });
    }} />
  ) : <span className="truncate text-xs text-ink-soft">{workspaces[0]?.name}</span>;

  const nav = NAV.map((item) => (
    <Link key={item.to} to={item.to} activeOptions={{ exact: item.to === "/" }}
      className="flex h-12 items-center rounded-sm px-2.5 text-sm lowercase lg:h-8"
      inactiveProps={{ className: "text-ink hover:bg-sunken" }}
      activeProps={{ className: "bg-accent text-accent-ink font-bold", "aria-current": "page" }}
      onClick={() => setMenuOpen(false)}>
      {item.label}
    </Link>
  ));

  return (
    <div className="min-h-screen">
      <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-[60] focus:border focus:border-ink focus:bg-surface focus:px-4 focus:py-3">
        Skip to content
      </a>
      <header className="sticky top-0 z-40 border-b border-line bg-surface">
        <div className="mx-auto flex h-14 max-w-7xl items-center gap-2 px-4 lg:h-12 lg:gap-5">
          <Link to="/" className="flex shrink-0 items-center gap-2 text-lg font-bold tracking-tight" aria-label="cronpad home">
            <Logo className="size-7" />cronpad
          </Link>
          <nav aria-label="Main" className="hidden items-center gap-1 lg:flex">{nav}</nav>
          <div className="ml-auto hidden min-w-0 items-center lg:flex">
            {workspaceControl("workspace-desktop")}
          </div>
          <div className="ml-auto flex shrink-0 items-center gap-0.5 sm:gap-1 lg:ml-0 lg:gap-2">
            {/* A bare icon on phones; the bordered search pill (as on the site) from lg up. */}
            <button type="button" onClick={openPalette} title="Search jobs, pages and commands" aria-keyshortcuts="Meta+K Control+K"
              className="inline-flex size-11 items-center justify-center gap-2 rounded text-ink-soft hover:bg-sunken hover:text-ink sm:size-8 lg:h-8 lg:w-auto lg:border lg:border-ink/40 lg:pl-2.5 lg:pr-1 lg:hover:border-ink/70">
              <Search aria-hidden="true" className="size-4" />
              {/* The visible word is the accessible name too (screen-reader only below lg). */}
              <span className="sr-only lg:not-sr-only lg:pr-4 lg:text-sm">search</span>
              <span aria-hidden="true" className="hidden items-center gap-0.5 lg:flex"><Kbd k="mod" /><Kbd k="K" /></span>
            </button>
            <ThemeToggle />
            <Button variant="ghost" size="sm" className="hidden lg:inline-flex" onClick={signOut}><LogOut />Sign out</Button>
            <Button ref={menuButton} variant="ghost" size="icon" className="lg:hidden" aria-label={menuOpen ? "Close menu" : "Open menu"} aria-expanded={menuOpen} aria-controls="mobile-menu" onClick={() => setMenuOpen(!menuOpen)}>
              {menuOpen ? <X /> : <Menu />}
            </Button>
          </div>
        </div>
        {menuOpen ? <div id="mobile-menu" className="grid gap-3 border-t border-line px-4 py-3 lg:hidden">
          <nav aria-label="Mobile main" className="grid">{nav}</nav>
          {workspaceControl("workspace-mobile")}
          <Button variant="outline" className="w-full" onClick={signOut}><LogOut />Sign out</Button>
        </div> : null}
      </header>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} onSignOut={() => void signOut()} onShowShortcuts={openHelp} />
      <ShortcutsDialog open={helpOpen} onOpenChange={setHelpOpen} />
      <main id="main" className="mx-auto min-w-0 max-w-7xl px-4 pb-12 pt-5 sm:pt-6" tabIndex={-1}>
        <SuspensionBanner />
        <Outlet />
      </main>
    </div>
  );
}
