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
# produces. Both tool versions are checked rather than assumed: a different
# oapi-codegen writes a different file, and the CI check then fails with a diff
# that names no cause.
#
# oapi-codegen truncates and rewrites internal/api/api.gen.go in place, so a kill
# mid-write leaves it short. A load failure does not: the generator reads and
# validates the document before it opens the output. Regenerating is the recovery
# either way.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Keep in step with the version .github/workflows/ci.yml installs.
readonly want_codegen='v2.8.0'

if ! command -v python3 >/dev/null; then
  printf 'python3 not found. spec/preprocess.py needs 3.10 or newer.\n' >&2
  exit 1
fi

if ! python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 1)'; then
  printf 'python3 is %s; spec/preprocess.py needs 3.10 or newer for PEP 604 unions.\n' \
    "$(python3 -c 'import sys; print(".".join(map(str, sys.version_info[:3])))')" >&2
  exit 1
fi

if ! command -v oapi-codegen >/dev/null; then
  printf 'oapi-codegen not found. Install it with:\n  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@%s\n' \
    "$want_codegen" >&2
  exit 1
fi

have_codegen="$(oapi-codegen --version | tail -1)"
if [ "$have_codegen" != "$want_codegen" ]; then
  printf 'oapi-codegen is %s, not %s. A different version writes a different file, so CI would reject it. Install the pinned one:\n  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@%s\n' \
    "$have_codegen" "$want_codegen" "$want_codegen" >&2
  exit 1
fi

# mktemp -d rather than a filename template: BSD treats the template as a prefix
# and GNU substitutes it, so the two platforms disagree about what the name is.
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

printf '+ spec/preprocess.py\n' >&2
python3 spec/preprocess.py spec/openapi.json "$workdir/prepared.json"

printf '+ oapi-codegen\n' >&2
oapi-codegen -config oapi-codegen.yaml "$workdir/prepared.json"

printf 'Wrote internal/api/api.gen.go\n' >&2
