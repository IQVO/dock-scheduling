package postgres

import (
	"github.com/claudioed/dock-scheduling/internal/application/ports"
)

// Compile-time proof the pgx adapters implement the outbound ports.
var (
	_ ports.AppointmentRepo  = (*AppointmentRepo)(nil)
	_ ports.CalendarRepo     = (*CalendarRepo)(nil)
	_ ports.DoorView         = (*DoorStore)(nil)
	_ ports.AsnView          = (*AsnStore)(nil)
	_ ports.CutoffView       = (*CutoffStore)(nil)
	_ ports.ProcessedEvents  = (*ProcessedEventRepo)(nil)
	_ ports.IdempotencyStore = (*IdempotencyStore)(nil)
	_ ports.EventPublisher   = (*OutboxPublisher)(nil)
	_ ports.UnitOfWork       = (*UnitOfWork)(nil)
)
