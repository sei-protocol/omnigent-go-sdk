# The vendored spec

`openapi.json` here is a **snapshot** of the omnigent server's OpenAPI document,
copied in rather than fetched at build time. It is this module's contract of
record: no route outside it is a route this module reaches by design.

Apache-2.0, © Databricks, Inc. — see `NOTICE` at the repository root.

## Where it comes from

The document is `openapi.json` at the root of
[omnigent-ai/omnigent](https://github.com/omnigent-ai/omnigent). A running server
serves the same document, so either source works. Prefer the repository copy: you
can pin it to a commit, and you cannot pin a deployment.

**Current snapshot: `179774eb` (2026-08-19).**

Record the commit here whenever you refresh, because nothing else does. That line
is the only provenance this repository has.

## How to refresh

```bash
cp <omnigent-checkout>/openapi.json spec/openapi.json
git diff spec/openapi.json
bin/generate.sh                      # internal/api follows the document
go test ./...
```

Read that diff rather than skimming it. It is the server's contract changing
underneath this module. It is also the only place a removed or retyped field
becomes visible before a consumer hits it at runtime.

Read what `bin/generate.sh` prints, too. It reports a count per transform in
`spec/preprocess.py`, and refuses to run when any of them matches nothing. A
transform that stops matching does not break the build. It returns a type to
being wrong the way it was wrong before the transform existed. Money back to
`float32` is the case to watch.

Treat this document as an input trusted at the level of source code. It decides
generated Go type expressions, and an `x-go-type-import` in it would decide a
generated import. The recipe above is a copy with no integrity check, so the
review of that diff is the only control there is.

Upstream moves more than once a day, and this document moves independently of the
Python client beside it. Re-resolve `refs/heads/main` before you trust a local
checkout. A static client tree is not evidence that this file is current.

## What is and is not guarded

**Nothing guards this file against the server.** A snapshot nobody refreshes goes
quietly stale. Reasoning about server behaviour from a stale copy is how a missing
field reaches a consumer. Refresh deliberately. Treat this document as evidence of
what the server did on the date above, not what it does now.

Tests guard the other direction. Types in this module declare only fields this
document declares, and the conformance suite enforces that over every exported
type. For fields it checks one direction on purpose: the module never claims a
field the server does not have, and omitting one the server does have passes.

Events are the exception. `TestEveryUnionMemberIsRegistered` checks both
directions, so a variant this document publishes and the decoder does not handle
fails the suite rather than arriving as an `UnknownEvent`. Nothing checks that the
module reaches every route, because reaching every route is not a goal.

Some contracts sit outside this document entirely. The server registers the events
route `include_in_schema=False`, so neither its body nor its responses appear here.
Anything reached that way is a hand-written contract that no gate covers, and
`doc.go` names each one.
