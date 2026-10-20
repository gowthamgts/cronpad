#!/bin/sh
# Thin launcher; the Go subcommand also works in shell-free images.
set -eu
exec cronpad entrypoint "$@"
