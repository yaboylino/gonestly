package sql

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

// CreateNested inserts v together with the rows in its relation fields, in one transaction:
// either all rows are stored or none.
//
//	user := User{Name: "Ada", Posts: []Post{{Title: "Engines"}}, Bio: &Bio{About: "Mathematician"}}
//	err := users.CreateNested(ctx, &user)
//
// The rows that belong to v, in slices and pointers, get the key of v. A row that v belongs to,
// in a pointer, is inserted first when it is new, which means its primary key is zero; one
// with a key is taken to exist. The whole tree is filled with the stored rows, so generated
// keys and defaults are there afterwards. Rows that are not in a relation field are left alone.
func (r *Repository[T, ID]) CreateNested(ctx context.Context, v *T) error {
	if err := r.model.resolveRelations(); err != nil {
		return err
	}
	st := &nestedState{seen: map[uintptr]bool{}}
	err := r.db.Transaction(ctx, func(ctx context.Context) error {
		return createNested(ctx, r.store, reflect.ValueOf(v).Elem(), st)
	})
	if err != nil {
		// The rolled back rows do not exist, so their keys must not look as if they do.
		for i := len(st.keys) - 1; i >= 0; i-- {
			st.keys[i].field.Set(st.keys[i].value)
		}
	}
	return err
}

// nestedState holds the rows that are handled, so that rows that point at each other are
// inserted once, and the primary keys they had before, to restore them when the insert fails.
type nestedState struct {
	seen map[uintptr]bool
	keys []savedKey
}

type savedKey struct{ field, value reflect.Value }

// createNested inserts the row in rv and the rows around it.
func createNested(ctx context.Context, s store, rv reflect.Value, st *nestedState) error {
	if st.seen[rv.Addr().Pointer()] {
		return nil
	}
	st.seen[rv.Addr().Pointer()] = true
	if err := s.model.resolveRelations(); err != nil {
		return err
	}
	if s.model.pk == nil {
		return fmt.Errorf("%w: %s has no primary key, which CreateNested needs to link rows", ErrInvalidModel, s.model.typ.Name())
	}

	// The rows that this row belongs to come first: they hold the key that this row needs.
	for _, rel := range s.model.relations {
		field := rv.FieldByIndex(rel.index)
		if rel.kind != belongsTo || field.IsNil() {
			continue
		}
		parent := field.Elem()
		if parent.FieldByIndex(rel.targetModel.pk.index).IsZero() {
			if err := createNested(ctx, store{db: s.db, model: rel.targetModel}, parent, st); err != nil {
				return fmt.Errorf("sql: creating %s.%s: %w", s.model.typ.Name(), rel.name, err)
			}
		}
		if err := copyKey(rv.FieldByIndex(rel.ownerField.index), parent.FieldByIndex(rel.otherField.index)); err != nil {
			return fmt.Errorf("sql: %s.%s: %w", s.model.typ.Name(), rel.name, err)
		}
	}

	pk := rv.FieldByIndex(s.model.pk.index)
	saved := reflect.New(pk.Type()).Elem()
	saved.Set(pk)
	st.keys = append(st.keys, savedKey{field: pk, value: saved})
	if err := s.create(ctx, rv); err != nil {
		return err
	}

	for _, rel := range s.model.relations {
		if rel.kind == belongsTo {
			continue
		}
		field := rv.FieldByIndex(rel.index)
		var children []reflect.Value
		switch {
		case rel.kind == hasMany:
			for i := range field.Len() {
				children = append(children, field.Index(i))
			}
		case !field.IsNil():
			children = append(children, field.Elem())
		}
		for _, child := range children {
			if err := copyKey(child.FieldByIndex(rel.otherField.index), rv.FieldByIndex(rel.ownerField.index)); err != nil {
				return fmt.Errorf("sql: %s.%s: %w", s.model.typ.Name(), rel.name, err)
			}
			if err := createNested(ctx, store{db: s.db, model: rel.targetModel}, child, st); err != nil {
				return fmt.Errorf("sql: creating %s.%s: %w", s.model.typ.Name(), rel.name, err)
			}
		}
	}
	return nil
}

// copyKey stores the key in src in dst, which can be another kind of number or a pointer.
func copyKey(dst, src reflect.Value) error {
	for src.Kind() == reflect.Pointer {
		if src.IsNil() {
			return errors.New("the key to copy is NULL")
		}
		src = src.Elem()
	}
	target := dst
	if dst.Kind() == reflect.Pointer {
		target = reflect.New(dst.Type().Elem()).Elem()
	}
	switch {
	case target.CanInt() && src.CanInt():
		target.SetInt(src.Int())
	case target.CanInt() && src.CanUint():
		target.SetInt(int64(src.Uint())) //nolint:gosec // a key above the range of int64 does not occur in practice
	case target.CanUint() && src.CanUint():
		target.SetUint(src.Uint())
	case target.CanUint() && src.CanInt():
		target.SetUint(uint64(src.Int())) //nolint:gosec // a key is never negative
	case src.Type().AssignableTo(target.Type()):
		target.Set(src)
	default:
		return fmt.Errorf("a %s key cannot be stored in a %s", src.Type(), target.Type())
	}
	if dst.Kind() == reflect.Pointer {
		dst.Set(target.Addr())
	}
	return nil
}
