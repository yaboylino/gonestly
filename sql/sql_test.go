package sql_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
	"github.com/yaboylino/gonestly/sql"
)

type User struct {
	ID        int64     `db:"id"`
	Name      string    `db:"name"`
	Email     *string   `db:"email"`
	TeamID    int64     `db:"team_id"`
	CreatedAt time.Time `db:"created_at,default"`
}

var (
	userID     = sql.Col[int64]("id")
	userName   = sql.Col[string]("name")
	userEmail  = sql.Col[*string]("email")
	userTeamID = sql.Col[int64]("team_id")
)

func names(users []User) string {
	out := make([]string, len(users))
	for i, u := range users {
		out[i] = u.Name
	}
	return strings.Join(out, ",")
}

func TestRawQueries(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		createUsers(t, db)
		ctx := t.Context()

		users, err := sql.Query[User](ctx, db, "SELECT * FROM users WHERE team_id = ? ORDER BY id", 1)
		if err != nil {
			t.Fatal(err)
		}
		if names(users) != "Ada,Grace" || *users[0].Email != "ada@example.com" || users[0].CreatedAt.IsZero() {
			t.Errorf("users = %+v", users)
		}

		linus, err := sql.QueryOne[User](ctx, db, "SELECT * FROM users WHERE name = ?", "Linus")
		if err != nil || linus.Email != nil {
			t.Errorf("Linus = %+v, %v", linus, err)
		}

		count, err := sql.QueryOne[int64](ctx, db, "SELECT COUNT(*) FROM users WHERE id IN (?) AND name <> '?'", []int64{1, 2, 3})
		if err != nil || count != 3 {
			t.Errorf("count = %d, %v", count, err)
		}

		if _, err := sql.QueryOne[User](ctx, db, "SELECT * FROM users WHERE name = ?", "nobody"); !errors.Is(err, sql.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
		if _, err := sql.QueryOne[User](ctx, db, "SELECT * FROM users"); !errors.Is(err, sql.ErrTooManyRows) {
			t.Errorf("err = %v, want ErrTooManyRows", err)
		}
		type partial struct {
			Name string `db:"name"`
		}
		if _, err := sql.Query[partial](ctx, db, "SELECT name, email FROM users"); err == nil || !strings.Contains(err.Error(), `the column "email" has no field in partial; add a field with db:"email"`) {
			t.Errorf("err = %v", err)
		}
		if _, err := sql.Query[int64](ctx, db, "SELECT id, name FROM users"); err == nil || !strings.Contains(err.Error(), "needs one column, the query returned 2") {
			t.Errorf("err = %v", err)
		}
		if _, err := sql.Query[int64](ctx, db, "SELECT id FROM users WHERE id = ?"); err == nil || !strings.Contains(err.Error(), "the query has more placeholders than the 0 arguments") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestBuilder(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		createUsers(t, db)
		ctx := t.Context()
		users := sql.From[User](db)

		tests := []struct {
			query *sql.Builder[User]
			want  string
		}{
			{users.Where(userTeamID.Eq(1)).OrderBy(userName.Desc()), "Grace,Ada"},
			{users.Where(userName.Ne("Ada")).OrderBy(userID.Asc()), "Grace,Linus"},
			{users.Where(userID.Gt(1), userID.Lte(3)).OrderBy(userID.Asc()), "Grace,Linus"},
			{users.Where(userID.Gte(3)), "Linus"},
			{users.Where(userID.Lt(2)), "Ada"},
			{users.Where(userName.Like("%r%")), "Grace"},
			{users.Where(userID.In(1, 3)).OrderBy(userID.Asc()), "Ada,Linus"},
			{users.Where(userID.NotIn(1, 3)), "Grace"},
			{users.Where(userID.In()), ""},
			{users.Where(userID.NotIn()).OrderBy(userID.Asc()), "Ada,Grace,Linus"},
			{users.Where(userEmail.IsNull()), "Linus"},
			{users.Where(userEmail.Eq(nil)), "Linus"},
			{users.Where(userEmail.IsNotNull(), userEmail.Ne(nil)).OrderBy(userID.Asc()), "Ada,Grace"},
			{users.Where(userID.Between(2, 3)).OrderBy(userID.Asc()), "Grace,Linus"},
			{users.Where(sql.Or(userName.Eq("Ada"), userName.Eq("Linus"))).OrderBy(userID.Asc()), "Ada,Linus"},
			{users.Where(sql.Not(sql.And(userTeamID.Eq(1), userName.Eq("Ada")))).OrderBy(userID.Asc()), "Grace,Linus"},
			{users.Where(sql.Or()), ""},
			{users.Where(sql.Raw("lower(name) = lower(?)", "GRACE")), "Grace"},
			{users.OrderBy(userID.Asc()).Limit(2), "Ada,Grace"},
			{users.OrderBy(userID.Asc()).Offset(1), "Grace,Linus"},
			{users.OrderBy(userID.Asc()).Limit(1).Offset(1), "Grace"},
		}
		for _, tt := range tests {
			got, err := tt.query.All(ctx)
			query, _, _ := tt.query.SQL()
			if err != nil || names(got) != tt.want {
				t.Errorf("%s\n  got %q, %v, want %q", query, names(got), err, tt.want)
			}
		}

		if n, err := users.Where(userTeamID.Eq(1)).Count(ctx); err != nil || n != 2 {
			t.Errorf("count = %d, %v", n, err)
		}
		if ok, err := users.Where(userName.Eq("nobody")).Exists(ctx); err != nil || ok {
			t.Errorf("exists = %v, %v", ok, err)
		}
		if first, err := users.OrderBy(userID.Desc()).First(ctx); err != nil || first.Name != "Linus" {
			t.Errorf("first = %+v, %v", first, err)
		}
		if _, err := users.Where(userName.Eq("nobody")).First(ctx); !errors.Is(err, sql.ErrNotFound) {
			t.Errorf("err = %v", err)
		}

		if n, err := users.Where(userTeamID.Eq(2)).Update(ctx, userTeamID.Set(3), userName.Set("Linus T")); err != nil || n != 1 {
			t.Errorf("update = %d, %v", n, err)
		}
		if _, err := users.Update(ctx, userName.Set("x")); !errors.Is(err, sql.ErrUnsafeQuery) {
			t.Errorf("err = %v, want ErrUnsafeQuery", err)
		}
		if _, err := users.Where(userID.Eq(1)).Update(ctx); err == nil {
			t.Error("Update without assignments succeeded")
		}
		if _, err := users.Delete(ctx); !errors.Is(err, sql.ErrUnsafeQuery) {
			t.Errorf("err = %v, want ErrUnsafeQuery", err)
		}
		if n, err := users.Where(userTeamID.Eq(3)).Delete(ctx); err != nil || n != 1 {
			t.Errorf("delete = %d, %v", n, err)
		}
		if n, err := users.Where(sql.Always()).Delete(ctx); err != nil || n != 2 {
			t.Errorf("delete all = %d, %v", n, err)
		}
	})
}

func TestBuilderSQL(t *testing.T) {
	db := sql.New(nil, sql.Postgres)
	query, args, err := sql.From[User](db).
		Where(userEmail.Like("%@example.com"), sql.Or(userTeamID.Eq(1), userID.In(4, 5))).
		OrderBy(userName.Asc(), userID.Desc()).Limit(10).Offset(20).SQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "id", "name", "email", "team_id", "created_at" FROM "users" WHERE ("email" LIKE ? AND ("team_id" = ? OR "id" IN (?, ?))) ORDER BY "name", "id" DESC LIMIT 10 OFFSET 20`
	if query != want || !reflect.DeepEqual(args, []any{"%@example.com", int64(1), int64(4), int64(5)}) {
		t.Errorf("query = %s\nwant    %s\nargs = %v", query, want, args)
	}
}

type Team struct {
	Code string `db:"code,pk"`
	Name string `db:"name"`
}

func TestRepository(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		createUsers(t, db)
		exec(t, db, "CREATE TABLE teams (code VARCHAR(64) PRIMARY KEY, name TEXT NOT NULL)")
		ctx := t.Context()
		repo, err := sql.NewRepository[User, int64](db)
		if err != nil {
			t.Fatal(err)
		}

		email := "barbara@example.com"
		user := User{Name: "Barbara", Email: &email, TeamID: 2}
		if err := repo.Create(ctx, &user); err != nil {
			t.Fatal(err)
		}
		if user.ID != 4 || user.CreatedAt.IsZero() {
			t.Errorf("created = %+v", user)
		}
		explicit := User{ID: 100, Name: "Explicit", CreatedAt: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
		if err := repo.Create(ctx, &explicit); err != nil {
			t.Fatal(err)
		}

		found, err := repo.FindByID(ctx, 100)
		if err != nil || found.Name != "Explicit" || !found.CreatedAt.Equal(explicit.CreatedAt) {
			t.Errorf("found = %+v, %v", found, err)
		}
		if _, err := repo.FindByID(ctx, 999); !errors.Is(err, sql.ErrNotFound) {
			t.Errorf("err = %v", err)
		}

		found.Name = "Renamed"
		if err := repo.Update(ctx, &found); err != nil || found.Name != "Renamed" {
			t.Errorf("update = %+v, %v", found, err)
		}
		missing := User{ID: 999, Name: "x"}
		if err := repo.Update(ctx, &missing); !errors.Is(err, sql.ErrNotFound) {
			t.Errorf("err = %v", err)
		}

		team1, err := repo.FindAll(ctx, userTeamID.Eq(1))
		if err != nil || len(team1) != 2 {
			t.Errorf("team 1 = %+v, %v", team1, err)
		}
		if n, err := repo.Count(ctx); err != nil || n != 5 {
			t.Errorf("count = %d, %v", n, err)
		}
		if err := repo.Delete(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if err := repo.Delete(ctx, 100); !errors.Is(err, sql.ErrNotFound) {
			t.Errorf("err = %v", err)
		}
		if got, _ := repo.Query().Where(userName.Eq("Barbara")).First(ctx); got.ID != 4 {
			t.Errorf("query = %+v", got)
		}

		teams, err := sql.NewRepository[Team, string](db)
		if err != nil {
			t.Fatal(err)
		}
		team := Team{Code: "core", Name: "Core"}
		if err := teams.Create(ctx, &team); err != nil {
			t.Fatal(err)
		}
		if got, err := teams.FindByID(ctx, "core"); err != nil || got.Name != "Core" {
			t.Errorf("team = %+v, %v", got, err)
		}
	})
}

type noKey struct {
	Name string `db:"name"`
}

type Person struct {
	ID int64
}

func (Person) TableName() string { return "people" }

type Category struct {
	ID   int64
	Slug string
}

type Address struct {
	ID int64
}

type embeddedBase struct {
	ID        int64
	CreatedAt time.Time
}

type Article struct {
	embeddedBase
	Title  string
	Ignore string `db:"-"`
}

func TestModels(t *testing.T) {
	db := sql.New(nil, sql.SQLite)
	tables := map[string]string{}
	for name, query := range map[string]func() (string, []any, error){
		"person":   sql.From[Person](db).SQL,
		"category": sql.From[Category](db).SQL,
		"address":  sql.From[Address](db).SQL,
		"article":  sql.From[Article](db).SQL,
	} {
		q, _, err := query()
		if err != nil {
			t.Fatal(err)
		}
		tables[name] = q
	}
	want := map[string]string{
		"person":   `SELECT "id" FROM "people"`,
		"category": `SELECT "id", "slug" FROM "categories"`,
		"address":  `SELECT "id" FROM "addresses"`,
		"article":  `SELECT "id", "created_at", "title" FROM "articles"`,
	}
	if !reflect.DeepEqual(tables, want) {
		t.Errorf("tables = %v", tables)
	}

	if _, err := sql.NewRepository[noKey, int64](db); err == nil || !strings.Contains(err.Error(), `noKey has no primary key; tag a field db:",pk" or name it ID`) {
		t.Errorf("err = %v", err)
	}
	if _, err := sql.NewRepository[User, string](db); err == nil || !strings.Contains(err.Error(), "the primary key User.ID is a int64, not a string") {
		t.Errorf("err = %v", err)
	}

	type nested struct{ X int }
	type invalid struct {
		A int    `db:"a,pk"`
		B int    `db:"b,pk"`
		C int    `db:"a"`
		D int    `db:"d,unique"`
		E nested `db:"e"`
		*Person
	}
	_, _, err := sql.From[invalid](db).SQL()
	for _, part := range []string{
		"invalid.A and invalid.C both map to the column \"a\"",
		"invalid.D has the unknown db option \"unique\"",
		"invalid.E has the struct type sql_test.nested",
		"invalid.ID is embedded through a pointer",
		"invalid has more than one pk field: A and B",
	} {
		if err == nil || !strings.Contains(err.Error(), part) || !errors.Is(err, sql.ErrInvalidModel) {
			t.Errorf("error does not contain %q: %v", part, err)
		}
	}
	if _, err := sql.From[int](db).All(context.Background()); err == nil || !strings.Contains(err.Error(), "int is not a struct") {
		t.Errorf("err = %v", err)
	}
}

func TestTransactions(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		createUsers(t, db)
		ctx := t.Context()
		repo, _ := sql.NewRepository[User, int64](db)
		count := func() int64 {
			n, err := repo.Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}

		err := db.Transaction(ctx, func(ctx context.Context) error {
			return repo.Create(ctx, &User{Name: "Committed"})
		})
		if err != nil || count() != 4 {
			t.Fatalf("commit: %v, count %d", err, count())
		}

		failure := errors.New("failure")
		err = db.Transaction(ctx, func(ctx context.Context) error {
			if err := repo.Create(ctx, &User{Name: "Rolled back"}); err != nil {
				return err
			}
			return failure
		})
		if !errors.Is(err, failure) || count() != 4 {
			t.Fatalf("rollback: %v, count %d", err, count())
		}

		func() {
			defer func() {
				if recover() == nil {
					t.Error("the panic was not passed on")
				}
			}()
			_ = db.Transaction(ctx, func(ctx context.Context) error {
				_ = repo.Create(ctx, &User{Name: "Panicked"})
				panic("boom")
			})
		}()
		if count() != 4 {
			t.Fatalf("a panic did not roll back, count %d", count())
		}

		err = db.Transaction(ctx, func(ctx context.Context) error {
			if err := repo.Create(ctx, &User{Name: "Outer"}); err != nil {
				return err
			}
			inner := db.Transaction(ctx, func(ctx context.Context) error {
				if err := repo.Create(ctx, &User{Name: "Inner"}); err != nil {
					return err
				}
				return failure
			})
			if !errors.Is(inner, failure) {
				t.Errorf("inner = %v", inner)
			}
			return db.Transaction(ctx, func(ctx context.Context) error {
				return repo.Create(ctx, &User{Name: "Second inner"})
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		all, _ := repo.Query().Where(userID.Gt(3)).OrderBy(userID.Asc()).All(ctx)
		if names(all) != "Committed,Outer,Second inner" {
			t.Errorf("rows = %s", names(all))
		}
	})
}

var migrations = fstest.MapFS{
	"0001_create_teams.up.sql":   {Data: []byte("CREATE TABLE teams (code VARCHAR(64) PRIMARY KEY, name TEXT NOT NULL);\nINSERT INTO teams (code, name) VALUES ('core', 'Core');")},
	"0001_create_teams.down.sql": {Data: []byte("DROP TABLE teams;")},
	"0002_add_team_size.up.sql":  {Data: []byte("ALTER TABLE teams ADD COLUMN size INTEGER NOT NULL DEFAULT 0;")},
	"README.md":                  {Data: []byte("ignored")},
}

func TestMigrations(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		migrator, err := sql.NewMigrator(db, migrations)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(migrator.Migrations()); got != 2 {
			t.Errorf("migrations = %d", got)
		}

		applied, err := migrator.Up(ctx)
		if err != nil || len(applied) != 2 || applied[0].Name != "create_teams" || applied[1].Version != 2 {
			t.Fatalf("applied = %+v, %v", applied, err)
		}
		if again, err := migrator.Up(ctx); err != nil || len(again) != 0 {
			t.Errorf("second Up = %+v, %v", again, err)
		}
		size, err := sql.QueryOne[int64](ctx, db, "SELECT size FROM teams WHERE code = ?", "core")
		if err != nil || size != 0 {
			t.Errorf("size = %d, %v", size, err)
		}

		status, err := migrator.Status(ctx)
		if err != nil || len(status) != 2 || !status[0].Applied || status[1].AppliedAt.IsZero() {
			t.Errorf("status = %+v, %v", status, err)
		}

		if _, err := migrator.Down(ctx); err == nil || !strings.Contains(err.Error(), "migration 2_add_team_size has no down file") {
			t.Errorf("err = %v", err)
		}

		changed := fstest.MapFS{}
		for name, file := range migrations {
			changed[name] = file
		}
		changed["0001_create_teams.up.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE teams (code TEXT);")}
		changedMigrator, _ := sql.NewMigrator(db, changed)
		if _, err := changedMigrator.Up(ctx); err == nil || !strings.Contains(err.Error(), "migration 1_create_teams was changed after it was applied") {
			t.Errorf("err = %v", err)
		}

		missing := fstest.MapFS{"0002_add_team_size.up.sql": migrations["0002_add_team_size.up.sql"]}
		missingMigrator, _ := sql.NewMigrator(db, missing)
		if _, err := missingMigrator.Up(ctx); err == nil || !strings.Contains(err.Error(), "migration 1_create_teams is applied but its file is missing") {
			t.Errorf("err = %v", err)
		}

		late := fstest.MapFS{"0000_early.up.sql": {Data: []byte("SELECT 1;")}}
		for name, file := range migrations {
			late[name] = file
		}
		lateMigrator, _ := sql.NewMigrator(db, late)
		if _, err := lateMigrator.Up(ctx); err == nil || !strings.Contains(err.Error(), "migration 0_early is pending but the newer migration 2 is already applied") {
			t.Errorf("err = %v", err)
		}

		onlyFirst := fstest.MapFS{
			"0001_create_teams.up.sql":   migrations["0001_create_teams.up.sql"],
			"0001_create_teams.down.sql": migrations["0001_create_teams.down.sql"],
		}
		exec(t, db, "DELETE FROM gonestly_migrations WHERE version = 2")
		firstMigrator, _ := sql.NewMigrator(db, onlyFirst)
		reverted, err := firstMigrator.Down(ctx)
		if err != nil || reverted == nil || reverted.Version != 1 {
			t.Fatalf("reverted = %+v, %v", reverted, err)
		}
		if nothing, err := firstMigrator.Down(ctx); err != nil || nothing != nil {
			t.Errorf("second Down = %+v, %v", nothing, err)
		}
		if _, err := db.Exec(ctx, "SELECT 1 FROM teams"); err == nil {
			t.Error("the table still exists after Down")
		}

		broken := fstest.MapFS{"0001_broken.up.sql": {Data: []byte("CREATE TABLE ( broken")}}
		brokenMigrator, _ := sql.NewMigrator(db, broken)
		if _, err := brokenMigrator.Up(ctx); err == nil || !strings.Contains(err.Error(), "applying migration 1_broken") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestInvalidMigrationFiles(t *testing.T) {
	_, err := sql.NewMigrator(nil, fstest.MapFS{
		"create_users.up.sql":           {Data: []byte("x")},
		"0001_Users.up.sql":             {Data: []byte("x")},
		"0002_a.up.sql":                 {Data: []byte("x")},
		"0002_b.up.sql":                 {Data: []byte("x")},
		"0003_empty.up.sql":             {Data: []byte("  ")},
		"0004_only_down.down.sql":       {Data: []byte("x")},
		"99999999999999999999_x.up.sql": {Data: []byte("x")},
	})
	for _, part := range []string{
		"create_users.up.sql does not follow <version>_<name>.up.sql",
		"0001_Users.up.sql does not follow",
		"version 2 is used by",
		"migration 3_empty has no up file, or it is empty",
		"migration 4_only_down has no up file",
		"99999999999999999999_x.up.sql has an invalid version",
	} {
		if err == nil || !strings.Contains(err.Error(), part) {
			t.Errorf("error does not contain %q: %v", part, err)
		}
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := sql.Open(t.Context(), sql.Options{Driver: "nope"}); err == nil || !strings.Contains(err.Error(), `unknown dialect for the driver "nope"`) {
		t.Errorf("err = %v", err)
	}
	if _, err := sql.Open(t.Context(), sql.Options{Driver: "sqlite3", Dialect: sql.SQLite}); err == nil || !strings.Contains(err.Error(), `the driver "sqlite3" is not registered; import it`) {
		t.Errorf("err = %v", err)
	}
	if _, err := sql.Open(t.Context(), sql.Options{Driver: "pgx", DSN: "postgres://nobody@127.0.0.1:1/none?connect_timeout=1"}); err == nil || !strings.Contains(err.Error(), "connecting to the database") {
		t.Errorf("err = %v", err)
	}
}

func TestGroupBy(t *testing.T) {
	groups := sql.GroupBy([]User{{Name: "a", TeamID: 1}, {Name: "b", TeamID: 2}, {Name: "c", TeamID: 1}}, func(u User) int64 { return u.TeamID })
	if names(groups[1]) != "a,c" || names(groups[2]) != "b" {
		t.Errorf("groups = %v", groups)
	}
}

type userController struct {
	users *sql.Repository[User, int64]
}

func (c *userController) Routes(r *gonestly.Router) {
	gonestly.Get(r, "/users/{id}", func(ctx context.Context, req struct {
		ID int64 `path:"id"`
	},
	) (User, error) {
		return c.users.FindByID(ctx, req.ID)
	})
}

func TestModule(t *testing.T) {
	dsn := "file:" + t.TempDir() + "/app.db"
	moduleMigrations := fstest.MapFS{
		"0001_users.up.sql": {Data: []byte("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT, team_id BIGINT NOT NULL DEFAULT 0, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP); INSERT INTO users (name) VALUES ('Ada');")},
	}
	type config struct{ DSN string }
	root := &gonestly.Module{
		Name: "app",
		Imports: gonestly.Imports(
			&gonestly.Module{Name: "config", Global: true, Providers: gonestly.Providers(gonestly.Value(config{DSN: dsn})), Exports: gonestly.Exports(gonestly.Type[config]())},
			sql.ModuleFunc(func(c config) sql.Options {
				return sql.Options{Driver: "sqlite", DSN: c.DSN, Migrations: moduleMigrations}
			}),
		),
		Providers:   gonestly.Providers(sql.NewRepository[User, int64]),
		Controllers: gonestly.Controllers(func(users *sql.Repository[User, int64]) *userController { return &userController{users: users} }),
	}
	app := gonestlytest.Start(t, root)

	var ada User
	app.Get("/users/1").ExpectStatus(http.StatusOK).JSON(&ada)
	if ada.Name != "Ada" {
		t.Errorf("user = %+v", ada)
	}
	if p := app.Get("/users/2").Problem(); p.Status != http.StatusNotFound || p.Detail != "The requested record does not exist." {
		t.Errorf("problem = %+v", p)
	}

	db := gonestlytest.Resolve[*sql.DB](app)
	if err := app.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Std().PingContext(t.Context()); err == nil {
		t.Error("the database is still open after Stop")
	}

	broken := &gonestly.Module{
		Name: "app",
		Imports: gonestly.Imports(sql.Module(sql.Options{Driver: "sqlite", DSN: "file:" + t.TempDir() + "/b.db", Migrations: fstest.MapFS{
			"0001_broken.up.sql": {Data: []byte("NOT SQL")},
		}})),
	}
	brokenApp, err := gonestly.New(broken, gonestly.WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := brokenApp.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "sql: migrating: sql: applying migration 1_broken") {
		t.Errorf("err = %v", err)
	}
}

func TestForeignKeysOffRebuildsATableWithoutCascades(t *testing.T) {
	db := openSQLite(t)
	exec(t, db,
		"CREATE TABLE parents (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
		"CREATE TABLE kids (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parents (id) ON DELETE CASCADE)",
		"INSERT INTO parents (id, name) VALUES (1, 'a'), (2, 'b')",
		"INSERT INTO kids (parent_id) VALUES (1), (1), (2)",
	)
	rebuild := "CREATE TABLE parents__new (id INTEGER PRIMARY KEY, name TEXT);\nINSERT INTO parents__new (id, name) SELECT id, name FROM parents;\nDROP TABLE parents;\nALTER TABLE parents__new RENAME TO parents;\n"
	migrate := func(script string) error {
		dir := fstest.MapFS{"0001_rebuild.up.sql": {Data: []byte(script)}}
		migrator, err := sql.NewMigrator(db, dir)
		if err != nil {
			t.Fatal(err)
		}
		_, err = migrator.Up(t.Context())
		return err
	}
	if err := migrate("-- gonestly:foreign-keys-off\n" + rebuild); err != nil {
		t.Fatal(err)
	}
	n, err := sql.QueryOne[int64](t.Context(), db, "SELECT COUNT(*) FROM kids")
	if err != nil || n != 3 {
		t.Fatalf("%d kids after the rebuild, %v; want 3", n, err)
	}
	// The setting is back, and the cascade still works.
	exec(t, db, "DELETE FROM parents WHERE id = 1")
	if n, _ := sql.QueryOne[int64](t.Context(), db, "SELECT COUNT(*) FROM kids"); n != 1 {
		t.Errorf("%d kids after deleting a parent, want 1: the foreign keys are off or the key was lost", n)
	}
	if on, _ := sql.QueryOne[int64](t.Context(), db, "PRAGMA foreign_keys"); on != 1 {
		t.Errorf("foreign_keys = %d after the migration", on)
	}
}

func TestForeignKeysOffFailsOnViolationsAndRollsBack(t *testing.T) {
	db := openSQLite(t)
	exec(t, db,
		"CREATE TABLE parents (id INTEGER PRIMARY KEY)",
		"CREATE TABLE kids (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parents (id))",
		"INSERT INTO parents (id) VALUES (1)",
		"INSERT INTO kids (parent_id) VALUES (1)",
	)
	script := "-- gonestly:foreign-keys-off\nDELETE FROM parents;\n"
	migrator, err := sql.NewMigrator(db, fstest.MapFS{"0001_break.up.sql": {Data: []byte(script)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Up(t.Context()); err == nil || !strings.Contains(err.Error(), "violate a foreign key") {
		t.Fatalf("err = %v", err)
	}
	if n, _ := sql.QueryOne[int64](t.Context(), db, "SELECT COUNT(*) FROM parents"); n != 1 {
		t.Errorf("the migration was not rolled back: %d parents", n)
	}
	if on, _ := sql.QueryOne[int64](t.Context(), db, "PRAGMA foreign_keys"); on != 1 {
		t.Errorf("foreign_keys = %d after a failed migration", on)
	}
}
