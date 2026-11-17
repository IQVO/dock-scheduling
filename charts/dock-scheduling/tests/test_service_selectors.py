#!/usr/bin/env python3
"""Assert this chart's Service selectors isolate each component.

Why this exists (fleet-wide): `app.kubernetes.io/name` + `instance` are
identical on every pod a release creates (api, mcp, relay,
analytics-projector, analytics-reports). A Service that selects on those two
alone selects ALL of them -- verified live in other fleet charts, where a
Kong request for an api /healthz was answered by the wrong pod. Every
Deployment here carries `app.kubernetes.io/component` in selector.matchLabels
and its pod labels, and every Service selects on it, so each Service selects
EXACTLY ONE Deployment. The no-show sweep CronJob's pods carry the same
name/instance labels too, so this test also proves no Service ever selects a
Job pod. It additionally checks each Deployment renders the env its binary
reads and never the env of a sibling binary (the chart-vs-binary contract of
cmd/*/main.go's "Environment:" header comments), with no duplicates.

Run: python3 charts/dock-scheduling/tests/test_service_selectors.py
Needs: helm, PyYAML.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "dock-scheduling"  # contains the chart name, so fullname == RELEASE

# Dummy values, no password: the chart refuses to render without a database
# source, Kafka brokers and the four consumer groups (the real runtime).
DB = ["--set", "database.url=postgres://u@example.invalid:5432/db"]
KAFKA = ["--set", "kafka.brokers=kafka.example.invalid:9092"]
GROUPS = [
    "--set", "config.yardConsumerGroup=dock-scheduling-yard",
    "--set", "config.facilityConsumerGroup=dock-scheduling-facility",
    "--set", "config.asnConsumerGroup=dock-scheduling-asn",
    "--set", "config.cptConsumerGroup=dock-scheduling-cpt",
]
BASE = DB + KAFKA + GROUPS
ENABLE_EVERYTHING = BASE + [
    "--set", "mcp.enabled=true",
    "--set", "relay.enabled=true",
    "--set", "analytics.enabled=true",
    "--set", "analytics.database.projectorUrl=postgres://projector@example.invalid:5432/a",
    "--set", "analytics.database.reportsUrl=postgres://reports@example.invalid:5432/a",
    "--set", "config.outboxRelayInterval=7s",
    "--set", "config.outboxBatchSize=50",
    "--set", "config.shutdownDrainDelay=3s",
    "--set", "autoscaling.api.enabled=true",
    "--set", "gatewayApi.enabled=true",
    "--set", "ingress.enabled=true",
]

# Env each Deployment's binary actually reads (cmd/*/main.go header
# comments). Everything else must NOT appear on that Deployment.
WANT_ENV: dict[str, set[str]] = {
    RELEASE: {  # cmd/api
        "HTTP_ADDR", "LOG_LEVEL", "DATABASE_URL", "MIGRATIONS_DATABASE_URL", "KAFKA_BROKERS",
        "YARD_CONSUMER_GROUP", "FACILITY_CONSUMER_GROUP", "ASN_CONSUMER_GROUP", "CPT_CONSUMER_GROUP",
        "OUTBOX_RELAY_INTERVAL", "OUTBOX_BATCH_SIZE",
        "OUTBOX_MAX_ATTEMPTS", "SHUTDOWN_DRAIN_DELAY", "ENVIRONMENT", "SERVICE_VERSION",
        "OTEL_EXPORTER_OTLP_ENDPOINT",
    },
    f"{RELEASE}-mcp": {  # cmd/mcp
        "MCP_ADDR", "LOG_LEVEL", "DATABASE_URL", "MIGRATIONS_DATABASE_URL", "KAFKA_BROKERS",
        "SHUTDOWN_DRAIN_DELAY", "ENVIRONMENT", "SERVICE_VERSION", "OTEL_EXPORTER_OTLP_ENDPOINT",
    },
    f"{RELEASE}-relay": {  # cmd/relay
        "ADMIN_ADDR", "OUTBOX_RELAY_INTERVAL", "OUTBOX_BATCH_SIZE", "OUTBOX_MAX_ATTEMPTS",
        "DATABASE_URL", "KAFKA_BROKERS",
    },
    f"{RELEASE}-projector": {  # cmd/projector
        "ANALYTICS_DATABASE_URL", "KAFKA_BROKERS", "ANALYTICS_CONSUMER_GROUP", "ADMIN_ADDR",
        "LOG_LEVEL", "SHUTDOWN_DRAIN_DELAY",
    },
    f"{RELEASE}-reports": {  # cmd/reports
        "ANALYTICS_DATABASE_URL", "HTTP_ADDR", "LOG_LEVEL", "SHUTDOWN_DRAIN_DELAY",
        "ENVIRONMENT", "SERVICE_VERSION", "OTEL_EXPORTER_OTLP_ENDPOINT",
    },
}
# Required on the everything-set render (set through values), so a rename in
# values.yaml cannot silently stop rendering a var the binary reads.
REQUIRED_WHEN_SET = {
    RELEASE: {"OUTBOX_RELAY_INTERVAL", "OUTBOX_BATCH_SIZE",
              "OUTBOX_MAX_ATTEMPTS", "SHUTDOWN_DRAIN_DELAY"},
    f"{RELEASE}-mcp": {"SHUTDOWN_DRAIN_DELAY"},
    f"{RELEASE}-relay": {"OUTBOX_RELAY_INTERVAL", "OUTBOX_BATCH_SIZE", "OUTBOX_MAX_ATTEMPTS"},
    f"{RELEASE}-projector": {"SHUTDOWN_DRAIN_DELAY"},
    f"{RELEASE}-reports": {"SHUTDOWN_DRAIN_DELAY"},
}
COMMANDS = {
    f"{RELEASE}-mcp": ["/app/mcp"],
    f"{RELEASE}-relay": ["/app/relay"],
    f"{RELEASE}-projector": ["/app/projector"],
    f"{RELEASE}-reports": ["/app/reports"],
}


def render(extra_args: list[str]) -> list[dict]:
    result = subprocess.run(
        ["helm", "template", RELEASE, str(CHART_DIR), *extra_args],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise SystemExit(f"FAIL: helm template {' '.join(extra_args)}:\n{result.stderr}")
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available; cannot assert selectors", file=sys.stderr)
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(result.stdout) if d]


def selector_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("selector") or {}


def pod_labels_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("template", {}).get("metadata", {}).get("labels") or {}


def matches(selector: dict, labels: dict) -> bool:
    return bool(selector) and all(labels.get(k) == v for k, v in selector.items())


def env_names(dep: dict) -> list[str]:
    return [e["name"] for e in dep["spec"]["template"]["spec"]["containers"][0].get("env", [])]


def check_components(docs: list[dict], failures: list[str]) -> None:
    services = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Service"}
    deployments = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Deployment"}
    cronjobs = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "CronJob"}

    if RELEASE not in services:
        failures.append("the api Service was not rendered")
    elif selector_of(services[RELEASE]).get("app.kubernetes.io/component") != "api":
        failures.append("the api Service selector must pin component=api")

    mcp = f"{RELEASE}-mcp"
    if mcp not in services:
        failures.append("the MCP Service was not rendered with mcp.enabled=true")
    elif selector_of(services[mcp]).get("app.kubernetes.io/component") != "mcp":
        failures.append("the MCP Service selector must pin component=mcp")

    reports = f"{RELEASE}-reports"
    if reports not in services:
        failures.append("the reports Service was not rendered with analytics.enabled=true")
    elif selector_of(services[reports]).get("app.kubernetes.io/component") != "analytics-reports":
        failures.append("the reports Service selector must pin component=analytics-reports")

    # The dedicated relay renders NO Service at all (its /healthz is probed
    # directly); nothing else may select its pods.
    for name in services:
        if "relay" in name:
            failures.append(f"a Service {name} was rendered for the relay, which owns none")

    # Every Deployment's own selector must pin a component too, and be
    # satisfied by its pod labels.
    for name, dep in deployments.items():
        match_labels = dep["spec"]["selector"].get("matchLabels") or {}
        if "app.kubernetes.io/component" not in match_labels:
            failures.append(f"Deployment {name} selector.matchLabels lacks app.kubernetes.io/component")
        if not matches(match_labels, pod_labels_of(dep)):
            failures.append(f"Deployment {name} pod labels do not satisfy its own selector")
        container = dep["spec"]["template"]["spec"]["containers"][0]
        if name in COMMANDS and container.get("command") != COMMANDS[name]:
            failures.append(f"Deployment {name} must run command {COMMANDS[name]}")
        env = env_names(dep)
        want = WANT_ENV.get(name)
        if want is None:
            failures.append(f"unexpected Deployment {name}: extend WANT_ENV in this test")
            continue
        stray = set(env) - want
        if stray:
            failures.append(f"Deployment {name} renders env its binary never reads: {sorted(stray)}")
        missing = REQUIRED_WHEN_SET.get(name, set()) - set(env)
        if missing:
            failures.append(f"Deployment {name} does not render {sorted(missing)} on the everything-set render")
        duplicates = sorted({n for n in env if env.count(n) > 1})
        if duplicates:
            failures.append(f"Deployment {name} renders env vars twice: {duplicates}")

    # The real invariant: each Service selects exactly one Deployment...
    for svc_name, svc in services.items():
        sel = selector_of(svc)
        hit = [d for d, dep in deployments.items() if matches(sel, pod_labels_of(dep))]
        if len(hit) != 1:
            failures.append(f"Service {svc_name} selects {len(hit)} Deployments {sorted(hit)}; expected exactly 1")

    # ...and NEVER a CronJob pod (the sweep shares name/instance labels).
    for svc_name, svc in services.items():
        sel = selector_of(svc)
        hit = [j for j, job in cronjobs.items() if matches(sel, pod_labels_of(job["spec"]["jobTemplate"])) ]
        if hit:
            failures.append(f"Service {svc_name} selects CronJob pods {hit}; a Job pod must never match a Service selector")

    # The api HPA owns replicas when enabled.
    hpa = next((d for d in docs if d.get("kind") == "HorizontalPodAutoscaler"), None)
    if hpa is None or hpa["spec"]["scaleTargetRef"]["name"] != RELEASE:
        failures.append("the api HPA was not rendered (or does not scale the api Deployment)")
    elif "replicas" in deployments.get(RELEASE, {}).get("spec", {}):
        failures.append("the api Deployment must omit replicas when its HPA owns them")

    # Optional components stay off the default render.
    default = render(BASE)
    names = [d["metadata"]["name"] for d in default]
    for suffix in ("-mcp", "-relay", "-projector", "-reports"):
        if any(n.endswith(suffix) for n in names):
            failures.append(f"an optional component rendered on the default render: {suffix}")


def main() -> None:
    failures: list[str] = []
    check_components(render(ENABLE_EVERYTHING), failures)
    if failures:
        for f in failures:
            print(f"FAIL: {f}", file=sys.stderr)
        raise SystemExit(1)
    print("PASS: dock-scheduling Service selectors isolate every component")


if __name__ == "__main__":
    main()
