package sql

import (
	"context"
	"fmt"
	"reflect"
	"slices"
)

// maxKeysPerQuery keeps the list of keys of one query under the argument limit of every database.
const maxKeysPerQuery = 10000

// Path is a relation that Include can load, optionally with relations of its own.
type Path[From any] interface {
	node() includeNode
	from(From)
}

type includeNode struct {
	name     string
	target   reflect.Type
	children []includeNode
}

// Rel names a relation of From to rows of To, such as the posts of a user. `gonestly generate
// columns` writes one for every relation of a model, in <Model>Relations.
type Rel[From, To any] struct {
	name   string
	target reflect.Type
	inner  []includeNode
}

// Relation returns the relation that the field with the given Go name declares.
func Relation[From, To any](field string) Rel[From, To] {
	return Rel[From, To]{name: field, target: reflect.TypeFor[To]()}
}

// With also loads relations of the related rows, such as the comments of the posts of a user.
// Every level costs one query, however many rows there are.
func (r Rel[From, To]) With(inner ...Path[To]) Rel[From, To] {
	r.inner = slices.Clone(r.inner)
	for _, path := range inner {
		r.inner = append(r.inner, path.node())
	}
	return r
}

func (r Rel[From, To]) node() includeNode {
	return includeNode{name: r.name, target: r.target, children: r.inner}
}
func (Rel[From, To]) from(From) {}

func includeNodes[T any](paths []Path[T]) []includeNode {
	nodes := make([]includeNode, len(paths))
	for i, path := range paths {
		nodes[i] = path.node()
	}
	return nodes
}

// checkNodes reports the first relation in the tree that the models do not have.
func checkNodes(m *model, nodes []includeNode) error {
	for _, node := range nodes {
		rel, err := m.relation(node.name)
		if err != nil {
			return err
		}
		if node.target != nil && node.target != rel.target {
			return fmt.Errorf("%w: %s.%s holds %s, not %s; regenerate the relations with gonestly generate columns",
				ErrInvalidModel, m.typ.Name(), node.name, rel.target.Name(), node.target.Name())
		}
		if err := checkNodes(rel.targetModel, node.children); err != nil {
			return err
		}
	}
	return nil
}

// loadRelations fills the relation fields of rows, one query per relation. rows are the
// addressable struct values of model m.
func loadRelations(ctx context.Context, db *DB, m *model, rows []reflect.Value, nodes []includeNode) error {
	if len(rows) == 0 {
		return nil
	}
	for _, node := range nodes {
		rel, err := m.relation(node.name)
		if err != nil {
			return err
		}
		related, err := loadRelation(ctx, db, rel, rows)
		if err != nil {
			return fmt.Errorf("sql: loading %s.%s: %w", m.typ.Name(), rel.name, err)
		}
		if len(node.children) > 0 {
			if err := loadRelations(ctx, db, rel.targetModel, related, node.children); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadRelation loads one relation for all rows and returns the addressable related rows that
// were assigned, so that their own relations can be loaded next.
func loadRelation(ctx context.Context, db *DB, rel *relation, rows []reflect.Value) ([]reflect.Value, error) {
	var keys []any
	seen := map[any]bool{}
	for _, row := range rows {
		key, ok, err := keyOf(row.FieldByIndex(rel.ownerField.index))
		if err != nil {
			return nil, err
		}
		if ok && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}

	children, err := queryByKeys(ctx, db, rel.targetModel, rel.otherField, keys)
	if err != nil {
		return nil, err
	}
	byKey := map[any][]int{}
	for i := range children.Len() {
		if key, ok, err := keyOf(children.Index(i).FieldByIndex(rel.otherField.index)); err != nil {
			return nil, err
		} else if ok {
			byKey[key] = append(byKey[key], i)
		}
	}

	var related []reflect.Value
	for _, row := range rows {
		field := row.FieldByIndex(rel.index)
		key, ok, _ := keyOf(row.FieldByIndex(rel.ownerField.index))
		matches := byKey[key]
		if !ok {
			matches = nil
		}
		switch rel.kind {
		case hasMany:
			// An empty slice, not nil, so that a row without related rows encodes as [].
			list := reflect.MakeSlice(field.Type(), len(matches), len(matches))
			for j, i := range matches {
				list.Index(j).Set(children.Index(i))
			}
			field.Set(list)
			for j := range matches {
				related = append(related, field.Index(j))
			}
		default:
			field.SetZero()
			if len(matches) == 0 {
				continue
			}
			// Every row gets a copy of its own, so changing one does not change another.
			child := reflect.New(rel.target)
			child.Elem().Set(children.Index(matches[0]))
			field.Set(child)
			related = append(related, child.Elem())
		}
	}
	return related, nil
}

// queryByKeys returns the rows of m whose column has one of the keys, ordered by primary key.
func queryByKeys(ctx context.Context, db *DB, m *model, column *field, keys []any) (reflect.Value, error) {
	all := reflect.MakeSlice(reflect.SliceOf(m.typ), 0, 0)
	for chunk := range slices.Chunk(keys, maxKeysPerQuery) {
		b := &sqlBuilder{dialect: db.dialect}
		b.write("SELECT ")
		for i, f := range m.fields {
			if i > 0 {
				b.write(", ")
			}
			b.ident(f.column)
		}
		b.write(" FROM ")
		b.ident(m.table)
		b.write(" WHERE ")
		inList{column: column.column, values: chunk}.appendSQL(b)
		if m.pk != nil {
			b.write(" ORDER BY ")
			b.ident(m.pk.column)
		}
		rows, err := db.query(ctx, b.sql.String(), b.args)
		if err != nil {
			return all, err
		}
		found, err := scanStructs(db, rows, m)
		if err != nil {
			return all, err
		}
		all = reflect.AppendSlice(all, found)
	}
	return all, nil
}

// keyOf returns a value that rows can be matched by: numbers are int64 whatever their type, and
// a nil pointer, which is a NULL column, has no key.
func keyOf(v reflect.Value) (any, bool, error) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, false, nil
		}
		v = v.Elem()
	}
	switch {
	case v.CanInt():
		return v.Int(), true, nil
	case v.CanUint():
		return int64(v.Uint()), true, nil //nolint:gosec // a key above the range of int64 does not occur in practice
	case !v.Type().Comparable():
		return nil, false, fmt.Errorf("%w: the key type %s cannot be compared", ErrInvalidModel, v.Type())
	default:
		return v.Interface(), true, nil
	}
}
