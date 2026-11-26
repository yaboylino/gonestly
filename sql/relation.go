package sql

import (
	"fmt"
	"reflect"
	"strings"
)

type relationKind int

const (
	// hasMany is a slice of rows of another model that hold the key of this one.
	hasMany relationKind = iota
	// hasOne is one row of another model that holds the key of this one.
	hasOne
	// belongsTo is one row of another model, whose key this model holds.
	belongsTo
)

func (k relationKind) String() string {
	return [...]string{"has many", "has one", "belongs to"}[k]
}

// relation connects two models through a key column. The field name and tag come from the model
// that declares it; the models and fields it refers to are looked up when it is first used.
type relation struct {
	name   string
	index  []int
	kind   relationKind
	target reflect.Type
	// key is the column that holds the other side's key: in the target for hasMany and hasOne,
	// in the declaring model for belongsTo. references is the column it points at, or "" for
	// the primary key.
	key        string
	references string
	// direction is hasOne or belongsTo when the tag says which side holds the key, and
	// otherwise the direction of a pointer is read from the columns.
	direction *relationKind

	targetModel *model
	// ownerField is the field of the model that declares the relation that takes part in the
	// join, and otherField the one of the target.
	ownerField, otherField *field
}

// parseRelation reads a field tagged rel:"<key column>[,references=<column>][,has|belongs]".
func parseRelation(owner reflect.Type, sf reflect.StructField, tag string) (*relation, string) {
	where := owner.Name() + "." + sf.Name
	key, options, _ := strings.Cut(tag, ",")
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, where + " has an empty rel tag; name the column that holds the key, such as rel:\"author_id\""
	}
	rel := &relation{name: sf.Name, index: sf.Index, key: key}
	for _, option := range strings.Split(options, ",") {
		option = strings.TrimSpace(option)
		switch name, value, _ := strings.Cut(option, "="); {
		case option == "":
		case name == "references" && value != "":
			rel.references = value
		case option == "has", option == "belongs":
			kind := hasOne
			if option == "belongs" {
				kind = belongsTo
			}
			rel.direction = &kind
		default:
			return nil, fmt.Sprintf("%s has the unknown rel option %q; use references=<column>, has or belongs", where, option)
		}
	}

	t := sf.Type
	switch {
	case t.Kind() == reflect.Slice && isStructModel(t.Elem()):
		rel.kind, rel.target = hasMany, t.Elem()
	case t.Kind() == reflect.Pointer && isStructModel(t.Elem()):
		rel.kind, rel.target = hasOne, t.Elem()
	case isStructModel(t):
		return nil, fmt.Sprintf("%s is a %s, which cannot tell a missing row from an empty one; use *%s", where, t, t)
	default:
		return nil, fmt.Sprintf("%s has the type %s; a relation is a slice of structs for rows that belong to this one, or a pointer to a struct", where, t)
	}
	return rel, ""
}

// relation returns the relation with the Go name of its field.
func (m *model) relation(name string) (*relation, error) {
	if err := m.resolveRelations(); err != nil {
		return nil, err
	}
	rel, ok := m.byRel[name]
	if !ok {
		names := make([]string, len(m.relations))
		for i, r := range m.relations {
			names[i] = r.name
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("%w: %s has no relations; declare one with a field tagged rel:\"<key column>\"", ErrInvalidModel, m.typ.Name())
		}
		return nil, fmt.Errorf("%w: %s has no relation %q; it has %s", ErrInvalidModel, m.typ.Name(), name, strings.Join(names, ", "))
	}
	return rel, nil
}

// resolveRelations checks every relation against the model it points at, once.
func (m *model) resolveRelations() error {
	m.relOnce.Do(func() {
		var problems []string
		for _, rel := range m.relations {
			if problem := m.resolve(rel); problem != "" {
				problems = append(problems, problem)
			}
		}
		if len(problems) > 0 {
			m.relErr = fmt.Errorf("%w: %s", ErrInvalidModel, strings.Join(problems, "; "))
		}
	})
	return m.relErr
}

func (m *model) resolve(rel *relation) string {
	where := m.typ.Name() + "." + rel.name
	target, err := modelOf(rel.target)
	if err != nil {
		return fmt.Sprintf("%s points at %s, which is not a valid model: %v", where, rel.target.Name(), err)
	}
	rel.targetModel = target

	if rel.kind != hasMany {
		switch {
		case rel.direction != nil:
			rel.kind = *rel.direction
		case m.byColumn[rel.key] != nil && target.byColumn[rel.key] != nil:
			return fmt.Sprintf("%s is ambiguous: both %s and %s have the column %q; add ,belongs if %s holds the key or ,has if %s does",
				where, m.typ.Name(), target.typ.Name(), rel.key, m.typ.Name(), target.typ.Name())
		case m.byColumn[rel.key] != nil:
			rel.kind = belongsTo
		}
	} else if rel.direction != nil {
		return where + " is a slice, which always holds rows that carry the key; remove has or belongs"
	}

	// The side that holds the key, and the side that is pointed at.
	holder, pointed := target, m
	if rel.kind == belongsTo {
		holder, pointed = m, target
	}
	keyField, ok := holder.byColumn[rel.key]
	if !ok {
		return fmt.Sprintf("%s uses the key column %q, but %s has no such column; it has %s", where, rel.key, holder.typ.Name(), strings.Join(holder.columns(), ", "))
	}
	pointedField := pointed.pk
	if rel.references != "" {
		if pointedField, ok = pointed.byColumn[rel.references]; !ok {
			return fmt.Sprintf("%s references the column %q, but %s has no such column", where, rel.references, pointed.typ.Name())
		}
	}
	if pointedField == nil {
		return fmt.Sprintf("%s needs a primary key in %s to point at; tag a field db:\",pk\" or name it ID", where, pointed.typ.Name())
	}
	keyType, pointedType := holder.typ.FieldByIndex(keyField.index).Type, pointed.typ.FieldByIndex(pointedField.index).Type
	if !joinable(keyType, pointedType) {
		return fmt.Sprintf("%s joins %s.%s (%s) with %s.%s (%s), which are different kinds of keys",
			where, holder.typ.Name(), keyField.name, keyType, pointed.typ.Name(), pointedField.name, pointedType)
	}
	if rel.kind == belongsTo {
		rel.ownerField, rel.otherField = keyField, pointedField
	} else {
		rel.ownerField, rel.otherField = pointedField, keyField
	}
	return ""
}

// joinable reports whether two key types can be compared: numbers with numbers, and otherwise
// the same type, with or without a pointer for a nullable column.
func joinable(a, b reflect.Type) bool {
	a, b = indirectType(a), indirectType(b)
	if isIntegerKind(a.Kind()) && isIntegerKind(b.Kind()) {
		return true
	}
	return a == b
}

func indirectType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}

func isIntegerKind(k reflect.Kind) bool {
	return k >= reflect.Int && k <= reflect.Uint64
}
