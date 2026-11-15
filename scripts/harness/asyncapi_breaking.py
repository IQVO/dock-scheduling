#!/usr/bin/env python3
"""Fail when a change to apis/asyncapi.yaml breaks its pinning consumers.

Five consumers pin this repo's apis/asyncapi.yaml@develop byte-for-byte
(warehouse-docs network/model/contract-edges.json): exception-management,
inbound-receiving, outbound-shipping, warehouse-planning and yard-management,
all on warehouse.dock-scheduling.events. The spec's own envelope rule is
additive-only ("a breaking payload change is a new .v2 type, never a
mutation"), so this gate fails the PR on:

  1. a channel removed (matched by Kafka address, not the YAML key), or a
     message removed from (or moved off) a channel it was on;
  2. a required data field removed or renamed;
  3. a data field narrowed: a type the old schema allowed that the new one no
     longer allows (type changed, type list shrunk, `nullable: true` dropped)
     or an enum value dropped.

Both AsyncAPI shapes are handled: 2.x (this repo today — channels keyed BY
the topic address, message lists under publish/subscribe) and 3.0 (channels
keyed by a logical name with an `address`, channel-level `messages` maps).

Usage: asyncapi_breaking.py OLD.yaml NEW.yaml | --self-check
Exit 0 = additive or unchanged, 1 = breaking, 2 = cannot judge (a spec that
cannot be read or parsed is NOT a pass: a gate that cannot look must say so).
"""
import sys

import yaml

PINNING_CONSUMERS = (
    "exception-management, inbound-receiving, outbound-shipping, "
    "warehouse-planning, yard-management"
)


def resolve(doc, node):
    """Follow one internal $ref ('#/components/...') into its definition."""
    if isinstance(node, dict) and isinstance(node.get("$ref"), str):
        cur = doc
        for part in node["$ref"].lstrip("#/").split("/"):
            cur = cur[part]
        return cur
    return node


def refs_names(doc, cand):
    """Message `name`s from a candidate node: a 3.0 map (values), a list, or one message."""
    if cand is None:
        return set()
    if isinstance(cand, dict):
        cand = list(cand.values())      # 3.0 map: logical key -> $ref/message
    elif not isinstance(cand, list):
        cand = [cand]
    names = set()
    for m in cand:
        m = resolve(doc, m)
        if isinstance(m, dict) and m.get("name"):
            names.add(m["name"])
    return names


def message_names(doc, holder):
    """The message `name`s on one holder: a 2.x operation (message/messages),
    a 3.0 operation (messages map), or a 3.0 channel-level messages map."""
    if not isinstance(holder, dict):
        return set()
    if "messages" in holder:
        return refs_names(doc, holder["messages"])
    if "message" in holder:
        one = holder["message"]
        return refs_names(doc, one.get("oneOf") if isinstance(one, dict) and "oneOf" in one else [one])
    return refs_names(doc, holder)      # the holder itself is a 3.0 messages map


def channel_messages(doc):
    """Kafka address -> set of message names on it."""
    out = {}
    for key, ch in (doc.get("channels") or {}).items():
        ch = ch if isinstance(ch, dict) else {}
        names = set()
        for holder in [ch.get("publish"), ch.get("subscribe"), ch.get("messages")]:
            names |= message_names(doc, holder)
        out[ch.get("address", key)] = names
    return out


def data_schema(doc, message):
    """The CloudEvents `data` schema of a message: payload $ref -> allOf branches."""
    payload = resolve(doc, message.get("payload"))
    branches = [payload] + (payload.get("allOf") or [] if isinstance(payload, dict) else [])
    fallback = None
    for node in branches:
        props = (resolve(doc, node) or {}).get("properties") or {}
        if "data" in props:
            data = resolve(doc, props["data"])
            if isinstance(data, dict) and ("required" in data or "properties" in data):
                return data
            fallback = fallback or data
    return fallback if isinstance(fallback, dict) else {}


def allowed_types(prop):
    t = prop.get("type")
    ts = set(t) if isinstance(t, list) else {t} if isinstance(t, str) else set()
    return ts | {"null"} if prop.get("nullable") else ts


def find_message(doc, name):
    for msg in ((doc.get("components") or {}).get("messages") or {}).values():
        msg = resolve(doc, msg)
        if isinstance(msg, dict) and msg.get("name") == name:
            return msg
    return None


def breaking(old, new):
    """Human-readable findings for every breaking change old -> new."""
    out = []
    old_cm, new_cm = channel_messages(old), channel_messages(new)
    for addr in sorted(old_cm):
        if addr not in new_cm:
            out.append(f"channel {addr} was removed (consumers still subscribe to it)")
            continue
        for name in sorted(old_cm[addr] - new_cm[addr]):
            gone = f"message {name} was removed from channel {addr}"
            moved = f"message {name} moved off channel {addr}"
            out.append(gone if name not in {m for v in new_cm.values() for m in v} else moved)
    for name in sorted({m for v in old_cm.values() for m in v}):
        om, nm = find_message(old, name), find_message(new, name)
        if om is None or nm is None:
            continue  # a removal, already reported above
        odata, ndata = data_schema(old, om), data_schema(new, nm)
        for f in sorted(set(odata.get("required") or []) - set(ndata.get("required") or [])):
            out.append(f"message {name}: required data field {f} was removed or renamed")
        oprops, nprops = odata.get("properties") or {}, ndata.get("properties") or {}
        for f in sorted(set(oprops) & set(nprops)):
            ots, nts = allowed_types(oprops[f]), allowed_types(nprops[f])
            if ots and nts and not nts >= ots:
                out.append(f"message {name}: data field {f} narrowed its type ({sorted(ots)} -> {sorted(nts)})")
            oen, nen = oprops[f].get("enum"), nprops[f].get("enum")
            if oen is not None and nen is not None and not set(nen) >= set(oen):
                out.append(f"message {name}: data field {f} dropped enum values {sorted(set(oen) - set(nen))}")
    return out


def self_check():
    """Minimal built-in proof on synthetic 2.x- and 3.0-shaped docs."""
    def doc(addr_key=None, address=None, required=("version",)):
        data = {"type": "object", "required": list(required), "properties": {
            "version": {"type": "integer"},
            "reason": {"type": "string", "enum": ["A", "B"], "nullable": True}}}
        envelope = {"properties": {"data": {"type": "object"}}}
        cloudevent = {"allOf": [
            {"$ref": "#/components/schemas/Env"},
            {"properties": {"data": {"$ref": "#/components/schemas/D"}}}]}
        message = {"name": "E1", "payload": {"$ref": "#/components/schemas/C"}}
        return {
            "asyncapi": "2.6.0",
            "channels": {addr_key or "warehouse.dock-scheduling.events": {
                **({"address": address} if address else {}),
                "subscribe": {"message": {"oneOf": [{"$ref": "#/components/messages/E1"}]}},
            }},
            "components": {
                "messages": {"E1": message},
                "schemas": {"C": cloudevent, "Env": envelope, "D": data},
            },
        }
    old = doc()
    assert breaking(old, old) == [], "identical docs must be clean"
    assert breaking(old, doc(required=())) != [], "a dropped required field must break"
    renamed = doc()
    renamed["components"]["schemas"]["D"]["required"] = ["revision"]
    assert breaking(old, renamed) != [], "a renamed required field must break"
    narrowed = doc()
    narrowed["components"]["schemas"]["D"]["properties"]["reason"]["enum"] = ["A"]
    assert breaking(old, narrowed) != [], "a dropped enum value must break"
    unnull = doc()
    unnull["components"]["schemas"]["D"]["properties"]["reason"]["nullable"] = False
    assert breaking(old, unnull) != [], "dropping nullable must break"
    widened = doc()
    widened["components"]["schemas"]["D"]["properties"]["reason"]["enum"] = ["A", "B", "C"]
    widened["components"]["schemas"]["D"]["properties"]["new_field"] = {"type": "string"}
    assert breaking(old, widened) == [], "additive changes must stay clean"
    removed = doc()
    removed["channels"]["warehouse.dock-scheduling.events"]["subscribe"]["message"]["oneOf"] = []
    assert breaking(old, removed) != [], "a removed message must break"
    v3 = doc(addr_key="events", address="warehouse.dock-scheduling.events")  # 3.0 shape
    v3["channels"]["events"].pop("subscribe")
    v3["channels"]["events"]["messages"] = {"e1": {"$ref": "#/components/messages/E1"}}
    assert breaking(doc(), v3) == [], "the same contract in 3.0 shape must be clean"
    print("self-check OK")
    return 0


def main(argv):
    if "--self-check" in argv:
        return self_check()
    args = [a for a in argv if not a.startswith("--")]
    if len(args) != 2:
        print(__doc__)
        return 2
    try:
        with open(args[0]) as fh:
            old = yaml.safe_load(fh)
        with open(args[1]) as fh:
            new = yaml.safe_load(fh)
    except Exception as exc:  # unreadable or unparseable: not a pass
        print(f"cannot judge the specs: {exc}")
        return 2
    found = breaking(old, new)
    if not found:
        print("apis/asyncapi.yaml: no breaking changes (additive or unchanged)")
        return 0
    for line in found:
        print(f"::error::asyncapi breaking change: {line}")
    print("\nPinning consumers of apis/asyncapi.yaml@develop "
          f"(warehouse-docs contract-edges.json): {PINNING_CONSUMERS}.\n"
          "A breaking payload change must be a NEW .v2 dataschema (the spec's own\n"
          "envelope rule), never a mutation of the v1 contract.")
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
