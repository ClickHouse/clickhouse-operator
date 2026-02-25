{{- define "gvList" -}}
{{- $groupVersions := . -}}
# API Reference

This document provides detailed API reference for the ClickHouse Operator custom resources.

{{ range $groupVersions }}
{{ template "gvDetails" . }}
{{ end }}
{{- end -}}
