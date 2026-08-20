# AGENTS.md

Instructions for this repository. A direct instruction in the conversation wins
over this file.

## What this module is

A Go client for the omnigent server. It is being rebuilt to match the shape of the
upstream Python client in `omnigent-ai/omnigent`, under `sdks/python-client`. That
client is an agent-interaction library; this one was a typed transport, and the
rebuild closes the difference.

Read `doc.go` before changing the public surface.

## The vendored description is the contract

`spec/openapi.json` is a pinned snapshot of the server's OpenAPI document.

- Declare only fields the document declares, with the type and optionality it
  declares. Six conformance tests check every exported type the decoder reaches,
  across five dimensions.
  They do not check presence: omitting a property the document declares passes,
  deliberately.
- Add an event variant to `eventRegistry` and to `schemaFor` together. The tests
  fail when the two disagree, and that is deliberate.
- Refresh the description on purpose, and record the source commit in
  `spec/README.md`. Nothing else records it.
- Re-resolve upstream's `refs/heads/main` before trusting a local checkout.
  Upstream moves more than once a day, and `openapi.json` moves independently of
  the Python client beside it.
- Add a file to `NOTICE` when you copy an upstream schema or property description
  into it. The list is exhaustive by measurement, not by convention, and
  `TestNoticeNamesEveryFileCarryingUpstreamProse` measures it in both directions,
  so a file you forget fails the suite rather than shipping unattributed.
  `NOTICE` is the list; do not restate it here.

`bin/generate.sh` produces `internal/api`. Run it after the spec moves, and commit
the result. Never edit that package by hand: the next run discards the edit.

Generation runs over `spec/preprocess.py`'s output, not over `spec/openapi.json`.
That stage stamps the Go type decisions no OpenAPI document can carry. Generating
from the raw document compiles, and is wrong in twelve currency fields and
seventy-one collections. Add a transform there rather than editing the generated
file or the vendored spec.

The public types are one-line declarations over that package. Use `type X = api.X`
where the type carries no methods, and `type X api.X` where it does, because Go
refuses a method on another package's type. Nothing in the public API names
`internal/api`, so a generated identifier is never a name a consumer depends on.

`docs/adr/0001-generate-wire-types-behind-a-facade.md` records the decision and
its costs. It supersedes the rule that stood here, which ruled out a generator
outright.

## Checks

```sh
bin/check.sh
```

Five legs: `gofmt -l`, `go build`, `go vet`, `go test -race`, `go mod tidy -diff`.
Run it before opening a pull request, and report what it said.

The module depends only on what the generated client needs:
`github.com/oapi-codegen/runtime`, and the two modules it pulls in. Adding
another needs explicit human approval. A dependency's own `go` directive sets a
floor this module cannot declare below, so one dependency decides who is able to
import this package.

## Releases

**A change to `version.json` merged to the default branch cuts a tag and a GitHub
Release.** No label gates that path. A published version is immutable: the module
proxy serves it, and `sum.golang.org` has recorded its hash.

Do not change `version.json`. A release is its own pull request and needs
explicit human approval. No version bump here is incidental: the merge is
the release.

## Style

- Comments state the present. Provenance belongs in the commit and the pull
  request, never in the source.
- A doc comment says what a thing is. Rationale that a future editor needs goes
  where they will be editing.
- Optional fields are pointers, so a caller can tell "the server sent zero" from
  "the server sent nothing". Slices and maps are the exception: nil already
  carries that.
- Write prose in Simplified Technical English: active voice, one instruction per
  sentence, short sentences.

## Conventions for a change

- Conventional Commits subjects: `type(scope)!: description`.
- Branch first. Never push to `main`. Never force-push.
- Do not merge or publish a pull request without explicit approval.
