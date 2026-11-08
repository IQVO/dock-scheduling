#!/usr/bin/env python3
"""Assert the no-show sweep CronJob renders correctly.

The model's NoShowSweep policy is REST-triggered (POST /no-show-sweeps) and
deliberately has no in-process timer: a Kubernetes CronJob is the external
scheduler. This test pins that contract:

- DEFAULT values render exactly one CronJob (dock-scheduling-no-show-sweep),
  every 5 minutes, POSTing an empty body with a fresh Idempotency-Key to
  this release's own Service at /no-show-sweeps, with concurrencyPolicy
  Forbid and bounded start/active deadlines + history.
- sweeps.enabled=false renders no CronJob; sweeps.noShow.schedule is
  overridable.
- The CronJob pods must never match a Deployment's Service selector.
- Fail fast (copied from fulfillment-execution's ADR 0048 shape): the Job
  runs /app/sweep (per-attempt timeout, bounded backed-off retry, non-zero
  exit on give-up, Istio sidecar quit on every exit path), backoffLimit 0,
  an activeDeadlineSeconds, and the retry budget (attempts x timeout +
  (attempts - 1) x max backoff) fits inside that deadline; a values set
  that breaks the budget fails the render.

Run: python3 charts/dock-scheduling/tests/test_sweeps_cronjob.py
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "dock-scheduling"
JOB_NAME = f"{RELEASE}-no-show-sweep"
SWEEP_PATH = "/no-show-sweeps"
# Postgres is the only runtime: the chart refuses to render without a
# DATABASE_URL source, brokers and the four consumer groups.
REQUIRED = [
    "--set", "database.url=postgres://u@example.invalid:5432/db",
    "--set", "kafka.brokers=kafka.example.invalid:9092",
    "--set", "config.yardConsumerGroup=dock-scheduling-yard",
    "--set", "config.facilityConsumerGroup=dock-scheduling-facility",
    "--set", "config.asnConsumerGroup=dock-scheduling-asn",
    "--set", "config.cptConsumerGroup=dock-scheduling-cpt",
]

WANT_ENV = {
    "SWEEP_REQUEST_TIMEOUT": "15s",
    "SWEEP_MAX_ATTEMPTS": "4",
    "SWEEP_RETRY_BACKOFF": "1s",
    "SWEEP_MAX_BACKOFF": "8s",
    "SIDECAR_QUIT_URL": "http://127.0.0.1:15020/quitquitquit",
}


def render_raw(extra_args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["helm", "template", RELEASE, str(CHART_DIR), *REQUIRED, *extra_args],
        capture_output=True, text=True, check=False,
    )


def render(extra_args: list[str]) -> list[dict]:
    proc = render_raw(extra_args)
    if proc.returncode != 0:
        raise SystemExit(f"FAIL: helm template {extra_args} failed: {proc.stderr}")
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available", file=sys.stderr)
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(proc.stdout) if d]


def cronjobs(docs: list[dict]) -> dict[str, dict]:
    return {d["metadata"]["name"]: d for d in docs if d.get("kind") == "CronJob"}


def seconds(value: str) -> int:
    if not value.endswith("s") or not value[:-1].isdigit():
        raise ValueError(f"expected whole seconds like '15s', got {value!r}")
    return int(value[:-1])


def check_fail_fast(name: str, job_spec: dict) -> list[str]:
    """The sweep entrypoint fails fast and the budget fits the deadline."""
    failures: list[str] = []
    if job_spec.get("backoffLimit") != 0:
        failures.append(f"{name}: backoffLimit must be 0, got {job_spec.get('backoffLimit')!r}")
    deadline = job_spec.get("activeDeadlineSeconds")
    if not deadline:
        failures.append(f"{name}: activeDeadlineSeconds must be set")
        return failures
    container = job_spec["template"]["spec"]["containers"][0]
    if container.get("command") != ["/app/sweep"]:
        failures.append(f"{name}: command must be ['/app/sweep'] (the fail-fast entrypoint), "
                        f"got {container.get('command')!r}")
    env = {e["name"]: e.get("value") for e in container.get("env") or []}
    if env.get("SWEEP_URL") != f"http://{RELEASE}:80{SWEEP_PATH}":
        failures.append(f"{name}: SWEEP_URL must target the release Service, got {env.get('SWEEP_URL')!r}")
    for key, value in WANT_ENV.items():
        if env.get(key) != value:
            failures.append(f"{name}: {key} must default to {value!r}, got {env.get(key)!r}")
    try:
        attempts = int(env.get("SWEEP_MAX_ATTEMPTS") or "0")
        budget = attempts * seconds(env.get("SWEEP_REQUEST_TIMEOUT") or "") + \
            (attempts - 1) * seconds(env.get("SWEEP_MAX_BACKOFF") or "")
    except ValueError as exc:
        failures.append(f"{name}: unparsable retry settings: {exc}")
        return failures
    if budget >= deadline:
        failures.append(f"{name}: retry budget {budget}s must be below activeDeadlineSeconds {deadline}s")
    return failures


def check_budget_guard() -> list[str]:
    """A values set whose retry budget exceeds the deadline must fail the render."""
    failures: list[str] = []
    proc = render_raw(["--set", "sweeps.requestTimeoutSeconds=60"])
    if proc.returncode == 0:
        failures.append("requestTimeoutSeconds=60 (budget 4x60+3x8 > 120) must fail the render")
    elif "activeDeadlineSeconds" not in proc.stderr:
        failures.append(f"budget guard must name activeDeadlineSeconds, got: {proc.stderr.strip()}")
    docs = render(["--set", "sweeps.sidecarQuitUrl="])
    for name, job in cronjobs(docs).items():
        container = job["spec"]["jobTemplate"]["spec"]["template"]["spec"]["containers"][0]
        env = {e["name"]: e.get("value") for e in container.get("env") or []}
        if env.get("SIDECAR_QUIT_URL", None) != "":
            failures.append(f"{name}: sweeps.sidecarQuitUrl='' must render SIDECAR_QUIT_URL empty "
                            f"(quit disabled), got {env.get('SIDECAR_QUIT_URL')!r}")
    return failures


def main() -> int:
    failures: list[str] = []

    if cronjobs(render(["--set", "sweeps.enabled=false"])):
        failures.append("sweeps.enabled=false must render no CronJob")

    # Defaults: the sweep is ON -- no --set needed.
    docs = render([])
    jobs = cronjobs(docs)
    if set(jobs) != {JOB_NAME}:
        failures.append(f"expected exactly [{JOB_NAME}] by default, got {sorted(jobs)}")
    job = jobs.get(JOB_NAME)
    if job:
        spec = job["spec"]
        if spec["schedule"] != "*/5 * * * *":
            failures.append(f"default schedule must be '*/5 * * * *', got {spec['schedule']!r}")
        if spec["concurrencyPolicy"] != "Forbid":
            failures.append("concurrencyPolicy must be Forbid")
        if not spec.get("startingDeadlineSeconds"):
            failures.append("startingDeadlineSeconds must be set")
        for key in ("successfulJobsHistoryLimit", "failedJobsHistoryLimit"):
            if key not in spec:
                failures.append(f"{key} must be set")
        job_spec = spec["jobTemplate"]["spec"]
        pod = job_spec["template"]["spec"]
        if pod["restartPolicy"] != "Never":
            failures.append("restartPolicy must be Never")
        failures.extend(check_fail_fast(JOB_NAME, job_spec))

    # CronJob pods must not be selectable by any Service in the chart.
    services = [d for d in docs if d.get("kind") == "Service"]
    for name, job in jobs.items():
        labels = job["spec"]["jobTemplate"]["spec"]["template"]["metadata"]["labels"]
        for svc in services:
            sel = svc["spec"].get("selector") or {}
            if sel and all(labels.get(k) == v for k, v in sel.items()):
                failures.append(f"{name}: pods match Service {svc['metadata']['name']}")

    docs = render(["--set", "sweeps.noShow.schedule=*/15 * * * *"])
    job = cronjobs(docs).get(JOB_NAME)
    if not job or job["spec"]["schedule"] != "*/15 * * * *":
        failures.append("sweeps.noShow.schedule must be overridable")

    failures.extend(check_budget_guard())

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1
    print("PASS: the no-show sweep CronJob is ON by default (every 5 minutes, Forbid, "
          "/app/sweep fail-fast entrypoint, retry budget inside the deadline, sidecar quit); "
          "sweeps.enabled=false and the schedule override work, pods unreachable via Services")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
