{{- define "gvList" -}}
{{- $groupVersions := . -}}
---
slug: /clickhouse-operator/reference/api-reference
title: 'ClickHouse Operator API reference'
keywords: ['kubernetes', 'operator', 'API reference']
description: 'This document provides detailed API reference for the ClickHouse Operator custom resources.'
sidebarTitle: 'API reference'
---

This document provides detailed API reference for the ClickHouse Operator custom resources.

{{- range $groupVersions -}}
{{- template "gvDetails" . -}}
{{- end }}
{{ end -}}