# The vendored spec

`openapi.json` here is a **snapshot** of the omnigent server's OpenAPI document,
copied in rather than fetched at build time. It is this module's contract of
record: no route outside it is a route this module reaches by design.

Apache-2.0, © Databricks, Inc. — see `NOTICE` at the repository root.

## The written contract beside it

`omnigent/server/API.md` in the upstream repository is the closest thing to a
specification for the session stream. It documents the stream's live-tail-only
rule, the reconnect contract, and the session lifecycle states, and it says of
itself that `openapi.json` is the single source of truth and its own tables are
"derived / illustrative".

It does not cover everything this module depends on. Nothing public documents that
`response.created` is dropped before the subscriber bus, and nothing public
documents tool-call redelivery or how a `call_id` is scoped. For those, the
citable source is a pinned permalink into upstream's code or its Python client —
`blob/<sha>/path#Lx-Ly`, never `blob/main`, because main moves daily.

Upstream states in `API.md` that this API answers to no external standard: "No
external reference. Purpose-built for agent-native workflows." A third party has asked
upstream to document the turn-scoped contract, in omnigent-ai/omnigent#2754, which
is open.

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
```

Read that diff rather than skimming it. It is the server's contract changing
underneath this module. It is also the only place a removed or retyped field
becomes visible before a consumer hits it at runtime.

Upstream moves more than once a day, and this document moves independently of the
Python client beside it. Re-resolve `refs/heads/main` before you trust a local
checkout. A static client tree is not evidence that this file is current.

## What is and is not guarded

**Nothing guards this file against the server.** A snapshot nobody refreshes goes
quietly stale. Reasoning about server behaviour from a stale copy is how a missing
field reaches a consumer. Refresh deliberately. Treat this document as evidence of
what the server did on the date above, not what it does now.

A test guards the other direction. Types in this module declare only fields this
document declares, and a conformance test enforces that over every exported type.
It checks one direction on purpose: the module never claims a field the server
does not have. It does not check that the module reaches every route, because
reaching every route is not a goal.

Some contracts sit outside this document entirely. The server registers the events
route `include_in_schema=False`, so neither its body nor its responses appear here.
Anything reached that way is a hand-written contract that no gate covers, and
`doc.go` names each one.
