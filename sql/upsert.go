package sql

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// maxStatementArgs keeps a statement for many rows under the argument limit of every database.
const maxStatementArgs = 30000

// CreateAll inserts every value, in as few statements as possible and in one transaction, so
// either all rows are stored or none. Unlike Create it does not fill the values with the
// stored rows; query them when generated keys are needed.
func (r *Repository[T, ID]) CreateAll(ctx context.Context, values []T) error {
	return r.insertAll(ctx, values, nil, false)
}

// Upsert inserts v, or updates the row that already has its key, and fills v with the stored
// row. The key is the primary key, or the given unique columns, such as "email". An update
// keeps the primary key and the zero fields tagged db:",default", such as a creation time.
//
// MySQL does not look at the columns: there, a clash with any unique key updates that row.
func (r *Repository[T, ID]) Upsert(ctx context.Context, v *T, columns ...string) error {
	conflict, err := r.conflictColumns(columns)
	if err != nil {
		return err
	}
	rv := reflect.ValueOf(v).Elem()
	b := &sqlBuilder{dialect: r.db.dialect}
	fields := r.insertFields(rv)
	r.writeInsert(b, fields, []reflect.Value{rv})
	r.db.dialect.upsert(b, conflict, r.updateColumns(fields, conflict), r.autoKey())

	if r.db.dialect.returning() {
		b.write(" RETURNING ")
		r.columns(b)
		_, err := r.queryInto(ctx, b, rv)
		return err
	}
	return r.db.Transaction(ctx, func(ctx context.Context) error {
		result, err := r.db.Exec(ctx, b.sql.String(), b.args...)
		if err != nil {
			return err
		}
		// The database reports the key of the row, new or existing, when it generates the key.
		by := conflict
		if key := rv.FieldByIndex(r.model.pk.index); key.IsZero() && r.autoKey() != "" {
			if err := r.setGeneratedKey(key, result); err != nil {
				return err
			}
			by = []string{r.model.pk.column}
		}
		found, err := r.reloadBy(ctx, rv, by)
		if err == nil && !found {
			return fmt.Errorf("sql: the upserted %s could not be read back", r.model.typ.Name())
		}
		return err
	})
}

// UpsertAll inserts every value or updates the rows that already have their key, in as few
// statements as possible and in one transaction. Like CreateAll it does not fill the values.
func (r *Repository[T, ID]) UpsertAll(ctx context.Context, values []T, columns ...string) error {
	conflict, err := r.conflictColumns(columns)
	if err != nil {
		return err
	}
	return r.insertAll(ctx, values, conflict, true)
}

func (r *Repository[T, ID]) insertAll(ctx context.Context, values []T, conflict []string, upsert bool) error {
	if len(values) == 0 {
		return nil
	}
	rows := reflect.ValueOf(values)
	return r.db.Transaction(ctx, func(ctx context.Context) error {
		// Rows that leave the same columns to the database share a statement.
		for start := 0; start < rows.Len(); {
			fields := r.insertFields(rows.Index(start))
			batch := []reflect.Value{rows.Index(start)}
			limit := max(1, maxStatementArgs/max(1, len(fields)))
			if len(fields) == 0 {
				limit = 1
			}
			for i := start + 1; i < rows.Len() && len(batch) < limit && slices.Equal(fields, r.insertFields(rows.Index(i))); i++ {
				batch = append(batch, rows.Index(i))
			}
			b := &sqlBuilder{dialect: r.db.dialect}
			r.writeInsert(b, fields, batch)
			if upsert {
				r.db.dialect.upsert(b, conflict, r.updateColumns(fields, conflict), r.autoKey())
			}
			if _, err := r.db.Exec(ctx, b.sql.String(), b.args...); err != nil {
				return err
			}
			start += len(batch)
		}
		return nil
	})
}

// insertFields are the fields to insert for a row: all but a zero key and zero defaults.
func (r *Repository[T, ID]) insertFields(row reflect.Value) []*field {
	var fields []*field
	for _, f := range r.model.fields {
		if (f.pk || f.dbValue) && row.FieldByIndex(f.index).IsZero() {
			continue
		}
		fields = append(fields, f)
	}
	return fields
}

func (r *Repository[T, ID]) writeInsert(b *sqlBuilder, fields []*field, rows []reflect.Value) {
	b.write("INSERT INTO ")
	b.ident(r.model.table)
	if len(fields) == 0 {
		b.write(r.db.dialect.emptyInsert())
		return
	}
	b.write(" (")
	for i, f := range fields {
		if i > 0 {
			b.write(", ")
		}
		b.ident(f.column)
	}
	b.write(") VALUES ")
	for n, row := range rows {
		if n > 0 {
			b.write(", ")
		}
		b.write("(")
		for i, f := range fields {
			if i > 0 {
				b.write(", ")
			}
			b.arg(row.FieldByIndex(f.index).Interface())
		}
		b.write(")")
	}
}

// conflictColumns checks the columns that identify an existing row; the default is the key.
func (r *Repository[T, ID]) conflictColumns(columns []string) ([]string, error) {
	if len(columns) == 0 {
		return []string{r.model.pk.column}, nil
	}
	for _, column := range columns {
		if _, ok := r.model.byColumn[column]; !ok {
			return nil, fmt.Errorf("%w: %s has no column %q to upsert on; it has %s", ErrInvalidModel, r.model.typ.Name(), column, strings.Join(r.model.columns(), ", "))
		}
	}
	return columns, nil
}

// updateColumns are the inserted columns that an upsert overwrites in an existing row.
func (r *Repository[T, ID]) updateColumns(fields []*field, conflict []string) []string {
	var columns []string
	for _, f := range fields {
		if !f.pk && !slices.Contains(conflict, f.column) {
			columns = append(columns, f.column)
		}
	}
	return columns
}

// autoKey is the primary key column when its type is a number that a database can generate.
func (r *Repository[T, ID]) autoKey() string {
	if key := r.model.typ.FieldByIndex(r.model.pk.index).Type; key.Kind() >= reflect.Int && key.Kind() <= reflect.Uint64 {
		return r.model.pk.column
	}
	return ""
}

// reloadBy fills v with the stored row whose columns have the values of v.
func (r *Repository[T, ID]) reloadBy(ctx context.Context, v reflect.Value, columns []string) (bool, error) {
	b := &sqlBuilder{dialect: r.db.dialect}
	b.write("SELECT ")
	r.columns(b)
	b.write(" FROM ")
	b.ident(r.model.table)
	for i, column := range columns {
		if i == 0 {
			b.write(" WHERE ")
		} else {
			b.write(" AND ")
		}
		b.ident(column)
		b.write(" = ")
		b.arg(v.FieldByIndex(r.model.byColumn[column].index).Interface())
	}
	return r.queryInto(ctx, b, v)
}
