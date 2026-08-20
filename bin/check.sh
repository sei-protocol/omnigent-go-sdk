#!/usr/bin/env bash
# Everything enforced about the Go module, in one place: a contributor runs this
# directly and CI's test job runs the same script, so what passes locally cannot
# drift from what runs on the pull request.
#
# Drift against the vendored spec is checked by `go test`, not by a separate
# job: the conformance tests read spec/openapi.json directly and assert that no
# exported type declares a field the document omits. That needs nothing beyond
# the Go toolchain, so it belongs here rather than in a job of its own.
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
