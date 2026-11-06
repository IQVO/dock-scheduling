package repository

import "time"

// AdherenceQuery selects the schedule-adherence report: the half-open range
// [From, To) and the optional site and carrier narrowing.
type AdherenceQuery struct {
	SiteID      string
	CarrierSCAC string
	From        time.Time
	To          time.Time
}

// AdherenceRow is one (site, carrier) line of the schedule-adherence report:
// how many appointments arrived on time, early or late, or never showed. The
// carrier is "-" when the appointment had no SCAC.
type AdherenceRow struct {
	SiteID      string
	CarrierSCAC string
	OnTime      int
	Early       int
	Late        int
	NoShow      int
}
