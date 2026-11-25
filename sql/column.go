package sql

import (
	"reflect"
	"strings"
)

// sqlBuilder collects SQL with ? placeholders and its arguments.
type sqlBuilder struct {
	dialect Dialect
	sql     strings.Builder
	args    []any
}

func (b *sqlBuilder) write(parts ...string) {
	for _, p := range parts {
		b.sql.WriteString(p)
	}
}

func (b *sqlBuilder) arg(v any) {
	b.sql.WriteByte('?')
	b.args = append(b.args, v)
}

func (b *sqlBuilder) ident(name string) {
	b.sql.WriteString(b.dialect.Quote(name))
}

// Condition is a WHERE clause, built from columns or with And, Or, Not and Raw.
type Condition interface {
	appendSQL(b *sqlBuilder)
}

// Column is a typed column of a table, used to build conditions, orderings and updates.
// The CLI generates them for your models; declare them by hand with Col.
type Column[T any] struct {
	name string
}

// Col declares a column of type T, such as Col[string]("email") or Col[int64]("users.id").
func Col[T any](name string) Column[T] {
	return Column[T]{name: name}
}

// Name returns the column name.
func (c Column[T]) Name() string { return c.name }

type comparison struct {
	column string
	op     string
	value  any
}

func (c comparison) appendSQL(b *sqlBuilder) {
	b.ident(c.column)
	b.write(" ", c.op, " ")
	b.arg(c.value)
}

type nullCheck struct {
	column string
	not    bool
}

func (c nullCheck) appendSQL(b *sqlBuilder) {
	b.ident(c.column)
	if c.not {
		b.write(" IS NOT NULL")
	} else {
		b.write(" IS NULL")
	}
}

type inList struct {
	column string
	values []any
	not    bool
}

func (c inList) appendSQL(b *sqlBuilder) {
	if len(c.values) == 0 {
		// An empty list matches nothing, and its negation matches everything.
		if c.not {
			b.write("1 = 1")
		} else {
			b.write("1 = 0")
		}
		return
	}
	b.ident(c.column)
	if c.not {
		b.write(" NOT")
	}
	b.write(" IN (")
	for i, v := range c.values {
		if i > 0 {
			b.write(", ")
		}
		b.arg(v)
	}
	b.write(")")
}

type between struct {
	column   string
	low, max any
}

func (c between) appendSQL(b *sqlBuilder) {
	b.ident(c.column)
	b.write(" BETWEEN ")
	b.arg(c.low)
	b.write(" AND ")
	b.arg(c.max)
}

func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

// Eq matches rows where the column equals v; a nil pointer matches NULL.
func (c Column[T]) Eq(v T) Condition {
	if isNilValue(v) {
		return nullCheck{column: c.name}
	}
	return comparison{c.name, "=", v}
}

// Ne matches rows where the column differs from v; a nil pointer matches values that are not NULL.
func (c Column[T]) Ne(v T) Condition {
	if isNilValue(v) {
		return nullCheck{column: c.name, not: true}
	}
	return comparison{c.name, "<>", v}
}

// Gt matches rows where the column is greater than v.
func (c Column[T]) Gt(v T) Condition { return comparison{c.name, ">", v} }

// Gte matches rows where the column is greater than or equal to v.
func (c Column[T]) Gte(v T) Condition { return comparison{c.name, ">=", v} }

// Lt matches rows where the column is less than v.
func (c Column[T]) Lt(v T) Condition { return comparison{c.name, "<", v} }

// Lte matches rows where the column is less than or equal to v.
func (c Column[T]) Lte(v T) Condition { return comparison{c.name, "<=", v} }

// Like matches rows where the column matches a LIKE pattern, such as "%@example.com".
func (c Column[T]) Like(pattern string) Condition { return comparison{c.name, "LIKE", pattern} }

// Between matches rows where the column lies between low and high, inclusive.
func (c Column[T]) Between(low, high T) Condition { return between{c.name, low, high} }

// In matches rows where the column equals one of the values; no values match nothing.
func (c Column[T]) In(values ...T) Condition { return inList{column: c.name, values: toAny(values)} }

// NotIn matches rows where the column equals none of the values.
func (c Column[T]) NotIn(values ...T) Condition {
	return inList{column: c.name, values: toAny(values), not: true}
}

// IsNull matches rows where the column is NULL.
func (c Column[T]) IsNull() Condition { return nullCheck{column: c.name} }

// IsNotNull matches rows where the column is not NULL.
func (c Column[T]) IsNotNull() Condition { return nullCheck{column: c.name, not: true} }

// Asc orders by the column, smallest first.
func (c Column[T]) Asc() Order { return Order{column: c.name} }

// Desc orders by the column, largest first.
func (c Column[T]) Desc() Order { return Order{column: c.name, desc: true} }

// Set assigns v to the column in Builder.Update.
func (c Column[T]) Set(v T) Assignment { return Assignment{column: c.name, value: v} }

func toAny[T any](values []T) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// Order is an ORDER BY term, created with Column.Asc or Column.Desc.
type Order struct {
	column string
	desc   bool
}

// Assignment sets a column in an update, created with Column.Set.
type Assignment struct {
	column string
	value  any
}

type group struct {
	op         string
	conditions []Condition
}

func (g group) appendSQL(b *sqlBuilder) {
	if len(g.conditions) == 0 {
		// An empty AND is true and an empty OR is false, as in logic.
		if g.op == " AND " {
			b.write("1 = 1")
		} else {
			b.write("1 = 0")
		}
		return
	}
	b.write("(")
	for i, c := range g.conditions {
		if i > 0 {
			b.write(g.op)
		}
		c.appendSQL(b)
	}
	b.write(")")
}

// And matches rows that satisfy every condition.
func And(conditions ...Condition) Condition { return group{op: " AND ", conditions: conditions} }

// Or matches rows that satisfy at least one condition.
func Or(conditions ...Condition) Condition { return group{op: " OR ", conditions: conditions} }

type not struct{ condition Condition }

func (n not) appendSQL(b *sqlBuilder) {
	b.write("NOT (")
	n.condition.appendSQL(b)
	b.write(")")
}

// Not matches rows that do not satisfy the condition.
func Not(condition Condition) Condition { return not{condition} }

type raw struct {
	sql  string
	args []any
}

func (r raw) appendSQL(b *sqlBuilder) {
	b.write("(", r.sql, ")")
	b.args = append(b.args, r.args...)
}

// Raw is a condition written in SQL with ? placeholders, for what columns cannot express:
//
//	sql.Raw("lower(email) = lower(?)", email)
func Raw(sql string, args ...any) Condition { return raw{sql: sql, args: args} }

// Always matches every row; pass it to Where to update or delete a whole table on purpose.
func Always() Condition { return raw{sql: "1 = 1"} }
