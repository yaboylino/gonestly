package schema

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/yaboylino/gonestly/sql"
)

const (
	postgresEnv = "GONESTLY_TEST_POSTGRES"
	mysqlEnv    = "GONESTLY_TEST_MYSQL"
)

func col(name string, kind Kind) Column { return Column{Name: name, Type: Type{Kind: kind}} }

func str(name string, size int) Column {
	return Column{Name: name, Type: Type{Kind: String, Size: size}}
}

func (c Column) null() Column { c.Nullable = true; return c }

func (c Column) def(v string) Column { c.Default = v; return c }

func pk() Column {
	return Column{Name: "id", Type: Type{Kind: Int64}, PK: true, Auto: true}
}

func table(name string, columns ...Column) Table { return Table{Name: name, Columns: columns} }

func (t Table) index(name string, unique bool, columns ...string) Table {
	t.Indexes = append(t.Indexes, Index{Name: name, Columns: columns, Unique: unique})
	return t
}

func (t Table) fk(name, column, refTable, onDelete string) Table {
	t.ForeignKeys = append(t.ForeignKeys, ForeignKey{Name: name, Columns: []string{column}, RefTable: refTable, RefColumns: []string{"id"}, OnDelete: onDelete})
	return t
}

func schemaOf(tables ...Table) Schema { return Schema{Version: snapshotVersion, Tables: tables} }

type testDB struct {
	db      *sql.DB
	dialect Dialect
}

// databases runs a test on SQLite and on every server that the environment names.
func databases(t *testing.T, test func(t *testing.T, d testDB)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		dsn := "file:" + filepath.Join(t.TempDir(), "test.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
		test(t, testDB{open(t, "sqlite", dsn), SQLite})
	})
	if dsn := os.Getenv(postgresEnv); dsn != "" {
		t.Run("postgres", func(t *testing.T) {
			admin := open(t, "pgx", dsn)
			name := "test_" + strings.ToLower(rand.Text())
			mustExec(t, admin, "CREATE SCHEMA "+name)
			t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+name+" CASCADE") })
			separator := "?"
			if strings.Contains(dsn, "?") {
				separator = "&"
			}
			test(t, testDB{open(t, "pgx", dsn+separator+"search_path="+name), Postgres})
		})
	}
	if dsn := os.Getenv(mysqlEnv); dsn != "" {
		t.Run("mysql", func(t *testing.T) {
			admin := open(t, "mysql", dsn)
			name := "test_" + strings.ToLower(rand.Text())
			mustExec(t, admin, "CREATE DATABASE "+name)
			t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE "+name) })
			slash := strings.LastIndexByte(dsn, '/')
			parameters := ""
			if i := strings.IndexByte(dsn[slash:], '?'); i >= 0 {
				parameters = dsn[slash+i:]
			}
			test(t, testDB{open(t, "mysql", dsn[:slash+1]+name+parameters), MySQL})
		})
	}
}

func open(t *testing.T, driver, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(t.Context(), sql.Options{Driver: driver, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(t.Context(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func execErr(t *testing.T, db *sql.DB, query string, args ...any) error {
	t.Helper()
	_, err := db.Exec(t.Context(), query, args...)
	return err
}

// migrations applies schemas one after the other with the real migrator, from files.
type migrations struct {
	t       *testing.T
	d       testDB
	dir     string
	current Schema
	now     time.Time
}

func newMigrations(t *testing.T, d testDB) *migrations {
	return &migrations{t: t, d: d, dir: t.TempDir(), current: Schema{Version: snapshotVersion}, now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// to makes the migration to a schema, applies it and returns the plan.
func (m *migrations) to(next Schema, renames ...Rename) Plan {
	m.t.Helper()
	plan, err := NewPlan(m.d.dialect, m.current, next, renames)
	if err != nil {
		m.t.Fatal(err)
	}
	if plan.Empty() {
		return plan
	}
	m.now = m.now.Add(time.Second)
	version, err := NextVersion(m.dir, m.now)
	if err != nil {
		m.t.Fatal(err)
	}
	if _, err := WriteMigration(m.dir, version, "step", plan.Up, plan.Down); err != nil {
		m.t.Fatal(err)
	}
	migrator, err := sql.NewMigrator(m.d.db, os.DirFS(m.dir))
	if err != nil {
		m.t.Fatal(err)
	}
	if _, err := migrator.Up(m.t.Context()); err != nil {
		m.t.Fatalf("applying\n%s\n%v", plan.Up, err)
	}
	m.current = next
	return plan
}

// down reverts the latest migration.
func (m *migrations) down() {
	m.t.Helper()
	migrator, err := sql.NewMigrator(m.d.db, os.DirFS(m.dir))
	if err != nil {
		m.t.Fatal(err)
	}
	if _, err := migrator.Down(m.t.Context()); err != nil {
		m.t.Fatalf("reverting: %v", err)
	}
}

func count(t *testing.T, db *sql.DB, query string) int64 {
	t.Helper()
	n, err := sql.QueryOne[int64](t.Context(), db, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}
