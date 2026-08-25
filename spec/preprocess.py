"""Stamp Go type decisions onto a copy of the vendored spec.

`bin/generate.sh` runs this, then oapi-codegen over its output. Nothing else
reads the prepared copy, and it is not committed.

The shape follows omnigent's own `scripts/dump_openapi.py`. The producer emits
what it can infer, and a stage adds the rest. There the additions are metadata
for docs and SDK tooling. Here they are the Go type decisions an OpenAPI document
has no way to carry: a formatless `number` is not a float32, an absent slice is
not a pointer to a slice, and `MCP` is not spelled `Mcp`.

Every transform is additive and idempotent. Each uses setdefault semantics, so a
future `x-go-*` extension written into the document upstream wins over the
default this file would apply.

See `docs/adr/0001-generate-wire-types-behind-a-facade.md` for why the module
generates these types rather than writing them.
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
    """A nil slice or map is already absent; a pointer to one adds nothing.

    Known gap, not a regression: two fields in the document distinguish an empty
    collection from an absent one. `UpdateSessionRequest.terminal_launch_args`
    says "a list (including `[]`) replaces the stored value wholesale ... `None`
    leaves unchanged", and `UpdateProjectRequest.config` says the same of `{}`.

    `UpdateProjectRequest` is unreachable through this module. `UpdateSessionRequest`
    is not: `Sessions.Update` takes one. So a caller cannot send `[]` to clear
    `terminal_launch_args` wholesale, which is what the property offers. That is
    unchanged from before this transform existed, because the hand-written type was
    already a plain slice with `omitempty`. Fixing it means giving both fields back
    their pointers, which changes the public surface, so it belongs in its own
    change. `setdefault` above is what makes that fix expressible from the document.
    """
    n = 0
    if isinstance(node, dict):
        for prop in (node.get("properties") or {}).values():
            if not isinstance(prop, dict):
                continue
            branches = [prop] + [b for b in prop.get("anyOf", []) if isinstance(b, dict)]
            for b in branches:
                if b.get("type") in ("array", "object") or "additionalProperties" in b:
                    if prop.setdefault("x-go-type-skip-optional-pointer", True):
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
        if new in schemas:
            raise SystemExit(
                f"rename {old} -> {new} would overwrite the schema already named "
                f"{new}; the document changed and this stage cannot resolve it"
            )
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
    """Keep a closed string a plain string.

    A generator names the type after the schema that contains it, so one wire
    enum becomes several Go types that will not assign to each other:
    `SessionResponseStatus` and `SessionListItemStatus` describe the same four
    session states. Untyped constants over a string field keep one wire enum one
    Go type.

    `const` counts as well as `enum`. Every event variant pins its discriminator
    with `const`, so without this each variant's `Type` field gets its own type
    and `EventType() string` cannot return it.
    """
    n = 0
    if isinstance(node, dict):
        for prop in (node.get("properties") or {}).values():
            if not isinstance(prop, dict):
                continue
            branches = [prop] + [b for b in prop.get("anyOf", []) if isinstance(b, dict)]
            for b in branches:
                closed = b.get("enum") is not None or b.get("const") is not None
                if closed and b.get("type") == "string":
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


def drop_open_ended_objects(schemas) -> int:
    """Stop an open-ended object from growing a catch-all field.

    Three schemas declare `additionalProperties: true`. A generator turns that
    into `AdditionalProperties map[string]interface{}` plus `Get`, `Set`,
    `MarshalJSON` and `UnmarshalJSON`. Those arrive as public API here, because
    the three are aliases rather than defined types, and the generated
    `MarshalJSON` decides emptiness by testing the field against nil. It never
    reads a struct tag, so `omitempty` stops working: an empty non-nil slice
    marshals as `[]` where it used to be absent.

    The hand-written types this module shipped declared no catch-all, so
    dropping it holds the public surface still. Retaining unknown properties is
    a real improvement and it belongs in its own change, where the marshalling
    regression can be dealt with rather than ridden along.
    """
    n = 0
    for schema in schemas.values():
        if schema.get("additionalProperties") is True:
            del schema["additionalProperties"]
            n += 1
    return n


def main() -> int:
    with open(sys.argv[1], encoding="utf-8") as doc:
        spec = json.load(doc)
    schemas = spec["components"]["schemas"]
    w = widen_numbers(schemas)
    p = drop_collection_pointers(schemas)
    i = fix_initialisms(schemas)
    e = flatten_enums(schemas)
    u = defer_union_decode(schemas)
    o = drop_open_ended_objects(schemas)
    r = rename_schemas(spec)
    schemas = spec["components"]["schemas"]
    counts = [
        ("x-go-type float64", w),
        ("x-go-type-skip-optional-ptr", p),
        ("x-go-name initialism fixes", i),
        ("closed strings left string", e),
        ("union payloads left raw", u),
        ("catch-all fields dropped", o),
        ("schema renames", r),
    ]
    # A transform that matches nothing has stopped describing the document. It
    # fails no build, because the generated type is merely wrong in the way it
    # was wrong before the transform existed: money back to float32, a
    # collection back behind a pointer. So assert here, where the count is known.
    for name, count in counts:
        print(f"  {name:28} : {count}")

    silent = [name for name, count in counts if count == 0]
    if silent:
        raise SystemExit(
            "these transforms matched nothing, so the document's shape moved "
            "underneath them: " + ", ".join(silent)
        )

    with open(sys.argv[2], "w", encoding="utf-8") as out:
        json.dump(spec, out, indent=2, sort_keys=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
