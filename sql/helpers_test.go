package sql_test

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/yaboylino/gonestly/sql"
)

// postgresEnv and mysqlEnv point at databases for the tests; without them only SQLite is tested.
const (
	postgresEnv = "GONESTLY_TEST_POSTGRES"
	mysqlEnv    = "GONESTLY_TEST_MYSQL"
)

func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "test.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open(t.Context(), sql.Options{Driver: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openPostgres opens a connection whose search path is a schema of its own, dropped afterwards.
func openPostgres(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(postgresEnv)
	admin, err := sql.Open(t.Context(), sql.Options{Driver: "pgx", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := "test_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })

	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	db, err := sql.Open(t.Context(), sql.Options{Driver: "pgx", DSN: dsn + separator + "search_path=" + schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openMySQL opens a connection to a database of its own, dropped afterwards.
func openMySQL(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(mysqlEnv)
	admin, err := sql.Open(t.Context(), sql.Options{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := "test_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE "+name) })

	// The connection string has the form user:password@tcp(host:port)/database?parameters.
	slash := strings.LastIndexByte(dsn, '/')
	parameters := ""
	if i := strings.IndexByte(dsn[slash:], '?'); i >= 0 {
		parameters = dsn[slash+i:]
	}
	db, err := sql.Open(t.Context(), sql.Options{Driver: "mysql", DSN: dsn[:slash+1] + name + parameters})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// eachDatabase runs the test against SQLite and, when configured, PostgreSQL and MySQL.
func eachDatabase(t *testing.T, test func(t *testing.T, db *sql.DB)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { test(t, openSQLite(t)) })
	t.Run("postgres", func(t *testing.T) {
		if os.Getenv(postgresEnv) == "" {
			t.Skip("set " + postgresEnv + " to a PostgreSQL connection string to run this test")
		}
		test(t, openPostgres(t))
	})
	t.Run("mysql", func(t *testing.T) {
		if os.Getenv(mysqlEnv) == "" {
			t.Skip("set " + mysqlEnv + " to a MySQL connection string to run this test")
		}
		test(t, openMySQL(t))
	})
}

func exec(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, s := range statements {
		if _, err := db.Exec(t.Context(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// autoID is the type of an auto incrementing primary key in each dialect.
func autoID(db *sql.DB) string {
	switch db.Dialect() {
	case sql.Postgres:
		return "BIGSERIAL PRIMARY KEY"
	case sql.MySQL:
		return "BIGINT AUTO_INCREMENT PRIMARY KEY"
	default:
		return "INTEGER PRIMARY KEY"
	}
}

func createUsers(t *testing.T, db *sql.DB) {
	t.Helper()
	exec(t, db,
		"CREATE TABLE users (id "+autoID(db)+", name TEXT NOT NULL, email TEXT, team_id BIGINT, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)",
		"INSERT INTO users (name, email, team_id) VALUES ('Ada', 'ada@example.com', 1), ('Grace', 'grace@example.com', 1), ('Linus', NULL, 2)",
	)
}
