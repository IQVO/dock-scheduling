//go:build integration

package main

import (
	"os"
	"testing"

	// Every Postgres-touching integration test of this package boots its
	// database through internal/testing/pgtest, which drives the
	// testcontainers postgres module imported here — the package's shared
	// anchor for the TestPostgresIntegrationTestsUseTestcontainers sensor.
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

// The infrastructure comes from testcontainers, never from the environment:
// pgtest boots ONE Postgres and kafkatest ONE Kafka per test binary (each
// lazily); TestMain only tears them down.
func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}
