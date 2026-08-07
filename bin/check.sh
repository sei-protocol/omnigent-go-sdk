#!/usr/bin/env bash
# Everything enforced about the Go module, in one place: a contributor runs this
# directly and CI's test job runs the same script, so what passes locally cannot
# drift from what runs on the pull request.
#
# The generated-bindings check is deliberately NOT here: it needs python and
# oapi-codegen, and this script sticks to the Go toolchain. CI runs it as its own
# job, so a contributor without python is unimpeded and drift is still caught.
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
