package features_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/cucumber/godog"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// registerStandardsSteps registers the steps of standard_vocabulary.feature
// (ADR 0013). The facts are read from the wire data the in-memory outbox twin
// rendered, so what is asserted is the payload a partner would receive.
func registerStandardsSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^site (\S+) has door (\S+) taking (Inbound|Outbound|Both) flows and the GLN (\d+)$`, w.seedDoorWithGLN)
	sc.Step(`^facility-layout registers dock door (\S+) in zone (\S+) of site (\S+) taking (Inbound|Outbound|Both) flows with the GLN "([^"]*)"$`, w.registerDoorWithGLN)
	sc.Step(`^the (Dock\w+) fact of appointment "([^"]+)" has (x12_status|cbv_biz_step|read_point_gln) "([^"]+)"$`, w.wantStandardField)
	sc.Step(`^the (Dock\w+) fact of appointment "([^"]+)" has no (x12_status|cbv_biz_step|read_point_gln)$`, w.wantNoStandardField)
	sc.Step(`^the (Dock\w+) fact of appointment "([^"]+)" has exactly the fields (.+)$`, w.wantExactFields)
	sc.Step(`^a consumer written before the standard fields decodes the (Dock\w+) fact of appointment "([^"]+)" and ignores them$`, w.wantLegacyDecode)
}

func (w *world) seedDoorWithGLN(site, door, flow, gln string) error {
	zone := "zone-" + site
	if err := w.store.Doors().UpsertZone(w.ctx, zone, site); err != nil {
		return err
	}
	f, err := shared.ParseDockFlow(flow)
	if err != nil {
		return err
	}
	return w.store.Doors().UpsertDoor(w.ctx, door, zone, f, gln)
}

// registerDoorWithGLN is facility-layout's pair of facts (the zone, then the Dock
// slot) reaching the consumers' use cases, the GLN being the slot's optional one.
func (w *world) registerDoorWithGLN(door, zone, site, flow, gln string) error {
	if err := w.svc.ConsumeZoneRegistered(w.ctx, w.eventID(), zone, site); err != nil {
		return err
	}
	return w.svc.ConsumeSlotRegistered(w.ctx, w.eventID(), door, zone, "Dock", flow, gln)
}

// fact returns the wire data of the latest event of that name for the appointment.
func (w *world) fact(event, alias string) (map[string]any, json.RawMessage, error) {
	id, err := w.id(alias)
	if err != nil {
		return nil, nil, err
	}
	wire := w.store.PublishedWire()
	for i := len(wire) - 1; i >= 0; i-- {
		var data map[string]any
		if wire[i].Event != event || json.Unmarshal(wire[i].Data, &data) != nil || data["appointment_id"] != id {
			continue
		}
		return data, wire[i].Data, nil
	}
	return nil, nil, fmt.Errorf("no %s fact for appointment %q (%s) was published; published: %v", event, alias, id, w.eventNames())
}

func (w *world) wantStandardField(event, alias, field, want string) error {
	data, _, err := w.fact(event, alias)
	if err != nil {
		return err
	}
	if got, present := data[field]; !present || got != want {
		return fmt.Errorf("%s of %s: %s = %v (present %v), want %q; data %v", event, alias, field, got, present, want, data)
	}
	return nil
}

// wantNoStandardField: the field is OMITTED, not null and not empty.
func (w *world) wantNoStandardField(event, alias, field string) error {
	data, _, err := w.fact(event, alias)
	if err != nil {
		return err
	}
	if got, present := data[field]; present {
		return fmt.Errorf("%s of %s carries %s = %v, want it omitted; data %v", event, alias, field, got, data)
	}
	return nil
}

func (w *world) wantExactFields(event, alias, list string) error {
	data, _, err := w.fact(event, alias)
	if err != nil {
		return err
	}
	var want, got []string
	for _, f := range strings.Split(list, ",") {
		want = append(want, strings.TrimSpace(f))
	}
	for k := range data {
		got = append(got, k)
	}
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%s of %s has the fields %v, want exactly %v", event, alias, got, want)
	}
	return nil
}

// legacyBooked is the struct a consumer written before ADR 0013 decodes (the
// shape outbound-shipping reads from DockAppointmentBooked): it knows none of
// the standard fields and, like every fleet consumer, tolerates unknown ones.
type legacyBooked struct {
	AppointmentID string `json:"appointment_id"`
	SiteID        string `json:"site_id"`
	Direction     string `json:"direction"`
	DoorCode      string `json:"door_code"`
	Carrier       string `json:"carrier"`
	CarrierSCAC   string `json:"carrier_scac"`
	CPTID         string `json:"cpt_id"`
	WindowStart   string `json:"window_start"`
	WindowEnd     string `json:"window_end"`
	Version       int64  `json:"version"`
}

func (w *world) wantLegacyDecode(event, alias string) error {
	data, raw, err := w.fact(event, alias)
	if err != nil {
		return err
	}
	var withFields legacyBooked
	if err := json.Unmarshal(raw, &withFields); err != nil {
		return fmt.Errorf("a consumer written before the standard fields cannot decode %s: %w", raw, err)
	}
	for _, f := range []string{"x12_status", "cbv_biz_step", "read_point_gln"} {
		delete(data, f)
	}
	stripped, _ := json.Marshal(data)
	var without legacyBooked
	if err := json.Unmarshal(stripped, &without); err != nil {
		return err
	}
	if withFields != without || withFields.AppointmentID == "" || withFields.DoorCode == "" || withFields.CPTID == "" || withFields.Version != 1 {
		return fmt.Errorf("the standard fields changed what the old consumer reads:\n with:    %+v\n without: %+v", withFields, without)
	}
	return nil
}
