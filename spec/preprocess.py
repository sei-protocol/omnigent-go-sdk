"""Stamp Go type decisions onto a copy of the vendored spec.

Nothing runs this. It is the evidence behind
`docs/adr/0001-generate-wire-types-behind-a-facade.md`, which is a proposal at
status Proposed. `AGENTS.md` still holds: this module runs no code generator.
Accepting the ADR is what would change that.

The shape follows omnigent's own `scripts/dump_openapi.py`. The producer emits
what it can infer, and a stage adds the rest. There the additions are metadata
for docs and SDK tooling. Here they are the Go type decisions an OpenAPI
document has no way to carry.

Measured against upstream `9d54826e` with oapi-codegen v2.8.0: the six
transforms below make the generated types match the hand-written ones in
`types.go`, `session_types.go`, `event.go`, and `enums.go` field for field, type
for type, and tag for tag, over ten types and 53 event variants.

Usage::

    python3 spec/preprocess.py spec/openapi.json /tmp/prepared.json
"""

import json
import re
import sys

# Words the ToCamelCaseWithInitialisms normalizer does not know. It handles ID.
INITIALISMS = ["LLM", "MCP", "USD", "API", "URL", "TTL", "SSE", "CPU", "UI"]

# A schema node holds data, not a schema, under these keys. Never rewrite them.
DATA_KEYS = ("default", "example", "examples", "const", "enum")


def go_name(field: str) -> str | None:
    """Return the Go field name when the default normalizer would get it wrong."""
    parts = field.split("_")
    fixed, changed = [], False
    for part in parts:
        if part.upper() in INITIALISMS:
            fixed.append(part.upper())
            changed = True
        else:
            fixed.append(part[:1].upper() + part[1:])
    return "".join(fixed) if changed else None


def widen_numbers(node) -> int:
    """Give every formatless `number` an explicit float64, not the float32 default."""
    n = 0
    if isinstance(node, dict):
        if node.get("type") == "number" and "format" not in node and "x-go-type" not in node:
            node["x-go-type"] = "float64"
            n += 1
        for key, value in node.items():
            if key not in DATA_KEYS:
                n += widen_numbers(value)
    elif isinstance(node, list):
        for item in node:
            n += widen_numbers(item)
    return n


def drop_collection_pointers(node) -> int:
    """A nil slice or map is already absent; a pointer to one adds nothing."""
    n = 0
    if isinstance(node, dict):
        for prop in (node.get("properties") or {}).values():
            if not isinstance(prop, dict):
                continue
            branches = [prop] + [b for b in prop.get("anyOf", []) if isinstance(b, dict)]
            for b in branches:
                if b.get("type") in ("array", "object") or "additionalProperties" in b:
                    prop["x-go-type-skip-optional-pointer"] = True
                    n += 1
                    break
        for key, value in node.items():
            if key not in DATA_KEYS:
                n += drop_collection_pointers(value)
    elif isinstance(node, list):
        for item in node:
            n += drop_collection_pointers(item)
    return n


def fix_initialisms(node) -> int:
    """Restore initialism casing the default normalizer does not know."""
    n = 0
    if isinstance(node, dict):
        for field, prop in (node.get("properties") or {}).items():
            name = go_name(field)
            if name and isinstance(prop, dict) and "x-go-name" not in prop:
                prop["x-go-name"] = name
                n += 1
        for key, value in node.items():
            if key not in DATA_KEYS:
                n += fix_initialisms(value)
    elif isinstance(node, list):
        for item in node:
            n += fix_initialisms(item)
    return n


def rename_schemas(spec) -> int:
    """Give a schema whose name carries an initialism its Go spelling."""
    schemas = spec["components"]["schemas"]
    mapping = {}
    for name in list(schemas):
        fixed = name
        for word in INITIALISMS:
            fixed = re.sub(
                r"%s%s(?=[A-Z0-9_]|$)" % (word[0], word[1:].lower()),
                word,
                fixed,
            )
        if fixed != name:
            mapping[name] = fixed
    for old, new in mapping.items():
        schemas[new] = schemas.pop(old)
    if mapping:
        blob = json.dumps(spec)
        for old, new in mapping.items():
            blob = blob.replace(
                '"#/components/schemas/%s"' % old,
                '"#/components/schemas/%s"' % new,
            )
        spec.clear()
        spec.update(json.loads(blob))
    return len(mapping)


def flatten_enums(node) -> int:
    """Keep an enum a plain string.

    A generator names an enum type after its containing schema, so one wire enum
    becomes several incompatible Go types — `SessionResponseStatus` will not
    assign to `SessionListItemStatus`. Untyped constants over a string field
    keep one wire enum one Go type.
    """
    n = 0
    if isinstance(node, dict):
        for prop in (node.get("properties") or {}).values():
            if not isinstance(prop, dict):
                continue
            branches = [prop] + [b for b in prop.get("anyOf", []) if isinstance(b, dict)]
            for b in branches:
                if b.get("enum") and b.get("type") == "string":
                    prop.setdefault("x-go-type", "string")
                    n += 1
                    break
        for key, value in node.items():
            if key not in DATA_KEYS:
                n += flatten_enums(value)
    elif isinstance(node, list):
        for item in node:
            n += flatten_enums(item)
    return n


def defer_union_decode(schemas) -> int:
    """Keep a union payload as raw JSON so the caller decodes it once, by kind.

    A generator turns a `oneOf` payload into a wrapper type with one accessor
    per variant. `ConversationItem.data` carries whichever payload the item's
    `type` names, and this module's block layer already dispatches on that
    field, so the wrapper would be decoded and discarded. One site, named here
    rather than inferred, because no rule distinguishes it from a union a caller
    does want typed.
    """
    data = schemas.get("ConversationItem", {}).get("properties", {}).get("data")
    if data is None or "x-go-type" in data:
        return 0
    data["x-go-type"] = "json.RawMessage"
    return 1


def main() -> int:
    spec = json.load(open(sys.argv[1]))
    schemas = spec["components"]["schemas"]
    w = widen_numbers(schemas)
    p = drop_collection_pointers(schemas)
    i = fix_initialisms(schemas)
    e = flatten_enums(schemas)
    u = defer_union_decode(schemas)
    r = rename_schemas(spec)
    schemas = spec["components"]["schemas"]
    json.dump(spec, open(sys.argv[2], "w"), indent=2, sort_keys=True)
    print(f"  x-go-type float64            : {w}")
    print(f"  x-go-type-skip-optional-ptr  : {p}")
    print(f"  x-go-name initialism fixes   : {i}")
    print(f"  enums flattened to string    : {e}")
    print(f"  union payloads left raw      : {u}")
    print(f"  schema renames               : {r}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
