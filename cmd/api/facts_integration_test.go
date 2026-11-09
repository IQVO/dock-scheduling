//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	inboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/inbound/kafka"
	outboundkafka "github.com/claudioed/dock-scheduling/internal/adapters/outbound/kafka"
	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
)

// The consumer groups of the real wiring tests: the environment of a
// deployment (ADR 0009). The legacy importer is ON so all five consumers run.
const (
	yardGroup     = "it-yard"
	facilityGroup = "it-facility"
	asnGroup      = "it-asn"
	cptGroup      = "it-cpt"
	legacyGroup   = "it-legacy"

	door2 = "WH1-DOCK-IN-02"
)

// The producers' topics. The facts below are published on them exactly as the
// producers publish: CloudEvents 1.0 structured JSON, snake_case (camelCase
// for facility-layout), full `type`.
const (
	producerYard     = "yard-management"
	producerFacility = "facility-layout"
	producerInbound  = "inbound-receiving"
)

// consumerEnv sets the five *_CONSUMER_GROUP variables.
func consumerEnv(t *testing.T) {
	t.Helper()
	t.Setenv("YARD_CONSUMER_GROUP", yardGroup)
	t.Setenv("FACILITY_CONSUMER_GROUP", facilityGroup)
	t.Setenv("ASN_CONSUMER_GROUP", asnGroup)
	t.Setenv("CPT_CONSUMER_GROUP", cptGroup)
	t.Setenv("DOCK_LEGACY_IMPORT_CONSUMER_GROUP", legacyGroup)
}

// createConsumedTopics creates every topic the service reads, and its dead
// letter topic (the broker has auto-creation off).
func createConsumedTopics(t *testing.T) {
	t.Helper()
	for _, topic := range []string{inboundkafka.YardTopic, inboundkafka.FacilityTopic, inboundkafka.InboundReceivingTopic, inboundkafka.ProcessPathTopic} {
		kafkatest.CreateTopic(t, topic)
		kafkatest.CreateTopic(t, inboundkafka.DeadLetterTopic(topic))
	}
}

var eventSeq atomic.Int64

// publish sends one CloudEvent from a producer onto topic, keyed by subject.
func publish(t *testing.T, topic, producer, eventType, subject string, data map[string]any) {
	t.Helper()
	n := eventSeq.Add(1)
	raw, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": fmt.Sprintf("5d1c0a00-0000-4000-8000-%012d", n), "source": "/warehouse/" + producer,
		"type": eventType, "subject": subject, "time": time.Now().UTC().Format(time.RFC3339),
		"datacontenttype": "application/json", "dataschema": "urn:warehouse:" + producer + ":events:" + eventType[strings.LastIndex(eventType, ".")+1:] + ":v1",
		"data": data,
	})
	if err != nil {
		t.Fatal(err)
	}
	kafkatest.Produce(t, topic, kafkatest.Message{Key: subject, Value: raw})
}

func rfc(ts time.Time) string { return ts.UTC().Format(time.RFC3339) }

// The facts, as the producers publish them.
func publishZone(t *testing.T, zoneID, site string) {
	t.Helper()
	publish(t, inboundkafka.FacilityTopic, producerFacility, inboundkafka.TypeZoneRegistered, zoneID,
		map[string]any{"zoneId": zoneID, "siteCode": site, "areaCode": "A-DOCK", "zoneCode": "DOCK"})
}

func publishDoor(t *testing.T, code, zoneID, flow string) {
	t.Helper()
	publish(t, inboundkafka.FacilityTopic, producerFacility, inboundkafka.TypeLocationSlotRegistered, code,
		map[string]any{"locationCode": code, "zoneId": zoneID, "locationType": "DOCK_DOOR", "role": "Dock", "dockFlow": flow})
}

func publishAsn(t *testing.T, number string) {
	t.Helper()
	publish(t, inboundkafka.InboundReceivingTopic, producerInbound, inboundkafka.TypeASNRegistered, number,
		map[string]any{"asn_number": number, "expected_arrival": rfc(time.Now().Add(48 * time.Hour)), "lines": []any{}})
}

func visitCheckedIn(t *testing.T, visitID, appointmentID string, arrivedAt time.Time) {
	t.Helper()
	publish(t, inboundkafka.YardTopic, producerYard, inboundkafka.TypeVisitCheckedIn, visitID, map[string]any{
		"visit_id": visitID, "site_id": "WH1", "carrier": "ACME Freight", "carrier_scac": "ACMF", "purpose": "LIVE_UNLOAD",
		"appointment_id": appointmentID, "trailers": []any{}, "arrived_at": rfc(arrivedAt), "version": 1,
	})
}

func visitRejected(t *testing.T, visitID, appointmentID string) {
	t.Helper()
	publish(t, inboundkafka.YardTopic, producerYard, inboundkafka.TypeVisitRejected, visitID, map[string]any{
		"visit_id": visitID, "site_id": "WH1", "carrier_scac": "ACMF", "appointment_id": appointmentID,
		"reason": "WRONG_DOOR_OR_TIME", "rejected_at": rfc(time.Now()),
	})
}

func trailerDocked(t *testing.T, trailerID, door, appointmentID string, at time.Time) {
	t.Helper()
	publish(t, inboundkafka.YardTopic, producerYard, inboundkafka.TypeTrailerDocked, trailerID, map[string]any{
		"trailer_id": trailerID, "site_id": "WH1", "door_code": door, "appointment_id": appointmentID,
		"asn_numbers": []any{}, "docked_at": rfc(at), "version": 3,
	})
}

func trailerUndocked(t *testing.T, trailerID, door, appointmentID string, at time.Time) {
	t.Helper()
	publish(t, inboundkafka.YardTopic, producerYard, inboundkafka.TypeTrailerUndocked, trailerID, map[string]any{
		"trailer_id": trailerID, "site_id": "WH1", "door_code": door, "appointment_id": appointmentID,
		"undocked_at": rfc(at), "version": 7,
	})
}

// waitCommitted waits until group committed n messages of topic: every fact
// published before was handled.
func waitCommitted(t *testing.T, group, topic string, n int64) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%s to commit %d messages of %s", group, n, topic), 90*time.Second, func() bool {
		offset, ok := kafkatest.TryCommittedOffset(t, group, topic)
		return ok && offset >= n
	})
}

// bookAt books an INBOUND appointment through REST on door for [start, start+1h).
func (j *journey) bookAt(t *testing.T, key, doorCode string, start time.Time, asns ...string) string {
	t.Helper()
	body := map[string]any{
		"direction": "INBOUND", "doorCode": doorCode, "carrier": "ACME Freight", "carrierScac": "ACMF",
		"windowStart": rfc(start), "windowEnd": rfc(start.Add(time.Hour)), "asnNumbers": asns,
	}
	status, _, raw := j.a.do(t, "POST", "/appointments", body, map[string]string{"Idempotency-Key": key})
	if status != http.StatusCreated {
		t.Fatalf("POST /appointments (%s) = %d: %s", key, status, raw)
	}
	id, _ := decode(t, raw)["appointmentId"].(string)
	return id
}

// view reads the appointment through REST.
func (j *journey) view(t *testing.T, id string) map[string]any {
	t.Helper()
	status, _, raw := j.a.do(t, "GET", "/appointments/"+id, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /appointments/%s = %d: %s", id, status, raw)
	}
	return decode(t, raw)
}

// waitState waits until the appointment is in state and returns its view.
func (j *journey) waitState(t *testing.T, id, state string) map[string]any {
	t.Helper()
	var v map[string]any
	waitFor(t, fmt.Sprintf("%s to reach %s", id, state), 60*time.Second, func() bool {
		status, _, raw := j.a.do(t, "GET", "/appointments/"+id, nil, nil)
		if status != http.StatusOK {
			return false
		}
		v = decode(t, raw)
		return v["state"] == state
	})
	return v
}

func nested(t *testing.T, v map[string]any, key string) map[string]any {
	t.Helper()
	m, ok := v[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %v, want an object in %v", key, v[key], v)
	}
	return m
}

// referenceData is the facility layout and the ASNs arriving THROUGH KAFKA. The
// door's slot is published BEFORE its zone (X5): the slot is held pending, a
// booking on it is refused as an unknown door, and it resolves once the zone
// lands.
func (j *journey) referenceData(t *testing.T) {
	publishDoor(t, door, "zone-WH1-in", "Inbound")
	waitCommitted(t, facilityGroup, inboundkafka.FacilityTopic, 1)
	if zones, doors := count(t, j.databaseURL, `SELECT count(*) FROM zones`), count(t, j.databaseURL, `SELECT count(*) FROM doors`); zones != 0 || doors != 1 {
		t.Fatalf("after the slot alone: zones = %d, doors = %d, want 0 and 1 (held pending)", zones, doors)
	}
	body := map[string]any{
		"direction": "INBOUND", "doorCode": door, "carrier": "ACME Freight", "carrierScac": "ACMF",
		"windowStart": rfc(j.windowStart), "windowEnd": rfc(j.windowEnd), "asnNumbers": []string{asn},
	}
	status, _, raw := j.a.do(t, "POST", "/appointments", body, map[string]string{"Idempotency-Key": "it-pending-zone"})
	if status != http.StatusUnprocessableEntity || decode(t, raw)["type"] != problems+"unknown-door" {
		t.Fatalf("a door whose zone has not arrived = %d: %s, want 422 unknown-door", status, raw)
	}
	publishZone(t, "zone-WH1-in", "WH1")
	publishDoor(t, door2, "zone-WH1-in", "Both")
	for _, n := range []string{asn, "ASN-IT-2", "ASN-IT-3", "ASN-IT-4", "ASN-IT-5", "ASN-IT-6", "ASN-IT-7"} {
		publishAsn(t, n)
	}
	waitCommitted(t, facilityGroup, inboundkafka.FacilityTopic, 3)
	waitCommitted(t, asnGroup, inboundkafka.InboundReceivingTopic, 7)
	if n := count(t, j.databaseURL, `SELECT count(*) FROM doors`); n != 2 {
		t.Errorf("doors rows = %d, want 2 (both slots fed through Kafka)", n)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM zones WHERE site_code = 'WH1'`); n != 1 {
		t.Errorf("zones rows = %d, want 1", n)
	}
	if n := count(t, j.databaseURL, `SELECT count(*) FROM asns WHERE status = 'REGISTERED'`); n != 7 {
		t.Errorf("registered ASN rows = %d, want 7", n)
	}
}

// facts walks the appointment lifecycle driven ONLY by yard facts on the
// producer's topic, plus the out-of-order, ignored, dead-lettered, ASN and legacy
// cases.
func (j *journey) facts(t *testing.T) {
	base := j.windowStart.Add(24 * time.Hour)
	var yardSent int64
	yard := func() int64 { yardSent++; return yardSent }

	t.Run("a gate arrival, the dock and the door release take the appointment to COMPLETED", func(t *testing.T) {
		id := j.bookAt(t, "it-fact-b", door2, base, "ASN-IT-2")
		visitCheckedIn(t, "visit-it-b", id, base.Add(5*time.Minute))
		yard()
		v := j.waitState(t, id, "CHECKED_IN")
		arrival := nested(t, v, "arrival")
		if arrival["status"] != "ON_TIME" || arrival["visitId"] != "visit-it-b" || arrival["implicit"] != false {
			t.Errorf("arrival = %v, want ON_TIME from visit-it-b, not implicit", arrival)
		}
		trailerDocked(t, "trl-it-b", door2, id, base.Add(10*time.Minute))
		yard()
		trailerUndocked(t, "trl-it-b", door2, id, base.Add(50*time.Minute))
		yard()
		v = j.waitState(t, id, "COMPLETED")
		if v["completedAt"] == nil || v["version"] != float64(3) {
			t.Errorf("completed view = %v, want completedAt set at version 3 (booked, checked in, completed)", v)
		}
		j.completed = id
	})

	t.Run("a rejected visit at the gate turns the booked appointment away", func(t *testing.T) {
		id := j.bookAt(t, "it-fact-c", door2, base.Add(2*time.Hour), "ASN-IT-3")
		visitRejected(t, "visit-it-c", id)
		yard()
		v := j.waitState(t, id, "CANCELLED")
		if v["cancellationReason"] != "TURNED_AWAY" {
			t.Errorf("cancellationReason = %v, want TURNED_AWAY", v["cancellationReason"])
		}
		j.turnedAway = id
	})

	t.Run("a door release that outruns the gate fact completes implicitly and the late gate fact is ignored", func(t *testing.T) {
		id := j.bookAt(t, "it-fact-d", door2, base.Add(4*time.Hour), "ASN-IT-4")
		trailerUndocked(t, "trl-it-d", door2, id, base.Add(4*time.Hour+30*time.Minute))
		yard()
		v := j.waitState(t, id, "COMPLETED")
		if arrival := nested(t, v, "arrival"); arrival["implicit"] != true || arrival["status"] != "ON_TIME" {
			t.Errorf("arrival = %v, want an implicit ON_TIME arrival (X6)", arrival)
		}
		visitCheckedIn(t, "visit-it-d", id, base.Add(4*time.Hour+5*time.Minute))
		yard()
		waitCommitted(t, yardGroup, inboundkafka.YardTopic, yardSent)
		after := j.view(t, id)
		if after["state"] != "COMPLETED" || after["version"] != v["version"] {
			t.Errorf("after the late gate fact: state %v version %v, want COMPLETED at %v unchanged", after["state"], after["version"], v["version"])
		}
		metrics := j.metrics(t)
		for _, want := range []string{"dock_scheduling_implicit_arrivals_total 1", `dock_scheduling_ignored_facts_total{reason="terminal_appointment"} 1`} {
			if !strings.Contains(metrics, want) {
				t.Errorf("/metrics lacks %q", want)
			}
		}
		j.implicit = id
	})

	t.Run("a fact for an unknown appointment is ignored and counted; a poison fact is dead-lettered", func(t *testing.T) {
		visitCheckedIn(t, "visit-it-x", "appt-00000000-0000-4000-8000-000000000000", base)
		yard()
		publish(t, inboundkafka.YardTopic, producerYard, inboundkafka.TypeVisitCheckedIn, "visit-poison",
			map[string]any{"site_id": "WH1", "appointment_id": "appt-00000000-0000-4000-8000-000000000001"}) // no visit_id
		yard()
		waitCommitted(t, yardGroup, inboundkafka.YardTopic, yardSent)
		if m := j.metrics(t); !strings.Contains(m, `dock_scheduling_ignored_facts_total{reason="unknown_appointment"} 1`) {
			t.Errorf("/metrics lacks the unknown_appointment count")
		}
		dead := kafkatest.ReadN(t, inboundkafka.DeadLetterTopic(inboundkafka.YardTopic), 1, 30*time.Second)
		if !strings.Contains(string(dead[0].Value), "visit-poison") {
			t.Errorf("dead letter = %s, want the poison message", dead[0].Value)
		}
	})

	t.Run("ASNCancelled cancels the booked appointment covering only that ASN", func(t *testing.T) {
		id := j.bookAt(t, "it-fact-e", door2, base.Add(6*time.Hour), "ASN-IT-5")
		publish(t, inboundkafka.InboundReceivingTopic, producerInbound, inboundkafka.TypeASNCancelled, "ASN-IT-5", map[string]any{"asn_number": "ASN-IT-5"})
		v := j.waitState(t, id, "CANCELLED")
		if v["cancellationReason"] != "ASN_CANCELLED" {
			t.Errorf("cancellationReason = %v, want ASN_CANCELLED", v["cancellationReason"])
		}
		j.asnCancelled = id
	})

	t.Run("the strangler stage A importer takes a legacy appointment once", func(t *testing.T) {
		id := "appt-7e57ab1e-0000-4000-8000-00000000a001"
		legacy := map[string]any{
			"appointment_id": id, "door_code": door2, "carrier": "Legacy Freight",
			"window_start": rfc(base.Add(8 * time.Hour)), "window_end": rfc(base.Add(9 * time.Hour)), "asn_numbers": []string{"ASN-IT-6"},
		}
		publish(t, inboundkafka.InboundReceivingTopic, producerInbound, inboundkafka.TypeLegacyBooked, id, legacy)
		publish(t, inboundkafka.InboundReceivingTopic, producerInbound, inboundkafka.TypeLegacyBooked, id, legacy) // a second event id: no second import
		v := j.waitState(t, id, "BOOKED")
		if v["provenance"] != "legacy-import" || v["siteId"] != "WH1" || v["version"] != float64(1) {
			t.Errorf("legacy view = %v, want legacy-import at WH1, version 1", v)
		}
		j.legacy = id
	})

	t.Run("a trailer docked at another door reassigns the appointment (ActualDoorWins)", func(t *testing.T) {
		id := j.bookAt(t, "it-fact-f", door2, base.Add(10*time.Hour), "ASN-IT-7")
		trailerDocked(t, "trl-it-f", door, id, base.Add(10*time.Hour+5*time.Minute))
		yard()
		waitFor(t, id+" to move to "+door, 60*time.Second, func() bool { return j.view(t, id)["doorCode"] == door })
		if v := j.view(t, id); v["state"] != "BOOKED" || v["version"] != float64(2) {
			t.Errorf("reassigned view = %v, want BOOKED at version 2 (booked, door reassigned)", v)
		}
		j.reassigned = id
	})
}

// metrics is the body of GET /metrics.
func (j *journey) metrics(t *testing.T) string {
	t.Helper()
	status, _, raw := j.a.do(t, "GET", "/metrics", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /metrics = %d", status)
	}
	return string(raw)
}

// factEvents proves the events the fact-driven transitions published, on both topics.
func (j *journey) factEvents(t *testing.T) {
	waitFor(t, "the outbox to drain", 30*time.Second, func() bool { return count(t, j.databaseURL, `SELECT count(*) FROM outbox`) == 0 })
	byType := func(topic string) map[string][]map[string]any {
		n := kafkatest.EndOffset(t, topic)
		out := map[string][]map[string]any{}
		for _, m := range kafkatest.ReadN(t, topic, int(n), 60*time.Second) {
			e := decode(t, m.Value)
			name := fmt.Sprint(e["type"])
			name = name[strings.LastIndex(name, ".")+1:]
			data, _ := e["data"].(map[string]any)
			out[name] = append(out[name], data)
		}
		return out
	}
	has := func(events map[string][]map[string]any, name, appointment string, fields map[string]any) {
		t.Helper()
		for _, data := range events[name] {
			if data["appointment_id"] != appointment {
				continue
			}
			for k, want := range fields {
				if data[k] != want {
					t.Errorf("%s of %s: %s = %v, want %v", name, appointment, k, data[k], want)
				}
			}
			return
		}
		t.Errorf("no %s for %s among %v", name, appointment, events[name])
	}
	events := byType(outboundkafka.TopicEvents)
	has(events, "DockAppointmentCancelled", j.turnedAway, map[string]any{"reason": "TURNED_AWAY", "site_id": "WH1"})
	has(events, "DockAppointmentCancelled", j.asnCancelled, map[string]any{"reason": "ASN_CANCELLED"})
	has(events, "DockAppointmentBooked", j.legacy, map[string]any{"provenance": "legacy-import", "direction": "INBOUND"})
	legacyBooked := 0
	for _, data := range events["DockAppointmentBooked"] {
		if data["appointment_id"] == j.legacy {
			legacyBooked++
		}
	}
	if legacyBooked != 1 {
		t.Errorf("DockAppointmentBooked for the legacy appointment = %d, want exactly 1 (imported once)", legacyBooked)
	}
	if len(events["DockAppointmentCheckedIn"]) != 0 || len(events["DockAppointmentCompleted"]) != 0 {
		t.Errorf("analytics-only events leaked onto the events topic: %v", events)
	}
	// ADR 0010: the door reassignment is an integration event, on both topics.
	reassigned := map[string]any{"site_id": "WH1", "from_door_code": door2, "to_door_code": door, "version": float64(2)}
	has(events, "DockAppointmentDoorReassigned", j.reassigned, reassigned)
	analytics := byType(outboundkafka.TopicAnalytics)
	has(analytics, "DockAppointmentDoorReassigned", j.reassigned, reassigned)
	has(analytics, "DockAppointmentCheckedIn", j.completed, map[string]any{"arrival_status": "ON_TIME", "visit_id": "visit-it-b"})
	has(analytics, "DockAppointmentCompleted", j.completed, map[string]any{"door_code": door2})
	has(analytics, "DockAppointmentCompleted", j.implicit, nil)
	has(analytics, "DockAppointmentCancelled", j.turnedAway, map[string]any{"reason": "TURNED_AWAY"})
	if len(analytics["DockAppointmentCheckedIn"]) != 1 {
		t.Errorf("CheckedIn events = %d, want exactly 1: the implicit arrival raises none", len(analytics["DockAppointmentCheckedIn"]))
	}
}
