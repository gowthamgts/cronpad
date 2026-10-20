#!/bin/sh
set -eu
# Compare bytes before/after regeneration, including files not staged yet.
snapshot=$(mktemp -d)
trap 'rm -rf "$snapshot"' EXIT
cp -R internal/store/sqlitegen "$snapshot/sqlitegen"
cp -R internal/store/historygen "$snapshot/historygen"
cp web/src/api/schema.d.ts "$snapshot/schema.d.ts"
cp web/src/api/rules.ts "$snapshot/rules.ts"
go tool sqlc generate
diff -ru "$snapshot/sqlitegen" internal/store/sqlitegen
diff -ru "$snapshot/historygen" internal/store/historygen
pnpm --dir web generate
diff -u "$snapshot/schema.d.ts" web/src/api/schema.d.ts
diff -u "$snapshot/rules.ts" web/src/api/rules.ts
