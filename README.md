# dock-scheduling

Dock Scheduling (WMS-tier Supporting bounded context of the yard domain): Owns the door calendar of every site: every booked time window on every dock door, inbound and outbound, from booking through arrival to door release, the no-double-booking rule, door opening hours and closures, ETA-driven risk and no-shows.

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

Status: built on develop, registered in warehouse-infra develop (`terraform/apps.yaml` entry, helm values, dashboard and alert rules, image pinned at v0.1.2) and applied to the k3s cluster: read-only on 2026-11-10 the Argo CD application is Synced and Healthy and the `api` pod runs `v0.1.2` (api only: MCP, dedicated relay, analytics and the no-show sweep are switched off in infra; see the [runbook](https://iqvo.github.io/dock-scheduling/docs/operations/runbook#what-runs-on-the-k3s-cluster)). The design lives in the yard instance of
[warehouse-docs](https://iqvo.github.io/warehouse-docs/yard/contexts/dock-scheduling/); `yard/model/model.yaml` there is the single source of truth.

Runtime: Go, hexagonal, real PostgreSQL and Kafka only (`DATABASE_URL`, `KAFKA_BROKERS` are mandatory), CloudEvents 1.0 on every message
(`com.warehouse.wms.dock-scheduling.<entity>.<Event>`), transactional outbox.

See `docs/adr/` for decisions.

## Documentation

The documentation site of this service is published at **https://iqvo.github.io/dock-scheduling/** (source in `docs/`, built by `.github/workflows/docs.yml`). The design also appears in the yard instance of [warehouse-docs](https://iqvo.github.io/warehouse-docs/yard/contexts/dock-scheduling/).

| Page | What it holds |
| --- | --- |
| [Introduction](https://iqvo.github.io/dock-scheduling/docs/overview/introduction) | What the service owns and does not own, its capabilities |
| [Architecture](https://iqvo.github.io/dock-scheduling/docs/overview/architecture) | Hexagonal layout, the six binaries, container diagram, data stores |
| [Quickstart](https://iqvo.github.io/dock-scheduling/docs/overview/quickstart) | Prerequisites, make targets, first calls |
| [Configuration](https://iqvo.github.io/dock-scheduling/docs/operations/configuration) | Every environment variable per binary |
| [Runbook](https://iqvo.github.io/dock-scheduling/docs/operations/runbook) | Helm chart, probes, migrations, Kafka topics and groups, outbox relay, DLQ, sweep |
| [Observability](https://iqvo.github.io/dock-scheduling/docs/operations/observability) | Metrics, spans, logs, dashboards |
| [Troubleshooting](https://iqvo.github.io/dock-scheduling/docs/operations/troubleshooting) | Symptom, cause, check, fix |
| [Before the first release](https://iqvo.github.io/dock-scheduling/docs/operations/before-first-release) | Packaging checklist and its state |
| [Testing](https://iqvo.github.io/dock-scheduling/docs/development/testing) | The test pyramid, make targets, CI jobs |
| [Use cases](https://iqvo.github.io/dock-scheduling/docs/ddd/use-cases) | Every command use case |
| [Subdomain classification](https://iqvo.github.io/dock-scheduling/docs/ddd/subdomain-classification) | Why Supporting, and the neighbours |
| [DDD artifacts](https://iqvo.github.io/dock-scheduling/docs/ddd/ddd-artifacts) | Ubiquitous language, core domain chart, canvases, context map, EventStorming, diagrams, domain events |
| [API reference](https://iqvo.github.io/dock-scheduling/docs/api-reference) | Interfaces, endpoints, contract conventions |
| [Events](https://iqvo.github.io/dock-scheduling/docs/api-reference/events) | The CloudEvents catalogue |
| [MCP tools](https://iqvo.github.io/dock-scheduling/docs/mcp/tools) | Every MCP tool, resource and prompt |
| [Integration](https://iqvo.github.io/dock-scheduling/docs/ecosystem/integration) | Upstreams, downstreams, absent edges |
| [ADRs](https://iqvo.github.io/dock-scheduling/docs/adr) | The ten architecture decision records |

## Binaries

| Binary | Role |
| --- | --- |
| `cmd/api` | REST API, in-process outbox relay and the Kafka consumers |
| `cmd/mcp` | MCP server over the same use cases |
| `cmd/projector` | The only writer of the analytical database |
| `cmd/reports` | Read-only reports over the analytical database |
| `cmd/relay` | Optional stand-alone outbox relay |
| `cmd/sweep` | One-shot CronJob entrypoint for the no-show sweep |

Ports, probes and environment variables per binary are on the
[Architecture](https://iqvo.github.io/dock-scheduling/docs/overview/architecture) and
[Configuration](https://iqvo.github.io/dock-scheduling/docs/operations/configuration) pages.

