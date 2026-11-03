downloadmanagersquared (DMS²) includes the following third-party software.
The MPL-2.0 modules are used unmodified; their source code is at the address
given with each, at the version given.
{{range .}}
================================================================================
{{.Name}}{{if .Version}} {{.Version}}{{end}}
License: {{.LicenseName}}
{{- if eq .LicenseName "MPL-2.0"}}
Source code: Go module {{.Name}}{{if .Version}} version {{.Version}}{{end}}
  {{.LicenseURL}}
  {{- if .Version}}
  https://proxy.golang.org/{{.Name}}/@v/{{.Version}}.zip{{end}}
{{- end}}

{{.LicenseText}}
{{end}}