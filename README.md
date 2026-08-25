# omnigent-go-sdk

A Go client for the omnigent server.

## Status: rebuild in progress

We are rebuilding this module to match the shape of the upstream Python client at
[omnigent-ai/omnigent](https://github.com/omnigent-ai/omnigent), under
`sdks/python-client`. That client is an agent-interaction library. This module was
a typed transport. The rebuild closes the difference.

The first milestone removed the previous implementation and its code generator
and left the default branch not building, so that a reviewer could judge the
removal and the rebuild separately. This milestone restores it: `bin/check.sh`
returns all five legs green.

A generator writes the wire types again, on different terms. `bin/generate.sh`
produces `internal/api` from the vendored spec. Every public type is a one-line
declaration over it, so a generated name never reaches a consumer.
`docs/adr/0001-generate-wire-types-behind-a-facade.md` records why.

| Milestone | Contents |
|---|---|
| 1 | Remove the implementation and the generator. Refresh the vendored description |
| 2 | Client, transport, stream reader, errors, event types, the type floor |
| 3 | Session surface, files, child status, the bounded subtree walk |
| 4 | Turn loop, transcript blocks, stream transforms, hooks, tool dispatch |

## Installing

Pin a released version:

```sh
go get github.com/sei-protocol/omnigent-go-sdk@v0.1.2
```

`v0.1.2` is the last release, and it carries the *previous* implementation — not
the surface this rebuild describes. `@latest` resolves to it until the rebuild
publishes its own release.

`@main` carries the rebuild as it lands, so a consumer who wants the new surface
before that release pins a commit. The session, files, turn-loop and tool surfaces
are not there yet.

## The vendored description

`spec/openapi.json` is a pinned snapshot of the server's OpenAPI document, and it
is this module's contract of record. `spec/README.md` records which upstream
commit it came from and how to refresh it.

Types in this module declare only fields that document declares. The conformance
tests enforce that, and each field's Go type and optionality, and the event
decoder's coverage of the document's discriminators.

They check one direction only: a field the document declares and this module omits
passes. The surface is meant to be smaller than the document, not equal to it.

## Checks

```sh
bin/check.sh
```

Five legs: `gofmt -l`, `go build`, `go vet`, `go test -race`, `go mod tidy -diff`.
All five pass. CI runs the same script on the floor `go.mod` declares and on
current stable.

Two more legs run only in CI, both in the `Go SDK lint` job: `golangci-lint`, and
a check that regenerates `internal/api` and fails if the committed copy differs.
Each needs a tool outside the Go toolchain, which `bin/check.sh` deliberately
does not need. Run `bin/generate.sh` yourself after touching the spec or
`spec/preprocess.py`.

## Releasing

`version.json` drives releases through the org's shared workflows in
[sei-protocol/uci](https://github.com/sei-protocol/uci).

**A change to `version.json` merged to the default branch cuts a tag and a GitHub
Release.** No label gates that path. The workflow reads the `release` label only
when the push lands somewhere other than the default branch. Treat a version bump
as the release itself, not as a proposal.

On a pull request, `release-check` runs `gorelease` and `gocompat` against the
previous tag. Together they say whether the version increment is semver-honest.
Two limits are worth knowing. It runs only when the pull request changes
`version.json`, and it reads the base branch rather than the pull request's head.

This module ships no GoReleaser job, deliberately. It is a library, so the tag is
the release. `go get` resolves a version from the module proxy, and the proxy reads
it from the tag. No binary needs attaching.

A published version is immutable. The module proxy serves it, and `sum.golang.org`
has recorded its hash. Nobody can withdraw a release.
