package gonestly

import (
	"bytes"
	"encoding"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	timeType          = reflect.TypeFor[time.Time]()
	durationType      = reflect.TypeFor[time.Duration]()
	rawMessageType    = reflect.TypeFor[json.RawMessage]()
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	schemerType       = reflect.TypeFor[JSONSchemer]()
)

// JSONSchemer lets a type describe its own JSON Schema in the OpenAPI document, for types
// with custom JSON encoding.
type JSONSchemer interface {
	JSONSchema() *Schema
}

// Schema is a JSON Schema, as used by OpenAPI 3.1.
type Schema struct {
	Ref                  string     `json:"$ref,omitempty"`
	Type                 any        `json:"type,omitempty"`
	Format               string     `json:"format,omitempty"`
	Description          string     `json:"description,omitempty"`
	Enum                 []any      `json:"enum,omitempty"`
	Properties           Properties `json:"properties,omitempty"`
	Required             []string   `json:"required,omitempty"`
	AdditionalProperties *Schema    `json:"additionalProperties,omitempty"`
	Items                *Schema    `json:"items,omitempty"`
	AnyOf                []*Schema  `json:"anyOf,omitempty"`
	MinLength            *int       `json:"minLength,omitempty"`
	MaxLength            *int       `json:"maxLength,omitempty"`
	Pattern              string     `json:"pattern,omitempty"`
	Minimum              *float64   `json:"minimum,omitempty"`
	Maximum              *float64   `json:"maximum,omitempty"`
	ExclusiveMinimum     *float64   `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum     *float64   `json:"exclusiveMaximum,omitempty"`
	MinItems             *int       `json:"minItems,omitempty"`
	MaxItems             *int       `json:"maxItems,omitempty"`
	Examples             []any      `json:"examples,omitempty"`
	Deprecated           bool       `json:"deprecated,omitempty"`
}

// Property is one named property of an object schema.
type Property struct {
	Name   string
	Schema *Schema
}

// Properties keeps the properties of an object in the order of the struct fields.
type Properties []Property

// MarshalJSON encodes the properties as a JSON object in their declared order.
func (p Properties) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, prop := range p {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(prop.Name)
		if err != nil {
			return nil, err
		}
		schema, err := json.Marshal(prop.Schema)
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(schema)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// schemaGenerator turns Go types into schemas, sharing named struct types as components.
type schemaGenerator struct {
	components map[string]*Schema
	names      map[reflect.Type]string
	taken      map[string]reflect.Type
}

func newSchemaGenerator() *schemaGenerator {
	return &schemaGenerator{components: map[string]*Schema{}, names: map[reflect.Type]string{}, taken: map[string]reflect.Type{}}
}

func (g *schemaGenerator) schema(t reflect.Type) *Schema {
	if t.Implements(schemerType) {
		if s := reflect.Zero(t).Interface().(JSONSchemer).JSONSchema(); s != nil { //nolint:forcetypeassert // checked above
			return s
		}
	}
	switch {
	case t == timeType:
		return &Schema{Type: "string", Format: "date-time"}
	case t == durationType:
		return &Schema{Type: "integer", Format: "int64", Description: "A duration in nanoseconds."}
	case t == rawMessageType:
		return &Schema{}
	case t.Kind() != reflect.Pointer && (t.Implements(textMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType)) &&
		!t.Implements(jsonMarshalerType) && !reflect.PointerTo(t).Implements(jsonMarshalerType):
		return &Schema{Type: "string"}
	case t.Implements(jsonMarshalerType) || (t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(jsonMarshalerType)):
		return &Schema{}
	}

	switch t.Kind() {
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return &Schema{Type: "integer", Format: "int32"}
	case reflect.Int, reflect.Int64:
		return &Schema{Type: "integer", Format: "int64"}
	case reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return &Schema{Type: "integer", Format: "int32", Minimum: ptr(0.0)}
	case reflect.Uint, reflect.Uint64, reflect.Uintptr:
		return &Schema{Type: "integer", Format: "int64", Minimum: ptr(0.0)}
	case reflect.Float32:
		return &Schema{Type: "number", Format: "float"}
	case reflect.Float64:
		return &Schema{Type: "number", Format: "double"}
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return &Schema{Type: "string", Format: "byte"}
		}
		return &Schema{Type: "array", Items: g.schema(t.Elem())}
	case reflect.Map:
		return &Schema{Type: "object", AdditionalProperties: g.schema(t.Elem())}
	case reflect.Pointer:
		return nullable(g.schema(t.Elem()))
	case reflect.Struct:
		if t.Name() == "" {
			return g.object(t)
		}
		return g.ref(t)
	default:
		return &Schema{}
	}
}

// ref returns a reference to the component of a named struct, creating it on first use.
func (g *schemaGenerator) ref(t reflect.Type) *Schema {
	if name, ok := g.names[t]; ok {
		return &Schema{Ref: "#/components/schemas/" + name}
	}
	name := g.componentName(t)
	g.names[t] = name
	g.taken[name] = t
	g.components[name] = &Schema{}
	*g.components[name] = *g.object(t)
	return &Schema{Ref: "#/components/schemas/" + name}
}

// componentName names a component after its type, adding the package on a clash and
// spelling out type arguments, such as PageOfUser for Page[User].
func (g *schemaGenerator) componentName(t reflect.Type) string {
	name := t.Name()
	if open := strings.IndexByte(name, '['); open >= 0 {
		var args []string
		for _, arg := range strings.Split(strings.TrimSuffix(name[open+1:], "]"), ",") {
			arg = arg[strings.LastIndexAny(arg, "./*")+1:]
			args = append(args, upperFirst(arg))
		}
		name = name[:open] + "Of" + strings.Join(args, "And")
	}
	if other, ok := g.taken[name]; ok && other != t {
		pkg := t.PkgPath()
		name = pkg[strings.LastIndexByte(pkg, '/')+1:] + "." + name
	}
	for base, n := name, 2; g.taken[name] != nil && g.taken[name] != t; n++ {
		name = base + strconv.Itoa(n)
	}
	return name
}

// object describes the JSON encoding of a struct: its exported fields under their JSON names.
func (g *schemaGenerator) object(t reflect.Type) *Schema {
	s := &Schema{Type: "object", Properties: Properties{}}
	for _, f := range jsonFields(t) {
		fs := g.schema(f.field.Type)
		if f.asString {
			fs = &Schema{Type: "string"}
		}
		fs = describe(fs, f.field)
		s.Properties = append(s.Properties, Property{Name: f.name, Schema: fs})
		if f.required {
			s.Required = append(s.Required, f.name)
		}
	}
	return s
}

type jsonField struct {
	field    reflect.StructField
	name     string
	required bool
	asString bool
}

// jsonFields lists the fields encoding/json writes, following its rules for embedded structs.
func jsonFields(t reflect.Type) []jsonField {
	var fields []jsonField
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				fields = append(fields, jsonFields(et)...)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		optional := f.Type.Kind() == reflect.Pointer || strings.Contains(options, "omitempty") || strings.Contains(options, "omitzero")
		rules := validateRules(f)
		if _, ok := rules["required"]; ok {
			optional = false
		} else if _, ok := rules["omitempty"]; ok {
			optional = true
		}
		fields = append(fields, jsonField{field: f, name: name, required: !optional, asString: strings.Contains(options, "string")})
	}
	return fields
}

// describe adds the doc, example and validate tags of a field to its schema.
func describe(s *Schema, f reflect.StructField) *Schema {
	doc, hasDoc := f.Tag.Lookup("doc")
	example, hasExample := f.Tag.Lookup("example")
	rules := validateRules(f)
	if !hasDoc && !hasExample && len(rules) == 0 {
		return s
	}
	if s.Ref != "" {
		// A sibling of $ref describes this use of the component, not the component itself.
		s = &Schema{AnyOf: []*Schema{s}}
	} else {
		c := *s
		s = &c
	}
	if hasDoc {
		s.Description = doc
	}
	if hasExample {
		s.Examples = []any{exampleValue(example, f.Type)}
	}
	applyRules(s, rules, f.Type)
	return s
}

// validateRules parses the validate tag into rule names and parameters, up to "dive".
func validateRules(f reflect.StructField) map[string]string {
	tag := f.Tag.Get("validate")
	if tag == "" {
		return nil
	}
	rules := map[string]string{}
	for _, rule := range strings.Split(tag, ",") {
		if rule == "dive" {
			break
		}
		name, param, _ := strings.Cut(rule, "=")
		rules[name] = param
	}
	return rules
}

func applyRules(s *Schema, rules map[string]string, t reflect.Type) {
	base := t
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	target := s
	if len(s.AnyOf) > 0 {
		target = s.AnyOf[0]
	}
	for name, param := range rules {
		switch name {
		case "email":
			target.Format = "email"
		case "url", "http_url", "uri":
			target.Format = "uri"
		case "uuid", "uuid4", "uuid7":
			target.Format = "uuid"
		case "ip":
			target.Format = "ip"
		case "ipv4":
			target.Format = "ipv4"
		case "ipv6":
			target.Format = "ipv6"
		case "alphanum":
			target.Pattern = "^[a-zA-Z0-9]*$"
		case "numeric":
			target.Pattern = "^-?[0-9]+(\\.[0-9]+)?$"
		case "oneof":
			for _, v := range strings.Fields(param) {
				target.Enum = append(target.Enum, exampleValue(v, base))
			}
		case "min", "gte", "max", "lte", "len", "gt", "lt":
			applyBound(target, name, param, base)
		}
	}
}

func applyBound(s *Schema, rule, param string, t reflect.Type) {
	n, err := strconv.ParseFloat(param, 64)
	if err != nil {
		return
	}
	switch t.Kind() {
	case reflect.String:
		size := int(n)
		switch rule {
		case "min", "gte":
			s.MinLength = &size
		case "max", "lte":
			s.MaxLength = &size
		case "len":
			s.MinLength, s.MaxLength = &size, &size
		}
	case reflect.Slice, reflect.Array:
		size := int(n)
		switch rule {
		case "min", "gte":
			s.MinItems = &size
		case "max", "lte":
			s.MaxItems = &size
		case "len":
			s.MinItems, s.MaxItems = &size, &size
		}
	case reflect.Map:
	default:
		switch rule {
		case "min", "gte":
			s.Minimum = &n
		case "max", "lte":
			s.Maximum = &n
		case "gt":
			s.ExclusiveMinimum = &n
		case "lt":
			s.ExclusiveMaximum = &n
		case "len":
			s.Minimum, s.Maximum = &n, &n
		}
	}
}

// exampleValue converts tag text to the JSON type of t, so numbers are not shown as strings.
func exampleValue(text string, t reflect.Type) any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		if b, err := strconv.ParseBool(text); err == nil {
			return b
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		if n, err := strconv.ParseFloat(text, 64); err == nil {
			return n
		}
	case reflect.Slice, reflect.Map, reflect.Struct:
		var v any
		if json.Unmarshal([]byte(text), &v) == nil {
			return v
		}
	default:
	}
	return text
}

func nullable(s *Schema) *Schema {
	if typ, ok := s.Type.(string); ok && s.Ref == "" {
		c := *s
		c.Type = []string{typ, "null"}
		return &c
	}
	return &Schema{AnyOf: []*Schema{s, {Type: "null"}}}
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func ptr[T any](v T) *T { return &v }
