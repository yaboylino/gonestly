package gonestly

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const (
	defaultOpenAPIPath = "/openapi.json"
	defaultDocsPath    = "/docs"
	problemSchemaName  = "Problem"
)

// OpenAPIConfig configures the OpenAPI document and Swagger UI. Provide one in the root
// module to change the defaults.
type OpenAPIConfig struct {
	// Title and Version describe the API; they default to "API" and "0.0.0".
	Title   string
	Version string
	// Description introduces the API at the top of the documentation; Markdown works.
	Description string
	// Servers lists the base URLs of the API, such as "https://api.example.com". Without them,
	// clients use the address the document was loaded from.
	Servers []string
	// Tags describes the groups of routes, by name; routes are grouped by module by default.
	Tags map[string]string
	// JSONPath serves the document. It defaults to "/openapi.json".
	JSONPath string
	// DocsPath serves Swagger UI. It defaults to "/docs".
	DocsPath string
	// Disabled stops serving the document and Swagger UI; App.OpenAPI still builds it.
	Disabled bool
}

func (c OpenAPIConfig) withDefaults() OpenAPIConfig {
	if c.Title == "" {
		c.Title = "API"
	}
	if c.Version == "" {
		c.Version = "0.0.0"
	}
	if c.JSONPath == "" {
		c.JSONPath = defaultOpenAPIPath
	}
	if c.DocsPath == "" {
		c.DocsPath = defaultDocsPath
	}
	return c
}

// SecurityScheme describes how clients authenticate, such as a bearer token or an API key.
type SecurityScheme struct {
	// Type is "http", "apiKey", "oauth2" or "openIdConnect".
	Type string `json:"type"`
	// Scheme is the HTTP scheme for the type "http", such as "bearer" or "basic".
	Scheme string `json:"scheme,omitempty"`
	// BearerFormat hints at the token format, such as "JWT".
	BearerFormat string `json:"bearerFormat,omitempty"`
	// In and Name locate an API key: In is "header", "query" or "cookie".
	In          string `json:"in,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// SecuredGuard is a guard that tells the OpenAPI document how clients authenticate. Every
// route it protects gets the scheme as a security requirement and a 401 response.
type SecuredGuard interface {
	Guard
	SecurityScheme() (name string, scheme SecurityScheme)
}

type openAPIDocument struct {
	OpenAPI    string                                  `json:"openapi"`
	Info       openAPIInfo                             `json:"info"`
	Servers    []openAPIServer                         `json:"servers,omitempty"`
	Tags       []openAPITag                            `json:"tags,omitempty"`
	Paths      map[string]map[string]*openAPIOperation `json:"paths"`
	Components openAPIComponents                       `json:"components"`
}

type openAPIInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

type openAPIServer struct {
	URL string `json:"url"`
}

type openAPITag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type openAPIComponents struct {
	Schemas         map[string]*Schema        `json:"schemas,omitempty"`
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes,omitempty"`
}

type openAPIOperation struct {
	OperationID string                      `json:"operationId,omitempty"`
	Summary     string                      `json:"summary,omitempty"`
	Description string                      `json:"description,omitempty"`
	Tags        []string                    `json:"tags,omitempty"`
	Parameters  []openAPIParameter          `json:"parameters,omitempty"`
	RequestBody *openAPIRequestBody         `json:"requestBody,omitempty"`
	Responses   map[string]*openAPIResponse `json:"responses"`
	Security    any                         `json:"security,omitempty"`
	Deprecated  bool                        `json:"deprecated,omitempty"`
}

type openAPIParameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Description string  `json:"description,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Schema      *Schema `json:"schema"`
}

type openAPIRequestBody struct {
	Required bool                        `json:"required"`
	Content  map[string]openAPIMediaType `json:"content"`
}

type openAPIResponse struct {
	Description string                      `json:"description"`
	Content     map[string]openAPIMediaType `json:"content,omitempty"`
}

type openAPIMediaType struct {
	Schema *Schema `json:"schema"`
}

// buildOpenAPI describes every route; guards are needed for their security schemes.
func buildOpenAPI(routes []*route, config OpenAPIConfig, globalGuards []Guard) *openAPIDocument {
	g := newSchemaGenerator()
	problemType := reflect.TypeFor[Problem]()
	g.components[problemSchemaName] = problemSchema()
	g.names[problemType] = problemSchemaName
	g.taken[problemSchemaName] = problemType
	doc := &openAPIDocument{
		OpenAPI: "3.1.0",
		Info:    openAPIInfo{Title: config.Title, Version: config.Version, Description: config.Description},
		Paths:   map[string]map[string]*openAPIOperation{},
	}
	for _, url := range config.Servers {
		doc.Servers = append(doc.Servers, openAPIServer{URL: url})
	}

	operations := make([]*openAPIOperation, 0, len(routes))
	var tags []string
	securitySchemes := map[string]SecurityScheme{}
	for _, rt := range routes {
		op := buildOperation(g, rt, slices.Concat(globalGuards, rt.guards), securitySchemes)
		operations = append(operations, op)
		for _, tag := range op.Tags {
			if !slices.Contains(tags, tag) {
				tags = append(tags, tag)
			}
		}
		path := openAPIPath(rt.path)
		if doc.Paths[path] == nil {
			doc.Paths[path] = map[string]*openAPIOperation{}
		}
		doc.Paths[path][strings.ToLower(rt.method)] = op
	}

	uniqueOperationIDs(routes, operations)
	for _, tag := range tags {
		doc.Tags = append(doc.Tags, openAPITag{Name: tag, Description: config.Tags[tag]})
	}
	doc.Components.Schemas = g.components
	if len(securitySchemes) > 0 {
		doc.Components.SecuritySchemes = securitySchemes
		// An empty requirement says that a route is public on purpose.
		for _, methods := range doc.Paths {
			for _, op := range methods {
				if op.Security == nil {
					op.Security = []map[string][]string{}
				}
			}
		}
	}
	return doc
}

// uniqueOperationIDs makes every operation ID unique, as OpenAPI requires: a handler name used
// twice gets its tag as a prefix, and what still clashes is named after its method and path.
func uniqueOperationIDs(routes []*route, operations []*openAPIOperation) {
	count := func() map[string]int {
		counts := map[string]int{}
		for _, op := range operations {
			counts[op.OperationID]++
		}
		return counts
	}
	counts := count()
	for _, op := range operations {
		if counts[op.OperationID] > 1 && len(op.Tags) > 0 {
			op.OperationID = op.Tags[0] + "." + op.OperationID
		}
	}
	counts = count()
	for i, op := range operations {
		if counts[op.OperationID] > 1 {
			op.OperationID = operationIDFromPath(routes[i].method, routes[i].path)
		}
	}
	seen := map[string]int{}
	for _, op := range operations {
		seen[op.OperationID]++
		if n := seen[op.OperationID]; n > 1 {
			op.OperationID += strconv.Itoa(n)
		}
	}
}

func buildOperation(g *schemaGenerator, rt *route, guards []Guard, schemes map[string]SecurityScheme) *openAPIOperation {
	name := handlerShortName(rt.handlerName)
	op := &openAPIOperation{
		OperationID: rt.docs.operationID,
		Summary:     rt.docs.summary,
		Description: rt.docs.description,
		Tags:        rt.docs.tags,
		Deprecated:  rt.docs.deprecated,
		Responses:   map[string]*openAPIResponse{},
	}
	if op.OperationID == "" {
		op.OperationID = lowerFirst(name)
		if name == "" {
			op.OperationID = operationIDFromPath(rt.method, rt.path)
		}
	}
	if op.Summary == "" && name != "" {
		op.Summary = humanize(name)
	}
	if len(op.Tags) == 0 && rt.module != "" {
		op.Tags = []string{rt.module}
	}

	hasInput := false
	if rt.binder != nil {
		for _, f := range rt.binder.fields {
			if f.in == "form" {
				continue
			}
			sf := rt.reqType.FieldByIndex(f.index)
			paramType := sf.Type
			if paramType.Kind() == reflect.Pointer {
				// A pointer tells an absent parameter from a zero one; it is not nullable.
				paramType = paramType.Elem()
			}
			schema := describe(g.schema(paramType), sf)
			if schema.Description == sf.Tag.Get("doc") {
				// The parameter carries the description; repeating it in its schema adds noise.
				schema.Description = ""
			}
			op.Parameters = append(op.Parameters, openAPIParameter{
				Name:        f.name,
				In:          f.in,
				Description: sf.Tag.Get("doc"),
				Required:    f.in == "path" || hasRule(sf, "required"),
				Schema:      schema,
			})
			hasInput = true
		}
		if rt.binder.body {
			op.RequestBody = &openAPIRequestBody{
				Required: true,
				Content:  map[string]openAPIMediaType{"application/json": {Schema: bodySchema(g, rt)}},
			}
			hasInput = true
		}
		if rt.binder.form {
			mediaType := "application/x-www-form-urlencoded"
			if rt.binder.files {
				mediaType = "multipart/form-data"
			}
			op.RequestBody = &openAPIRequestBody{
				Required: true,
				Content:  map[string]openAPIMediaType{mediaType: {Schema: formSchema(g, rt)}},
			}
			hasInput = true
		}
	}

	status, description := successResponse(rt)
	response := &openAPIResponse{Description: description}
	if rt.raw == nil && !rt.emptyResult {
		response.Content = map[string]openAPIMediaType{"application/json": {Schema: g.schema(rt.resType)}}
	}
	op.Responses[strconv.Itoa(status)] = response
	if hasInput {
		op.Responses["400"] = problemResponse("The request is invalid.")
	}

	// Every guard must pass, so their schemes form one requirement rather than alternatives.
	requirement := map[string][]string{}
	for _, guard := range guards {
		secured, ok := guard.(SecuredGuard)
		if !ok {
			continue
		}
		name, scheme := secured.SecurityScheme()
		schemes[name] = scheme
		requirement[name] = []string{}
	}
	if len(requirement) > 0 {
		op.Security = []map[string][]string{requirement}
		op.Responses["401"] = problemResponse("Authentication is required.")
	}
	op.Responses["default"] = problemResponse("An unexpected error.")
	return op
}

// bodySchema describes the JSON body: the whole request type, or only its untagged fields
// when it also reads from the path, query or headers.
func bodySchema(g *schemaGenerator, rt *route) *Schema {
	if rt.reqType.Kind() != reflect.Struct || len(rt.binder.fields) == 0 {
		return g.schema(rt.reqType)
	}
	s := &Schema{Type: "object", Properties: Properties{}}
	for _, f := range jsonFields(rt.reqType) {
		if isBoundField(f.field) {
			continue
		}
		s.Properties = append(s.Properties, Property{Name: f.name, Schema: describe(g.schema(f.field.Type), f.field)})
		if f.required {
			s.Required = append(s.Required, f.name)
		}
	}
	return s
}

// formSchema describes the fields of a form; a file is required unless it is a pointer.
func formSchema(g *schemaGenerator, rt *route) *Schema {
	s := &Schema{Type: "object", Properties: Properties{}}
	for _, f := range rt.binder.fields {
		if f.in != "form" {
			continue
		}
		sf := rt.reqType.FieldByIndex(f.index)
		fieldType := sf.Type
		if fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		s.Properties = append(s.Properties, Property{Name: f.name, Schema: describe(g.schema(fieldType), sf)})
		if sf.Type == fileType || hasRule(sf, "required") {
			s.Required = append(s.Required, f.name)
		}
	}
	return s
}

func isBoundField(f reflect.StructField) bool {
	for _, source := range bindingSources {
		if _, ok := f.Tag.Lookup(source); ok {
			return true
		}
	}
	return false
}

func hasRule(f reflect.StructField, rule string) bool {
	_, ok := validateRules(f)[rule]
	return ok
}

func successResponse(rt *route) (int, string) {
	status := rt.status
	switch {
	case status != 0:
	case rt.raw != nil:
		status = http.StatusOK
	case rt.emptyResult:
		status = http.StatusNoContent
	case rt.method == http.MethodPost:
		status = http.StatusCreated
	default:
		status = http.StatusOK
	}
	return status, http.StatusText(status)
}

func problemResponse(description string) *openAPIResponse {
	return &openAPIResponse{
		Description: description,
		Content:     map[string]openAPIMediaType{"application/problem+json": {Schema: &Schema{Ref: "#/components/schemas/" + problemSchemaName}}},
	}
}

func problemSchema() *Schema {
	field := &Schema{Type: "object", Properties: Properties{
		{Name: "field", Schema: &Schema{Type: "string"}},
		{Name: "in", Schema: &Schema{Type: "string", Enum: []any{"body", "query", "path", "header"}}},
		{Name: "message", Schema: &Schema{Type: "string"}},
	}, Required: []string{"field", "message"}}
	return &Schema{
		Type:        "object",
		Description: "An error, described as problem details (RFC 9457).",
		Properties: Properties{
			{Name: "type", Schema: &Schema{Type: "string"}},
			{Name: "title", Schema: &Schema{Type: "string"}},
			{Name: "status", Schema: &Schema{Type: "integer"}},
			{Name: "detail", Schema: &Schema{Type: "string"}},
			{Name: "instance", Schema: &Schema{Type: "string"}},
			{Name: "errors", Schema: &Schema{Type: "array", Items: field}},
		},
		Required: []string{"type", "title", "status"},
	}
}

// openAPIPath writes a net/http pattern in OpenAPI syntax: {path...} becomes {path}.
func openAPIPath(path string) string {
	return strings.ReplaceAll(path, "...}", "}")
}

// handlerShortName returns a method or function name, such as FindOne for
// "users.(*Controller).FindOne-fm", or "" for an anonymous function.
func handlerShortName(name string) string {
	name = strings.TrimSuffix(name, "-fm")
	name = name[strings.LastIndexByte(name, '.')+1:]
	if strings.HasPrefix(name, "func") && strings.TrimLeft(name[4:], "0123456789") == "" {
		return ""
	}
	return name
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// humanize turns a Go name into words, such as "Find one" for FindOne.
func humanize(name string) string {
	words := strings.ToLower(strings.ReplaceAll(snakeWords(name), "_", " "))
	if words == "" {
		return name
	}
	r := []rune(words)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func snakeWords(name string) string {
	runes := []rune(name)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// operationIDFromPath derives an ID for anonymous handlers, such as getUsersById.
func operationIDFromPath(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, "{") {
			segment = "By" + upperFirst(strings.TrimSuffix(strings.Trim(segment, "{}"), "..."))
		}
		for _, part := range strings.FieldsFunc(segment, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			b.WriteString(upperFirst(part))
		}
	}
	return b.String()
}

// relativeURL returns the path of target relative to the directory of from, so Swagger UI
// still finds the document when the application is served under a prefix.
func relativeURL(from, target string) string {
	dir := from[:strings.LastIndexByte(from, '/')+1]
	if rest, ok := strings.CutPrefix(target, dir); ok {
		return rest
	}
	up := strings.Repeat("../", strings.Count(dir, "/")-1)
	return up + strings.TrimPrefix(target, "/")
}

// openAPIRouteConflict reports a route that would hide the document or Swagger UI.
func openAPIRouteConflict(routes []*route, config OpenAPIConfig) []error {
	var problems []error
	for _, rt := range routes {
		if rt.method == http.MethodGet && (rt.path == config.JSONPath || rt.path == config.DocsPath) {
			problems = append(problems, fmt.Errorf("%w: %s: the path is used for the OpenAPI documentation; change OpenAPIConfig.JSONPath or DocsPath, or the route", ErrInvalidRoute, rt))
		}
	}
	return problems
}
