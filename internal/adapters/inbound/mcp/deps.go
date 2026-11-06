package mcp

import (
	"context"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/application/views"
)

// Service is the slice of the application's use cases the tools drive:
// *usecases.Service satisfies it, and so does a test double. Every method is
// one the REST adapter calls too, with the same meaning.
type Service interface {
	GetAppointment(ctx context.Context, id string) (views.AppointmentView, error)
	ListAppointments(ctx context.Context, f repository.AppointmentFilter, limit int, cursor string) (views.AppointmentPage, error)
	ListAtRisk(ctx context.Context, siteID string, limit int, cursor string) (views.AppointmentPage, error)
	GetDoorAvailability(ctx context.Context, doorCode string, from, to time.Time, minutes int) (views.DoorAvailabilityView, error)
	GetDockCalendar(ctx context.Context, siteID string) (views.DockCalendarView, error)

	BookAppointment(ctx context.Context, cmd usecases.BookCommand) (views.AppointmentView, error)
	RescheduleAppointment(ctx context.Context, cmd usecases.RescheduleCommand) (views.AppointmentView, error)
	CancelAppointment(ctx context.Context, cmd usecases.CancelCommand) (views.AppointmentView, error)
	ReportEta(ctx context.Context, cmd usecases.EtaCommand) (views.EtaResult, error)
}

// Idempotent runs a write at most once per idempotency key: *usecases.Idempotency
// satisfies it.
type Idempotent interface {
	Do(ctx context.Context, key, fingerprint string, work func(ctx context.Context) ([]byte, error), classify usecases.Failure) (usecases.Outcome, error)
}

// Deps is everything the MCP tools need, injected by the composition root. The
// adapter never constructs an outbound adapter itself.
type Deps struct {
	Service     Service
	Idempotency Idempotent
}

var _ Service = (*usecases.Service)(nil)
var _ Idempotent = usecases.Idempotency{}
