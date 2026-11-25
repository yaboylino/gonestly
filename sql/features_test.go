package sql_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/sql"
)

type Account struct {
	ID    int64  `db:"id"`
	Email string `db:"email"`
}

func createAccounts(t *testing.T, db *sql.DB) {
	t.Helper()
	exec(t, db, "CREATE TABLE accounts (id "+autoID(db)+", email TEXT NOT NULL UNIQUE)", "INSERT INTO accounts (email) VALUES ('ada@example.com')")
}

func TestUniqueViolationIsConflict(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		createAccounts(t, db)
		repo, err := sql.NewRepository[Account, int64](db)
		if err != nil {
			t.Fatal(err)
		}

		_, execErr := db.Exec(ctx, "INSERT INTO accounts (email) VALUES (?)", "ada@example.com")
		createErr := repo.Create(ctx, &Account{Email: "ada@example.com"})
		keyErr := repo.Create(ctx, &Account{ID: 1, Email: "grace@example.com"})
		txErr := db.Transaction(ctx, func(ctx context.Context) error {
			return repo.Create(ctx, &Account{Email: "ada@example.com"})
		})
		for name, err := range map[string]error{"Exec": execErr, "Create": createErr, "primary key": keyErr, "Transaction": txErr} {
			var httpErr *gonestly.HTTPError
			if !errors.Is(err, sql.ErrConflict) || !errors.As(err, &httpErr) || httpErr.Status != http.StatusConflict {
				t.Errorf("%s: err = %v, want ErrConflict", name, err)
			}
		}
		// The error of the driver is still there for whoever needs the details.
		if !strings.Contains(strings.ToLower(createErr.Error()), "unique") {
			t.Errorf("the driver error is lost: %v", createErr)
		}

		if _, err := db.Exec(ctx, "INSERT INTO accounts (email) VALUES (?)", nil); err == nil || errors.Is(err, sql.ErrConflict) {
			t.Errorf("a NOT NULL violation = %v, want an error that is not a conflict", err)
		}
		if err := repo.Create(ctx, &Account{Email: "linus@example.com"}); err != nil {
			t.Errorf("Create after the conflicts = %v", err)
		}
	})
}

func TestTransactionOptions(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		createAccounts(t, db)

		err := db.Transaction(ctx, func(ctx context.Context) error {
			if _, err := sql.Query[Account](ctx, db, "SELECT * FROM accounts"); err != nil {
				t.Errorf("reading in a read only transaction: %v", err)
			}
			_, err := db.Exec(ctx, "INSERT INTO accounts (email) VALUES (?)", "grace@example.com")
			return err
		}, sql.ReadOnly())
		if err == nil {
			t.Error("a write in a read only transaction succeeded")
		}
		if n, err := sql.From[Account](db).Count(ctx); err != nil || n != 1 {
			t.Errorf("rows = %d, %v, want 1", n, err)
		}

		err = db.Transaction(ctx, func(ctx context.Context) error {
			return db.Transaction(ctx, func(context.Context) error { return nil }, sql.ReadOnly())
		})
		if err == nil || !strings.Contains(err.Error(), "a nested transaction cannot set options") {
			t.Errorf("nested options = %v", err)
		}

		// The connection of the read only transaction is writable again afterwards.
		for range 5 {
			if _, err := db.Exec(ctx, "UPDATE accounts SET email = email"); err != nil {
				t.Fatalf("a write after the read only transaction: %v", err)
			}
		}

		if db.Dialect() != sql.Postgres {
			return
		}
		err = db.Transaction(ctx, func(ctx context.Context) error {
			level, err := sql.QueryOne[string](ctx, db, "SHOW transaction_isolation")
			if err == nil && level != "serializable" {
				t.Errorf("isolation = %s", level)
			}
			return err
		}, sql.Isolation(sql.LevelSerializable))
		if err != nil {
			t.Errorf("serializable transaction = %v", err)
		}
	})
}

func TestMigrationWithoutTransaction(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		index := "CREATE INDEX accounts_email ON accounts (email);"
		if db.Dialect() == sql.Postgres {
			// PostgreSQL refuses this statement inside a transaction.
			index = "CREATE INDEX CONCURRENTLY accounts_email ON accounts (email);"
		}
		files := fstest.MapFS{
			"0001_create_accounts.up.sql":  {Data: []byte("CREATE TABLE accounts (id BIGINT PRIMARY KEY, email TEXT NOT NULL);")},
			"0002_index_accounts.up.sql":   {Data: []byte("-- gonestly:no-transaction\n" + index + "\nINSERT INTO accounts (id, email) VALUES (1, 'semi;colon');\n")},
			"0002_index_accounts.down.sql": {Data: []byte("-- gonestly:no-transaction\nDELETE FROM accounts;\nDROP INDEX accounts_email;\n")},
			"0003_broken.up.sql":           {Data: []byte("-- gonestly:no-transaction\nINSERT INTO accounts (id, email) VALUES (2, 'kept');\nINSERT INTO nowhere VALUES (1);\n")},
		}
		migrator, err := sql.NewMigrator(db, files)
		if err != nil {
			t.Fatal(err)
		}
		applied, err := migrator.Up(ctx)
		if err == nil || !strings.Contains(err.Error(), "applying migration 3_broken") || len(applied) != 2 {
			t.Fatalf("Up() = %v, %v", applied, err)
		}
		// Outside a transaction, what ran before the failure stays, and the migration is not recorded.
		emails, err := sql.Query[string](ctx, db, "SELECT email FROM accounts ORDER BY id")
		if err != nil || len(emails) != 2 || emails[0] != "semi;colon" || emails[1] != "kept" {
			t.Errorf("emails = %v, %v", emails, err)
		}
		statuses, err := migrator.Status(ctx)
		if err != nil || !statuses[1].Applied || statuses[2].Applied {
			t.Errorf("statuses = %+v, %v", statuses, err)
		}

		delete(files, "0003_broken.up.sql")
		migrator, err = sql.NewMigrator(db, files)
		if err != nil {
			t.Fatal(err)
		}
		if reverted, err := migrator.Down(ctx); err != nil || reverted == nil || reverted.Version != 2 {
			t.Errorf("Down() = %v, %v", reverted, err)
		}
	})
}
