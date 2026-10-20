#!/bin/sh
# First run downloads the pinned CLI only if absent; cached runs need no network.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/cronpad-restore.XXXXXX")
trap 'rm -rf "$scratch"' EXIT INT TERM
if command -v litestream >/dev/null 2>&1; then
    litestream=$(command -v litestream)
else
    GOBIN="$scratch/bin" go install github.com/benbjohnson/litestream/cmd/litestream@v0.3.13
    litestream="$scratch/bin/litestream"
fi
mkdir -p "$scratch/bin"
CGO_ENABLED=0 go build -o "$scratch/bin/cronpad" ./cmd/cronpad
PATH="$(dirname "$litestream"):$scratch/bin:$PATH" python3 "$root/tools/litestream-restore-test.py" "$root" "$scratch"
