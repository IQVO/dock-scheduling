//go:build integration

package kafka

import (
	"os"
	"testing"

	// The Kafka-touching integration tests of this package start their broker
	// through internal/testing/kafkatest, which drives the testcontainers kafka
	// and postgres modules imported here: the package's anchor for the
	// TestKafkaIntegrationTestsUseTestcontainers and
	// TestPostgresIntegrationTestsUseTestcontainers sensors.
	_ "github.com/testcontainers/testcontainers-go/modules/kafka"
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/claudioed/dock-scheduling/internal/testing/kafkatest"
	"github.com/claudioed/dock-scheduling/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}
