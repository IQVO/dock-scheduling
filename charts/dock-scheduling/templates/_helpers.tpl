{{/*
Expand the name of the chart.
*/}}
{{- define "dock-scheduling.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "dock-scheduling.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "dock-scheduling.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "dock-scheduling.labels" -}}
helm.sh/chart: {{ include "dock-scheduling.chart" . }}
{{ include "dock-scheduling.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels. Shared by every pod this release creates, so every
Deployment and Service ALSO pins app.kubernetes.io/component (api, mcp,
relay, analytics-projector, analytics-reports): a Service selecting on these
two alone would select every component.
*/}}
{{- define "dock-scheduling.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dock-scheduling.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "dock-scheduling.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "dock-scheduling.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding DATABASE_URL: the operator's own
(database.existingSecret) or the one this chart creates from database.url.
*/}}
{{- define "dock-scheduling.databaseSecretName" -}}
{{- if .Values.database.existingSecret }}
{{- .Values.database.existingSecret }}
{{- else }}
{{- include "dock-scheduling.fullname" . }}-database
{{- end }}
{{- end }}

{{/*
Fully qualified name of the MCP server deployment/service (docs/adr/0007).
*/}}
{{- define "dock-scheduling.mcpFullname" -}}
{{- include "dock-scheduling.fullname" . }}-mcp
{{- end }}

{{/*
Fully qualified name of the dedicated outbox relay deployment (cmd/relay).
No Service: it serves only the kubelet's /healthz probe on ADMIN_ADDR.
*/}}
{{- define "dock-scheduling.relayFullname" -}}
{{- include "dock-scheduling.fullname" . }}-relay
{{- end }}

{{/*
Fully qualified name of the analytics projector deployment (ADR 0006).
*/}}
{{- define "dock-scheduling.projectorFullname" -}}
{{- include "dock-scheduling.fullname" . }}-projector
{{- end }}

{{/*
Fully qualified name of the analytics reports deployment/service (ADR 0006).
*/}}
{{- define "dock-scheduling.reportsFullname" -}}
{{- include "dock-scheduling.fullname" . }}-reports
{{- end }}

{{/*
Image reference shared by every workload (api, mcp, relay,
analytics-projector, analytics-reports and the sweep CronJob).
*/}}
{{- define "dock-scheduling.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
MANDATORY: the service runs on real Postgres only. cmd/api, cmd/mcp and
cmd/relay exit at boot without DATABASE_URL; there is no in-memory mode.
Fails the render naming the values instead of crash-looping.
*/}}
{{- define "dock-scheduling.requireDatabase" -}}
{{- if not (or .Values.database.url .Values.database.existingSecret) -}}
{{- fail "dock-scheduling requires database.url or database.existingSecret to be set (DATABASE_URL): the service runs on real Postgres only and exits at boot without it." -}}
{{- end -}}
{{- end -}}

{{/*
MANDATORY: the service runs on real Kafka only. cmd/api exits at boot
without KAFKA_BROKERS (the inline relay, the four consumers and /readyz all
need it), and so do cmd/mcp (its /readyz) and cmd/projector. There is no
default broker and no way to switch Kafka off. kafka.enabled is kept only so
values written before this chart still render (warehouse-infra sets it to
"true"): it may be true, never false.
*/}}
{{- define "dock-scheduling.requireKafka" -}}
{{- $_ := required "dock-scheduling requires kafka.brokers to be set (KAFKA_BROKERS, comma-separated): the service runs on real Kafka only and exits at boot without it." .Values.kafka.brokers -}}
{{- if and (hasKey .Values.kafka "enabled") (or (not .Values.kafka.enabled) (eq (toString .Values.kafka.enabled) "false")) -}}
{{- fail "kafka.enabled is false — Kafka cannot be switched off: the service runs on real Kafka only. Drop the key or set it to true and set kafka.brokers." -}}
{{- end -}}
{{- end -}}

{{/*
The four local-copy/policy consumers always run inside cmd/api, each under a
STABLE consumer group id that must come from values (cmd/api exits at boot
without YARD_/FACILITY_/ASN_/CPT_CONSUMER_GROUP). A stable id is what lets a
rescheduled pod resume from its committed offsets.
*/}}
{{- define "dock-scheduling.requireConsumerGroups" -}}
{{- if not .Values.config.yardConsumerGroup -}}
{{- fail "config.yardConsumerGroup is required (YARD_CONSUMER_GROUP): the yard-management consumer always runs on real Kafka and cmd/api exits at boot without a group. Use a STABLE id." -}}
{{- end -}}
{{- if not .Values.config.facilityConsumerGroup -}}
{{- fail "config.facilityConsumerGroup is required (FACILITY_CONSUMER_GROUP): the facility-layout consumer always runs on real Kafka and cmd/api exits at boot without a group. Use a STABLE id." -}}
{{- end -}}
{{- if not .Values.config.asnConsumerGroup -}}
{{- fail "config.asnConsumerGroup is required (ASN_CONSUMER_GROUP): the inbound-receiving ASN consumer always runs on real Kafka and cmd/api exits at boot without a group. Use a STABLE id." -}}
{{- end -}}
{{- if not .Values.config.cptConsumerGroup -}}
{{- fail "config.cptConsumerGroup is required (CPT_CONSUMER_GROUP): the process-path-management CPT consumer always runs on real Kafka and cmd/api exits at boot without a group. Use a STABLE id." -}}
{{- end -}}
{{- end -}}

{{/*
analytics.enabled needs an analytical DSN source (the projector refuses to
boot without ANALYTICS_DATABASE_URL). The projector consumes
warehouse.dock-scheduling.analytics from kafka.brokers, which the api
Deployment already requires (requireKafka); the projector's consumer group
comes from analytics.projector.consumerGroup.
*/}}
{{- define "dock-scheduling.requireAnalyticsConfig" -}}
{{- if not (or .Values.analytics.database.projectorUrl .Values.analytics.database.existingSecret) -}}
{{- fail "analytics.enabled is true but neither analytics.database.projectorUrl nor analytics.database.existingSecret is set — the projector and reports binaries refuse to boot without ANALYTICS_DATABASE_URL." -}}
{{- end -}}
{{- end -}}

{{/*
Pod hardening + shutdown room (docs/before-first-release.md row 7): the
binary shuts down sequentially -- /readyz flip, SHUTDOWN_DRAIN_DELAY (5s
default), HTTP drain (10s), consumers drain (10s), relay drain (10s) -- so
terminationGracePeriodSeconds must cover drainDelay + 30s or a rolling
update SIGKILLs a pod mid-drain. The guard parses the same Go durations the
binary parses (plain seconds also accepted).
*/}}
{{- define "dock-scheduling.requireShutdownBudget" -}}
{{- $drain := 5 -}}
{{- $raw := .Values.config.shutdownDrainDelay | toString -}}
{{- if $raw -}}
{{- if $raw | hasSuffix "ms" -}}
{{- fail "config.shutdownDrainDelay must be a Go duration of whole seconds (e.g. 5s), not milliseconds" -}}
{{- else if $raw | hasSuffix "s" -}}
{{- $drain = $raw | trimSuffix "s" | float64 | int -}}
{{- else -}}
{{- $drain = $raw | float64 | int -}}
{{- end -}}
{{- end -}}
{{- $need := add $drain 30 -}}
{{- if lt (int .Values.terminationGracePeriodSeconds) $need -}}
{{- fail (printf "terminationGracePeriodSeconds is %d but the binary's shutdown sequence needs at least %d (SHUTDOWN_DRAIN_DELAY %ds + HTTP drain 10s + consumers drain 10s + relay drain 10s); a rolling update would SIGKILL a pod mid-drain" (int .Values.terminationGracePeriodSeconds) $need $drain) -}}
{{- end -}}
{{- end -}}

{{/*
Every render-time requirement of the real runtime, in one include. The api
Deployment (always rendered) includes it, so a mis-set release fails
`helm template` / `helm install` whichever component is enabled.
*/}}
{{- define "dock-scheduling.requireRealRuntime" -}}
{{- include "dock-scheduling.requireDatabase" . -}}
{{- include "dock-scheduling.requireKafka" . -}}
{{- include "dock-scheduling.requireConsumerGroups" . -}}
{{- include "dock-scheduling.requireShutdownBudget" . -}}
{{- end -}}

{{/*
Name of the Secret holding the analytical DSNs: the operator's own
(analytics.database.existingSecret, keys ANALYTICS_DATABASE_URL and
ANALYTICS_READER_DATABASE_URL) or the one this chart creates.
*/}}
{{- define "dock-scheduling.analyticsSecretName" -}}
{{- if .Values.analytics.database.existingSecret }}
{{- .Values.analytics.database.existingSecret }}
{{- else }}
{{- include "dock-scheduling.fullname" . }}-analytics
{{- end }}
{{- end }}

{{/*
Comma-joined names of this chart's enabled KongPlugins, for the
konghq.com/plugins annotation on every HTTPRoute this chart renders.
Empty string (annotation omitted) when none are enabled — standalone
installs without Kong are unaffected.
*/}}
{{- define "dock-scheduling.kongPluginNames" -}}
{{- $names := list -}}
{{- range $name, $cfg := .Values.kongPlugins -}}
{{- if $cfg.enabled -}}
{{- $names = append $names (printf "%s-%s" (include "dock-scheduling.fullname" $) $name) -}}
{{- end -}}
{{- end -}}
{{- join "," $names -}}
{{- end }}
