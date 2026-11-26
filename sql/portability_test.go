package sql_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/yaboylino/gonestly/sql"
)

type Counter struct {
	ID   int64 `db:"id"`
	Hits int64 `db:"hits,default"`
}

type Token struct {
	Code string `db:"code,pk"`
	Note string `db:"note"`
}

// The repository must behave the same with and without RETURNING.
func TestRepositoryWithoutReturning(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		exec(t, db, "CREATE TABLE counters (id "+autoID(db)+", hits BIGINT NOT NULL DEFAULT 7)")
		counters, err := sql.NewRepository[Counter, int64](db)
		if err != nil {
			t.Fatal(err)
		}
		var counter Counter
		if err := counters.Create(ctx, &counter); err != nil || counter.ID != 1 || counter.Hits != 7 {
			t.Fatalf("an insert of only defaults = %+v, %v", counter, err)
		}
		// An update that changes nothing still finds its row.
		if err := counters.Update(ctx, &counter); err != nil {
			t.Errorf("an unchanged update = %v", err)
		}
		if rows, err := counters.Query().OrderBy(sql.Col[int64]("id").Asc()).Offset(1).All(ctx); err != nil || len(rows) != 0 {
			t.Errorf("an offset without a limit = %v, %v", rows, err)
		}

		if db.Dialect() != sql.MySQL {
			return
		}
		exec(t, db, "CREATE TABLE tokens (code VARCHAR(36) PRIMARY KEY DEFAULT (UUID()), note VARCHAR(20) NOT NULL)")
		tokens, err := sql.NewRepository[Token, string](db)
		if err != nil {
			t.Fatal(err)
		}
		err = tokens.Create(ctx, &Token{Note: "generated"})
		if err == nil || !strings.Contains(err.Error(), "mysql cannot return the generated Token.Code, a string; set it before Create") {
			t.Errorf("a generated key that is not an auto increment = %v", err)
		}
		if n, err := tokens.Count(ctx); err != nil || n != 0 {
			t.Errorf("rows after the refused insert = %d, %v", n, err)
		}
		token := Token{Code: "fixed", Note: "explicit"}
		if err := tokens.Create(ctx, &token); err != nil || token.Note != "explicit" {
			t.Errorf("an explicit key = %+v, %v", token, err)
		}
	})
}

// A routine or trigger body holds semicolons that must not split the migration.
func TestMigrationWithRoutineBody(t *testing.T) {
	bodies := map[sql.Dialect]string{
		sql.SQLite: `CREATE TRIGGER count_hits AFTER INSERT ON hits
BEGIN
  UPDATE totals SET hits = hits + 1;
  UPDATE totals SET hits = hits + 1;
END;`,
		sql.Postgres: `CREATE FUNCTION count_hit() RETURNS void LANGUAGE SQL
BEGIN ATOMIC
  UPDATE totals SET hits = hits + CASE WHEN hits >= 0 THEN 1 ELSE 0 END;
  UPDATE totals SET hits = hits + 1;
END;
CREATE FUNCTION count_hits() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM count_hit(); RETURN NEW; END; $$;
CREATE TRIGGER count_hits AFTER INSERT ON hits FOR EACH ROW EXECUTE FUNCTION count_hits();`,
		sql.MySQL: `CREATE TRIGGER count_hits AFTER INSERT ON hits FOR EACH ROW
BEGIN
  IF NEW.id > 0 THEN
    UPDATE totals SET hits = hits + CASE WHEN hits >= 0 THEN 1 ELSE 0 END;
  END IF;
  UPDATE totals SET hits = hits + 1;
END;`,
	}
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		for _, prefix := range []string{"", "-- gonestly:no-transaction\n"} {
			if db.Dialect() == sql.Postgres {
				exec(t, db, "DROP TABLE IF EXISTS hits", "DROP FUNCTION IF EXISTS count_hits", "DROP FUNCTION IF EXISTS count_hit")
			}
			exec(t, db, "DROP TABLE IF EXISTS gonestly_migrations", "DROP TABLE IF EXISTS hits", "DROP TABLE IF EXISTS totals")
			migrator, err := sql.NewMigrator(db, fstest.MapFS{
				"0001_hits.up.sql": {Data: []byte(prefix + "CREATE TABLE hits (id BIGINT PRIMARY KEY);\nCREATE TABLE totals (hits BIGINT NOT NULL);\nINSERT INTO totals (hits) VALUES (0);\n" + bodies[db.Dialect()] + "\nINSERT INTO hits (id) VALUES (1);\n")},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := migrator.Up(ctx); err != nil {
				t.Fatalf("Up() with prefix %q = %v", prefix, err)
			}
			if total, err := sql.QueryOne[int64](ctx, db, "SELECT hits FROM totals"); err != nil || total != 2 {
				t.Errorf("total with prefix %q = %d, %v, want 2", prefix, total, err)
			}
		}
	})
}

// Two applications that start together must not both apply the same migration.
func TestConcurrentMigrations(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		files := fstest.MapFS{"0001_once.up.sql": {Data: []byte("CREATE TABLE once (id BIGINT PRIMARY KEY);")}}
		total := make(chan int, 4)
		for range cap(total) {
			go func() {
				migrator, err := sql.NewMigrator(db, files)
				if err != nil {
					t.Error(err)
				}
				applied, err := migrator.Up(t.Context())
				if err != nil {
					t.Errorf("Up() = %v", err)
				}
				total <- len(applied)
			}()
		}
		sum := 0
		for range cap(total) {
			sum += <-total
		}
		if sum != 1 {
			t.Errorf("the migration was applied %d times, want once", sum)
		}
	})
}
