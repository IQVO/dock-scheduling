#!/usr/bin/env python3
"""Tests for asyncapi_breaking.py's pure comparison: no network, real shapes.

The synthetic docs mirror apis/asyncapi.yaml as it is written today: an
AsyncAPI 2.6 spec whose channels are keyed BY the Kafka address, whose
operations carry message oneOf ref lists, and whose messages wrap a
CloudEvents envelope (allOf) whose `data` is a $ref into a *Data schema.
The 3.0-shaped variant (logical channel key + address + channel-level
messages map) is exercised too, because the brief for this gate names it.
"""
import copy
import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("aab", os.path.join(HERE, "asyncapi_breaking.py"))
aab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(aab)


def doc():
    return {
        "asyncapi": "2.6.0",
        "channels": {
            "warehouse.dock-scheduling.events": {
                "subscribe": {"message": {"oneOf": [
                    {"$ref": "#/components/messages/Booked"},
                    {"$ref": "#/components/messages/Cancelled"},
                ]}},
            },
        },
        "components": {
            "messages": {
                "Booked": {"name": "Booked", "payload": {"$ref": "#/components/schemas/BookedCloudEvent"}},
                "Cancelled": {"name": "Cancelled", "payload": {"$ref": "#/components/schemas/CancelledCloudEvent"}},
            },
            "schemas": {
                "Envelope": {"required": ["data"], "properties": {"data": {"type": "object"}}},
                "BookedCloudEvent": {"allOf": [
                    {"$ref": "#/components/schemas/Envelope"},
                    {"properties": {"data": {"$ref": "#/components/schemas/BookedData"}}},
                ]},
                "CancelledCloudEvent": {"allOf": [
                    {"$ref": "#/components/schemas/Envelope"},
                    {"properties": {"data": {"$ref": "#/components/schemas/CancelledData"}}},
                ]},
                "BookedData": {"type": "object", "required": ["site_id", "version"],
                               "properties": {"site_id": {"type": "string"}, "version": {"type": "integer"}}},
                "CancelledData": {"type": "object", "required": ["reason"],
                                  "properties": {"reason": {"type": "string", "enum": ["A", "B"], "nullable": True}}},
            },
        },
    }


class Breaking(unittest.TestCase):
    def test_identical_docs_are_clean(self):
        old = doc()
        self.assertEqual(aab.breaking(old, copy.deepcopy(old)), [])

    def test_removed_channel_is_breaking(self):
        new = doc()
        del new["channels"]["warehouse.dock-scheduling.events"]
        self.assertEqual(["channel warehouse.dock-scheduling.events was removed (consumers still subscribe to it)"],
                         aab.breaking(doc(), new))

    def test_renamed_channel_address_is_breaking(self):
        new = doc()
        new["channels"]["warehouse.dock-scheduling.events.v2"] = \
            new["channels"].pop("warehouse.dock-scheduling.events")
        self.assertIn("channel warehouse.dock-scheduling.events was removed",
                      " | ".join(aab.breaking(doc(), new)))

    def test_message_moved_off_a_live_channel_is_breaking(self):
        base = doc()
        base["channels"]["warehouse.dock-scheduling.analytics"] = {
            "subscribe": {"message": {"oneOf": [{"$ref": "#/components/messages/Cancelled"}]}}}
        new = copy.deepcopy(base)
        new["channels"]["warehouse.dock-scheduling.events"]["subscribe"]["message"]["oneOf"].pop()
        self.assertIn("message Cancelled moved off channel warehouse.dock-scheduling.events",
                      " | ".join(aab.breaking(base, new)))

    def test_removed_message_is_breaking(self):
        new = doc()
        new["channels"]["warehouse.dock-scheduling.events"]["subscribe"]["message"]["oneOf"].pop()
        found = aab.breaking(doc(), new)
        self.assertEqual(found, ["message Cancelled was removed from channel warehouse.dock-scheduling.events"])

    def test_removed_required_data_field_is_breaking(self):
        new = doc()
        new["components"]["schemas"]["BookedData"]["required"] = ["version"]
        self.assertEqual(aab.breaking(doc(), new),
                         ["message Booked: required data field site_id was removed or renamed"])

    def test_renamed_required_data_field_is_breaking(self):
        new = doc()
        new["components"]["schemas"]["BookedData"]["required"] = ["site", "version"]
        self.assertEqual(aab.breaking(doc(), new),
                         ["message Booked: required data field site_id was removed or renamed"])

    def test_type_narrowing_is_breaking(self):
        new = doc()
        new["components"]["schemas"]["BookedData"]["properties"]["site_id"] = {"type": "integer"}
        self.assertEqual(aab.breaking(doc(), new),
                         ["message Booked: data field site_id narrowed its type (['string'] -> ['integer'])"])

    def test_dropped_nullable_is_breaking(self):
        new = doc()
        new["components"]["schemas"]["CancelledData"]["properties"]["reason"]["nullable"] = False
        self.assertEqual(aab.breaking(doc(), new),
                         ["message Cancelled: data field reason narrowed its type (['null', 'string'] -> ['string'])"])

    def test_dropped_enum_value_is_breaking(self):
        new = doc()
        new["components"]["schemas"]["CancelledData"]["properties"]["reason"]["enum"] = ["A"]
        self.assertEqual(aab.breaking(doc(), new),
                         ["message Cancelled: data field reason dropped enum values ['B']"])

    def test_additive_changes_are_clean(self):
        new = doc()
        new["channels"]["warehouse.dock-scheduling.events"]["subscribe"]["message"]["oneOf"].append(
            {"$ref": "#/components/messages/Suspended"})
        new["components"]["messages"]["Suspended"] = {
            "name": "Suspended", "payload": {"$ref": "#/components/schemas/Envelope"}}
        new["components"]["schemas"]["BookedData"]["properties"]["tolerance"] = {"type": "integer"}
        new["components"]["schemas"]["BookedData"]["required"].append("tolerance")
        new["components"]["schemas"]["CancelledData"]["properties"]["reason"]["enum"] = ["A", "B", "C"]
        self.assertEqual(aab.breaking(doc(), new), [])

    def test_the_same_contract_in_3_0_shape_is_clean(self):
        old3 = doc()
        new3 = doc()
        for d in (old3, new3):
            ch = d["channels"].pop("warehouse.dock-scheduling.events")
            d["channels"]["dockEvents"] = {
                "address": "warehouse.dock-scheduling.events",
                "messages": {"publish": ch["subscribe"]["message"]["oneOf"][0]},
            }
        old3["channels"]["dockEvents"]["messages"]["second"] = ch["subscribe"]["message"]["oneOf"][1]
        # old3 keeps both messages via the map; new3 drops Cancelled -> breaking
        new3["channels"]["dockEvents"]["messages"]["publish"] = {"$ref": "#/components/messages/Booked"}
        new3["channels"]["dockEvents"]["messages"]["second"] = {"$ref": "#/components/messages/Cancelled"}
        self.assertEqual(aab.breaking(old3, copy.deepcopy(new3)), [])
        new3["channels"]["dockEvents"]["messages"].pop("second")
        self.assertIn("message Cancelled was removed from channel warehouse.dock-scheduling.events",
                      " | ".join(aab.breaking(old3, new3)))


if __name__ == "__main__":
    unittest.main()
