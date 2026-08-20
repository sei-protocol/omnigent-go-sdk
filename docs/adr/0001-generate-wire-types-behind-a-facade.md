# 1. Generate the wire types from the vendored spec, behind a hand-written facade

## Status

Proposed, 2026-08-20.

Re-adding a generator is a one-way door. Accept or reject this record. It does
not describe what the module already does.

## Context

`spec/openapi.json` is this module's contract of record. The server generates it,
upstream moves it more than once a day, and nothing in this repository guards the
snapshot against the server. `spec/README.md` states that gap plainly.

The module tracks that document by hand. Four files hold the wire types:

| File | Lines |
|---|---|
| `types.go` | 270 |
| `session_types.go` | 939 |
| `event.go` | 1283 |
| `enums.go` | 151 |
| Total | **2643** |

Four defects follow from writing those by hand.

1. **The snapshot drifts and nothing says so.** Measured on 2026-08-20: our pin
   `179774eb` lags upstream `9d54826e` by one schema. `SessionTitleEvent` is the
   53rd event variant. `event.go` declares 53 event structs, but one of those is
   our own `UnknownEvent`, so the module is a variant behind and the suite is
   green.
2. **The conformance test checks one direction only.** `conformance_test.go`
   proves the module declares no field the document lacks. It cannot prove the
   module declares every field the document has, and it does not try. A new
   field is invisible.
3. **`schemaFor` is a hand-maintained index.** 94 entries name the spec schema
   each type mirrors. 87 map a name to itself. The table exists to let the
   conformance test find the schema, and it rots on every rename.
4. **Incomplete evidence sank a prior attempt.** A deleted
   `oapi-codegen.yaml` recorded that a generated client "would name its methods
   after FastAPI's path-mangled operationIds, return raw `*http.Response`, and
   have no SSE support". Each clause is true. The conclusion does not follow,
   because the config never considered generating into `internal/` behind a
   facade, where a generated method name is unreachable from a consumer.

Upstream solves the same class of problem the same way. `scripts/dump_openapi.py`
generates the document from FastAPI, then post-processes it. That stage relabels
the version, materializes the `ServerStreamEvent` union, and rewrites the SSE
content entries. It then calls `_enrich_spec()`, which adds "document-level
metadata for docs / SDK tooling — none of which FastAPI emits". The producer
emits what it can infer. A stage adds the rest.

## Decision

Generate the wire types and the low-level client into `internal/api`. Keep the
public surface hand-written. Add a pre-process stage that stamps the Go type
decisions the document cannot carry.

- **The pre-process stage.** Roughly 120 lines. It reads the vendored document
  and writes a prepared copy. Six transforms, each measured against upstream
  `9d54826e`:

  | Transform | Sites | Corrects |
  |---|---|---|
  | `x-go-type: float64` | 12 | a formatless `number` becomes `float32` |
  | `x-go-type-skip-optional-pointer` | 71 | `*[]T` and `*map[K]V` on optional collections |
  | `x-go-name` | 18 | `LlmModel`, `TotalCostUsd`, `McpStartup` |
  | enum to `x-go-type: string` | 21 | one wire enum becoming 81 Go types |
  | schema rename | 2 | `McpServerStartup`, `SessionMcpStartupEvent` |
  | `x-go-type: json.RawMessage` | 1 | deferred decode on `ConversationItem.data` |

  Generation also needs `skip-prune: true`, because `oapi-codegen` does not read
  the OAS 3.2 `itemSchema` keyword. Without it the generator cannot reach the
  event union, so it prunes every variant.

- **The generated package is internal.** `internal/api` is unreachable from a
  consumer, so a path-mangled operation name never appears in the public API.

- **The public types are aliases.** The ten data types carry no methods, so
  `type SessionResponse = api.SessionResponse` is legal and the public name is
  unchanged.

- **The event union keeps its seal.** Go refuses a method on an alias to another
  package's type. Each variant therefore becomes a defined type:
  `type SessionStatusEvent api.SessionStatusEvent`. That type is local, so it
  carries `EventType()` and the seal marker. The conversion is a free cast.

- **The facade stays hand-written.** 6347 lines. It holds the redirect and
  credential policy, the SSE reader, the turn loop, and the block folding. It
  also holds the transforms, the tool dispatch, the hooks, and the paged
  iterators.

Measured end to end at `oapi-codegen` v2.8.0. The facade compiles and vets clean
on the generated types. Ten of ten types match field for field. The generator
emits 53 of 53 event variants.

A throwaway spike produced those numbers, and nothing here carries its code.
Accepting this record is what would add the stage, the config, and the generated
package.

## Consequences

Positive:

- **The refresh becomes a build step.** A new field or a 53rd variant arrives
  when the document does, rather than when a person notices.
- **The change is not breaking.** The ten aliased types are identical field for
  field, type for type, and tag for tag. The public surface does not move, so
  this can land in a patch release.
- **`schemaFor` retires, and so does its test.** A generated type does not need
  a table asserting it mirrors the schema it came from.
- **The `float32` defect closes locally.** `x-go-type: float64` fixes all 12
  currency fields with no upstream dependency. Upstream issue
  omnigent-ai/omnigent#5119 asks for `format: double` at the source. That change
  would retire three of the six transforms. This decision does not wait on it.
- **The event union stops being hand-copied.** 664 of `event.go`'s 1283 lines are
  struct declarations for 53 variants that upstream owns.

Negative:

- **Three dependencies where there are none.** `oapi-codegen/runtime`,
  `go-jsonmerge/v2`, and `google/uuid`. `go.mod`'s current comment argues the
  language floor is ours alone to justify because nothing else constrains it.
  That stops being true.
- **The pre-process stage is a second thing to maintain.** It encodes six
  decisions about Go types. A new schema shape can need a seventh, and nothing
  fails loudly when one is missing — the type is merely wrong in the old way.
- **30890 generated lines enter the tree**, 6709 of models and 24181 of client,
  against 2643 removed. A reviewer reads the pre-process stage and the config,
  not the output.
- **The wire types lose their curated prose.** `event.go` carries 305 lines
  of wrapped doc comments. The generated types carry upstream's descriptions on
  one line each, prefixed with the field name. The facade keeps its own docs.
- **`stream.go` stays hand-written.** 579 lines. `oapi-codegen` ignores
  `itemSchema`, and its typed response reads the whole body, which for an SSE
  stream never ends. This is the piece upstream added `itemSchema` for, and no
  Go generator reads it.
- **Two conformance gates need rewriting, not fixing.** `doc_links_test.go` and
  the field-type check walk this package's own AST. Neither resolves a member
  across an alias into another package.

## Alternatives considered

**Keep writing the types by hand.** Rejected: the drift is not hypothetical. The
module is one event variant behind today, and the suite is green, which is the
defect. Every future field arrives the same way.

**Adopt `ogen` instead.** Rejected: it drops all 52 event variants silently. A
generator that omits the largest part of the contract without an error is worse
than no generator.

**Wait for upstream to set numeric formats.** Rejected as a blocker, filed as a
courtesy. `x-go-type: float64` closes the defect locally, so
omnigent-ai/omnigent#5119 would simplify the pre-process stage rather than enable
it. `openapi-generator` 7.24.0 cannot read an OAS 3.2 document at all. The
consumers that fix would help most therefore cannot generate from it today.

**Generate into the public package.** Rejected: the operation names carry the
route. `StoreHostHarnessCredentialV1HostsHostIDHarnessesHarnessCredentialPost`
is a public identifier we would be unable to change, and the 44 path-mangled
parameter types with it.

**Generate the models and keep the hand-written client.** Rejected: it splits
the request path across a generated types package and a hand-written caller for
no gain. The generated `ClientWithResponses` returns typed responses, and
`HttpRequestDoer` accepts the policy-carrying `*http.Client` this module already
builds. This module therefore gains the client half at no cost.
