package jobs

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// databases runs a test on SQLite and on every server that the environment names.
func databases(t *testing.T, test func(t *testing.T, db *sql.DB)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		dsn := "file:" + filepath.Join(t.TempDir(), "test.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
		test(t, open(t, "sqlite", dsn, "sqlite"))
	})
	if dsn := os.Getenv(postgresEnv); dsn != "" {
		t.Run("postgres", func(t *testing.T) {
			admin := open(t, "pgx", dsn, "")
			schema := "test_" + strings.ToLower(rand.Text())
			mustExec(t, admin, "CREATE SCHEMA "+schema)
			t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
			separator := "?"
			if strings.Contains(dsn, "?") {
				separator = "&"
			}
			test(t, open(t, "pgx", dsn+separator+"search_path="+schema, "postgres"))
		})
	}
	if dsn := os.Getenv(mysqlEnv); dsn != "" {
		t.Run("mysql", func(t *testing.T) {
			admin := open(t, "mysql", dsn, "")
			name := "test_" + strings.ToLower(rand.Text())
			mustExec(t, admin, "CREATE DATABASE "+name)
			t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE "+name) })
			slash := strings.LastIndexByte(dsn, '/')
			parameters := ""
			if i := strings.IndexByte(dsn[slash:], '?'); i >= 0 {
				parameters = dsn[slash+i:]
			}
			test(t, open(t, "mysql", dsn[:slash+1]+name+parameters, "mysql"))
		})
	}
}

// open connects and, when a dialect is given, creates the jobs table.
func open(t *testing.T, driver, dsn, dialect string) *sql.DB {
	t.Helper()
	db, err := sql.Open(t.Context(), sql.Options{Driver: driver, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if dialect != "" {
		up, _, err := Migration(dialect)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(up, ";") {
			if strings.TrimSpace(statement) != "" {
				mustExec(t, db, statement)
			}
		}
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(t.Context(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func newQueue(t *testing.T, db *sql.DB, opts Options) *Queue {
	t.Helper()
	q, err := New(db, opts)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// clock is a time that tests move by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func noWait(int) time.Duration { return 0 }

func status(t *testing.T, db *sql.DB, id int64) record {
	t.Helper()
	rows, err := sql.Query[record](t.Context(), db, "SELECT * FROM "+Table+" WHERE id = ?", id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("job %d: %v, %d rows", id, err, len(rows))
	}
	return rows[0]
}
