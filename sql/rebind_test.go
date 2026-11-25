package sql

import (
	"reflect"
	"strings"
	"testing"
)

func TestRebind(t *testing.T) {
	tests := []struct {
		dialect Dialect
		query   string
		args    []any
		want    string
		wantArg []any
	}{
		{Postgres, "SELECT * FROM t WHERE a = ? AND b = ?", []any{1, 2}, "SELECT * FROM t WHERE a = $1 AND b = $2", []any{1, 2}},
		{SQLite, "SELECT * FROM t WHERE a = ?", []any{1}, "SELECT * FROM t WHERE a = ?", []any{1}},
		{Postgres, "SELECT '?', \"col?\", 'it''s ?' FROM t WHERE a = ?", []any{1}, "SELECT '?', \"col?\", 'it''s ?' FROM t WHERE a = $1", []any{1}},
		{Postgres, "SELECT 1 -- what?\nWHERE a = ? /* why? */", []any{1}, "SELECT 1 -- what?\nWHERE a = $1 /* why? */", []any{1}},
		{Postgres, "SELECT $body$ ? $body$, $$?$$ WHERE a = ?", []any{1}, "SELECT $body$ ? $body$, $$?$$ WHERE a = $1", []any{1}},
		{Postgres, "SELECT data ?? 'key' FROM t WHERE id = ?", []any{1}, "SELECT data ? 'key' FROM t WHERE id = $1", []any{1}},
		{Postgres, "WHERE id IN (?) AND name = ?", []any{[]int{1, 2, 3}, "x"}, "WHERE id IN ($1, $2, $3) AND name = $4", []any{1, 2, 3, "x"}},
		{Postgres, "WHERE data = ?", []any{[]byte("raw")}, "WHERE data = $1", []any{[]byte("raw")}},
		{Postgres, "WHERE price > $1x AND a = ?", []any{1}, "WHERE price > $1x AND a = $1", []any{1}},
		{Postgres, "SELECT 1 /* unterminated", nil, "SELECT 1 /* unterminated", []any{}},
		{Postgres, "SELECT 1 -- comment", nil, "SELECT 1 -- comment", []any{}},
	}
	for _, tt := range tests {
		got, args, err := rebind(tt.dialect, tt.query, tt.args)
		if err != nil || got != tt.want || !reflect.DeepEqual(args, tt.wantArg) {
			t.Errorf("rebind(%q)\n  got  %q %v %v\n  want %q %v", tt.query, got, args, err, tt.want, tt.wantArg)
		}
	}

	for _, tt := range []struct {
		query string
		args  []any
		want  string
	}{
		{"a = ? AND b = ?", []any{1}, "more placeholders than the 1 arguments"},
		{"a = ?", []any{1, 2}, "the query has 1 placeholders but 2 arguments"},
		{"a IN (?)", []any{[]int{}}, "argument 1 is an empty slice"},
	} {
		if _, _, err := rebind(Postgres, tt.query, tt.args); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("rebind(%q) error = %v, want %q", tt.query, err, tt.want)
		}
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{"OrderItem": "order_items", "Box": "boxes", "Company": "companies", "Day": "days", "Match": "matches", "Wish": "wishes", "Bus": "buses"} {
		if got := plural(snakeCase(in)); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
	if snakeCase("APIKey") != "api_key" || snakeCase("UserID") != "user_id" || plural("") != "" {
		t.Error("snake case")
	}
	if quoteIdentifier(`users.we"ird`) != `"users"."we""ird"` {
		t.Error(quoteIdentifier(`users.we"ird`))
	}
}

// A slice the query builder passes as one value, such as an array column, is not an IN list.
func TestBuilderArgumentsAreNotExpanded(t *testing.T) {
	b := &sqlBuilder{dialect: Postgres}
	And(Col[[]string]("tags").Eq([]string{"a", "b"}), Raw("id IN (?)", []int{1, 2})).appendSQL(b)
	query, args, err := rebind(Postgres, b.sql.String(), b.args)
	if err != nil {
		t.Fatal(err)
	}
	if want := `("tags" = $1 AND (id IN ($2, $3)))`; query != want {
		t.Errorf("query = %s, want %s", query, want)
	}
	if want := []any{[]string{"a", "b"}, 1, 2}; !reflect.DeepEqual(args, want) {
		t.Errorf("args = %#v, want %#v", args, want)
	}
}
