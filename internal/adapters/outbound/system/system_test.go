package system

import (
	"regexp"
	"testing"
	"time"
)

func TestClockIsUTCAtMicrosecondPrecision(t *testing.T) {
	before := time.Now().UTC().Truncate(time.Microsecond)
	got := Clock{}.Now()
	if got.Location() != time.UTC {
		t.Fatalf("location = %v, want UTC", got.Location())
	}
	if got.Nanosecond()%1000 != 0 {
		t.Fatalf("%v is finer than a microsecond", got)
	}
	if got.Before(before) {
		t.Fatalf("%v is before %v", got, before)
	}
}

func TestIDsMatchTheDomainPatterns(t *testing.T) {
	appt := regexp.MustCompile(`^appt-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	clo := regexp.MustCompile(`^clo-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	ids := IDs{}
	a1, a2 := ids.NewAppointmentID(), ids.NewAppointmentID()
	c1, c2 := ids.NewClosureID(), ids.NewClosureID()
	if !appt.MatchString(a1) || !clo.MatchString(c1) {
		t.Fatalf("unexpected id shapes: %s %s", a1, c1)
	}
	if a1 == a2 || c1 == c2 {
		t.Fatal("ids must be unique")
	}
}
