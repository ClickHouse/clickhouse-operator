{{/*
Expand the name of the chart.
*/}}
{{- define "clickhouse-cluster.chart-name" -}}
{{ trimSuffix "-helm" .Chart.Name }}
{{- end }}

{{/*
Fully-qualified release-scoped name used as the base of all CR names.
If the release name already contains the chart name, just use the release name.
*/}}
{{- define "clickhouse-cluster.fullname" -}}
{{- $name := include "clickhouse-cluster.chart-name" . }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
ClickHouseCluster CR name. Respects clickhouse.meta.name.
*/}}
{{- define "clickhouse-cluster.clickhouse.name" -}}
{{- if .Values.clickhouse.meta.name }}
{{- .Values.clickhouse.meta.name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- include "clickhouse-cluster.fullname" . | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
KeeperCluster CR name. Respects keeper.meta.name. Defaults to fullname.
*/}}
{{- define "clickhouse-cluster.keeper.name" -}}
{{- if .Values.keeper.meta.name }}
{{- .Values.keeper.meta.name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- (include "clickhouse-cluster.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
