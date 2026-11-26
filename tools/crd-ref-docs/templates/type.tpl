{{- define "type" -}}
{{- $type := . -}}
{{- if markdownShouldRenderType $type -}}

#### <a name="{{ markdownTypeID $type | markdownSafeID }}">{{ $type.Name }}</a>

{{ if $type.IsAlias }}**Underlying type:** {{ markdownRenderTypeLink $type.UnderlyingType  }}{{ end }}
{{ if $type.References }}
**Appears in:**
{{- range $type.SortedReferences }}
- {{ markdownRenderTypeLink . }}
{{- end }}
{{- end }}

{{ $type.Doc }}

{{ if $type.Members -}}

##### Fields

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
{{ if $type.GVK -}}
| `apiVersion` | String | Yes |  | We are on version `{{ $type.GVK.Group }}/{{ $type.GVK.Version }}` of the API. |
| `kind` | String | Yes |  | This is a `{{ $type.GVK.Kind }}` resource |
{{ end -}}
{{ range $type.Members -}}
{{ if not .Markers.notImplementedHide -}}
| `{{ .Name }}` | {{ markdownRenderType .Type }} | {{ if .Markers.optional }}No{{ else }}Yes{{ end }} | {{ if .Default }}`{{ markdownRenderFieldDoc .Default }}`{{ end }} | {{ template "type_members" . }} |
{{ end -}}
{{ end -}}
{{ end -}}
{{ if $type.EnumValues }}
##### Possible Values

| Value | Description |
| --- | --- |
{{ range $type.EnumValues -}}
| `{{ .Name }}` | {{ markdownRenderFieldDoc .Doc }} |
{{ end -}}
{{ end }}

{{- end }}
{{- end -}}
