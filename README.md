# omnigent-go-sdk

A Go client for the omnigent server.

## Status: rebuild in progress

We are rebuilding this module to match the shape of the upstream Python client at
[omnigent-ai/omnigent](https://github.com/omnigent-ai/omnigent), under
`sdks/python-client`. That client is an agent-interaction library. This module was
a typed transport. The rebuild closes the difference.

This commit removed the previous implementation and its code generator. **The
default branch does not build until the foundation lands.** That is deliberate, so
that a reviewer judges the removal and the rebuild separately.

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

`v0.1.2` is the last release carrying the previous implementation, and it stays
available. Do not install from `@main` while the rebuild is in progress, because
the default branch does not build.

`@latest` resolves normally once the rebuild publishes its first release. The
module sits at the repository root, so the proxy matches it.

## The vendored description

`spec/openapi.json` is a pinned snapshot of the server's OpenAPI document, and it
is this module's contract of record. `spec/README.md` records which upstream
commit it came from and how to refresh it.

Types in this module declare only fields that document declares. A conformance
test enforces it in that one direction: the module never claims a field the server
does not have.

## Checks

```sh
bin/check.sh
```

Five legs: `gofmt -l`, `go build`, `go vet`, `go test -race`, `go mod tidy -diff`.
The foundation milestone returns all five to green. Before it lands, three fail:
`go vet` and `go test` find no package to act on, and `go mod tidy -diff` reports
that tidy would strip the whole `require` block.

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
