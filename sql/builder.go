package sql

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
)

// Builder builds a query on the table of T. Every method returns a new query, so a query can be
// shared and extended safely.
type Builder[T any] struct {
	db      *DB
	model   *model
	err     error
	where   []Condition
	orders  []Order
	limit   int
	offset  int
	columns []string
}

// From starts a query on the table of T, selecting every mapped column.
//
//	users, err := sql.From[User](db).Where(email.Like("%@example.com")).OrderBy(name.Asc()).All(ctx)
func From[T any](db *DB) *Builder[T] {
	m, err := modelOf(reflect.TypeFor[T]())
	q := &Builder[T]{db: db, model: m, err: err}
	if m != nil {
		q.columns = m.columns()
	}
	return q
}

func (q *Builder[T]) clone() *Builder[T] {
	c := *q
	c.where = slices.Clip(q.where)
	c.orders = slices.Clip(q.orders)
	return &c
}

// Where adds conditions; all of them must match.
func (q *Builder[T]) Where(conditions ...Condition) *Builder[T] {
	c := q.clone()
	c.where = append(c.where, conditions...)
	return c
}

// OrderBy adds orderings, applied in order.
func (q *Builder[T]) OrderBy(orders ...Order) *Builder[T] {
	c := q.clone()
	c.orders = append(c.orders, orders...)
	return c
}

// Limit returns at most n rows.
func (q *Builder[T]) Limit(n int) *Builder[T] {
	c := q.clone()
	c.limit = n
	return c
}

// Offset skips the first n rows.
func (q *Builder[T]) Offset(n int) *Builder[T] {
	c := q.clone()
	c.offset = n
	return c
}

// All returns every matching row.
func (q *Builder[T]) All(ctx context.Context) ([]T, error) {
	b, err := q.selectSQL()
	if err != nil {
		return nil, err
	}
	rows, err := q.db.query(ctx, b.sql.String(), b.args)
	if err != nil {
		return nil, err
	}
	return scanRows[T](rows)
}

// First returns the first matching row, or ErrNotFound.
func (q *Builder[T]) First(ctx context.Context) (T, error) {
	var zero T
	rows, err := q.Limit(1).All(ctx)
	if err != nil {
		return zero, err
	}
	if len(rows) == 0 {
		return zero, ErrNotFound
	}
	return rows[0], nil
}

// Count returns the number of matching rows, ignoring order, limit and offset.
func (q *Builder[T]) Count(ctx context.Context) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	b := q.builder()
	b.write("SELECT COUNT(*) FROM ")
	b.ident(q.model.table)
	q.appendWhere(b)
	rows, err := q.db.query(ctx, b.sql.String(), b.args)
	if err != nil {
		return 0, err
	}
	counts, err := scanRows[int64](rows)
	if err != nil {
		return 0, err
	}
	return counts[0], nil
}

// Exists reports whether at least one row matches; it stops at the first one.
func (q *Builder[T]) Exists(ctx context.Context) (bool, error) {
	if q.err != nil {
		return false, q.err
	}
	b := q.builder()
	b.write("SELECT 1 FROM ")
	b.ident(q.model.table)
	q.appendWhere(b)
	b.write(" LIMIT 1")
	rows, err := q.db.query(ctx, b.sql.String(), b.args)
	if err != nil {
		return false, err
	}
	found, err := scanRows[int64](rows)
	return len(found) > 0, err
}

// Update sets columns on every matching row and returns the number of rows changed. Without
// a condition it returns ErrUnsafeQuery; use Where(sql.Always()) to update every row.
func (q *Builder[T]) Update(ctx context.Context, assignments ...Assignment) (int64, error) {
	if err := q.checkWrite(); err != nil {
		return 0, err
	}
	if len(assignments) == 0 {
		return 0, errors.New("sql: Update needs at least one assignment")
	}
	b := q.builder()
	b.write("UPDATE ")
	b.ident(q.model.table)
	b.write(" SET ")
	for i, a := range assignments {
		if i > 0 {
			b.write(", ")
		}
		b.ident(a.column)
		b.write(" = ")
		b.arg(a.value)
	}
	q.appendWhere(b)
	return q.exec(ctx, b)
}

// Delete removes every matching row and returns the number of rows removed. Without a
// condition it returns ErrUnsafeQuery; use Where(sql.Always()) to empty the table.
func (q *Builder[T]) Delete(ctx context.Context) (int64, error) {
	if err := q.checkWrite(); err != nil {
		return 0, err
	}
	b := q.builder()
	b.write("DELETE FROM ")
	b.ident(q.model.table)
	q.appendWhere(b)
	return q.exec(ctx, b)
}

// SQL returns the SELECT statement with ? placeholders and its arguments, for logging or tests.
func (q *Builder[T]) SQL() (string, []any, error) {
	b, err := q.selectSQL()
	if err != nil {
		return "", nil, err
	}
	return b.sql.String(), b.plainArgs(), nil
}

func (q *Builder[T]) selectSQL() (*sqlBuilder, error) {
	if q.err != nil {
		return nil, q.err
	}
	b := q.builder()
	b.write("SELECT ")
	for i, column := range q.columns {
		if i > 0 {
			b.write(", ")
		}
		b.ident(column)
	}
	b.write(" FROM ")
	b.ident(q.model.table)
	q.appendWhere(b)
	for i, o := range q.orders {
		if i == 0 {
			b.write(" ORDER BY ")
		} else {
			b.write(", ")
		}
		b.ident(o.column)
		if o.desc {
			b.write(" DESC")
		}
	}
	if q.limit > 0 {
		b.write(" LIMIT ", strconv.Itoa(q.limit))
	}
	if q.offset > 0 {
		if q.limit == 0 && q.db.dialect == SQLite {
			// SQLite only accepts OFFSET after a LIMIT; -1 means no limit.
			b.write(" LIMIT -1")
		}
		b.write(" OFFSET ", strconv.Itoa(q.offset))
	}
	return b, nil
}

func (q *Builder[T]) builder() *sqlBuilder {
	return &sqlBuilder{dialect: q.db.dialect}
}

func (q *Builder[T]) appendWhere(b *sqlBuilder) {
	if len(q.where) == 0 {
		return
	}
	b.write(" WHERE ")
	And(q.where...).appendSQL(b)
}

func (q *Builder[T]) checkWrite() error {
	if q.err != nil {
		return q.err
	}
	if len(q.where) == 0 {
		return ErrUnsafeQuery
	}
	return nil
}

func (q *Builder[T]) exec(ctx context.Context, b *sqlBuilder) (int64, error) {
	result, err := q.db.Exec(ctx, b.sql.String(), b.args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
