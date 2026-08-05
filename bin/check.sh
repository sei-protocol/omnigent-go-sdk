#!/usr/bin/env bash
# Everything enforced about the Go module, in one place: `just go-sdk-test` and
# the Lint workflow's required `Pre-commit checks` job both run this script, so
# what a contributor runs locally cannot drift from what gates the PR.
#
# golangci-lint is deliberately not here. It needs its own install, and it runs
# in the advisory Go SDK workflow instead; this script sticks to what the Go
# toolchain already provides so the required job needs nothing extra.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

run() {
  printf '+ %s\n' "$*" >&2
  "$@"
}

# gofmt reports rather than exits non-zero, so check its output.
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  printf 'gofmt: these files need formatting:\n%s\n' "$unformatted" >&2
  exit 1
fi

run go build ./...
run go vet ./...
# -race because the SSE reader's idle watchdog runs on a timer goroutine.
run go test ./... -race
# The module graph must be settled; `-diff` fails instead of rewriting go.mod.
run go mod tidy -diff
