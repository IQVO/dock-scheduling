package usecases

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/dockcalendar"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// DefaultListLimit and MaxListLimit bound every paged listing of the
// in-process readers (the MCP tools); the REST page package carries the same
// numbers for the query string.
const (
	DefaultListLimit = 100
	MaxListLimit     = 500
	// MaxAvailabilityRange is the longest range GetDoorAvailability answers.
	MaxAvailabilityRange = 14 * 24 * time.Hour
)

// ErrInvalidListQuery marks a limit or cursor a paged listing rejects.
var ErrInvalidListQuery = errors.New("list limit or cursor is invalid")

// ErrInvalidAvailabilityQuery marks a range GetDoorAvailability rejects.
var ErrInvalidAvailabilityQuery = errors.New("availability range must be from < to, at most 14 days, minutes 1..240")

// ErrDoorNotFound is returned when a door query names a door that is not known.
var ErrDoorNotFound = errors.New("door not found")

// GetAppointment returns one appointment (GetAppointment).
func (s *Service) GetAppointment(ctx context.Context, id string) (views.AppointmentView, error) {
	if _, err := dockappointment.NewID(id); err != nil {
		return views.AppointmentView{}, err
	}
	a, err := s.d.Appointments.Get(ctx, id)
	if err != nil {
		return views.AppointmentView{}, err
	}
	return toView(a), nil
}

// ListAppointments serves one keyset page (ListAppointments): at most limit
// appointments (0 means DefaultListLimit) ordered by window start then id,
// strictly after the opaque cursor ("" means the first page).
func (s *Service) ListAppointments(ctx context.Context, f repository.AppointmentFilter, limit int, cursor string) (views.AppointmentPage, error) {
	n, err := listLimit(limit)
	if err != nil {
		return views.AppointmentPage{}, err
	}
	after, err := decodeCursor(cursor)
	if err != nil {
		return views.AppointmentPage{}, err
	}
	rows, err := s.d.Appointments.ListPage(ctx, f, after, n+1)
	if err != nil {
		return views.AppointmentPage{}, err
	}
	page := views.AppointmentPage{Items: make([]views.AppointmentView, 0, len(rows))}
	for i, a := range rows {
		if i == n {
			page.NextCursor = encodeCursor(rows[n-1])
			break
		}
		page.Items = append(page.Items, toView(a))
	}
	return page, nil
}

// ListAtRisk lists the appointments flagged at risk (the MCP read tool).
func (s *Service) ListAtRisk(ctx context.Context, siteID string, limit int, cursor string) (views.AppointmentPage, error) {
	return s.ListAppointments(ctx, repository.AppointmentFilter{SiteID: siteID, State: string(dockappointment.Booked), AtRiskOnly: true}, limit, cursor)
}

func listLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultListLimit, nil
	}
	if limit < 1 || limit > MaxListLimit {
		return 0, fmt.Errorf("%w: limit must be 1..%d", ErrInvalidListQuery, MaxListLimit)
	}
	return limit, nil
}

type cursorToken struct {
	Sort string `json:"s"`
	ID   string `json:"i"`
}

func decodeCursor(cursor string) (*repository.AppointmentAfter, error) {
	if cursor == "" {
		return nil, nil
	}
	bad := fmt.Errorf("%w: cursor is not a valid page token", ErrInvalidListQuery)
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, bad
	}
	var p cursorToken
	if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
		return nil, bad
	}
	at, err := time.Parse(time.RFC3339Nano, p.Sort)
	if err != nil {
		return nil, bad
	}
	return &repository.AppointmentAfter{WindowStart: at.UTC(), ID: p.ID}, nil
}

func encodeCursor(a *dockappointment.DockAppointment) string {
	b, _ := json.Marshal(cursorToken{Sort: a.Window().Start().Format(time.RFC3339Nano), ID: a.ID().String()})
	return base64.RawURLEncoding.EncodeToString(b)
}

// GetDockCalendar returns a site's calendar with defaults applied (GetDockCalendar).
func (s *Service) GetDockCalendar(ctx context.Context, siteID string) (views.DockCalendarView, error) {
	site, err := shared.NewSiteID(siteID)
	if err != nil {
		return views.DockCalendarView{}, err
	}
	cal, err := s.d.Calendars.Get(ctx, site)
	if err != nil {
		return views.DockCalendarView{}, err
	}
	return toCalendarView(cal), nil
}

// GetDoorAvailability answers the free windows of a door inside [from, to)
// (GetDoorAvailability). minutes 0 means the site's slot granularity.
func (s *Service) GetDoorAvailability(ctx context.Context, doorCode string, from, to time.Time, minutes int) (views.DoorAvailabilityView, error) {
	code, err := shared.NewDoorCode(doorCode)
	if err != nil {
		return views.DoorAvailabilityView{}, err
	}
	if !to.After(from) || to.Sub(from) > MaxAvailabilityRange || minutes < 0 || minutes > 240 {
		return views.DoorAvailabilityView{}, ErrInvalidAvailabilityQuery
	}
	door, err := s.d.Doors.Door(ctx, code)
	if err != nil {
		return views.DoorAvailabilityView{}, err
	}
	if door == nil {
		return views.DoorAvailabilityView{}, ErrDoorNotFound
	}
	cal, err := s.d.Calendars.Get(ctx, door.Site)
	if err != nil {
		return views.DoorAvailabilityView{}, err
	}
	active, err := s.d.Appointments.ListActiveOnDoor(ctx, code)
	if err != nil {
		return views.DoorAvailabilityView{}, err
	}
	spans := make([]dockcalendar.Span, 0, len(active))
	for _, a := range active {
		w := a.Window()
		spans = append(spans, dockcalendar.Span{Start: w.Start(), End: w.End(), Ref: a.ID().String()})
	}
	free, busy := cal.Availability(code, from.UTC(), to.UTC(), spans, time.Duration(minutes)*time.Minute)
	return views.DoorAvailabilityView{
		DoorCode: code.String(), SiteID: door.Site.String(), From: from.UTC(), To: to.UTC(),
		GranularityMinutes: cal.Settings().SlotGranularityMinutes, FreeWindows: toSpanViews(free), Busy: toSpanViews(busy),
	}, nil
}
