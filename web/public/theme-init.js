// Applies the saved theme before first paint. External (not inline) so the
// dashboard's Content-Security-Policy can forbid inline scripts.
// In hosted mode (meta cronpad-cookie-domain), a `cronpad-theme` cookie shared with the
// public site wins over this origin's localStorage; see src/lib/theme.ts.
(function () {
  try {
    var mode = null;
    if (document.querySelector('meta[name="cronpad-cookie-domain"]')) {
      var match = /(?:^|;\s*)cronpad-theme=(light|dark|system)(?:;|$)/.exec(document.cookie);
      if (match) mode = match[1];
    }
    if (!mode) mode = localStorage.getItem("cronpad-theme") || "system";
    var dark = mode === "dark" || (mode !== "light" && window.matchMedia("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.toggle("dark", dark);
  } catch {
    /* storage unavailable: fall back to light */
  }
})();
