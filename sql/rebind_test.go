package sql

import (
	"errors"
	"fmt"
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
		{MySQL, "SELECT `a?`, 'it\\'s ?', \"q\\\"?\" FROM t WHERE a = ? # why?\nAND b IN (?)", []any{1, []int{2, 3}}, "SELECT `a?`, 'it\\'s ?', \"q\\\"?\" FROM t WHERE a = ? # why?\nAND b IN (?, ?)", []any{1, 2, 3}},
		{MySQL, "SELECT 5--? -- what?\n", []any{3}, "SELECT 5--? -- what?\n", []any{3}},
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
	if got := MySQL.Quote("users.we`ird"); got != "`users`.`we``ird`" {
		t.Error(got)
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

func TestSplitStatements(t *testing.T) {
	script := `-- gonestly:no-transaction
CREATE INDEX CONCURRENTLY a ON t (x); -- the first
/* a ; in a comment */
INSERT INTO t (x) VALUES ('semi;colon');
CREATE FUNCTION f() RETURNS int AS $body$ BEGIN RETURN 1; END; $body$ LANGUAGE plpgsql;

-- nothing after this
;
`
	want := []string{
		"-- gonestly:no-transaction\nCREATE INDEX CONCURRENTLY a ON t (x)",
		"-- the first\n/* a ; in a comment */\nINSERT INTO t (x) VALUES ('semi;colon')",
		"CREATE FUNCTION f() RETURNS int AS $body$ BEGIN RETURN 1; END; $body$ LANGUAGE plpgsql",
	}
	if got := splitStatements(Postgres, script); !reflect.DeepEqual(got, want) {
		t.Errorf("statements = %q\nwant %q", got, want)
	}
}

func TestSplitRoutineBodies(t *testing.T) {
	postgres := `CREATE FUNCTION add(a int, b int) RETURNS int LANGUAGE SQL
BEGIN ATOMIC
  SELECT CASE WHEN a > 0 THEN a ELSE 0 END;
  SELECT a + b;
END;
BEGIN;
SELECT 1;`
	if got := splitStatements(Postgres, postgres); len(got) != 3 || !strings.HasSuffix(got[0], "SELECT a + b;\nEND") || got[1] != "BEGIN" {
		t.Errorf("postgres statements = %q", got)
	}

	mysql := "CREATE TABLE `a;b` (begin INT, note VARCHAR(9) DEFAULT 'x\\';'); # one; comment\n" + `/*!40101 SET NAMES utf8mb4 */;
CREATE TRIGGER audit BEFORE UPDATE ON t FOR EACH ROW
BEGIN
  IF NEW.a <> OLD.a THEN
    SET NEW.b = CASE WHEN NEW.a > 0 THEN IF(NEW.a > 5, 2, 1) ELSE 0 END;
  END IF;
  WHILE NEW.c < 3 DO SET NEW.c = NEW.c + 1; END WHILE;
  CASE NEW.d WHEN 1 THEN SET NEW.e = 1; ELSE BEGIN SET NEW.e = 2; END; END CASE;
END;
BEGIN;
INSERT INTO t VALUES (1);
COMMIT;`
	got := splitStatements(MySQL, mysql)
	if len(got) != 6 || !strings.HasPrefix(got[1], "# one; comment\n/*!40101") || !strings.HasSuffix(got[2], "END CASE;\nEND") || got[3] != "BEGIN" || got[5] != "COMMIT" {
		t.Errorf("mysql statements = %d %q", len(got), got)
	}
}

func TestMySQLConnectionString(t *testing.T) {
	for dsn, want := range map[string]string{
		"root:pw@tcp(db:3306)/shop":                 "root:pw@tcp(db:3306)/shop?parseTime=true",
		"root:p/w?@tcp(db:3306)/shop?charset=utf8":  "root:p/w?@tcp(db:3306)/shop?charset=utf8&parseTime=true",
		"root:pw@tcp(db:3306)/shop?parseTime=false": "root:pw@tcp(db:3306)/shop?parseTime=false",
		"not a connection string":                   "not a connection string",
	} {
		if got := mysqlDSN(dsn); got != want {
			t.Errorf("mysqlDSN(%q) = %q, want %q", dsn, got, want)
		}
	}
}

type numberedError struct{ Number uint16 }

func (numberedError) Error() string { return "Error 1062 (23000): Duplicate entry" }

func TestMySQLUniqueViolation(t *testing.T) {
	for _, err := range []error{
		numberedError{Number: 1062},
		fmt.Errorf("wrapped: %w", &numberedError{Number: 1586}),
		errors.Join(errors.New("other"), numberedError{Number: 1062}),
		errors.New("Error 1062 (23000): Duplicate entry 'a' for key 'b'"),
	} {
		if !MySQL.isUniqueViolation(err) {
			t.Errorf("%v is not seen as a unique violation", err)
		}
	}
	for _, err := range []error{numberedError{Number: 1048}, errors.New("Error 1048 (23000): Column cannot be null")} {
		if MySQL.isUniqueViolation(err) {
			t.Errorf("%v is seen as a unique violation", err)
		}
	}
}
