package sql_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yaboylino/gonestly/sql"
)

type Member struct {
	ID        int64   `db:"id"`
	Email     string  `db:"email"`
	Name      string  `db:"name"`
	Visits    int64   `db:"visits,default"`
	Note      *string `db:"note"`
	Signature string  `db:"-"`
}

func createMembers(t *testing.T, db *sql.DB) *sql.Repository[Member, int64] {
	t.Helper()
	exec(t, db, "CREATE TABLE members (id "+autoID(db)+", email VARCHAR(255) NOT NULL UNIQUE, name VARCHAR(255) NOT NULL, visits BIGINT NOT NULL DEFAULT 5, note TEXT)")
	repo, err := sql.NewRepository[Member, int64](db)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestCreateAll(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		repo := createMembers(t, db)

		// More rows than fit in one statement, with a row in the middle that sets its own key
		// and visits, which leaves other columns to the database than its neighbors.
		members := make([]Member, 9000)
		for i := range members {
			members[i] = Member{Email: fmt.Sprintf("member-%d@example.com", i), Name: "Member"}
		}
		members[4000].ID, members[4000].Visits = 50000, 9
		if err := repo.CreateAll(ctx, members); err != nil {
			t.Fatal(err)
		}
		if n, err := repo.Count(ctx); err != nil || n != 9000 {
			t.Fatalf("rows = %d, %v", n, err)
		}
		if explicit, err := repo.FindByID(ctx, 50000); err != nil || explicit.Visits != 9 || explicit.Email != "member-4000@example.com" {
			t.Errorf("the row with its own key = %+v, %v", explicit, err)
		}
		if first, err := repo.Query().Where(sql.Col[string]("email").Eq("member-0@example.com")).First(ctx); err != nil || first.Visits != 5 {
			t.Errorf("the default was not left to the database: %+v, %v", first, err)
		}

		// One row that fails undoes the whole batch.
		err := repo.CreateAll(ctx, []Member{{Email: "new@example.com", Name: "New"}, {Email: "member-1@example.com", Name: "Taken"}})
		if !errors.Is(err, sql.ErrConflict) {
			t.Errorf("a duplicate in the batch = %v", err)
		}
		if n, _ := repo.Count(ctx); n != 9000 {
			t.Errorf("rows after the failed batch = %d", n)
		}
		if err := repo.CreateAll(ctx, nil); err != nil {
			t.Errorf("no rows = %v", err)
		}
	})
}

func TestUpsert(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		repo := createMembers(t, db)

		ada := Member{Email: "ada@example.com", Name: "Ada"}
		if err := repo.Upsert(ctx, &ada, "email"); err != nil || ada.ID != 1 || ada.Visits != 5 {
			t.Fatalf("insert = %+v, %v", ada, err)
		}
		exec(t, db, "UPDATE members SET visits = 12 WHERE id = 1")

		// The same email updates the name and keeps the key and the default column.
		note := "renamed"
		again := Member{Email: "ada@example.com", Name: "Ada Lovelace", Note: &note}
		if err := repo.Upsert(ctx, &again, "email"); err != nil {
			t.Fatal(err)
		}
		if again.ID != 1 || again.Name != "Ada Lovelace" || again.Visits != 12 || again.Note == nil || *again.Note != "renamed" {
			t.Errorf("update = %+v", again)
		}
		if n, _ := repo.Count(ctx); n != 1 {
			t.Errorf("rows = %d, want 1", n)
		}

		// An upsert that changes nothing still finds its row.
		same := Member{Email: "ada@example.com", Name: "Ada Lovelace", Note: &note}
		if err := repo.Upsert(ctx, &same, "email"); err != nil || same.ID != 1 || same.Visits != 12 {
			t.Errorf("an unchanged upsert = %+v, %v", same, err)
		}
		unchanged := Member{Email: "ada@example.com", Name: "Ada Lovelace", Note: &note}
		if err := repo.UpsertAll(ctx, []Member{unchanged}, "email"); err != nil {
			t.Errorf("an unchanged batch = %v", err)
		}

		// Without columns the primary key decides.
		byKey := Member{ID: 1, Email: "ada@example.org", Name: "Ada", Visits: 20}
		if err := repo.Upsert(ctx, &byKey); err != nil || byKey.Email != "ada@example.org" || byKey.Visits != 20 || byKey.Note != nil {
			t.Errorf("by key = %+v, %v", byKey, err)
		}
		fresh := Member{Email: "grace@example.com", Name: "Grace"}
		// An upsert that updated may have used up a number, so only a new key is certain.
		if err := repo.Upsert(ctx, &fresh); err != nil || fresh.ID < 2 {
			t.Errorf("a new row by key = %+v, %v", fresh, err)
		}

		if err := repo.Upsert(ctx, &fresh, "nickname"); !errors.Is(err, sql.ErrInvalidModel) || !strings.Contains(err.Error(), `Member has no column "nickname" to upsert on; it has id, email, name, visits, note`) {
			t.Errorf("an unknown column = %v", err)
		}

		batch := []Member{
			{Email: "ada@example.org", Name: "Ada again"},
			{Email: "linus@example.com", Name: "Linus"},
			{Email: "grace@example.com", Name: "Grace Hopper"},
		}
		if err := repo.UpsertAll(ctx, batch, "email"); err != nil {
			t.Fatal(err)
		}
		all, err := repo.Query().OrderBy(sql.Col[string]("email").Asc()).All(ctx)
		if err != nil || len(all) != 3 || all[0].Name != "Ada again" || all[0].ID != 1 || all[0].Visits != 20 || all[1].Name != "Grace Hopper" || all[2].Name != "Linus" {
			t.Errorf("after the batch = %+v, %v", all, err)
		}
	})
}

type Setting struct {
	Name  string `db:"name,pk"`
	Value string `db:"value"`
}

type Tag struct {
	Name string `db:"name,pk"`
}

// A key that the application chooses, and a table of nothing but its key.
func TestUpsertWithOwnKey(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		exec(t, db, "CREATE TABLE settings (name VARCHAR(64) PRIMARY KEY, value VARCHAR(255) NOT NULL)", "CREATE TABLE tags (name VARCHAR(64) PRIMARY KEY)")
		settings, _ := sql.NewRepository[Setting, string](db)
		for _, value := range []string{"light", "dark"} {
			setting := Setting{Name: "theme", Value: value}
			if err := settings.Upsert(ctx, &setting); err != nil || setting.Value != value {
				t.Fatalf("upsert %s = %+v, %v", value, setting, err)
			}
		}
		if stored, err := settings.FindByID(ctx, "theme"); err != nil || stored.Value != "dark" {
			t.Errorf("stored = %+v, %v", stored, err)
		}

		tags, _ := sql.NewRepository[Tag, string](db)
		for range 2 {
			tag := Tag{Name: "go"}
			if err := tags.Upsert(ctx, &tag); err != nil || tag.Name != "go" {
				t.Fatalf("a row of only its key = %+v, %v", tag, err)
			}
		}
		if n, _ := tags.Count(ctx); n != 1 {
			t.Errorf("tags = %d", n)
		}
	})
}
