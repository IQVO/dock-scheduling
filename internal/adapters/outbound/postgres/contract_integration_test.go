//go:build integration

package postgres_test

import (
	"testing"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
	"github.com/claudioed/dock-scheduling/internal/testing/repocontract"
)

// The same behavioural contract the in-memory double passes, run against a
// real Postgres: every contract case gets its own database cloned from the
// migrated template.
func TestPostgresSatisfiesTheRepositoryContract(t *testing.T) {
	repocontract.Run(t, func(t *testing.T) repocontract.Env {
		pool := pgtest.NewPool(t)
		return repocontract.Env{
			Appointments: postgres.NewAppointmentRepo(pool), Calendars: postgres.NewCalendarRepo(pool), Doors: postgres.NewDoorStore(pool),
			Asns: postgres.NewAsnStore(pool), Cutoffs: postgres.NewCutoffStore(pool), Processed: postgres.NewProcessedEventRepo(pool),
			Idempotency: postgres.NewIdempotencyStore(pool), Events: postgres.NewOutboxPublisher(pool), UoW: postgres.NewUnitOfWork(pool),
		}
	})
}
