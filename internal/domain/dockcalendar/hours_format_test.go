package dockcalendar_test

import (
	"testing"

	dc "github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
)

// A row reads back exactly as declared: two digits per field, so the padding
// rule is pinned at its 9 / 10 boundary for hours and for minutes.
func TestOperatingHoursReadBackAsDeclared(t *testing.T) {
	for _, c := range []struct{ opens, closes string }{
		{"06:00", "22:00"}, {"09:09", "10:10"}, {"10:00", "10:01"}, {"00:00", "24:00"}, {"09:10", "23:59"},
	} {
		row, err := dc.NewOperatingHours("", dc.Mon, c.opens, c.closes, "UTC")
		if err != nil {
			t.Fatalf("%s-%s: %v", c.opens, c.closes, err)
		}
		if row.OpensAt() != c.opens || row.ClosesAt() != c.closes {
			t.Errorf("%s-%s read back as %s-%s", c.opens, c.closes, row.OpensAt(), row.ClosesAt())
		}
	}
}
