#!/usr/bin/env sh
# Reject both imported package paths and private modules, including replacements.
set -eu
packages=$(go list -deps ./...)
modules=$(go list -m all)
printf '%s\n%s\n' "$packages" "$modules" | awk '
    /(^|[[:space:]\/])cronpad-(ee|cloud)([[:space:]\/@]|$)/ {
        print "Public repository must not depend on private module: " $0 > "/dev/stderr"
        forbidden = 1
    }
    END { exit forbidden ? 1 : 0 }
'
