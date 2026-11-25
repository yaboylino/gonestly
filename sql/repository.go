package sql

import (
	"context"
	"fmt"
	"reflect"
)

// Repository gives the table of T the usual create, read, update and delete operations, by
// primary key of type ID. The primary key is the field tagged db:",pk", or the field named ID.
//
// Register it as a provider directly: gonestly.Provide(sql.NewRepository[User, int64]).
type Repository[T any, ID comparable] struct {
	db    *DB
	model *model
}

// NewRepository returns a repository for T. It fails when T cannot be mapped to a table or its
// primary key cannot hold an ID, so mistakes surface when the application starts.
func NewRepository[T any, ID comparable](db *DB) (*Repository[T, ID], error) {
	t := reflect.TypeFor[T]()
	m, err := modelOf(t)
	if err != nil {
		return nil, err
	}
	if m.pk == nil {
		return nil, fmt.Errorf("%w: %s has no primary key; tag a field db:\",pk\" or name it ID", ErrInvalidModel, t.Name())
	}
	idType := reflect.TypeFor[ID]()
	if pkType := t.FieldByIndex(m.pk.index).Type; !idType.AssignableTo(pkType) {
		return nil, fmt.Errorf("%w: the primary key %s.%s is a %s, not a %s", ErrInvalidModel, t.Name(), m.pk.name, pkType, idType)
	}
	return &Repository[T, ID]{db: db, model: m}, nil
}

// Query starts a query on the table, for everything beyond the methods of the repository.
func (r *Repository[T, ID]) Query() *Builder[T] {
	return From[T](r.db)
}

// FindByID returns the row with the given primary key, or ErrNotFound.
func (r *Repository[T, ID]) FindByID(ctx context.Context, id ID) (T, error) {
	return r.Query().Where(comparison{r.model.pk.column, "=", id}).First(ctx)
}

// FindAll returns every row that matches all conditions, or every row without conditions.
func (r *Repository[T, ID]) FindAll(ctx context.Context, conditions ...Condition) ([]T, error) {
	return r.Query().Where(conditions...).All(ctx)
}

// Count returns the number of rows that match all conditions.
func (r *Repository[T, ID]) Count(ctx context.Context, conditions ...Condition) (int64, error) {
	return r.Query().Where(conditions...).Count(ctx)
}

// Create inserts v and fills it with the stored row, including generated values. A zero
// primary key and zero fields tagged db:",default" are left to the database.
func (r *Repository[T, ID]) Create(ctx context.Context, v *T) error {
	rv := reflect.ValueOf(v).Elem()
	b := &sqlBuilder{dialect: r.db.dialect}
	b.write("INSERT INTO ")
	b.ident(r.model.table)

	var columns []*field
	for _, f := range r.model.fields {
		if (f.pk || f.dbValue) && rv.FieldByIndex(f.index).IsZero() {
			continue
		}
		columns = append(columns, f)
	}
	if len(columns) == 0 {
		b.write(" DEFAULT VALUES")
	} else {
		b.write(" (")
		for i, f := range columns {
			if i > 0 {
				b.write(", ")
			}
			b.ident(f.column)
		}
		b.write(") VALUES (")
		for i, f := range columns {
			if i > 0 {
				b.write(", ")
			}
			b.arg(rv.FieldByIndex(f.index).Interface())
		}
		b.write(")")
	}
	r.returning(b)
	_, err := r.queryInto(ctx, b, rv)
	return err
}

// Update stores every field of v in the row with its primary key, and fills v with the stored
// row. It returns ErrNotFound when no row has that key.
func (r *Repository[T, ID]) Update(ctx context.Context, v *T) error {
	rv := reflect.ValueOf(v).Elem()
	b := &sqlBuilder{dialect: r.db.dialect}
	b.write("UPDATE ")
	b.ident(r.model.table)
	b.write(" SET ")
	first := true
	for _, f := range r.model.fields {
		if f.pk {
			continue
		}
		if !first {
			b.write(", ")
		}
		first = false
		b.ident(f.column)
		b.write(" = ")
		b.arg(rv.FieldByIndex(f.index).Interface())
	}
	if first {
		return fmt.Errorf("sql: %s has no columns to update besides its primary key", r.model.typ.Name())
	}
	b.write(" WHERE ")
	b.ident(r.model.pk.column)
	b.write(" = ")
	b.arg(rv.FieldByIndex(r.model.pk.index).Interface())
	r.returning(b)
	found, err := r.queryInto(ctx, b, rv)
	if err == nil && !found {
		return ErrNotFound
	}
	return err
}

// Delete removes the row with the given primary key, or returns ErrNotFound.
func (r *Repository[T, ID]) Delete(ctx context.Context, id ID) error {
	n, err := r.Query().Where(comparison{r.model.pk.column, "=", id}).Delete(ctx)
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (r *Repository[T, ID]) returning(b *sqlBuilder) {
	b.write(" RETURNING ")
	for i, f := range r.model.fields {
		if i > 0 {
			b.write(", ")
		}
		b.ident(f.column)
	}
}

func (r *Repository[T, ID]) queryInto(ctx context.Context, b *sqlBuilder, v reflect.Value) (bool, error) {
	rows, err := r.db.query(ctx, b.sql.String(), b.args)
	if err != nil {
		return false, err
	}
	return scanInto(r.db, rows, r.model, v)
}
