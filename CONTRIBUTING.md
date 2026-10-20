# Contributing

Before starting work, open an issue (or join a relevant existing issue) and discuss
the proposed change with the maintainers. Once the approach is agreed on, raise a
pull request and link the relevant issue. **Pull requests without a relevant issue
will be closed by default.**

Run `just check` before submitting changes. Use commit email addresses linked to your GitHub account.

Contributors sign [CLA version 1.2](CLA.md) once per version and retain their copyright.
The current project owner is Gowtham Gopalakrishnan ([@gowthamgts](https://github.com/gowthamgts)).
The CLA permits commercial licensing and use in proprietary or hosted offerings,
and allows the agreement to transfer with the project to a successor, including
an open-source organization, under the same terms. Earlier-version signers must
sign 1.2 on their next PR; already-merged code remains covered by its original grant.

For vulnerabilities, use GitHub's private “Report a vulnerability” feature under
this repository's Security tab; do not open a public issue. If private reporting
is not enabled, contact the repository owner privately before disclosing details.

## Contributor License Agreement

1. Open your pull request. A bot comment explains what to do.
2. Read `CLA.md`, then comment on the pull request with exactly: `I have read the CLA Document and I hereby sign the CLA`
3. The `cla/signed` check turns green. You only sign once per CLA version.

Every commit must use an email address linked to your GitHub account (**Settings → Emails**), or the check can't match it to you. Comment `recheck` to re-run the check after fixing commits.
Maintainers listed in the CLA workflow's allow-list are exempt from signing.

## Set up

```sh
git clone https://github.com/gowthamgts/cronpad && cd cronpad
just dev            # Go server on :8080 + dashboard dev server on :5173, logs in logfmt
```

`just dev` needs nothing besides Go and Node and turns on development login so you can sign in without an OAuth app. Open `http://localhost:5173/`: Vite serves the dashboard with hot reload and proxies `/api` and `/auth` to the Go server, which you restart after Go changes.

`just --list` shows every task. The ones you'll use most:

| Command | Does |
|---|---|
| `just dev` | Run the Go server and the dashboard dev server |
| `just generate` | Regenerate sqlc and the TypeScript API client |
| `just lint-go` | Validate the Go lint config and lint normal + integration-tagged code (no Node or Docker needed) |
| `just test` | Go tests with the race detector, plus the dashboard's unit tests |
| `just test-integration` | Integration tests: queue loss and crash recovery, the 1000 jobs/s SLO, and OIDC login against a Dex testcontainer |
| `just e2e` | Builds `./bin/cronpad`, then runs the browser smoke test with axe checks and the Lighthouse accessibility audit (needs Google Chrome) |
| `just check` | Generate (must produce no diff), lint, test: what CI runs |
| `just loadtest` | Throughput and start-lag percentiles on your machine |

## Repository layout

This repository holds the server, the HTTP API and the dashboard. The landing page
and the user documentation live in a separate repository and deployment; changes
that affect documented behavior (config keys, API operations, metrics) need a
matching docs change there.

| Path | Contents |
|---|---|
| `cmd/cronpad` | Entry point and CLI |
| `internal/schedule` | Cron, Quartz, RRULE and natural-language parsers |
| `internal/queue`, `internal/dispatcher`, `internal/worker` | The scheduling engine, including the SQLite queue |
| `internal/sink` | Webhook and command sinks |
| `internal/store` | SQLite access (sqlc) and migrations for both databases |
| `internal/api`, `internal/mcpserver`, `internal/auth` | HTTP API, MCP server, sign-in |
| `internal/logx`, `internal/telemetry` | Logging and OpenTelemetry |
| `api/openapi.yaml` | The HTTP API contract |
| `web/` | Dashboard (React), served at `/` |
| `skills/cronpad` | The agent skill |

## Code conventions

- Pass `context.Context` first; log only with context-taking `slog` calls, using the field names in `internal/logx/keys.go`.
- All time comes from the injected clock, so tests never sleep.
- Generated code is committed; `just check` fails if it's stale.

### Go lint policy

`just lint-go` uses the golangci-lint version pinned in `tools/lint/go.mod`; no
separate global installation is needed. CI runs it through `just check`. Both
normal and `integration` builds are linted, including tests. Generated Go files are excluded only when they carry the standard
`// Code generated ... DO NOT EDIT.` marker.

The baseline favors correctness over style: standard Go analysis, wrapped-error
handling, HTTP response and SQL row checks, context-aware I/O and propagation, Unicode
bidirectional-control detection, and selected security checks. The `gosec`
allow-list targets TLS/crypto, HTTP server configuration, SQL formatting, and
filesystem hazards; it is not a comprehensive security audit. Intentional command
execution, configurable paths, and non-security retry randomness need human
review rather than blanket audit warnings. Revisit the allow-list when upgrading
the pinned tool. Context checks apply to tests too: use `t.Context()` for test
I/O, or a bounded child context. Function-length, complexity, naming-length, and
forced parallel test rules are deliberately not enforced.

Format Go changes with the pinned formatter:

```sh
go tool -modfile=tools/lint/go.mod golangci-lint fmt --config .golangci.yml
```

Prefer fixing findings over suppressing them. When a suppression is genuinely
needed, name the specific linter and explain the invariant, for example
`//nolint:bodyclose // Ownership of the response body transfers to the caller.`
Unused or unexplained `nolint` directives fail lint. Avoid package-wide exclusions.

### Context and logging contracts

- `noctx` rejects known context-free HTTP, SQL, network and subprocess APIs;
  `contextcheck` checks inheritance when a parent context is available. Context
  parameters come first, and context keys use dedicated types. Pass request or
  worker contexts through helpers instead of starting over with `Background()`.
- `Background()` belongs at genuine roots (process startup or context-free legacy
  interfaces), not inside request/execution work. For intentional shutdown or
  background work, derive a bounded context from the parent; use `WithoutCancel`
  only when detachment is necessary. Static analysis cannot prove all propagation
  through interfaces and third-party code, so cancellation tests still matter.
- All application logs configured through `logx.Setup` include time, level,
  message, service and version. Blank service/version settings fall back to
  `cronpad`/`unknown`. A valid span adds `trace_id` and `span_id`; never invent IDs
  for untraced work. Logging calls must take a context, use static messages and
  snake-case keys, and cannot override slog's time/level/message/source envelope.
- HTTP request-completion logs require `request_id`, `method`, `route`,
  `status_code` and `duration_ms`. The middleware enriches nested logs with the
  request ID and method. Log route templates, never raw URLs or query strings;
  unmatched routes use `<unmatched>`.
- Once execution is established, sink and completion logs require `job_id`,
  `run_id`, `workspace_id`, `occ`, `attempt`, `trigger`, `scheduled_at` and `sink`.
  Completion logs also require `outcome`, `status_code`, `duration_ms`,
  `start_lag_ms`, `sla_breached` and `error` (empty on success). Earlier scheduler
  events can only include identifiers known at that stage.

`internal/logx/contract_test.go`, `internal/app/request_logging_test.go` and
`internal/worker/log_contract_test.go` assert these contracts against emitted
records, including nested operations, failures/panics, JSON/logfmt and the log
bridge. Field presence is enforced by these tests, not by `sloglint`: generic
static analysis cannot infer which contextual fields an arbitrary event needs.
Extend the contracts when adding new event families.

See the [contributor documentation](https://cronpad.com/docs/contributing).
