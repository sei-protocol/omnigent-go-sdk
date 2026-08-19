# 1. Rebuild the client rather than re-land it

Date: 2026-08-19

## Status

Accepted. Supersedes the approach drafted as `001-sdk-review-and-ergonomics`.

## Context

The client reached its default branch as one change of twenty-six thousand lines.
No reviewer formed a verdict on it. Two ways out were considered.

The first kept the implementation and re-landed it as a sequence of small changes
ending at a tree identical to its start, so the empty final diff proved fidelity.
That approach had two problems. Its own steps made the empty diff impossible: it
moved three declarations, fixed two defects and rewrote three documentation
claims, so the final tree could not match the first. More importantly it left the
shape unchanged, and the shape is the actual debt.

The shape is the debt because of what the module is next to. Upstream's Python
client is an agent-interaction library: 9,298 lines across twenty-seven modules,
fifty public symbols, and a turn loop. This module was a typed transport. The
difference is a missing layer, not a missing feature, and our only consumer wrote
1,062 lines of conversation turn semantics to fill it.

A code generator produced 4,215 lines of types exporting 162 symbols from the
vendored description. A caller reaching the upstream experience needs roughly
eighty. While the generator decides the shape, every ergonomic type sits beside a
generated type that duplicates it, and the two drift.

## Decision

Delete the implementation and the generator, then rebuild by hand against the same
description across three further changes. The rebuild is a superset of the previous
reach and a fraction of the previous surface.

The deletion is its own change, and it leaves the default branch not building.
That is accepted so that a reviewer judges a removal separately from a rebuild.

A conformance test replaces the generator's drift gate. It asserts that this
module never declares a field the description omits. It deliberately does not
assert the reverse, because reaching every route is not a goal.

## Consequences

The public surface is ours to shape, and ours to keep honest. `eventRegistry` and
`schemaFor` travel together. The tests fail when the two disagree.

The module has no dependencies, so its language floor is ours alone to justify and
it dropped from 1.24.0 to 1.23.0.

The default branch does not build between the first change and the second. No
check may gate merges in that window, because one would make the deletion
unmergeable. Consumers stay insulated: `v0.1.2` remains available and immutable.

Re-adding a generator later means reconciling hand-authored types against
generated ones, which is the coupling this decision removes. Treat it as a
one-way door.
