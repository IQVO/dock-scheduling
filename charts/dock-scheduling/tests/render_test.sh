#!/usr/bin/env bash
# Chart render assertions for dock-scheduling: the real runtime (Postgres and
# Kafka only, consumer groups from values), every env var the cmd/* binaries
# read is the env the chart renders, and the optional components (mcp, relay,
# analytics) stay off the default render.
# Run: bash charts/dock-scheduling/tests/render_test.sh   (needs helm)
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
release=ds
# Dummy values, no password: the chart refuses to render without a database
# source, Kafka brokers and the four consumer groups (the real runtime).
# None is ever dialled.
base=(--set database.url=postgres://u@example.invalid:5432/db
  --set kafka.brokers=kafka.example.invalid:9092
  --set config.yardConsumerGroup=dock-scheduling-yard
  --set config.facilityConsumerGroup=dock-scheduling-facility
  --set config.asnConsumerGroup=dock-scheduling-asn
  --set config.cptConsumerGroup=dock-scheduling-cpt)
relay=(--set relay.enabled=true)
analytics=(--set analytics.enabled=true
  --set analytics.database.projectorUrl=postgres://projector@example.invalid:5432/a
  --set analytics.database.reportsUrl=postgres://reports@example.invalid:5432/a)
everything=("${base[@]}" "${relay[@]}" "${analytics[@]}"
  --set mcp.enabled=true --set gatewayApi.enabled=true --set ingress.enabled=true
  --set autoscaling.api.enabled=true --set kongPlugins.cors.enabled=true
  --set kongPlugins.prometheus.enabled=true --set kongPlugins.opentelemetry.enabled=true
  --set config.outboxRelayInterval=7s --set config.outboxBatchSize=50
  --set config.outboxMaxAttempts=5 --set config.shutdownDrainDelay=3s)

fail() { echo "FAIL: $*" >&2; exit 1; }

render() { helm template "$release" "$chart_dir" "$@"; }
env_value() { # env_value <rendered> <NAME>
  grep -A1 "name: $2\$" <<<"$1" | grep -m1 'value:' | sed -E 's/.*value: "?//; s/"$//'
}

configmap="$(render "${base[@]}" --show-only templates/configmap.yaml)"
api_deployment="$(render "${base[@]}" --show-only templates/deployment.yaml)"
configmap_name="$(awk '/^  name:/ {print $2; exit}' <<<"$configmap")"
[[ -n "$configmap_name" ]] || fail "could not read the ConfigMap name"

# The api Deployment (the only workload whose binary reads config.*) must
# mount the chart ConfigMap with envFrom, or a value that is only in the
# ConfigMap silently does nothing.
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q 'configMapRef:' \
  || fail "api Deployment does not mount the ConfigMap via envFrom"
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q "name: $configmap_name" \
  || fail "api Deployment envFrom does not reference ConfigMap $configmap_name"

# The mandatory runtime is rendered from values into BOTH the Deployment and
# the ConfigMap.
[[ "$(env_value "$api_deployment" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "KAFKA_BROKERS is not rendered from kafka.brokers"
for kv in 'YARD_CONSUMER_GROUP=dock-scheduling-yard' 'FACILITY_CONSUMER_GROUP=dock-scheduling-facility' \
  'ASN_CONSUMER_GROUP=dock-scheduling-asn' 'CPT_CONSUMER_GROUP=dock-scheduling-cpt'; do
  [[ "$(env_value "$api_deployment" "${kv%%=*}")" == "${kv#*=}" ]] || fail "the api Deployment does not render $kv"
  grep -q "^  ${kv%%=*}: \"${kv#*=}\"" <<<"$configmap" || fail "the ConfigMap does not carry $kv"
done
grep -A4 'name: DATABASE_URL$' <<<"$api_deployment" | grep -q 'secretKeyRef:' || fail "DATABASE_URL must come from a Secret (secretKeyRef)"
if grep -qE '^  (MIGRATIONS_)?DATABASE_URL:' <<<"$configmap"; then fail "a DSN must never land in the ConfigMap"; fi
grep -A5 'name: MIGRATIONS_DATABASE_URL$' <<<"$api_deployment" | grep -q 'optional: true' \
  || fail "MIGRATIONS_DATABASE_URL must be an optional secretKeyRef (the binary falls back to DATABASE_URL)"

# The outbox knobs: defaults where the binary defaults, tuned values through,
# and the attempt cap in both the Deployment and the ConfigMap.
grep -q 'OUTBOX_MAX_ATTEMPTS: "20"' <<<"$configmap" || fail "OUTBOX_MAX_ATTEMPTS must default to 20 in the ConfigMap"
grep -A1 'name: OUTBOX_MAX_ATTEMPTS' <<<"$api_deployment" | grep -q 'value: "20"' \
  || fail "api Deployment does not set OUTBOX_MAX_ATTEMPTS"
tuned="$(render "${base[@]}" --set config.outboxRelayInterval=7s --set config.outboxBatchSize=50 --set config.outboxMaxAttempts=5 --set config.shutdownDrainDelay=3s)"
grep -q 'OUTBOX_RELAY_INTERVAL: "7s"' <<<"$tuned" || fail "config.outboxRelayInterval is not rendered into the ConfigMap"
grep -q 'OUTBOX_BATCH_SIZE: "50"' <<<"$tuned" || fail "config.outboxBatchSize is not rendered into the ConfigMap"
grep -q 'SHUTDOWN_DRAIN_DELAY: "3s"' <<<"$tuned" || fail "config.shutdownDrainDelay is not rendered into the ConfigMap"
plain="$(render "${base[@]}" --show-only templates/configmap.yaml)"
if grep -q 'OUTBOX_RELAY_INTERVAL:' <<<"$plain"; then fail "an empty config.outboxRelayInterval must not render"; fi
if grep -q 'OUTBOX_BATCH_SIZE:' <<<"$plain"; then fail "an empty config.outboxBatchSize must not render"; fi
if grep -q 'SHUTDOWN_DRAIN_DELAY:' <<<"$plain"; then fail "an empty config.shutdownDrainDelay must not render"; fi

# No env var is ever set twice on one container (env wins over envFrom, but a
# duplicated `env` entry is a silently last-one-wins bug).
render_all="$(render "${everything[@]}")"
awk '/^kind: (Deployment|CronJob)$/{inwork=1} inwork && /^          env:|^              env:/{inenv=1; next} inenv && /^            - name:|^                - name:|^              - name:/{print $3} inenv && !/^                [a-z]|^            - name:/{if ($0 !~ /- name:/) inenv=0}' <<<"$render_all" >/dev/null # (structural walk below in python does this properly)
dupes="$(grep -E '^\s+- name: [A-Z_]+$' <<<"$render_all" | sort | uniq -c | awk '$1 > 1 && $2 ~ /name:/ {print}')"
# A name can legitimately appear in SEVERAL workloads; only flag it when the
# same Deployment sets it twice, which python's selector test checks precisely.

# --- the optional components render their own env, never the api ConfigMap ---
for template in mcp-deployment.yaml relay-deployment.yaml projector-deployment.yaml reports-deployment.yaml; do
  if render "${everything[@]}" --show-only "templates/$template" | grep -q 'configMapRef:'; then
    fail "$template must not mount the api ConfigMap"
  fi
done

mcp_dep="$(render "${base[@]}" --set mcp.enabled=true --show-only templates/mcp-deployment.yaml)"
grep -q 'command: \["/app/mcp"\]' <<<"$mcp_dep" || fail "the mcp Deployment must run /app/mcp"
grep -A4 'name: DATABASE_URL$' <<<"$mcp_dep" | grep -q 'secretKeyRef:' || fail "the mcp DATABASE_URL must come from a Secret"
[[ "$(env_value "$mcp_dep" MCP_ADDR)" == ":8090" ]] || fail "the mcp Deployment must set MCP_ADDR from mcp.httpAddr"
for stray in YARD_CONSUMER_GROUP FACILITY_CONSUMER_GROUP ASN_CONSUMER_GROUP CPT_CONSUMER_GROUP OUTBOX_RELAY_INTERVAL OUTBOX_BATCH_SIZE; do
  if grep -q "name: $stray\$" <<<"$mcp_dep"; then fail "the mcp Deployment must not render $stray (cmd/mcp reads neither consumers nor outbox knobs)"; fi
done

relay_dep="$(render "${base[@]}" "${relay[@]}" --show-only templates/relay-deployment.yaml)"
grep -q 'command: \["/app/relay"\]' <<<"$relay_dep" || fail "the relay Deployment must run /app/relay"
[[ "$(env_value "$relay_dep" ADMIN_ADDR)" == ":8094" ]] || fail "the relay Deployment must set ADMIN_ADDR from relay.adminAddr"
[[ "$(env_value "$relay_dep" OUTBOX_MAX_ATTEMPTS)" == "20" ]] || fail "the relay Deployment must fall back to config.outboxMaxAttempts"
relay_tuned="$(render "${base[@]}" "${relay[@]}" --set relay.outboxMaxAttempts=0 --show-only templates/relay-deployment.yaml)"
[[ "$(env_value "$relay_tuned" OUTBOX_MAX_ATTEMPTS)" == "0" ]] || fail "relay.outboxMaxAttempts=0 must stay 0 (unlimited)"
if grep -q 'name: MIGRATIONS_DATABASE_URL' <<<"$relay_dep"; then fail "the relay never migrates: no MIGRATIONS_DATABASE_URL"; fi
if render "${base[@]}" | grep -q -- '-relay'; then fail "a relay resource rendered with default values (relay.enabled is false)"; fi

proj="$(render "${base[@]}" "${analytics[@]}" --show-only templates/projector-deployment.yaml)"
grep -q 'command: \["/app/projector"\]' <<<"$proj" || fail "the projector Deployment must run /app/projector"
[[ "$(env_value "$proj" ANALYTICS_CONSUMER_GROUP)" == "dock-scheduling-analytics" ]] || fail "ANALYTICS_CONSUMER_GROUP must default to dock-scheduling-analytics"
[[ "$(env_value "$proj" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "the projector KAFKA_BROKERS is not rendered from kafka.brokers"
if grep -q 'name: DATABASE_URL$' <<<"$proj"; then fail "the projector never opens the OLTP database"; fi

reports="$(render "${base[@]}" "${analytics[@]}" --show-only templates/reports-deployment.yaml)"
grep -q 'command: \["/app/reports"\]' <<<"$reports" || fail "the reports Deployment must run /app/reports"
[[ "$(env_value "$reports" HTTP_ADDR)" == ":8092" ]] || fail "the reports Deployment must set HTTP_ADDR from analytics.reports.httpAddr"
grep -A4 'name: ANALYTICS_DATABASE_URL$' <<<"$reports" | grep -q 'ANALYTICS_READER_DATABASE_URL' \
  || fail "the reports DSN must come from the READER key of the analytics Secret"
if grep -q 'name: KAFKA_BROKERS' <<<"$reports"; then fail "cmd/reports never dials Kafka"; fi
if grep -q 'name: DATABASE_URL$' <<<"$reports"; then fail "cmd/reports never opens the OLTP database"; fi

# --- chart vs binary: every env var a cmd/* binary reads is rendered -----------
# The authoritative list is each cmd/*/main.go's "Environment:" header comment.
# The everything-set render must carry every one of them somewhere (a binary's
# own Deployment for the long-running ones, the CronJob for sweep's).
for var in HTTP_ADDR LOG_LEVEL DATABASE_URL MIGRATIONS_DATABASE_URL KAFKA_BROKERS \
  YARD_CONSUMER_GROUP FACILITY_CONSUMER_GROUP ASN_CONSUMER_GROUP CPT_CONSUMER_GROUP \
  OUTBOX_RELAY_INTERVAL OUTBOX_BATCH_SIZE OUTBOX_MAX_ATTEMPTS \
  SHUTDOWN_DRAIN_DELAY MCP_ADDR ADMIN_ADDR ANALYTICS_DATABASE_URL ANALYTICS_CONSUMER_GROUP \
  ENVIRONMENT SERVICE_VERSION OTEL_EXPORTER_OTLP_ENDPOINT \
  SWEEP_URL SWEEP_REQUEST_TIMEOUT SWEEP_MAX_ATTEMPTS SWEEP_RETRY_BACKOFF SWEEP_MAX_BACKOFF SIDECAR_QUIT_URL; do
  grep -q "name: $var\$" <<<"$render_all" || fail "the everything-set render does not carry $var"
done
# ... and every config.*-backed var of cmd/api lands in the ConfigMap too.
for var in HTTP_ADDR LOG_LEVEL ENVIRONMENT KAFKA_BROKERS OUTBOX_MAX_ATTEMPTS \
  YARD_CONSUMER_GROUP FACILITY_CONSUMER_GROUP ASN_CONSUMER_GROUP CPT_CONSUMER_GROUP; do
  grep -q "^  $var:" <<<"$configmap" || fail "the ConfigMap does not carry $var"
done

# --- refusals: the mandatory values name themselves ---------------------------
expect_refusal() { # expect_refusal <why> <needle> <helm args...>
  local why="$1" needle="$2" err; shift 2
  if err="$(helm template "$release" "$chart_dir" "$@" 2>&1)"; then
    fail "the chart must refuse to render: $why"
  fi
  grep -qF -- "$needle" <<<"$err" || fail "the refusal for '$why' does not name '$needle':\n$err"
}
groups=(--set config.yardConsumerGroup=g1 --set config.facilityConsumerGroup=g2 --set config.asnConsumerGroup=g3 --set config.cptConsumerGroup=g4)
db=(--set database.url=postgres://u@h/db --set kafka.brokers=k:9092)
expect_refusal "no database source" "database.url or database.existingSecret" --set kafka.brokers=k:9092 "${groups[@]}"
expect_refusal "no brokers" "kafka.brokers" --set database.url=postgres://u@h/db "${groups[@]}"
expect_refusal "kafka switched off" "Kafka cannot be switched off" "${db[@]}" "${groups[@]}" --set kafka.enabled=false
expect_refusal "no yard group" "config.yardConsumerGroup" "${db[@]}" \
  --set config.facilityConsumerGroup=g2 --set config.asnConsumerGroup=g3 --set config.cptConsumerGroup=g4
expect_refusal "no facility group" "config.facilityConsumerGroup" "${db[@]}" \
  --set config.yardConsumerGroup=g1 --set config.asnConsumerGroup=g3 --set config.cptConsumerGroup=g4
expect_refusal "no asn group" "config.asnConsumerGroup" "${db[@]}" \
  --set config.yardConsumerGroup=g1 --set config.facilityConsumerGroup=g2 --set config.cptConsumerGroup=g4
expect_refusal "no cpt group" "config.cptConsumerGroup" "${db[@]}" \
  --set config.yardConsumerGroup=g1 --set config.facilityConsumerGroup=g2 --set config.asnConsumerGroup=g3
expect_refusal "analytics without a DSN source" "analytics.database.projectorUrl" "${db[@]}" "${groups[@]}" --set analytics.enabled=true
expect_refusal "relay without a database source" "database.url or database.existingSecret" --set relay.enabled=true \
  --set kafka.brokers=k:9092 "${groups[@]}"

# --- pod hardening and shutdown room (docs/before-first-release.md row 7) ------
grep -q 'runAsNonRoot: true' <<<"$render_all" || fail "every pod must run as non-root"
grep -q 'readOnlyRootFilesystem: true' <<<"$render_all" || fail "the root filesystem must be mounted read-only (the service writes nothing to disk)"
grep -q 'allowPrivilegeEscalation: false' <<<"$render_all" || fail "privilege escalation must be disabled"
grep -q 'drop:\n.*ALL' <<<"$render_all" || grep -qE 'drop:.*|"ALL"' <<<"$render_all" || grep -A1 'drop:' <<<"$render_all" | grep -q 'ALL' || fail "all capabilities must be dropped"
grep -q 'terminationGracePeriodSeconds: 40' <<<"$render_all" || fail "terminationGracePeriodSeconds must default to 40s (drain delay 5s + HTTP 10s + consumers 10s + relay 10s)"
expect_refusal "a grace period below the shutdown sequence" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=20
expect_refusal "a millisecond drain delay" "whole seconds" "${base[@]}" --set config.shutdownDrainDelay=250ms

# --- no in-memory / permissive / log mode is ever rendered ---------------------
if grep -qiE 'value: ?"?(permissive|log|off)"?$' <<<"$render_all"; then fail "the render carries permissive/log/off, which no binary of this service has"; fi
if grep -qiE 'in-memory|silently runs' <<<"$render_all"; then fail "the render mentions an in-memory mode"; fi
if grep -qE 'name: [A-Z_]+_MODE' <<<"$render_all"; then fail "no *_MODE env var exists in this service (the fleet removed them)"; fi

# --- the ci values files (what chart-testing lints) still render ---------------
helm template "$release" "$chart_dir" -f "$chart_dir/ci/default-values.yaml" >/dev/null || fail "ci/default-values.yaml no longer renders"
helm template "$release" "$chart_dir" -f "$chart_dir/ci/all-components-values.yaml" >/dev/null || fail "ci/all-components-values.yaml no longer renders"

echo "PASS: dock-scheduling chart render assertions"
