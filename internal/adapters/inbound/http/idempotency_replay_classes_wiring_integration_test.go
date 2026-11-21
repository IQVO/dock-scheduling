//go:build integration

package http_test

// The repo-specific half of the idempotency replay-class test
// (idempotency_replay_classes_integration_test.go is the verbatim fleet
// template). This is the ONLY file of the pair that knows the store:
// dock-scheduling keeps the key row in the request's transaction (JOINED
// mode, docs/adr/0008 and docs/adr/0014), so the strongest wiring is a real
// Postgres (internal/testing/pgtest boots one via testcontainers) with the
// production idempotency store, the production unit of work and the
// production middleware. The template drives the JOINED mode — the mode of
// every keyed route; the sweep's DETACHED mode is covered by the unit
// per-class tests (idempotency_classes_internal_test.go).

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	// The package's testcontainers anchor: this file touches pgx (through
	// pgtx), so the sensor wants the postgres module imported here.
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"

	inboundhttp "github.com/claudioed/dock-scheduling/internal/adapters/inbound/http"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres"
	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// replayClock is a fixed clock: the middleware stamps stored answers with it.
var replayClock = func() time.Time { return time.Date(2030, 1, 7, 6, 0, 0, 0, time.UTC) }

// newReplayOps returns the six operations over one clean database (a fresh
// pgtest pool with the migrations applied).
func newReplayOps(t *testing.T) replayOps {
	t.Helper()
	pool := pgtest.NewPool(t)
	if _, err := pool.Exec(context.Background(), `CREATE TABLE IF NOT EXISTS replay_probe (tag text PRIMARY KEY, at timestamptz NOT NULL)`); err != nil {
		t.Fatalf("create the write probe table: %v", err)
	}
	count := func(t *testing.T, query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	uow := postgres.NewUnitOfWork(pool)
	store := postgres.NewIdempotencyStore(pool)
	return replayOps{
		Catalogue: func() []problemEntry {
			out := make([]problemEntry, 0)
			for _, e := range inboundhttp.ReplayCatalogueForTest() {
				out = append(out, problemEntry{Slug: e.Slug, Status: e.Status, Class: e.Class})
			}
			return out
		},
		Middleware: func(t *testing.T, next http.Handler) http.Handler {
			t.Helper()
			return inboundhttp.KeyedForTest(store, uow, replayClock, false)(next)
		},
		Refuse: func(w http.ResponseWriter, r *http.Request, e problemEntry) {
			err, ok := inboundhttp.ReplayEntryErrorForTest(e.Slug)
			if !ok {
				http.Error(w, "no catalogue entry for slug "+e.Slug, http.StatusInternalServerError)
				return
			}
			inboundhttp.RefuseForTest(w, r, err)
		},
		Write: func(ctx context.Context, tag string) error {
			// One write made through the request's context, so it joins the
			// transaction the middleware opened (pgtx) exactly as a use
			// case's save does.
			tx, ok := pgtx.From(ctx)
			if !ok {
				return context.Canceled // no transaction: the write must not stand alone
			}
			_, err := tx.Exec(ctx, `INSERT INTO replay_probe (tag, at) VALUES ($1, now()) ON CONFLICT (tag) DO NOTHING`, tag)
			return err
		},
		Committed: func(t *testing.T, tag string) int {
			t.Helper()
			return count(t, `SELECT count(*) FROM replay_probe WHERE tag = $1`, tag)
		},
		KeyRows: func(t *testing.T, key string) int {
			t.Helper()
			return count(t, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, key)
		},
	}
}
