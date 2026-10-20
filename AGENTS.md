# Notes for agents

Short list of non-obvious things in this repo. Read the code for everything else.

## Frontend layout
- `web/` is the dashboard: React 19 + Vite + **Tailwind v4** (`@tailwindcss/vite`). There is no `tailwind.config.js`, PostCSS config or `ui/` preset any more; don't add them back.
- `web/packages/design` (`@cronpad/design`) is the shared design system: tokens (HSL triplets, light + `.dark`), Maple Mono `@font-face`, base styles, and the `.box`, `.box-title`, `.btn*`, `.field`, `.link`, `.eyebrow`, `.marker`, `.graph` and `.live-dot` classes. The public website also consumes it, from a pinned commit of this repo. Change shared look there, not in `web/src`.
- `web/src/index.css` holds only dashboard extensions: shadcn-style alias colours (`background`, `card`, `muted`, `primary`, …), compact buttons (40px phone / 32px desktop), `.section-title` (inline `// heading`), flat nested boxes, and a default `border-color` on `*` (v3 behaviour the ui components rely on).
- Theme scale quirks: `rounded-sm` = 3px, `rounded` = 4px, `rounded-md` = 6px. Don't "upgrade" `rounded-*` names. Use `outline-hidden`, not `outline-none`, to hide outlines.
- Colour tokens are bare HSL channels (`--ink: 160 30% 8%`), used as `hsl(var(--ink) / 0.3)`. Keep them that way; several arbitrary shadows depend on it.

## Fonts
- `web/packages/design/fonts/maple-mono-{400,500,700}.woff2` are subsets of `@fontsource/maple-mono` 5.3.0 (latin files), made with fontTools. Keep only weights 400/500/700, no italic; use `font-bold`/`font-medium`, never `font-semibold`/`font-extrabold`. To add glyphs, re-run with a wider `--unicodes`:
  `pyftsubset <latin-WEIGHT.woff2> --unicodes="U+0020-007E,U+00A0-017F,U+2010-2027,U+2030-203A,U+20AC,U+2190-21FF,U+2212,U+2248,U+2260,U+2264-2265,U+2500-257F,U+25B2-25BE,U+2713,U+2715" --flavor=woff2 --layout-features='kern,mark,mkmk,ccmp,locl' --no-hinting --desubroutinize`
- The dashboard CSP is `font-src 'self'`: fonts must stay external files, never data URLs.

## Dashboard CSP
- `internal/app/dashboard.go` sets a strict CSP: no inline scripts, one hashed inline style (react-remove-scroll). That's why `web/public/theme-init.js` is an external file.

## Theme and the hosted signed-in hint
- The theme is stored in localStorage under `cronpad-theme` (`light`/`dark`; absent = system).
- **Hosted mode only** (`config.SharedCookieDomain()`, derived from `server.public_url`, e.g. `dash.cronpad.com` → `cronpad.com`):
  - Go injects `<meta name="cronpad-cookie-domain">` into `index.html`. With it, the theme is also written to a `cronpad-theme` cookie on that domain, shared with the public website. The cookie wins over localStorage and stores `system` explicitly (so it can override a stale value).
  - Login sets `cronpad_signed_in=1` on that domain. It is a hint for the website's header only: never use it for authorization. It is cleared on logout and on any 401 that carries it. The session cookie stays host-only and `HttpOnly`.
- Self-hosted mode writes no shared cookies. If you change the cookie format, the website's `theme.js` must change too.

## Queue
- The queue (`internal/queue/sqlite.go`) is its own SQLite file, `<control-db-name>-queue.db` beside `database.path` unless `queue.path` is set. It is a rebuildable index: never back it up, and never move it into the control DB (it would contend with the single writer). Redis support was removed; keep the `queue.Queue` interface so another backend stays possible.
- Every `queue.Queue` implementation (SQLite, and the in-memory one used by tests) must pass `TestConformance` in `internal/queue`.

## API contract
- `api/openapi.yaml` is the public API contract. It is hand-maintained, embedded by `api/embed.go`, and served by the app. Whenever you change an API route, parameter, request/response shape, status code or auth requirement, update it in the same change.
- `internal/app/api_contract_test.go` validates real requests and responses against it. `TestOpenAPIContractEveryMountedRoute` requires every mounted route to be in the spec and the reverse. `TestMain` fails a full `go test ./internal/app` when an operation never returned a schema-validated 2xx response: add a success-path request (via `f.request`, or `validateResponse` for tests that call the handler directly). The check is skipped with `-run`/`-short`.
- It only covers what the tests send, so passing doesn't prove the spec is complete: check error paths and optional fields yourself. Request auth is stubbed in the validator, so `security` declarations are not verified.

## Config
- Every `CRONPAD_*` environment variable is parsed as configuration (unknown sections log a warning). Don't use that prefix for anything else; the dev-only Vite variable is `DEV_COOKIE_DOMAIN`.
- `config.example.toml` must stay byte-identical to `internal/config/defaults.toml` (a test checks this).

## Running and testing
- `just dev`: Go on :8080 + Vite on :5173. `just dev-hosted`: the same, in hosted mode, on `http://dash.cronpad.localhost:5173` with separate `./data/hosted-dev*.db` files, for testing shared cookies with the website's own `dev-hosted` recipe. Vite silently moves to :5174 if :5173 is busy, which breaks those URLs.
- `pnpm build` runs `tsc --noEmit` first, so any type error blocks the build.
- e2e (`pnpm e2e`) runs against `bin/cronpad`, which embeds `web/dist` via `go:embed`. Run `pnpm build`, then `go build -o bin/cronpad ./cmd/cronpad`, before it. Needs nothing else running.
- The e2e axe scan fails on serious violations. Ones we hit: a button's visible text must be part of its accessible name (`label-content-name-mismatch`), and a `<dl>` may only contain `dt`/`dd` or `div` groups of them.

## Owner preferences
- The dashboard is compact compared with the website: smaller buttons (40/32px). The header has a bare search icon on phones and a bordered search pill from `lg` up, plus a single theme button that cycles system → light → dark (same as the website), not a three-button group.
- Maple Mono, weights 400/500/700 only; the website's hero heading uses `font-medium`.
- Website code stays out of this repo; share through `web/packages/design` and `web/packages/schedule`, not by merging repos or using submodules.
- Shared-domain cookies are a hosted-service concern; self-hosted installs must not need any extra setup for them.
