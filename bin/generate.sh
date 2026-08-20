#!/usr/bin/env bash
# Regenerate internal/api from the vendored spec.
#
# Two steps, because one of them is not something an OpenAPI generator can do.
# spec/preprocess.py stamps the Go type decisions the document cannot carry, and
# oapi-codegen reads its output. Running oapi-codegen against spec/openapi.json
# directly compiles, and is wrong in twelve currency fields and seventy-one
# collections.
#
# The generated file is committed. This script is for refreshing it after the
# spec moves, and CI runs it to prove the committed copy is what the spec
# produces.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if ! command -v oapi-codegen >/dev/null; then
  printf 'oapi-codegen not found. Install it with:\n  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0\n' >&2
  exit 1
fi

prepared="$(mktemp -t omnigent-prepared-XXXXXX.json)"
trap 'rm -f "$prepared"' EXIT

printf '+ spec/preprocess.py\n' >&2
python3 spec/preprocess.py spec/openapi.json "$prepared"

printf '+ oapi-codegen\n' >&2
oapi-codegen -config oapi-codegen.yaml "$prepared"

printf 'Wrote internal/api/api.gen.go\n' >&2
