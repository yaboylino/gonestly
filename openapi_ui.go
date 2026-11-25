package gonestly

import (
	"bytes"
	"html/template"
)

// Swagger UI is loaded from a CDN at a pinned version, checked with subresource integrity.
const (
	swaggerUIVersion   = "5.34.1"
	swaggerUIScriptSRI = "sha384-XFYN47VzPiDY6qPtxYKg0nqUeW/upo20xf1j2SUE7gT/i35FJB4cuS/71Iegzk8Z"
	swaggerUIStyleSRI  = "sha384-4c3vsE5f+trhFyHrFrLM1fFGUn531NIaC7dcqEWIJdoRQJ5CkJbSuSCwXLpaS1KF"
)

var swaggerUITemplate = template.Must(template.New("docs").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@{{.Version}}/swagger-ui.css" integrity="{{.StyleSRI}}" crossorigin="anonymous">
<style>body { margin: 0; }</style>
</head>
<body>
<div id="docs"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@{{.Version}}/swagger-ui-bundle.js" integrity="{{.ScriptSRI}}" crossorigin="anonymous"></script>
<script>
SwaggerUIBundle({ url: {{.SpecURL}}, dom_id: "#docs", deepLinking: true, persistAuthorization: true });
</script>
</body>
</html>
`))

func renderSwaggerUI(title, specURL string) []byte {
	var buf bytes.Buffer
	_ = swaggerUITemplate.Execute(&buf, map[string]string{
		"Title":     title,
		"Version":   swaggerUIVersion,
		"StyleSRI":  swaggerUIStyleSRI,
		"ScriptSRI": swaggerUIScriptSRI,
		"SpecURL":   specURL,
	})
	return buf.Bytes()
}
