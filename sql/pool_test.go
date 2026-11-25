package sql_test

import (
	"context"
	"testing"
	"time"

	"github.com/yaboylino/gonestly/sql"
)

// A pool of one connection must be enough to migrate: the lock, the bookkeeping and the
// migrations all have to share it.
func TestMigrationsWithSingleConnection(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		db.Std().SetMaxOpenConns(1)

		migrator, err := sql.NewMigrator(db, migrations)
		if err != nil {
			t.Fatal(err)
		}
		if applied, err := migrator.Up(ctx); err != nil || len(applied) != 2 {
			t.Fatalf("Up() = %v, %v", applied, err)
		}
		if statuses, err := migrator.Status(ctx); err != nil || !statuses[1].Applied {
			t.Fatalf("Status() = %v, %v", statuses, err)
		}
		if reverted, err := migrator.Down(ctx); err == nil || reverted != nil {
			t.Fatalf("Down() = %v, %v, want an error for the missing down file", reverted, err)
		}
	})
}

func TestExistsAndPointerScan(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		createUsers(t, db)
		name := sql.Col[string]("name")
		for value, want := range map[string]bool{"Ada": true, "Nobody": false} {
			if got, err := sql.From[User](db).Where(name.Eq(value)).Exists(t.Context()); err != nil || got != want {
				t.Errorf("Exists(%s) = %v, %v, want %v", value, got, err, want)
			}
		}
		_, err := sql.Query[*User](t.Context(), db, "SELECT * FROM users")
		if err == nil || err.Error() != "sql: scan into sql_test.User, not into a pointer to it" {
			t.Errorf("Query[*User] = %v", err)
		}
	})
}
