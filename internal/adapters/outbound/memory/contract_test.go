package memory_test

import (
	"testing"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/memory"
	"github.com/claudioed/dock-scheduling/internal/testing/repocontract"
)

func TestMemoryStoreSatisfiesTheRepositoryContract(t *testing.T) {
	repocontract.Run(t, func(t *testing.T) repocontract.Env {
		s := memory.NewStore()
		return repocontract.Env{
			Appointments: s.Appointments(), Calendars: s.Calendars(), Carriers: s.Carriers(), Doors: s.Doors(), Asns: s.Asns(), Cutoffs: s.Cutoffs(),
			Processed: s, Idempotency: s.Idempotency(), Events: s, UoW: s,
		}
	})
}
