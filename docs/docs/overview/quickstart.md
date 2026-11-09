---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
description: "Prerequisites, make targets, running the tests and the first calls against dock-scheduling, taken from the Makefile, the OpenAPI examples and the BDD features."
---

# Quickstart

Source: `Makefile`, `go.mod`, `apis/openapi.yaml`, `features/*.feature`, `charts/dock-scheduling/values.yaml`.

This page is written from the repository, not from a recorded session: no binary, container, Postgres or Kafka was started to produce it, and the outputs shown in the examples come from the specification, not from a run.

## What you can do without infrastructure

Everything in the quality gate runs with no database and no broker: the unit tests, the `httptest` REST tests and the godog features use the in-memory test doubles (`internal/adapters/outbound/memory`). Only the integration suite needs Docker (testcontainers).

## Prerequisites

- **Go**: the version in `go.mod` (the `Dockerfile` builds with `golang:1.27-alpine`).
- `make`, `git`.
- For `make lint`: `golangci-lint` v2.14.0 (the Makefile prints the `go install` line when it is missing).
- For `make mutation` / `make mutation-full`: `gremlins` v0.6.0; for `make vuln`: `govulncheck`.
- For `make integration` only: Docker, because every integration test boots its own Postgres or Kafka through testcontainers (no `DATABASE_URL`, no compose file).

The platform runs **one** Kafka broker and a k3s cluster; the old kind cluster and the docker-compose Kafka are retired, and this repository has no compose file.

## Make targets

| Target | What it runs |
| --- | --- |
| `make help` | Lists the targets (default). |
| `make build` | `go build ./...` |
| `make vet` | `go vet ./...` |
| `make fmt` / `make fmt-check` | Format in place / fail on unformatted files. |
| `make lint` | `golangci-lint run --allow-parallel-runners ./...` |
| `make test` | `go test ./... -race`: unit, `httptest` and godog, no DB. |
| `make coverage` | The CI coverage run with a 90 % gate over `./internal/domain/...` and `./internal/application/...`. |
| `make integration` | `go test -tags=integration ./... -race -count=1` (Docker needed, not part of `check`). |
| `make bdd` | `go test ./... -run TestFeatures -v` (godog). |
| `make arch-test` | `go test ./internal/architecture/... -v` (fitness tests). |
| `make mutation` / `make mutation-full` | gremlins on the three domain packages / on `./internal/domain`. |
| `make vuln` | `govulncheck ./...` |
| `make check` | `fmt-check vet build lint test`: the fast pre-commit bundle (lefthook runs it on push). |
| `make check-all` | `check` plus `coverage arch-test bdd`: run before pushing. |

Details of the pyramid are in [Testing](/docs/development/testing).

## Running the service

`cmd/api` has no in-memory mode: it refuses to boot without Postgres and Kafka. To run it you need a reachable Postgres, a Kafka broker (the platform's one broker, or any broker you control) and four consumer group names. With those:

```bash
export DATABASE_URL='postgres://USER:PASSWORD@HOST:5432/dock_scheduling'
export KAFKA_BROKERS='broker-host:9092'
export YARD_CONSUMER_GROUP=dock-scheduling-yard
export FACILITY_CONSUMER_GROUP=dock-scheduling-facility
export ASN_CONSUMER_GROUP=dock-scheduling-asn
export CPT_CONSUMER_GROUP=dock-scheduling-cpt
go run ./cmd/api
```

It applies the embedded migrations at boot, then serves on `:8080`. The other binaries (`cmd/mcp`, `cmd/projector`, `cmd/reports`, `cmd/relay`, `cmd/sweep`) are started the same way with their own variables: see [Configuration](/docs/operations/configuration). In the cluster the Helm chart `charts/dock-scheduling` renders all of it; see the [Runbook](/docs/operations/runbook). The service is built on develop and registered in `warehouse-infra` develop, but not yet applied to the cluster (no pod runs yet).

## First calls

A booking needs the door (a `facility-layout` slot fact) and, for an inbound booking, the ASN (an `inbound-receiving` fact) to be in the local copies, and those arrive over Kafka. On a fresh database the first `POST /appointments` therefore answers 422 `unknown-door` until the facility events have been consumed. The BDD features seed the same facts directly with step definitions.

The calls below use the examples of `apis/openapi.yaml`. Every `POST` needs an `Idempotency-Key`.

```bash
# liveness and readiness
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz

# declare booking settings for site WH1 (a PUT replaces them)
curl -s -X PUT localhost:8080/sites/WH1/dock-calendar/settings \
  -H 'Content-Type: application/json' \
  -d '{"maxWindowMinutes":240,"slotGranularityMinutes":15,"arrivalToleranceMinutes":15,"noShowGraceMinutes":60}'

# read the calendar and a door's free windows
curl -s localhost:8080/sites/WH1/dock-calendar
curl -s 'localhost:8080/doors/WH1-DOCK-IN-01/availability?from=2026-11-06T00:00:00Z&to=2026-11-07T00:00:00Z'

# book an inbound window
curl -s -X POST localhost:8080/appointments \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"direction":"INBOUND","doorCode":"WH1-DOCK-IN-01","carrier":"ACME Freight","carrierScac":"ACMF","windowStart":"2026-11-06T08:00:00Z","windowEnd":"2026-11-06T10:00:00Z","asnNumbers":["ASN-1001"],"expectedSealNumber":"SEAL-77120"}'

# then, with the appointment id from the answer
curl -s localhost:8080/appointments/appt-123e4567-e89b-12d3-a456-426614174000
curl -s -X POST localhost:8080/appointments/appt-123e4567-e89b-12d3-a456-426614174000/eta-reports \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"etaAt":"2026-11-06T09:10:00Z","source":"carrier-portal"}'
curl -s -X POST localhost:8080/appointments/appt-123e4567-e89b-12d3-a456-426614174000/reschedules \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"doorCode":"WH1-DOCK-IN-02","windowStart":"2026-11-06T09:00:00Z","windowEnd":"2026-11-06T11:00:00Z"}'
curl -s -X POST localhost:8080/appointments/appt-123e4567-e89b-12d3-a456-426614174000/cancellations \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"reason":"CARRIER_REQUEST"}'

# declare operating hours and schedule a closure
curl -s -X PUT localhost:8080/sites/WH1/dock-calendar/operating-hours \
  -H 'Content-Type: application/json' \
  -d '{"operatingHours":[{"weekday":"MON","opensAt":"06:00","closesAt":"22:00","timezone":"America/Sao_Paulo"}]}'
curl -s -X POST localhost:8080/sites/WH1/dock-calendar/closures \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"doorCode":"WH1-DOCK-IN-01","from":"2026-11-10T00:00:00Z","to":"2026-11-11T00:00:00Z","reason":"Leveller maintenance"}'

# the no-show sweep (the CronJob does this every 5 minutes)
curl -s -X POST localhost:8080/no-show-sweeps -H "Idempotency-Key: $(uuidgen)"
```

A refusal is an RFC 7807 problem, for example `409 door-double-booked` or `422 window-in-past`; the full list is in [Troubleshooting](/docs/operations/troubleshooting) and the [API reference](/docs/api-reference). Behind Kong the same API is under `/api/dock-scheduling`.

The behaviour these calls rely on is specified by the godog features: `features/door_booking.feature` (no double booking across directions, flow mismatch, half-open windows), `door_calendar.feature` (hours, closures, settings), `asn_single_appointment.feature`, `yard_facts.feature` (arrival, docking, release applied from yard events), `no_show_sweep.feature` and `rest_contract.feature`.

## MCP and reports

- `cmd/mcp` serves MCP at `http://localhost:8090/mcp`; see [MCP tools](/docs/mcp/tools).
- `cmd/reports` serves `GET /reports/schedule-adherence` and `GET /reports/freshness` on `:8092` over the analytical database, which `cmd/projector` fills from the analytics topic.
