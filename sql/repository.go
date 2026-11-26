package sql

import (
	"context"
	stdsql "database/sql"
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
// primary key and zero fields tagged db:",default" are left to the database. In MySQL the
// database can only generate an auto increment key; set any other key before Create.
func (r *Repository[T, ID]) Create(ctx context.Context, v *T) error {
	rv := reflect.ValueOf(v).Elem()
	b := &sqlBuilder{dialect: r.db.dialect}
	r.writeInsert(b, r.insertFields(rv), []reflect.Value{rv})
	if r.db.dialect.returning() {
		b.write(" RETURNING ")
		r.columns(b)
		_, err := r.queryInto(ctx, b, rv)
		return err
	}
	// Without RETURNING the row is read back in the same transaction, on the same connection.
	return r.db.Transaction(ctx, func(ctx context.Context) error {
		result, err := r.db.Exec(ctx, b.sql.String(), b.args...)
		if err != nil {
			return err
		}
		key := rv.FieldByIndex(r.model.pk.index)
		if key.IsZero() {
			if err := r.setGeneratedKey(key, result); err != nil {
				return err
			}
		}
		found, err := r.reload(ctx, rv)
		if err == nil && !found {
			return fmt.Errorf("sql: the created %s could not be read back", r.model.typ.Name())
		}
		return err
	})
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
	if r.db.dialect.returning() {
		b.write(" RETURNING ")
		r.columns(b)
		found, err := r.queryInto(ctx, b, rv)
		if err == nil && !found {
			return ErrNotFound
		}
		return err
	}
	// The number of changed rows is zero for an unchanged row too, so the row itself tells.
	return r.db.Transaction(ctx, func(ctx context.Context) error {
		if _, err := r.db.Exec(ctx, b.sql.String(), b.args...); err != nil {
			return err
		}
		found, err := r.reload(ctx, rv)
		if err == nil && !found {
			return ErrNotFound
		}
		return err
	})
}

// Delete removes the row with the given primary key, or returns ErrNotFound.
func (r *Repository[T, ID]) Delete(ctx context.Context, id ID) error {
	n, err := r.Query().Where(comparison{r.model.pk.column, "=", id}).Delete(ctx)
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (r *Repository[T, ID]) columns(b *sqlBuilder) {
	for i, f := range r.model.fields {
		if i > 0 {
			b.write(", ")
		}
		b.ident(f.column)
	}
}

// reload fills v with the stored row that has its primary key.
func (r *Repository[T, ID]) reload(ctx context.Context, v reflect.Value) (bool, error) {
	return r.reloadBy(ctx, v, []string{r.model.pk.column})
}

// setGeneratedKey stores the auto increment value of an insert in the primary key field.
func (r *Repository[T, ID]) setGeneratedKey(key reflect.Value, result stdsql.Result) error {
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	switch {
	case key.CanInt():
		key.SetInt(id)
	case key.CanUint():
		key.SetUint(uint64(id)) //nolint:gosec // an auto increment value is never negative
	default:
		return fmt.Errorf("sql: %s cannot return the generated %s.%s, a %s; set it before Create",
			r.db.dialect.Name(), r.model.typ.Name(), r.model.pk.name, key.Type())
	}
	return nil
}

func (r *Repository[T, ID]) queryInto(ctx context.Context, b *sqlBuilder, v reflect.Value) (bool, error) {
	rows, err := r.db.query(ctx, b.sql.String(), b.args)
	if err != nil {
		return false, err
	}
	return scanInto(r.db, rows, r.model, v)
}
