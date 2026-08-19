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

- Declare only fields that document declares. `TestEveryDeclaredFieldExistsInTheSpec`
  enforces this over every exported type.
- Add an event variant to `eventRegistry` and to `schemaFor` together. The tests
  fail when the two disagree, and that is deliberate.
- Refresh the description on purpose, and record the source commit in
  `spec/README.md`. Nothing else records it.
- Re-resolve upstream's `refs/heads/main` before trusting a local checkout.
  Upstream moves more than once a day, and `openapi.json` moves independently of
  the Python client beside it.

This module runs no code generator. Do not add one. The previous one decided the
shape of the public surface, which is what this rebuild removes.

## Checks

```sh
bin/check.sh
```

Five legs: `gofmt -l`, `go build`, `go vet`, `go test -race`, `go mod tidy -diff`.
Run it before opening a pull request, and report what it said.

The module has no dependencies. Keep it that way unless a dependency earns its
place, because every one of them raises the language floor this module can claim.

## Releases

**A change to `version.json` merged to the default branch cuts a tag and a GitHub
Release.** No label gates that path. A published version is immutable: the module
proxy serves it, and `sum.golang.org` has recorded its hash.

Do not change `version.json` as part of an ordinary change.

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
