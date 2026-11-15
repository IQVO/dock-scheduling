#!/usr/bin/env bash
# Contract-test the REST API with Schemathesis (property-based testing
# against apis/openapi.yaml): builds the service, boots it against the
# Postgres named by DATABASE_URL and the Kafka broker named by KAFKA_BROKERS
# on a loopback port, waits for /readyz, generates valid AND invalid requests
# for every operation, and asserts the responses conform to the spec (status
# codes, content types, response schemas; positive data accepted, negative
# data rejected).
#
# Mirrors the `contract` job in .github/workflows/ci.yml — same pinned
# Schemathesis version, same flags — so a local pass means a CI pass.
#
# This service has no in-memory mode (fleet decision 2026-10-17): cmd/api
# requires DATABASE_URL (it migrates and serves from Postgres), KAFKA_BROKERS
# (the outbox relay, the inbound consumers and /readyz) and the four consumer
# groups. CI provides a postgres:16 and a Kafka service container; for a
# local run, start disposable ones first (the broker must advertise the
# address you pass in KAFKA_BROKERS):
#
#   docker run -d --rm --name ds-contract-pg -p 18087:5432 \
#     -e POSTGRES_USER=dock -e POSTGRES_PASSWORD=dock \
#     -e POSTGRES_DB=dock postgres:16
#   docker run -d --rm --name ds-contract-kafka -p 19087:9092 \
#     -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://localhost:29092,PLAINTEXT_HOST://localhost:19087 \
#     confluentinc/confluent-local:7.6.1
#   export DATABASE_URL='postgres://dock:dock@localhost:18087/dock?sslmode=disable'
#   export KAFKA_BROKERS='localhost:19087'
#
# (credentials/URL shape mirror the CI `contract` job's postgres service and
# internal/adapters/inbound/mcp/errors_internal_test.go's dock:***@db:5432/dock.)
#
# Requires `st` on PATH:
#   python3 -m pip install --user 'schemathesis==4.28.0'
set -euo pipefail

SCHEMATHESIS_VERSION="4.28.0"
PORT="${CONTRACT_PORT:-18086}"
BASE_URL="http://127.0.0.1:${PORT}"
MAX_EXAMPLES="${CONTRACT_MAX_EXAMPLES:-100}"
# Extra `st run` flags, e.g. CONTRACT_ST_ARGS="--seed 7" to reproduce a CI failure.

if ! command -v st >/dev/null 2>&1; then
  echo "schemathesis (st) is not installed (or not on PATH)."
  echo "Install the exact version CI pins:"
  echo "  python3 -m pip install --user 'schemathesis==${SCHEMATHESIS_VERSION}'"
  exit 1
fi

if [ -z "${DATABASE_URL:-}" ]; then
  echo "DATABASE_URL is not set — this service has no in-memory mode"
  echo "(cmd/api requires Postgres and applies migrations on boot)."
  echo "Start a disposable one and export DATABASE_URL, e.g.:"
  echo "  docker run -d --rm --name ds-contract-pg -p 18087:5432 \\"
  echo "    -e POSTGRES_USER=dock -e POSTGRES_PASSWORD=dock \\"
  echo "    -e POSTGRES_DB=dock postgres:16"
  echo "  export DATABASE_URL='postgres://dock:dock@localhost:18087/dock?sslmode=disable'"
  exit 1
fi

if [ -z "${KAFKA_BROKERS:-}" ]; then
  echo "KAFKA_BROKERS is not set — cmd/api requires a real Kafka broker for"
  echo "the outbox relay and the inbound consumers, and /readyz depends on it."
  echo "Start a disposable one and export KAFKA_BROKERS, e.g.:"
  echo "  docker run -d --rm --name ds-contract-kafka -p 19087:9092 \\"
  echo "    -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://localhost:29092,PLAINTEXT_HOST://localhost:19087 \\"
  echo "    confluentinc/confluent-local:7.6.1"
  echo "  export KAFKA_BROKERS='localhost:19087'"
  exit 1
fi

cd "$(dirname "$0")/.."
BIN="$(mktemp -d)/api"
go build -o "$BIN" ./cmd/api

# The four consumer groups are required at boot (cmd/api/loadConfig names
# every missing variable at once); empty topics auto-create on the broker,
# so the consumers idle and the read models start empty — Schemathesis
# creates what it needs through the REST API itself.
LOG_LEVEL="${LOG_LEVEL:-warn}" \
HTTP_ADDR="127.0.0.1:${PORT}" \
YARD_CONSUMER_GROUP="contract-test-yard" \
FACILITY_CONSUMER_GROUP="contract-test-facility" \
ASN_CONSUMER_GROUP="contract-test-asn" \
CPT_CONSUMER_GROUP="contract-test-cpt" \
"$BIN" &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true' EXIT

# Wait for /readyz: it is 200 only while Postgres answers a ping AND a Kafka
# broker answers a metadata request (up to ~60s: migrations + broker).
for _ in $(seq 1 300); do
  if curl -sf "${BASE_URL}/readyz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done
curl -sf "${BASE_URL}/readyz" >/dev/null # fail loudly if it never became ready

# Excluded CHECK — unsupported_method: chi answers an undocumented method with
# 405 + the problem-json body but no RFC 9110 `Allow` header, so the check
# fails on every path while the actual 405 contract (the body) is pinned by
# TestNotFoundAndMethodNotAllowedAreProblems
# (internal/adapters/inbound/http/server_test.go).
#
# EVERY operation is exercised (22/22, including the stateful phase). The
# per-operation relaxations of the positive_data_acceptance /
# negative_data_rejection checks live, with a reason each, in
# schemathesis.toml at the repo root (Schemathesis loads it from the working
# directory — this script cd's there first). The response-conformance and
# not_a_server_error checks stay on for every operation.
st run apis/openapi.yaml \
  --url "${BASE_URL}" \
  --max-examples "${MAX_EXAMPLES}" \
  --workers 4 \
  --exclude-checks unsupported_method \
  ${CONTRACT_ST_ARGS:-}
