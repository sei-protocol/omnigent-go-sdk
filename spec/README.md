# The vendored spec

`openapi.json` here is a **snapshot** of the omnigent server's OpenAPI document,
copied in rather than fetched at build time. `scripts/gen_go_client.py` reads it
to produce `models.gen.go` and `events.gen.go`.

Apache-2.0, © Databricks, Inc. — see `NOTICE` at the repository root. The
generated files reproduce this document's schema descriptions as Go doc
comments, which is why they carry the same licence.

## Where it comes from

The document is `openapi.json` at the root of
[omnigent-ai/omnigent](https://github.com/omnigent-ai/omnigent). A running
server serves the same document, so either source works; the repository copy is
the one to prefer, because it can be pinned to a commit and a deployment cannot.

**Current snapshot: `8d1ceb0a` (2026-08-07).**

Record the commit here whenever you refresh, because nothing else does. That
line is the only provenance this repository has.

## How to refresh

```bash
cp <omnigent-checkout>/openapi.json spec/openapi.json
python3 scripts/gen_go_client.py
git diff spec/openapi.json models.gen.go events.gen.go
```

Read that diff rather than skimming it. It is the server's contract changing
underneath this client, and it is the only place a removed field or a retyped
one becomes visible before a consumer hits it at runtime.

Regenerate with the Go version the `generated` CI job pins. `oapi-codegen`
formats through `go/printer`, so a different toolchain can move the bytes and
fail a check nobody's change caused.

## What is and is not guarded

CI regenerates into a temp directory and compares, so **the bindings cannot
drift from this file**. That gate exists because they did: the committed models
were generated from an older spec than the one vendored beside them, and a field
the newer spec carried was missing from the struct for as long as nothing re-ran
the generator.

**Nothing guards this file against the server.** A snapshot that is never
refreshed goes quietly stale, and reasoning about server behaviour from a stale
copy is how the defect above reached a consumer. Refresh deliberately, and treat
the spec as evidence of what the server did on the date above rather than what
it does now.

Four types are hand-written because the server documents neither their routes
nor their schemas: `SessionCreateRequest`, `SessionEventInput`, `EventAccepted`
and `ElicitationResult`. The events route is registered `include_in_schema=False`,
so neither its body nor its responses appear here at all. Nothing about those
four is covered by the gate, in either direction.
