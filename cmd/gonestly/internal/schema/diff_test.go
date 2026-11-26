package schema

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiffOfEqualSchemasIsEmpty(t *testing.T) {
	ops, err := Diff(v1(), v1(), DiffOptions{})
	if err != nil || len(ops) != 0 {
		t.Errorf("ops = %v, err = %v", ops, err)
	}
	plan, err := NewPlan(Postgres, v1(), v1(), nil)
	if err != nil || !plan.Empty() {
		t.Errorf("plan = %+v, err = %v", plan, err)
	}
}

func TestDestructiveChangesAreNamed(t *testing.T) {
	next := v1()
	next.Tables[0].Columns = []Column{pk(), str("email", 20), col("age", Int32)}
	next.Tables = append(next.Tables[:1], table("extra", pk()))
	ops, err := Diff(v1(), next, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(append(DataLoss(ops), Risks(ops)...), "\n")
	for _, want := range []string{
		"the table posts and all its rows are dropped",
		"the column users.name and its data are dropped",
		"changes from string(100) to string(20)",
		"the required column users.age has no default",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the warnings lack %q:\n%s", want, joined)
		}
	}
}

func TestWideningsAndOptionalColumnsAreNotWarnings(t *testing.T) {
	next := v1()
	next.Tables[0].Columns[1] = str("email", 400)
	next.Tables[0].Columns = append(next.Tables[0].Columns, str("bio", 10).null(), col("n", Int32).def("0"))
	next.Tables[1].Columns[3] = col("score", Int64).def("0")
	ops, _ := Diff(v1(), next, DiffOptions{})
	if warnings := append(DataLoss(ops), Risks(ops)...); len(warnings) != 0 {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestRequiredAndUniqueWarnings(t *testing.T) {
	a := schemaOf(table("t", pk(), str("x", 10).null()))
	b := schemaOf(table("t", pk(), str("x", 10)).index("t_x_key", true, "x"))
	ops, _ := Diff(a, b, DiffOptions{})
	joined := strings.Join(Risks(ops), "\n")
	if !strings.Contains(joined, "becomes required") || !strings.Contains(joined, "fails when rows hold the same value twice") {
		t.Errorf("warnings = %s", joined)
	}
}

func TestOpsComeInASafeOrder(t *testing.T) {
	next := schemaOf(
		table("comments", pk(), col("post_id", Int64)).fk("comments_post_id_fkey", "post_id", "posts", ""),
		table("posts", pk(), col("user_id", Int64)).fk("posts_user_id_fkey", "user_id", "users", ""),
		table("users", pk()),
	)
	ops, err := Diff(Schema{}, next, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, op := range ops {
		order = append(order, op.(CreateTable).Table.Name)
	}
	if strings.Join(order, ",") != "users,posts,comments" {
		t.Errorf("tables are created in the order %v", order)
	}
	ops, _ = Diff(next, Schema{}, DiffOptions{})
	order = nil
	for _, op := range ops {
		order = append(order, op.(DropTable).Table.Name)
	}
	if strings.Join(order, ",") != "comments,posts,users" {
		t.Errorf("tables are dropped in the order %v", order)
	}
}

func TestRenameCandidates(t *testing.T) {
	from := schemaOf(table("users", pk(), str("name", 50)), table("posts", pk(), str("title", 50)))
	to := schemaOf(table("members", pk(), str("name", 50)), table("posts", pk(), str("headline", 50), col("n", Int32)))
	tables := TableCandidates(from, to)
	if len(tables) != 1 || tables[0].Rename != (Rename{From: "users", To: "members"}) {
		t.Errorf("tables = %v", tables)
	}
	columns := ColumnCandidates(from, to, nil)
	if len(columns) != 1 || columns[0].Rename != (Rename{Table: "posts", From: "title", To: "headline"}) {
		t.Errorf("columns = %v", columns)
	}
	// A table that is renamed counts as the same table.
	both := schemaOf(table("members", pk(), str("full", 50)))
	if columns := ColumnCandidates(schemaOf(table("users", pk(), str("name", 50))), both, []Rename{{From: "users", To: "members"}}); len(columns) != 1 {
		t.Errorf("columns of a renamed table = %v", columns)
	}
	// Tables of another shape are not a rename.
	other := schemaOf(table("things", pk(), col("a", Int32), col("b", Int32), col("c", Bool)))
	if got := TableCandidates(schemaOf(table("users", pk(), str("name", 50))), other); len(got) != 0 {
		t.Errorf("candidates = %v", got)
	}
}

func TestSuggestName(t *testing.T) {
	for name, tt := range map[string]struct {
		from, to Schema
		want     string
	}{
		"a table":      {Schema{}, schemaOf(table("tracks", pk())), "create_tracks"},
		"a column":     {schemaOf(table("tracks", pk())), schemaOf(table("tracks", pk(), str("slug", 10).null())), "add_tracks_slug"},
		"a drop":       {schemaOf(table("tracks", pk(), str("slug", 10))), schemaOf(table("tracks", pk())), "drop_tracks_slug"},
		"a change":     {schemaOf(table("tracks", pk(), str("slug", 10))), schemaOf(table("tracks", pk(), str("slug", 20))), "change_tracks_slug"},
		"a table drop": {schemaOf(table("tracks", pk())), Schema{}, "drop_tracks"},
	} {
		ops, _ := Diff(tt.from, tt.to, DiffOptions{})
		if got := SuggestName(ops); got != tt.want {
			t.Errorf("%s: %q, want %q", name, got, tt.want)
		}
	}
	if got := SuggestName(nil); got != "update_schema" {
		t.Errorf("no ops: %q", got)
	}
	var many []Op
	for _, n := range []string{"a", "b", "c", "d"} {
		many = append(many, CreateTable{Table: table(n)})
	}
	if got := SuggestName(many); got != "create_a_and_create_b_and_create_c_and_more" {
		t.Errorf("many: %q", got)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	data, err := Marshal(v1())
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Marshal(back)
	if string(again) != string(data) {
		t.Errorf("the snapshot changes when it is read and written again")
	}
	ops, _ := Diff(v1(), back, DiffOptions{})
	if len(ops) != 0 {
		t.Errorf("a snapshot differs from its source: %v", ops)
	}
	if _, err := Unmarshal([]byte("{")); err == nil {
		t.Error("garbage was accepted")
	}
	if _, err := Unmarshal([]byte(`{"version": 99}`)); err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Errorf("a future snapshot: %v", err)
	}
}

func TestLoadAndSaveSnapshot(t *testing.T) {
	dir := t.TempDir()
	if s, err := LoadSnapshot(dir); err != nil || len(s.Tables) != 0 {
		t.Errorf("an empty directory: %+v, %v", s, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0001_old.up.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(dir); !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("migrations without a snapshot: %v", err)
	}
	if err := SaveSnapshot(dir, v1()); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSnapshot(dir)
	if err != nil || len(s.Tables) != 2 {
		t.Errorf("after saving: %+v, %v", s, err)
	}
}

func TestNextVersion(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 5, 0, time.UTC)
	for name, tt := range map[string]struct {
		files []string
		want  string
	}{
		"empty":            {nil, "20260301100005"},
		"older timestamps": {[]string{"20250101000000_a.up.sql"}, "20260301100005"},
		"the same second":  {[]string{"20260301100005_a.up.sql", "20260301100005_a.down.sql"}, "20260301100006"},
		"a later second":   {[]string{"20260301100009_a.up.sql"}, "20260301100010"},
		"counters":         {[]string{"0001_a.up.sql", "0002_b.up.sql"}, "0003"},
		"other files":      {[]string{"README.md", "migrations.go", "x_y.sql", SnapshotFile}, "20260301100005"},
	} {
		dir := t.TempDir()
		for _, f := range tt.files {
			if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := NextVersion(dir, now); err != nil || got != tt.want {
			t.Errorf("%s: %q, %v, want %q", name, got, err, tt.want)
		}
	}
}

func TestWriteMigrationRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteMigration(dir, "0001", "a", "up", "down"); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteMigration(dir, "0001", "a", "up", "down"); err == nil {
		t.Error("a migration was overwritten")
	}
}

func TestLongNamesFitPostgres(t *testing.T) {
	name := constraintName("a_very_long_table_name_for_testing", []string{"a_very_long_column_name_for_testing", "another_long_column"}, "idx")
	if len(name) > 63 || !strings.HasSuffix(name, "_idx") {
		t.Errorf("%q has %d characters", name, len(name))
	}
	if other := constraintName("a_very_long_table_name_for_testing", []string{"a_very_long_column_name_for_testing", "another_long_other"}, "idx"); other == name {
		t.Error("two different names were shortened to the same one")
	}
}
