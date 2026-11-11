package kafka

import (
	"context"
	"testing"
)

// The optional GS1 GLN of a Dock slot (facility-layout's ADR 0045) is kept with
// the door (ADR 0013). It is optional and tolerant: absent, null, or not a valid
// GLN never makes the slot unbookable and never dead-letters the registration.

func doorGLN(t *testing.T, w *world) string {
	t.Helper()
	door, err := w.store.Doors().Door(context.Background(), mustDoor(t, fixtureDoor))
	if err != nil || door == nil {
		t.Fatalf("door = %+v err=%v, want it bookable", door, err)
	}
	return door.GLN.String()
}

func TestFacility_ASlotCarriesItsGLNToTheDoorCopy(t *testing.T) {
	w := newWorld(t)
	fac := w.facility()
	w.handle(fac, fixture(t, "facility_ZoneRegistered"))
	w.handle(fac, withData(t, "facility_LocationSlotRegistered", eventIDFor(80), map[string]any{"gln": "4006381333931"}))
	if got := doorGLN(t, w); got != "4006381333931" {
		t.Fatalf("door GLN = %q, want the slot's", got)
	}
}

func TestFacility_ASlotWithoutAGLNHasNone(t *testing.T) {
	w := newWorld(t)
	fac := w.facility()
	w.handle(fac, fixture(t, "facility_ZoneRegistered"))
	w.handle(fac, fixture(t, "facility_LocationSlotRegistered")) // the shape before ADR 0045
	if got := doorGLN(t, w); got != "" {
		t.Fatalf("door GLN = %q, want none", got)
	}
	w.handle(fac, withData(t, "facility_LocationSlotRegistered", eventIDFor(81), map[string]any{"gln": nil}))
	if got := doorGLN(t, w); got != "" {
		t.Fatalf("a null gln is none, got %q", got)
	}
}

func TestFacility_AnInvalidGLNIsDroppedNotRefused(t *testing.T) {
	for name, gln := range map[string]string{"bad check digit": "4006381333932", "too short": "40063813339", "letters": "40063813339AB", "blank": "   "} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			fac := w.facility()
			w.handle(fac, fixture(t, "facility_ZoneRegistered"))
			w.handle(fac, withData(t, "facility_LocationSlotRegistered", eventIDFor(82), map[string]any{"gln": gln}))
			if got := doorGLN(t, w); got != "" { // still a bookable door, with no read point
				t.Fatalf("door GLN = %q, want it dropped", got)
			}
		})
	}
}

func TestFacility_AReRegistrationWithoutAGLNKeepsTheKnownOne(t *testing.T) {
	w := newWorld(t)
	fac := w.facility()
	w.handle(fac, fixture(t, "facility_ZoneRegistered"))
	w.handle(fac, withData(t, "facility_LocationSlotRegistered", eventIDFor(83), map[string]any{"gln": "4006381333931"}))
	w.handle(fac, withData(t, "facility_LocationSlotRegistered", eventIDFor(84), map[string]any{"dockFlow": "Inbound"}))
	if got := doorGLN(t, w); got != "4006381333931" {
		t.Fatalf("door GLN = %q, want the known one kept", got)
	}
}
