package repository

import "time"

// AppointmentFilter narrows the appointment listing; a blank field narrows
// nothing. From/To select appointments whose window overlaps [From, To).
type AppointmentFilter struct {
	SiteID      string
	DoorCode    string
	State       string
	Direction   string
	CarrierSCAC string
	AtRiskOnly  bool
	From        time.Time
	To          time.Time
}

// AppointmentAfter is the keyset position an appointment page starts after:
// the window start and the id of the last row of the previous page. The
// listing is ordered by (window start, id) ascending.
type AppointmentAfter struct {
	WindowStart time.Time
	ID          string
}

// Cutoff is one CPT cutoff of a site's schedule, as the local copy keeps it.
type Cutoff struct {
	ID         string
	LocalTime  string
	DaysOfWeek []string
	ShipMethod string
}
