package schema

import (
	"strings"
	"testing"

	"github.com/yaboylino/gonestly/sql"
)

func v1() Schema {
	return schemaOf(
		table("users", pk(), str("email", 100), str("name", 100)).index("users_email_key", true, "email"),
		table("posts", pk(), col("user_id", Int64), str("title", 100), col("score", Int32).def("0")).
			index("posts_title_idx", false, "title").
			fk("posts_user_id_fkey", "user_id", "users", "cascade"),
	)
}

func seed(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, "INSERT INTO users (email, name) VALUES ('ada@example.com', 'Ada'), ('grace@example.com', 'Grace')")
	mustExec(t, db, "INSERT INTO posts (user_id, title, score) VALUES (1, 'Engines', 5), (1, 'Notes', 7), (2, 'Compilers', 9)")
}

func TestCreateTables(t *testing.T) {
	databases(t, func(t *testing.T, d testDB) {
		m := newMigrations(t, d)
		m.to(v1())
		seed(t, d.db)
		if n := count(t, d.db, "SELECT COUNT(*) FROM posts"); n != 3 {
			t.Fatalf("%d posts", n)
		}
		// The constraints are in place.
		if err := execErr(t, d.db, "INSERT INTO users (email, name) VALUES ('ada@example.com', 'Again')"); err == nil {
			t.Error("a duplicate email was accepted")
		}
		if err := execErr(t, d.db, "INSERT INTO posts (user_id, title) VALUES (99, 'Orphan')"); err == nil {
			t.Error("a post without a user was accepted")
		}
		if err := execErr(t, d.db, "INSERT INTO users (email) VALUES ('x@example.com')"); err == nil {
			t.Error("a user without a name was accepted")
		}
		mustExec(t, d.db, "DELETE FROM users WHERE id = 1")
		if n := count(t, d.db, "SELECT COUNT(*) FROM posts"); n != 1 {
			t.Errorf("%d posts after deleting a user, want 1: the delete did not cascade", n)
		}
		m.down()
		if err := execErr(t, d.db, "SELECT 1 FROM users"); err == nil {
			t.Error("the tables survived the down migration")
		}
	})
}

func TestAddAndDropColumns(t *testing.T) {
	databases(t, func(t *testing.T, d testDB) {
		m := newMigrations(t, d)
		m.to(v1())
		seed(t, d.db)

		next := v1()
		next.Tables[0].Columns = append(next.Tables[0].Columns,
			str("nickname", 50).null(),
			col("active", Bool).def("true"),
			col("joined", Time).def("now"),
		)
		plan := m.to(next)
		if len(plan.DataLoss) != 0 {
			t.Errorf("warnings for adding columns with defaults: %v", plan.DataLoss)
		}
		if n := count(t, d.db, "SELECT COUNT(*) FROM users WHERE active"); d.dialect != SQLite && n != 2 {
			t.Errorf("%d active users, want 2", n)
		}
		mustExec(t, d.db, "UPDATE users SET nickname = 'ada' WHERE id = 1")
		if n := count(t, d.db, "SELECT COUNT(*) FROM users WHERE joined IS NOT NULL"); n != 2 {
			t.Errorf("%d users with a join date, want 2", n)
		}

		// Drop a column that is a plain column, and one that an index uses.
		slim := v1()
		slim.Tables[0].Columns = slim.Tables[0].Columns[:2]
		slim.Tables[0].Columns = []Column{pk(), str("email", 100)}
		plan = m.to(slim)
		if len(plan.DataLoss) == 0 {
			t.Error("dropping columns gave no warning")
		}
		if n := count(t, d.db, "SELECT COUNT(*) FROM users"); n != 2 {
			t.Errorf("%d users after dropping columns", n)
		}
		if err := execErr(t, d.db, "SELECT name FROM users"); err == nil {
			t.Error("a dropped column is still there")
		}
		if n := count(t, d.db, "SELECT COUNT(*) FROM posts"); n != 3 {
			t.Errorf("%d posts: the other table lost rows", n)
		}

		// Undoing a drop brings the column back, empty and optional.
		m.down()
		mustExec(t, d.db, "UPDATE users SET name = 'back'")
		m.down()
		if err := execErr(t, d.db, "SELECT joined FROM users"); err == nil {
			t.Error("the added columns survived the down migration")
		}
	})
}

func TestAlterColumns(t *testing.T) {
	databases(t, func(t *testing.T, d testDB) {
		m := newMigrations(t, d)
		m.to(v1())
		seed(t, d.db)

		next := v1()
		users := &next.Tables[0]
		users.Columns[1] = str("email", 300)
		users.Columns[2] = str("name", 100).null()
		next.Tables[1].Columns[3] = col("score", Int64).def("1")
		plan := m.to(next)
		if len(plan.DataLoss) != 0 {
			t.Errorf("widening gave warnings: %v", plan.DataLoss)
		}
		long := strings.Repeat("a", 250) + "@example.com"
		mustExec(t, d.db, "INSERT INTO users (email) VALUES (?)", long)
		if err := execErr(t, d.db, "INSERT INTO posts (user_id, title) VALUES (1, 'Defaults')"); err != nil {
			t.Fatal(err)
		}
		if n := count(t, d.db, "SELECT score FROM posts WHERE title = 'Defaults'"); n != 1 {
			t.Errorf("the new default is %d, want 1", n)
		}
		if n := count(t, d.db, "SELECT COUNT(*) FROM posts"); n != 4 {
			t.Errorf("%d posts", n)
		}
		if err := execErr(t, d.db, "INSERT INTO users (email, name) VALUES ('ada@example.com', 'Dup')"); err == nil {
			t.Error("the unique index was lost when the column changed")
		}
		if err := execErr(t, d.db, "INSERT INTO posts (user_id, title) VALUES (99, 'Orphan')"); err == nil {
			t.Error("the foreign key was lost when a column changed")
		}

		// A narrowing is a warning.
		narrow := v1()
		narrow.Tables[0].Columns[1] = str("email", 20)
		if _, err := NewPlan(d.dialect, next, narrow, nil); err != nil {
			t.Fatal(err)
		}
		plan, _ = NewPlan(d.dialect, next, narrow, nil)
		if len(plan.DataLoss) == 0 {
			t.Error("shrinking a column gave no warning")
		}
	})
}

func TestIndexesAndKeys(t *testing.T) {
	databases(t, func(t *testing.T, d testDB) {
		m := newMigrations(t, d)
		base := schemaOf(
			table("users", pk(), str("email", 100)),
			table("posts", pk(), col("user_id", Int64), str("title", 100)),
		)
		m.to(base)
		mustExec(t, d.db, "INSERT INTO users (email) VALUES ('a@example.com')")
		mustExec(t, d.db, "INSERT INTO posts (user_id, title) VALUES (1, 'One'), (1, 'Two')")
		mustExec(t, d.db, "INSERT INTO posts (user_id, title) VALUES (99, 'Orphan')")
		mustExec(t, d.db, "DELETE FROM posts WHERE title = 'Orphan'")

		next := base
		next.Tables = []Table{
			table("users", pk(), str("email", 100)).index("users_email_key", true, "email"),
			table("posts", pk(), col("user_id", Int64), str("title", 100)).
				index("posts_title_idx", false, "title").
				fk("posts_user_id_fkey", "user_id", "users", "restrict"),
		}
		m.to(next)
		if err := execErr(t, d.db, "INSERT INTO posts (user_id, title) VALUES (99, 'Orphan')"); err == nil {
			t.Error("the foreign key does not hold")
		}
		if err := execErr(t, d.db, "DELETE FROM users WHERE id = 1"); err == nil {
			t.Error("restrict let a user with posts go")
		}

		// Change the rule, then take the key and the indexes away again.
		cascade := next
		cascade.Tables = []Table{next.Tables[0], table("posts", pk(), col("user_id", Int64), str("title", 100)).
			index("posts_title_idx", false, "title").
			fk("posts_user_id_fkey", "user_id", "users", "cascade")}
		m.to(cascade)
		mustExec(t, d.db, "DELETE FROM users WHERE id = 1")
		if n := count(t, d.db, "SELECT COUNT(*) FROM posts"); n != 0 {
			t.Errorf("%d posts after a cascading delete", n)
		}
		m.to(base)
		mustExec(t, d.db, "INSERT INTO posts (user_id, title) VALUES (99, 'Orphan')")
		mustExec(t, d.db, "INSERT INTO users (email) VALUES ('same@example.com'), ('same@example.com')")
	})
}

func TestRenames(t *testing.T) {
	databases(t, func(t *testing.T, d testDB) {
		m := newMigrations(t, d)
		m.to(v1())
		seed(t, d.db)

		next := v1()
		next.Tables[0].Columns[2] = str("full_name", 100)
		next.Tables[0].Name = "people"
		next.Tables[0].Indexes = []Index{{Name: "users_email_key", Columns: []string{"email"}, Unique: true}}
		next.Tables[1].ForeignKeys[0].RefTable = "people"
		tables := TableCandidates(v1(), next)
		if len(tables) != 1 || tables[0].String() != "table users to people" {
			t.Fatalf("table candidates = %v", tables)
		}
		renames := []Rename{tables[0].Rename}
		columns := ColumnCandidates(v1(), next, renames)
		if len(columns) != 1 || columns[0].String() != "column users.name to full_name" {
			t.Fatalf("column candidates = %v", columns)
		}
		renames = append(renames, columns[0].Rename)
		plan := m.to(next, renames...)
		if len(plan.DataLoss) != 0 {
			t.Errorf("a rename gave warnings: %v", plan.DataLoss)
		}
		if n := count(t, d.db, "SELECT COUNT(*) FROM people WHERE full_name = 'Ada'"); n != 1 {
			t.Errorf("the renamed column lost its data: %d", n)
		}
		if n := count(t, d.db, "SELECT COUNT(*) FROM posts p JOIN people u ON u.id = p.user_id"); n != 3 {
			t.Errorf("the join through the renamed table gives %d rows", n)
		}
		m.down()
		if n := count(t, d.db, "SELECT COUNT(*) FROM users WHERE name = 'Grace'"); n != 1 {
			t.Errorf("undoing the rename lost data: %d", n)
		}
	})
}

func TestRebuildKeepsTablesThatPointAtIt(t *testing.T) {
	databases(t, func(t *testing.T, d testDB) {
		m := newMigrations(t, d)
		m.to(v1())
		seed(t, d.db)
		// users is the target of a cascading key; changing it must not delete the posts.
		next := v1()
		next.Tables[0].Columns[2] = str("name", 100).null()
		next.Tables[0].Columns = append(next.Tables[0].Columns, str("bio", 100).null())
		m.to(next)
		if n := count(t, d.db, "SELECT COUNT(*) FROM posts"); n != 3 {
			t.Errorf("%d posts after changing users, want 3", n)
		}
		if n := count(t, d.db, "SELECT COUNT(*) FROM users"); n != 2 {
			t.Errorf("%d users after changing users, want 2", n)
		}
		mustExec(t, d.db, "DELETE FROM users WHERE id = 2")
		if n := count(t, d.db, "SELECT COUNT(*) FROM posts"); n != 2 {
			t.Errorf("the cascade is gone after the change: %d posts", n)
		}
	})
}

func TestDropTablesAndCycles(t *testing.T) {
	databases(t, func(t *testing.T, d testDB) {
		m := newMigrations(t, d)
		cyclic := schemaOf(
			table("teams", pk(), str("name", 50), col("lead_id", Int64).null()).fk("teams_lead_id_fkey", "lead_id", "members", "setnull"),
			table("members", pk(), str("name", 50), col("team_id", Int64).null()).fk("members_team_id_fkey", "team_id", "teams", "setnull"),
		)
		m.to(cyclic)
		mustExec(t, d.db, "INSERT INTO teams (name) VALUES ('A')")
		mustExec(t, d.db, "INSERT INTO members (name, team_id) VALUES ('Ada', 1)")
		mustExec(t, d.db, "UPDATE teams SET lead_id = 1")
		m.to(Schema{Version: snapshotVersion})
		if err := execErr(t, d.db, "SELECT 1 FROM teams"); err == nil {
			t.Error("a table was not dropped")
		}
		m.down()
		if n := count(t, d.db, "SELECT COUNT(*) FROM members"); n != 0 {
			t.Errorf("a dropped table came back with %d rows", n)
		}
	})
}

func TestCompositeKeysAndTypes(t *testing.T) {
	databases(t, func(t *testing.T, d testDB) {
		m := newMigrations(t, d)
		all := schemaOf(table("things",
			Column{Name: "tenant", Type: Type{Kind: Int64}, PK: true},
			Column{Name: "code", Type: Type{Kind: String, Size: 20}, PK: true},
			col("small", Int16), col("medium", Int32), col("big", Int64),
			col("ratio", Float32), col("price", Float64),
			Column{Name: "amount", Type: Type{Kind: Decimal, Precision: 10, Scale: 2}},
			col("flag", Bool), col("at", Time), col("raw", Bytes).null(),
			col("doc", JSON).null(), col("note", Text).null(), col("ref", UUID).null(),
		))
		m.to(all)
		mustExec(t, d.db, "INSERT INTO things (tenant, code, small, medium, big, ratio, price, amount, flag, at) VALUES (1, 'a', 1, 2, 3, 1.5, 2.5, 10.25, ?, CURRENT_TIMESTAMP)", true)
		if err := execErr(t, d.db, "INSERT INTO things (tenant, code, small, medium, big, ratio, price, amount, flag, at) VALUES (1, 'a', 1, 2, 3, 1.5, 2.5, 10.25, ?, CURRENT_TIMESTAMP)", true); err == nil {
			t.Error("a duplicate composite key was accepted")
		}
		mustExec(t, d.db, "INSERT INTO things (tenant, code, small, medium, big, ratio, price, amount, flag, at) VALUES (2, 'a', 1, 2, 3, 1.5, 2.5, 10.25, ?, CURRENT_TIMESTAMP)", false)
	})
}

func TestUnsupportedChangesAreReported(t *testing.T) {
	a := schemaOf(table("t", pk(), str("x", 10)))
	b := schemaOf(table("t", pk(), Column{Name: "x", Type: Type{Kind: String, Size: 10}, PK: true}))
	if _, err := Diff(a, b, DiffOptions{}); err == nil || !strings.Contains(err.Error(), "primary key") {
		t.Errorf("a changed primary key: %v", err)
	}
	c := schemaOf(table("t", Column{Name: "id", Type: Type{Kind: Int64}, PK: true}))
	if _, err := Diff(schemaOf(table("t", pk())), c, DiffOptions{}); err == nil || !strings.Contains(err.Error(), "generated") {
		t.Errorf("a key that stops being generated: %v", err)
	}
}
