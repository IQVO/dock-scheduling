---
id: testing
title: Testing
sidebar_label: Testing
description: "The dock-scheduling test pyramid as it is: unit, httptest, godog features, integration, contract, mutation and fitness tests, the make targets and the CI jobs."
---

# Testing

Source: `Makefile`, `.github/workflows/ci.yml`, `.gremlins.yaml`, `features/`, `internal/architecture/`, `internal/testing/`, `charts/dock-scheduling/tests/`.

Counts below come from `grep` over the tree on `develop`, not from a test run: about 480 `func Test...` in `*_test.go` under `internal/`, `cmd/` and `features/`, of which 55 sit in 25 integration-tagged files and 37 are architecture fitness tests.

## The pyramid

| Layer | Where | What it proves | Needs |
| --- | --- | --- | --- |
| Unit (domain) | `internal/domain/{dockappointment,dockcalendar,shared}/*_test.go` | Aggregate invariants, state machine, window and overlap rules, availability, snapshots. | nothing |
| Unit (application) | `internal/application/usecases/*_test.go` | Use cases against the in-memory doubles: booking checks, change flows, fact consumption, idempotent wrapper, initiator attribution. | nothing |
| Adapter unit | `internal/adapters/**/*_test.go`, `cmd/*/main_test.go` | `httptest` REST handlers and problem mapping, idempotency middleware, readiness, CloudEvents encoder/decoder, Kafka consume loop with fake readers, relay with a fake store, MCP tools, telemetry, boot configuration of every binary, sweep retry logic (`cmd/sweep/sweep_test.go`). | nothing |
| BDD acceptance | `features/*.feature` + `*_steps_test.go` | The behaviour through the REST router with the in-memory doubles. 6 feature files, 49 scenarios: `door_booking` (10), `yard_facts` (10, one a Scenario Outline for the arrival status), `door_calendar` (9), `asn_single_appointment` (7), `no_show_sweep` (7), `rest_contract` (6). Runner `features/features_test.go` (`TestFeatures`, godog). | nothing |
| Integration | files with `//go:build integration` in `cmd/api`, `cmd/mcp`, `cmd/projector`, `cmd/reports`, `internal/adapters/inbound/kafka`, `.../outbound/{analyticsstore,outbox,postgres}` | The real Postgres and Kafka adapters, migrations, the outbox relay (including the attempt cap), consumers end to end, the real `run` entrypoints started and stopped. Every test boots its own containers via testcontainers (`internal/testing/{pgtest,kafkatest}`); `TestPostgresIntegrationTestsUseTestcontainers` and `TestKafkaIntegrationTestsUseTestcontainers` forbid a `DATABASE_URL` gate or a `t.Skip`. | Docker |
| Contract | `internal/testing/repocontract` (run against the memory **and** Postgres adapters), `internal/adapters/inbound/http/reports_contract_test.go` (routes and responses against `apis/openapi-reports.yaml`), `TestEventCatalogueMatchesContract` (ADR 0004 catalogue against `apis/asyncapi.yaml`) | The two repository implementations behave the same; the reports router matches its spec; the event catalogue matches the AsyncAPI. | Docker for the Postgres half |
| Mutation | `.gremlins.yaml`, `make mutation`, `make mutation-full` | Whether the domain tests assert rather than merely execute. Measured (gremlins v0.6.0, one worker, timeout coefficient 30): `dockappointment` 100 % efficacy (106 killed, 0 lived), `dockcalendar` 100 % (104 killed, 0 lived), `shared` 100 % (14 killed, 0 lived). Thresholds: efficacy 95, mutant coverage 95 (gremlins fails at or below the threshold). | gremlins |
| Fitness | `internal/architecture` (37 tests) | See below. | nothing |
| Chart | `charts/dock-scheduling/tests/` | `render_test.sh`, `test_service_selectors.py`, `test_sweeps_cronjob.py`: mandatory values refuse to render, every env var is rendered, no `*_MODE`, secret for the DSN, hardening, grace period, each Service selects exactly one Deployment, the sweep retry budget fits `activeDeadlineSeconds`. | helm, PyYAML |

There is no end-to-end suite across services and no load test; the autoscaling note in the chart says N>1 api replicas have not been load-tested.

## Fitness tests (`internal/architecture`)

| Test | Guards |
| --- | --- |
| `TestHexagonalArchitecture` | Dependency direction (arch-go): the domain depends on nothing internal except the domain, and the layer rules of the hexagon. |
| `TestRuntimeBinaryHasNoInMemoryAdapters` | No `cmd/` imports the memory adapter. |
| `TestNoGoFilesAtRepoRoot` | No `.go` at the root. |
| `TestCloudEventsOnly`, `TestNoEventEnvelopeToggleOrFlatEnvelope` | CloudEvents 1.0 on every message; no flat envelope or toggle. |
| `TestEventCatalogueMatchesContract` | The published event catalogue equals the AsyncAPI. |
| `TestKafkaConsumerGroupNeverHardcodedInline`, `TestReplayConsumersSetCommitInterval` | Group ids come from the environment; commit behaviour. |
| `TestKafkaWritersSetRequiredAcks`, `TestKafkaWriterTopicNotSetTwice` | acks=all; the topic is set per message, not on the writer too. |
| `TestDeadLetterCallsGuardCancellation` | Dead-letter calls do not hang shutdown. |
| `TestOutboxRelaysPassAttemptCap` | Every relay a `cmd/` builds passes `WithMaxAttempts`. |
| `TestNoAuthMiddlewareReintroduced` | No auth middleware on REST or MCP. |
| `TestMCPAdapterDependencyRule`, `TestEveryMCPToolDeclaresAuthority`, `TestMCPWriteToolsAreAtLeastL3`, `TestMCPServerOffersResourceAndPrompt`, `TestMCPOptionalToolInputsCarryOmitempty` | MCP structure, authority levels, resource and prompt present, optional inputs optional. |
| `TestPostgresIntegrationTestsUseTestcontainers`, `TestKafkaIntegrationTestsUseTestcontainers` | Integration tests are self-contained. |
| `TestA2ASkillsDeclareAuthority`, `TestAgentAdapterSkipRules` | A2A skill rules; this repo has no A2A server, so they skip. |
| `*SensorBites` / `*SensorFailsOnBadFixtures` tests | Each scanning sensor is proven to fail on a bad fixture. |

## Make targets

`make test`, `make coverage` (90 % gate on domain and application), `make bdd`, `make arch-test`, `make integration`, `make mutation`, `make mutation-full`, `make lint`, `make vuln`, `make check`, `make check-all`; the full list with what each runs is in the [Quickstart](/docs/overview/quickstart). The agent harness adds `make check-fast`, `make guide-lint` and `make harness-test`.

## CI (`.github/workflows/ci.yml`)

Triggers: push and pull request on `main` and `develop`, a weekly schedule (Monday 06:00 UTC, mutation and drift jobs only) and manual dispatch.

| Job | What it runs |
| --- | --- |
| `lint` | golangci-lint. |
| `guide-lint` | `scripts/harness/guide_lint.py` and `test_hook.py`: the agent guides and hooks. |
| `complexity` | gocyclo report (informational, gate 15). |
| `file-length` | File length of the Go files a PR touches (blocking). |
| `test` | `make coverage` with the 90 % gate, uploads the profile. |
| `bdd` | `go test ./... -run TestFeatures -v`. |
| `integration` | `go test -tags=integration ./... -race -count=1`. |
| `mutation-fast` | gremlins over every domain package. |
| `mutation` | Exhaustive `gremlins unleash ./internal/domain --workers 1 --timeout-coefficient 30`; scheduled or manual, opens a `harness:red` issue on failure. |
| `drift` | Scheduled or manual: dead code (`deadcode`), `go mod tidy -diff`, coverage-quality check. |
| `api-lint` | Spectral on `apis/openapi.yaml`, `apis/openapi-reports.yaml` and `apis/asyncapi.yaml` (fail on warn). |
| `vuln` | `govulncheck ./...` (installed at `@latest`). |
| `chart-tests` | Every `charts/dock-scheduling/tests/*.sh` and `test_*.py`. |
| `arch-test` | `go test ./internal/architecture/... -v`. |
| `helm-lint` | PRs only: `ct lint` on the chart. |
| `trivy-scan` | PRs only: builds the image and scans it. |
| `docker-publish` | Push to `main`, after lint, complexity, test, bdd, integration, mutation-fast, vuln, api-lint and arch-test: build and push to `ghcr.io/iqvo`, cosign sign, SBOM attest. |
| `release` | Push to `main`, after `docker-publish`: tag the image, package and publish the chart. |

The documentation site has its own workflow, `.github/workflows/docs.yml` (build on pull requests, deploy to GitHub Pages on push to `main`).

## Adding a test

1. Pure rule: add a table test next to the aggregate (`internal/domain/...`) and check `make mutation` still reports no survivors.
2. A new behaviour visible over REST: add a scenario to the matching `features/*.feature` and, if needed, a step in `features/*_steps_test.go`.
3. A new adapter behaviour that needs a real database or broker: an integration test under `//go:build integration`, using `pgtest` or `kafkatest`; add the case to `repocontract` when it is a repository behaviour both adapters must share.
4. A new event or route: update `apis/asyncapi.yaml` or `apis/openapi.yaml`; the catalogue and contract tests will tell you what else moves.
