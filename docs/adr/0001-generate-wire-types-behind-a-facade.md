# 1. Generate the wire types from the vendored spec, behind a hand-written facade

## Status

Accepted, 2026-08-20.

The pull request that changed this line is the implementation. `AGENTS.md` no
longer rules out a generator. The last section records where building this
corrected the record.

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
  | closed string to `x-go-type: string` | 81 | one wire enum becoming 81 Go types |
  | schema rename | 2 | `McpServerStartup`, `SessionMcpStartupEvent` |
  | `x-go-type: json.RawMessage` | 1 | deferred decode on `ConversationItem.data` |
  | drop `additionalProperties: true` | 2 | a catch-all field, and a generated marshaller that ignores `omitempty` |

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

A throwaway spike produced those numbers. The stage, the config and the
generated package shipped with the change that set this record to Accepted.

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

**Generate the models and keep the hand-written client.** Accepted, against what
this record first argued. `oapi-codegen.yaml` sets `client: false`.

The rejection assumed the generated client cost nothing to carry. Measured after
the fact: `client: true` emitted 24,188 lines nothing called. Turning it off
dropped none of the three dependencies, because the models need
`runtime.JSONMerge` and `openapi_types.File` on their own. Its default doer is a
bare `&http.Client{}`, which carries none of this module's redirect or credential
policy. The surface it added was the surface a caller would most easily pick up
by mistake.

## What building it corrected

Six claims above did not survive the implementation. They stand as written. A
record that edits its own reasoning afterwards stops being evidence of the
decision it claims to hold. A cross-review found most of what follows; the rest
came from measuring what the review disputed.

- **"Ten data types" was the spike's sample, not the surface.** The change ships
  42 aliases. A field-for-field comparison of all 42 against the types they
  replaced gives 31 identical and 11 differing. Eight of the 11 differ only as
  `map[string]any` against `map[string]interface{}`, which is one type spelled
  two ways. Three are real, and in all three the generator is right where the
  hand-written type was wrong. `PaginatedList.Data` becomes `[]interface{}`,
  which the document's own description asks for: "Items are heterogeneous ... no
  single concrete type satisfies all callers." `ReasoningData.Content` and
  `.Summary` become `[]map[string]string`, which the document declares.
  `SessionResponse.TerminalLaunchArgs` gains `omitempty`, which its absence from
  `required` asks for.
- **"The public surface does not move" is therefore false, and a patch release
  is the wrong vehicle.** Three exported field types change. They correct a wrong
  type rather than regress a right one, and they still break a caller at compile
  time.
- **A generated marshaller broke `omitempty` on three public types.**
  `additionalProperties: true` on three schemas produced a catch-all field and a
  `MarshalJSON` that tests a field against nil and never reads a struct tag. An
  empty non-nil slice marshalled as `[]` where it had been absent. That is a wire
  change on a released module, found by the review's dissenter. The seventh
  transform drops the catch-all, which restores the shape the hand-written types
  had.

  Two of the three, since v0.2.1. Dropping it on `ElicitationRequestParams` lost
  `tool_name`, which the document does not declare and the server sends among the
  properties the schema allows — and which a policy allowlist keys on, so the loss
  read as an allowlist matching nothing rather than as an error. Keeping the
  catch-all there was the repair, not the divergence: upstream sets `extra="allow"`
  on that model deliberately, mirroring MCP. The marshalling regression is accepted
  for that one type, which this package decodes and never encodes, and the extras
  reach a caller as `ElicitationCtx.Extra` rather than as a named field, so a value
  that is present and not a string stays distinguishable from an absent one.

  Keeping it also publishes the generated `AdditionalProperties`, `Get`, `Set`,
  `MarshalJSON` and `UnmarshalJSON` as this module's API, because these types are
  aliases. Dropping the catch-all again is breaking once a consumer calls `Get`.
- **The table above overstates what the enum transform first reached.** It
  claims 81 sites; the
  transform as first written matched `enum` only and fired on 21 of them. Every
  event variant pins its discriminator with `const`, so each generated variant
  carried its own `Type` type, which `EventType() string` cannot return. The
  build failed rather than shipping a wrong type, which is milder than the
  negative above predicts.
- **`schemaFor` did not retire, and neither did its tests.** The 94-row table
  went. A derivation replaced it: a type declared over `api.Y` names schema `Y`,
  so the declaration is the mapping. It reproduces the old table exactly,
  including all seven divergences. The tests survive with a changed purpose: they
  catch `spec/preprocess.py` stamping the wrong type rather than a hand-authoring
  mistake.
- **The wire types kept their curated prose, and `NOTICE` lost them anyway.** An
  alias carries the doc comment above it, so the type-level prose in `types.go`
  and `session_types.go` survived — reworded only by lowercasing the description's
  first letter. The attribution test matched case-sensitively and reported both
  files as carrying nothing. The change removed them from an Apache-2.0
  attribution list on that measurement. The matcher now folds case, and `NOTICE`
  names both files again.
- **The attribution gate needed widening as well.** The generated file reproduces
  483 upstream descriptions, and the test globbed the root package alone.
- **One of the two gates this record claimed to add already existed.**
  `TestEveryUnionMemberIsRegistered` already read the discriminator mapping and
  asserted set equality with `eventRegistry` in both directions. The duplicate is
  gone; the surviving test carries the better doc comment. The doc-link check
  following an alias into `internal/api` is the one genuinely new gate.
- **The counts here describe two different documents.** The transform counts and
  the "53 of 53 event variants" come from a spike against upstream `9d54826e`.
  The tree pins `179774eb`, which carries 52. `bin/generate.sh` prints the counts
  it actually applied, and now refuses to run when a transform matches nothing.

Measured after the change: `types.go` 270 lines to 43, `session_types.go` 939 to
94, `event.go` 1283 to 680, and `enums.go` unchanged at 151.
`internal/api/api.gen.go` holds 5150 generated lines, not the 30890 this record
predicted, because the client is off.
