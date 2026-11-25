package sql

import (
	stdsql "database/sql"
	"fmt"
	"reflect"
)

// scanRows reads every row into a T. A struct T is filled by column name; any other T is
// read from the single column of the result.
func scanRows[T any](rows *stdsql.Rows) ([]T, error) {
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	t := reflect.TypeFor[T]()

	var fields []*field
	switch {
	case isStructModel(t):
		m, err := modelOf(t)
		if err != nil {
			return nil, err
		}
		fields = make([]*field, len(columns))
		for i, column := range columns {
			f, ok := m.byColumn[column]
			if !ok {
				return nil, fmt.Errorf("sql: the column %q has no field in %s; add a field with db:%q", column, t.Name(), column)
			}
			fields[i] = f
		}
	case t.Kind() == reflect.Pointer && isStructModel(t.Elem()):
		return nil, fmt.Errorf("sql: scan into %s, not into a pointer to it", t.Elem())
	case len(columns) != 1:
		return nil, fmt.Errorf("sql: scanning into %s needs one column, the query returned %d", t, len(columns))
	}

	var result []T
	dest := make([]any, len(columns))
	for rows.Next() {
		var v T
		rv := reflect.ValueOf(&v).Elem()
		if fields == nil {
			dest[0] = &v
		} else {
			for i, f := range fields {
				dest[i] = rv.FieldByIndex(f.index).Addr().Interface()
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("sql: scanning a row into %s: %w", t, err)
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// scanInto fills the struct v with the single row of rows, for RETURNING clauses.
func scanInto(rows *stdsql.Rows, m *model, v reflect.Value) (bool, error) {
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}
	if !rows.Next() {
		return false, rows.Err()
	}
	dest := make([]any, len(columns))
	for i, column := range columns {
		f, ok := m.byColumn[column]
		if !ok {
			return false, fmt.Errorf("sql: the column %q has no field in %s", column, m.typ.Name())
		}
		dest[i] = v.FieldByIndex(f.index).Addr().Interface()
	}
	if err := rows.Scan(dest...); err != nil {
		return false, fmt.Errorf("sql: scanning a row into %s: %w", m.typ, err)
	}
	return true, rows.Err()
}
