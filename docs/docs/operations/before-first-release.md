---
id: before-first-release
title: Before the first release
sidebar_label: Before the first release
description: "The packaging checklist to finish before the first develop to main PR of dock-scheduling, with its state on develop."
---

# Before the first release: dock-scheduling

The first merge to `main` runs `docker-publish` and `release`. Two services built from
this template (pick-face-replenishment, task-interleaving) each needed a rework because
packaging was missing or wrong when that moment came. Work through this list on a feature
branch, **before** the first `develop` → `main` PR. Delete a row only when the service
really does not have that part, and say so in the PR.

`new-service.sh` already generated `Dockerfile` and `.dockerignore` for you (verbatim from
the fleet reference). The chart is NOT generated: it is shaped by what the service runs
(consumer groups, MCP, analytics, frontend), so copy the closest sibling's
`charts/<repo>/` and adapt it. References on `origin/develop`:
`IQVO/pick-face-replenishment` (Kafka consumers, MCP, analytics, frontend) and
`IQVO/task-interleaving`.

| # | Item | Done when | Guarded by |
|---|------|-----------|------------|
| 1 | **Real runtime only.** `cmd/api` wires Postgres and Kafka; the in-memory adapters (`internal/adapters/outbound/memory`) are test doubles. No `*_MODE`/permissive fallback, no default that boots "healthy" without `DATABASE_URL` or `KAFKA_BROKERS`. | `cmd/**` has no import of the memory package; the binary exits at boot on missing `DATABASE_URL`, `KAFKA_BROKERS` or a consumer group id. | `TestRuntimeBinaryHasNoInMemoryAdapters` (`internal/architecture/runtime_no_memory_fitness_test.go`). The boot-error behaviour has no generic sensor: cover it in `cmd/api` tests and in the chart test (row 8). |
| 2 | **No Go file at the repo root.** The godog runner and steps live in `features/` (`package features_test`, `Paths: "."`), binaries in `cmd/<name>/`, everything else in `internal/`. | `ls *.go` at the root prints nothing; `make bdd` still finds `TestFeatures`. | `TestNoGoFilesAtRepoRoot` (`internal/architecture/no_go_files_at_root_fitness_test.go`). |
| 3 | **`Dockerfile` builds every `cmd/*`.** Generated; confirm `ENTRYPOINT ["./api"]` matches your main binary and `EXPOSE` matches `HTTP_ADDR`. | `docker build .` succeeds locally; `docker run --rm <image> /app/api` starts (or exits with the intended config error). | `trivy-scan` job builds the image on every PR to `main`/`develop`; `docker-publish` on push to `main`. |
| 4 | **`.dockerignore`** excludes docs, charts, harness, tests output. Generated. | Build context is small; `.git`, `.claude/`, `docs/`, `charts/` are not sent. | none: review once. |
| 5 | **Helm chart `charts/dock-scheduling/`** with `Chart.yaml` `name: dock-scheduling`. The chart directory name, the `Chart.yaml` name and the `helm package` file name in the `release` job must all be `dock-scheduling`. | `helm lint charts/dock-scheduling` and `helm template` with the required values both pass. | `helm-lint` (PRs) and the `release` job (packages `dock-scheduling-<version>.tgz`). |
| 6 | **`DATABASE_URL` comes from a Secret** (`secretKeyRef`, chart `secret.yaml` or `database.existingSecret`), never from the ConfigMap or a literal. | `helm template` shows `secretKeyRef` for `DATABASE_URL`; the ConfigMap has no DSN. | chart test (row 8). |
| 7 | **Pod hardening and shutdown room.** `runAsNonRoot`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`; `terminationGracePeriodSeconds` greater than the sum of the binary's shutdown bounds (drain delay + HTTP + consumers + relay). | Values and Deployment carry all of it; the chart refuses a grace period below the sum. | chart test (row 8). |
| 8 | **Chart tests in `charts/dock-scheduling/tests/`** (`render_test.sh`, `test_service_selectors.py`): envFrom ConfigMap mounted, mandatory values refuse to render, every env var `cmd/api` reads is rendered, no `*_MODE`, Secret for the DSN, hardening, grace period, every Service selects exactly one Deployment. Copy and adapt the reference tests. | `bash charts/dock-scheduling/tests/render_test.sh` prints `PASS`. | `chart-tests` job runs every `tests/*.sh` and `test_*.py`. **It only warns when the directory is empty**, so an absent test passes CI: the checklist is the guard. |
| 9 | **Consumer group ids are chart values**, stable and service-prefixed, never derived from the pod name. | Each `*_CONSUMER_GROUP` is rendered from `config.*` and refuses to render empty. | chart test (row 8); `kafka-testing-and-consumers.md` rule. |
| 10 | **Optional CI jobs match the service.** `web` (needs `web/`), `docs-api-drift` (needs `docs/` and `apis/openapi.yaml`), `api-lint` (needs `apis/*.yaml` and the spectral rulesets): delete a job and its `docker-publish` `needs:` entry if the service has no such part, or add the part. | No job in `ci.yml` is permanently red or skipped for a missing directory. | first PR's CI run. |
| 11 | **Registry access** images push to `ghcr.io/iqvo` with the Actions token (`packages: write` on `docker-publish`/`release`, already in `ci.yml`); no Docker Hub secrets are needed. | A dry-run of `docker-publish` on a throwaway branch, or ask the repo admin. | none. |
| 12 | **`govulncheck` is clean** before the PR to `main` (CI `vuln` job installs `@latest`; `make vuln` runs the same command). | `make vuln` exits 0. | `vuln` job (a `docker-publish` prerequisite). |

Tick this list in the PR that adds `Dockerfile`/chart wiring, and keep the file: it is the
service's packaging record. Reference for the three failures this list prevents: a runtime that
can run without Postgres/Kafka (row 1), Go files at the root (row 2), a release that fails on
the first push to `main` because packaging was never built (rows 3-8).

## State on develop

Checked against the code of `origin/develop`; the checklist is a record, so re-check each row before the PR to `main`.

| # | State | Evidence |
|---|-------|----------|
| 1 | Met | `cmd/api` refuses to boot without `DATABASE_URL`, `KAFKA_BROKERS` and the four `*_CONSUMER_GROUP` variables (`cmd/api/config.go`); `TestRuntimeBinaryHasNoInMemoryAdapters` is in `internal/architecture`. |
| 2 | Met | No `.go` file at the repo root; the godog runner is `features/features_test.go`; `TestNoGoFilesAtRepoRoot` exists. |
| 3 | Met | The `Dockerfile` builds every `cmd/*` (six binaries) into `/app`; `ENTRYPOINT ["./api"]`, `EXPOSE 8080 8090 8091 8092 8094` covers the API, MCP, projector, reports and relay ports. |
| 4 | Met | `.dockerignore` excludes `.git`, `.github`, `*.md`, `docs/`, `charts/`, `web/`. |
| 5 | Met | `charts/dock-scheduling/Chart.yaml`; CI has `helm-lint` and the `release` job. |
| 6 | Met | `DATABASE_URL` is a `secretKeyRef` in every Deployment; the chart refuses to render without `database.url` or `database.existingSecret`. |
| 7 | Partly | Hardening is in `values.yaml`. `terminationGracePeriodSeconds` is 40 and `render_test.sh` refuses lower values, but the code's await bounds (consumers 30 s, relay 30 s) exceed the 10 s budgeted in the comment. See the [runbook](/docs/operations/runbook). |
| 8 | Met | `charts/dock-scheduling/tests/` holds `render_test.sh`, `test_service_selectors.py` and `test_sweeps_cronjob.py`; the `chart-tests` job runs them. |
| 9 | Met | The five consumer groups are chart values; the chart refuses to render an empty one. |
| 10 | Met | `ci.yml` has `api-lint` (three specs) and no `web` or `docs-api-drift` job, matching a repo with neither `web/` nor a generated-docs drift check. |
| 11 | Not verified | Registry access is an org setting; not checked from the repository. |
| 12 | Not verified | `make vuln` was not run for this page. |

The service is **built on develop, not deployed**: `warehouse-infra` has no `terraform/apps.yaml` entry for it.
