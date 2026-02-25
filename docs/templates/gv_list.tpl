{{- define "gvList" -}}
{{- $groupVersions := . -}}
---
position: 1
slug: /clickhouse-operator/reference/api-reference
title: 'ClickHouse Operator API reference'
keywords: ['kubernetes']
description: 'This document provides detailed API reference for the ClickHouse Operator custom resources.'
doc_type: 'reference'
sidebar_label: 'API reference'
---

# ClickHouse Operator API reference {#clickhouse-operator-api-reference}

This document provides detailed API reference for the ClickHouse Operator custom resources.

{{- range $groupVersions }}
{{- template "gvDetails" . }}
{{- end }}
{{- end -}}
