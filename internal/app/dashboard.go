package app

import (
	"bytes"
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
)

// dashboardCSP is deliberately strict: the SPA ships no inline script and talks
// only to its own origin.
// DECISION: style-src allows exactly one inline rule by hash: the scroll-lock rule
// (body[data-scroll-locked]) that react-remove-scroll injects when a Radix dialog opens.
// The hash is pinned by pnpm-lock.yaml; the Playwright smoke fails on any CSP violation,
// so a dependency change that alters the rule is caught there.
const dashboardCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'sha256-nzTgYzXYDNe6BAHiiI7NNlfK8n/auuOAhh2t92YvuXo='; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// mountDashboard serves the SPA at root, leaving registered backend routes in control.
func mountDashboard(r chi.Router, dist fs.FS, mcpPath, cookieDomain, publicURL string) {
	handler := dashboardHandler(dist, cookieDomain, publicURL)
	r.Get("/", handler)
	// DECISION: permanent redirects preserve old email links and bookmarks, including queries.
	legacy := func(w http.ResponseWriter, req *http.Request) {
		target := strings.TrimPrefix(req.URL.EscapedPath(), "/app")
		if target == "" {
			target = "/"
		}
		// Keep the redirect on this host even for a legacy path starting with two slashes.
		target = "/" + strings.TrimLeft(target, "/")
		if req.URL.RawQuery != "" {
			target += "?" + req.URL.RawQuery
		}
		http.Redirect(w, req, target, http.StatusPermanentRedirect)
	}
	r.Get("/app", legacy)
	r.Get("/app/*", legacy)
	// DECISION: use NotFound rather than a wildcard route so backend and extension
	// routes retain precedence; reserved backend paths never receive SPA HTML.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		if p == "/api" || strings.HasPrefix(p, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": "route not found"}})
			return
		}
		for _, prefix := range []string{"/auth", "/mcp", mcpPath, "/healthz", "/readyz", "/version", "/metrics", "/llms.txt", "/robots.txt"} {
			if prefix != "" && (p == prefix || strings.HasPrefix(p, prefix+"/")) {
				http.NotFound(w, req)
				return
			}
		}
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			http.NotFound(w, req)
			return
		}
		handler(w, req)
	})
}

// dashboardHandler serves the SPA. In hosted mode (config.SharedCookieDomain), index.html carries the shared
// cookie domain in a meta tag so
// public/theme-init.js and lib/theme.ts can share the theme cookie with the public site.
// With server.public_url set, the link-preview image becomes absolute, as Open Graph requires.
func dashboardHandler(dist fs.FS, cookieDomain, publicURL string) http.HandlerFunc {
	var cookieMeta, ogImage []byte
	if cookieDomain != "" {
		cookieMeta = []byte(`<head>` + "\n    " + `<meta name="cronpad-cookie-domain" content="` + html.EscapeString(cookieDomain) + `" />`)
	}
	if publicURL != "" {
		ogImage = []byte(`content="` + html.EscapeString(strings.TrimRight(publicURL, "/")) + `/og.png"`)
	}
	return func(w http.ResponseWriter, req *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", dashboardCSP)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		name := strings.TrimPrefix(path.Clean("/"+req.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if name != "index.html" {
			if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite fingerprints everything under assets/.
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					h.Set("Cache-Control", "public, max-age=3600")
				}
				http.ServeFileFS(w, req, dist, name)
				return
			}
			// A missing file with an extension is a real 404, not a client route.
			if path.Ext(name) != "" {
				http.NotFound(w, req)
				return
			}
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			h.Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("The dashboard is not built into this binary. Run `just build-web` and rebuild.\n"))
			return
		}
		if cookieMeta != nil {
			index = bytes.Replace(index, []byte("<head>"), cookieMeta, 1)
		}
		if ogImage != nil {
			index = bytes.ReplaceAll(index, []byte(`content="/og.png"`), ogImage)
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if req.Method != http.MethodHead {
			_, _ = w.Write(index)
		}
	}
}
