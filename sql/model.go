package sql

import (
	stdsql "database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	scannerType = reflect.TypeFor[stdsql.Scanner]()
	valuerType  = reflect.TypeFor[driver.Valuer]()
	timeType    = reflect.TypeFor[time.Time]()
	models      sync.Map // reflect.Type -> *modelResult
)

// TableNamer lets a model choose its table name instead of the default, the plural snake case
// of its type name, such as order_items for OrderItem.
type TableNamer interface {
	TableName() string
}

// model maps a struct type to a table and its columns.
type model struct {
	typ      reflect.Type
	table    string
	fields   []*field
	byColumn map[string]*field
	pk       *field
}

type field struct {
	name    string
	column  string
	index   []int
	pk      bool
	dbValue bool
}

type modelResult struct {
	model *model
	err   error
}

// modelOf returns the cached mapping of t, a struct type.
func modelOf(t reflect.Type) (*model, error) {
	if cached, ok := models.Load(t); ok {
		r := cached.(*modelResult) //nolint:forcetypeassert // only modelOf stores values
		return r.model, r.err
	}
	m, err := buildModel(t)
	models.Store(t, &modelResult{model: m, err: err})
	return m, err
}

func buildModel(t reflect.Type) (*model, error) {
	if !isStructModel(t) {
		return nil, fmt.Errorf("%w: %s is not a struct", ErrInvalidModel, t)
	}
	m := &model{typ: t, byColumn: map[string]*field{}, table: tableName(t)}
	var problems []string
	for _, sf := range reflect.VisibleFields(t) {
		if !sf.IsExported() {
			continue
		}
		tag, hasTag := sf.Tag.Lookup("db")
		if tag == "-" {
			continue
		}
		if sf.Anonymous && !hasTag && isStructModel(sf.Type) {
			continue
		}
		if len(sf.Index) > 1 && embeddedThroughPointer(t, sf.Index) {
			problems = append(problems, fmt.Sprintf("%s.%s is embedded through a pointer; embed the struct itself", t.Name(), sf.Name))
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		if name == "" {
			name = snakeCase(sf.Name)
		}
		f := &field{name: sf.Name, column: name, index: sf.Index}
		for _, opt := range strings.Split(options, ",") {
			switch strings.TrimSpace(opt) {
			case "":
			case "pk":
				f.pk = true
			case "default":
				f.dbValue = true
			default:
				problems = append(problems, fmt.Sprintf("%s.%s has the unknown db option %q; use pk or default", t.Name(), sf.Name, opt))
			}
		}
		if isStructModel(sf.Type) {
			problems = append(problems, fmt.Sprintf("%s.%s has the struct type %s; embed it, tag it db:\"-\" or implement sql.Scanner and driver.Valuer", t.Name(), sf.Name, sf.Type))
			continue
		}
		if prev, ok := m.byColumn[f.column]; ok {
			problems = append(problems, fmt.Sprintf("%s.%s and %s.%s both map to the column %q", t.Name(), prev.name, t.Name(), f.name, f.column))
			continue
		}
		m.fields = append(m.fields, f)
		m.byColumn[f.column] = f
	}

	for _, f := range m.fields {
		if f.pk {
			if m.pk != nil {
				problems = append(problems, fmt.Sprintf("%s has more than one pk field: %s and %s", t.Name(), m.pk.name, f.name))
			}
			m.pk = f
		}
	}
	if m.pk == nil {
		for _, f := range m.fields {
			if f.name == "ID" {
				f.pk = true
				m.pk = f
			}
		}
	}
	if len(m.fields) == 0 {
		problems = append(problems, t.Name()+" has no exported fields to map to columns")
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidModel, strings.Join(problems, "; "))
	}
	return m, nil
}

// isStructModel reports whether t is a plain struct, not a type the driver scans itself.
func isStructModel(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t != timeType &&
		!reflect.PointerTo(t).Implements(scannerType) && !t.Implements(valuerType)
}

func embeddedThroughPointer(t reflect.Type, index []int) bool {
	for _, i := range index[:len(index)-1] {
		f := t.Field(i)
		if f.Type.Kind() == reflect.Pointer {
			return true
		}
		t = f.Type
	}
	return false
}

func tableName(t reflect.Type) (name string) {
	if namer, ok := reflect.Zero(t).Interface().(TableNamer); ok {
		// A TableName method promoted through an embedded pointer panics on the zero value; the
		// embedding itself is reported as a problem, so fall back to the default name.
		defer func() {
			if recover() != nil {
				name = plural(snakeCase(t.Name()))
			}
		}()
		return namer.TableName()
	}
	return plural(snakeCase(t.Name()))
}

// plural handles regular English nouns; implement TableNamer for the others.
func plural(name string) string {
	switch {
	case name == "":
		return name
	case strings.HasSuffix(name, "s"), strings.HasSuffix(name, "x"), strings.HasSuffix(name, "z"),
		strings.HasSuffix(name, "ch"), strings.HasSuffix(name, "sh"):
		return name + "es"
	case strings.HasSuffix(name, "y") && len(name) > 1 && !strings.ContainsRune("aeiou", rune(name[len(name)-2])):
		return name[:len(name)-1] + "ies"
	default:
		return name + "s"
	}
}

// snakeCase converts a Go name to a column name, e.g. CreatedAt to created_at and APIKey to api_key.
func snakeCase(name string) string {
	runes := []rune(name)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func (m *model) columns() []string {
	columns := make([]string, len(m.fields))
	for i, f := range m.fields {
		columns[i] = f.column
	}
	return columns
}
