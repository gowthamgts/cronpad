# Metadata may be supplied by release automation or an injection check.
export BUILD_VERSION := env('BUILD_VERSION', `git describe --tags --always --dirty 2>/dev/null || printf dev`)
export BUILD_COMMIT := env('BUILD_COMMIT', `git rev-parse --verify HEAD 2>/dev/null || printf unknown`)
export BUILD_DATE := env('BUILD_DATE', `date -u +%Y-%m-%dT%H:%M:%SZ`)

default: list

list:
    just --list

# Go server on :8080 and the Vite dev server on :5173 (which proxies /api and /auth to Go).
# Open http://localhost:5173/. Nothing else needs to run: the queue is ./data/cronpad-queue.db.
# DECISION: no air/hot-reload for Go; restart `just dev` after Go changes. Dev login is on so no OAuth app is needed.
# DECISION: build then run the binaries directly; `go run` and `pnpm run` wrappers report
# their child's signal exit (`ELIFECYCLE ... SIGTERM`) when Ctrl+C stops the recipe.
dev: web-install
    #!/usr/bin/env sh
    set -eu
    mkdir -p bin
    go build -o bin/cronpad ./cmd/cronpad
    # A fixed, dev-only key so webhook jobs (signing secrets) and secrets work out of the box.
    : "${CRONPAD_SECRETS__ENCRYPTION_KEY:=Y3JvbnBhZC1kZXYtb25seS1pbnNlY3VyZS1rZXktMzI=}"
    export CRONPAD_SECRETS__ENCRYPTION_KEY
    # The landing site's dev server (cronpad-web, :4321) calls the schedule preview cross-origin.
    : "${CRONPAD_UI__CORS_ORIGINS:=http://localhost:4321}"
    export CRONPAD_UI__CORS_ORIGINS
    CRONPAD_LOGGING__FORMAT=logfmt CRONPAD_AUTH__DEV_LOGIN=true ./bin/cronpad serve &
    server=$!
    (cd web && exec ./node_modules/.bin/vite) &
    web=$!
    trap 'trap - INT TERM; kill -TERM "$server" "$web" 2>/dev/null || true' INT TERM
    # A trapped signal interrupts `wait`; keep waiting so both shut down cleanly.
    while kill -0 "$server" 2>/dev/null || kill -0 "$web" 2>/dev/null; do wait || true; done

# Open http://dash.cronpad.localhost:5173/ (dashboard) and http://cronpad.localhost:4321/ (site, from
# `just dev-hosted` in ~/code/cronpad-web). Hosted mode keeps its own databases under ./data.
# `just dev` as the hosted service on *.cronpad.localhost, sharing theme and sign-in cookies with the site.
dev-hosted:
    #!/usr/bin/env sh
    set -eu
    mkdir -p data
    export CRONPAD_MODE=hosted
    export CRONPAD_SERVER__PUBLIC_URL=http://dash.cronpad.localhost:5173
    export CRONPAD_UI__CORS_ORIGINS=http://cronpad.localhost:4321
    export CRONPAD_DATABASE__PATH=./data/hosted-dev.db
    export CRONPAD_DATABASE__HISTORY_PATH=./data/hosted-dev-history.db
    export DEV_COOKIE_DOMAIN=cronpad.localhost
    exec just dev

web-install:
    pnpm --dir web install --frozen-lockfile

generate: web-install
    go tool sqlc generate
    pnpm --dir web generate

check-generated: web-install
    ./tools/check-generated.sh

build: build-web
    mkdir -p bin
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/gowthamgts/cronpad/internal/version.Version=$BUILD_VERSION -X github.com/gowthamgts/cronpad/internal/version.Commit=$BUILD_COMMIT -X github.com/gowthamgts/cronpad/internal/version.BuildDate=$BUILD_DATE" -o bin/cronpad ./cmd/cronpad
    go build ./examples/ext-hello/...

test: test-go test-web

test-go:
    go test -race ./...

test-web: web-install
    pnpm --dir web test
    node --test 'tests/**/*.test.cjs'

test-integration:
    go test -count=1 -race -tags=integration -run='^TestGenericOIDCDexLogin$' -v ./internal/app
    # DECISION: queue-loss recovery tests run in one package, separate from unit queue
    # conformance. Measure the SLO without race-detector overhead; retain race coverage
    # for all recovery tests and the 100 jobs/s x 10s load.
    go test -count=1 -race -tags=integration -skip='^TestPipeline1000Jobs30Seconds$' -v ./internal/dispatcher/...
    go test -count=1 -tags=integration -run='^TestPipeline1000Jobs30Seconds$' -v ./internal/dispatcher/...

# Keep ordinary and integration-only Go files covered by the same pinned policy.
lint-go:
    go tool -modfile=tools/lint/go.mod golangci-lint config verify --config .golangci.yml
    go tool -modfile=tools/lint/go.mod golangci-lint run --config .golangci.yml ./...
    go tool -modfile=tools/lint/go.mod golangci-lint run --config .golangci.yml --build-tags=integration ./...

lint: lint-go lint-sql lint-web

lint-sql:
    go tool sqlc vet

lint-web: web-install
    pnpm --dir web lint
    pnpm --dir web typecheck

check-version:
    #!/usr/bin/env sh
    set -eu
    BUILD_VERSION=injection-check BUILD_COMMIT=injection-commit BUILD_DATE=2026-01-01T00:00:00Z just build
    output=$(./bin/cronpad version)
    test "$output" = 'cronpad injection-check commit=injection-commit build_date=2026-01-01T00:00:00Z edition=oss'

check: check-public-deps check-generated lint test check-version

migrate-new name:
    go tool goose -dir internal/store/migrations/control create {{name}} sql

docker:
    docker buildx build --load --build-arg VERSION="$BUILD_VERSION" --build-arg COMMIT="$BUILD_COMMIT" --build-arg BUILD_DATE="$BUILD_DATE" -f deploy/Dockerfile -t cronpad:dev .

up:
    docker compose -f deploy/docker-compose.yml up -d --build

down:
    docker compose -f deploy/docker-compose.yml down

bench:
    go test -run='^$' -bench=. ./internal/logx/... ./internal/schedule/...

build-web: web-install
    pnpm --dir web build

# Browser smoke + accessibility checks against the single built binary (./bin/cronpad).
# Needs Google Chrome.
e2e: build
    pnpm --dir web e2e
    pnpm --dir web lighthouse

loadtest:
    go run ./tools/loadgen

release:
    go tool -modfile=tools/release/go.mod goreleaser release --clean

# Go's fuzz flag targets one package; the parser fuzzers all live in schedule.
# DECISION: 100ms per minimization bounds quadratic shrinking while retaining fuzz coverage.
fuzz:
    #!/usr/bin/env sh
    set -eu
    for parser in Cron Quartz RRule Natural Once; do
        go test -run='^$' -fuzz="^Fuzz${parser}$" -fuzztime=60s -fuzzminimizetime=100ms -parallel=2 ./internal/schedule
    done

# Keep private downstream modules out of the public dependency graph.
check-public-deps:
    sh .github/scripts/check-public-deps.sh
