{{- define "validationRequired" -}}
{{- $optional := false }}
{{-   range . }}
{{-     if (eq (printf "%.8s" .) "Optional") }}{{ $optional = true }}{{ break }}{{ end }}
{{-     if (eq (printf "%.8s" .) "Required") }}{{ $optional = false }}{{ break }}{{ end }}
{{-   end }}
{{-   if $optional -}}false{{- else -}}true{{- end }}
{{- end }}
{{ define "type" -}}
{{ $type := . -}}
{{-  if markdownShouldRenderType $type }}

## {{ $type.Name }}

{{ $type.Doc }}

{{- if $type.GVK }}
### API Version and Kind

```yaml
apiVersion: {{ $type.GVK.Group }}/{{ $type.GVK.Version }}
kind: {{ $type.GVK.Kind }}
```
{{- end }}

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
{{- range $type.Members }}
{{-   if not (index (dict "apiVersion" true "kind" true "metadata" true) .Name | and $type.GVK) }}
| `{{ .Name }}` | {{ markdownRenderType .Type }} | {{ template "type_members" . }} | {{ template "validationRequired" .Validation }} | {{ markdownRenderDefault .Default }} |
{{-   end }}
{{- end }}
{{- if $type.References }}

Appears in:
{{-   range $type.SortedReferences }}
- {{ markdownRenderTypeLink . }}
{{-   end }}
{{- end }}
{{- if $type.EnumValues }}
| Field | Description |
|-------|-------------|
{{-   range $type.EnumValues }}
| `{{ .Name }}` | {{ markdownRenderFieldDoc .Doc }} |
{{-   end }}
{{- end }}
{{- end }}
{{- end }}
