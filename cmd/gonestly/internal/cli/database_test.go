package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/dbsetup"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/project"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/prompt"
)

func asker(input string) (*prompt.Prompter, *bytes.Buffer) {
	var out bytes.Buffer
	return prompt.New(strings.NewReader(input), &out), &out
}

func TestCompleteConfigAsks(t *testing.T) {
	// Kind, host, port, user, password and name, in the order they are asked.
	ask, out := asker("postgres\ndb.internal\n6543\napp\ns3cret\nshop\n")
	var cfg dbsetup.Config
	if err := completeConfig(&cfg, map[string]bool{}, ask, "myshop"); err != nil {
		t.Fatal(err)
	}
	want := dbsetup.Config{Kind: "postgres", Host: "db.internal", Port: 6543, User: "app", Password: "s3cret", Name: "shop"}
	if cfg != want {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("the password was echoed: %q", out)
	}

	// Empty answers take the defaults of the database and the project.
	ask, out = asker("mysql\n\n\n\n\n\n")
	cfg = dbsetup.Config{}
	if err := completeConfig(&cfg, map[string]bool{}, ask, "myshop"); err != nil {
		t.Fatal(err)
	}
	want = dbsetup.Config{Kind: "mysql", Host: "localhost", Port: 3306, User: "root", Name: "myshop"}
	if cfg != want {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}
	for _, line := range []string{"Host (localhost)", "Port (3306)", "User (root)", "Name of the database (myshop)"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("the output has no %q: %q", line, out)
		}
	}

	// Only what the flags left out is asked, and SQLite asks for a file alone.
	ask, _ = asker("\n")
	cfg = dbsetup.Config{Kind: "sqlite"}
	if err := completeConfig(&cfg, map[string]bool{}, ask, "myshop"); err != nil || cfg.Name != "myshop.db" {
		t.Errorf("sqlite = %+v, %v", cfg, err)
	}
	ask, _ = asker("pw\n")
	cfg = dbsetup.Config{Kind: "postgres", Host: "h", Port: 1234, User: "u", Name: "n"}
	given := map[string]bool{"host": true, "port": true, "user": true, "name": true}
	if err := completeConfig(&cfg, given, ask, "x"); err != nil || cfg.Password != "pw" || cfg.Host != "h" {
		t.Errorf("only the password = %+v, %v", cfg, err)
	}
}

func TestCompleteConfigWithoutTerminal(t *testing.T) {
	for name, tt := range map[string]struct {
		cfg   dbsetup.Config
		given map[string]bool
		want  string
	}{
		"no kind":         {dbsetup.Config{}, map[string]bool{}, "say which database to add"},
		"unknown kind":    {dbsetup.Config{Kind: "oracle"}, map[string]bool{}, `unknown database "oracle"`},
		"no password":     {dbsetup.Config{Kind: "mysql"}, map[string]bool{}, "give the password with the environment variable GONESTLY_DB_PASSWORD"},
		"sqlite and host": {dbsetup.Config{Kind: "sqlite", Host: "h"}, map[string]bool{"host": true}, "a SQLite database is a file"},
		"bad name":        {dbsetup.Config{Kind: "postgres", Name: "a-b"}, map[string]bool{"name": true, "password": true}, `the database name "a-b"`},
	} {
		if err := completeConfig(&tt.cfg, tt.given, nil, "shop"); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.want)
		}
	}
	// An empty password is fine when it is given on purpose.
	cfg := dbsetup.Config{Kind: "postgres"}
	if err := completeConfig(&cfg, map[string]bool{"password": true}, nil, "shop"); err != nil || cfg.Host != "localhost" || cfg.Port != 5432 || cfg.User != "postgres" || cfg.Name != "shop" {
		t.Errorf("defaults = %+v, %v", cfg, err)
	}
}

func TestDefaultName(t *testing.T) {
	for module, want := range map[string]string{"example.com/my-shop": "my_shop", "shop": "shop", "example.com/2fast": "app_2fast", "github.com/acme/Shop.API": "shop_api", "example.com/shop/v2": "shop", "example.com/a~b+c": "a_b_c"} {
		if got := defaultName(&project.Project{Module: module}); got != want {
			t.Errorf("defaultName(%s) = %s, want %s", module, got, want)
		}
	}
}

func TestAddDatabaseToProject(t *testing.T) {
	if testing.Short() {
		t.Skip("creates and builds a project")
	}
	work := t.TempDir()
	if out, code := run(t, work, "new", "shop", "--module", "example.com/shop", "--replace", repoRoot(t)); code != 0 {
		t.Fatalf("new failed:\n%s", out)
	}
	shop := filepath.Join(work, "shop")
	if out, code := run(t, shop, "generate", "resource", "products"); code == 0 || !strings.Contains(out, "needs a database") {
		t.Errorf("a resource without a database:\n%s", out)
	}

	t.Setenv(passwordEnv, "")
	if out, code := run(t, shop, "add", "database", "sqlite", "--name", "data/shop.db"); code != 0 {
		t.Fatalf("add database failed:\n%s", out)
	}
	env, _ := os.ReadFile(filepath.Join(shop, ".env"))
	if !strings.Contains(string(env), "DATABASE_URL=file:data/shop.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)") || !strings.Contains(string(env), "PORT=3000") {
		t.Errorf(".env:\n%s", env)
	}
	module, _ := os.ReadFile(filepath.Join(shop, "internal", "app", "module.go"))
	config, _ := os.ReadFile(filepath.Join(shop, "internal", "app", "config.go"))
	main, _ := os.ReadFile(filepath.Join(shop, "main.go"))
	for file, wants := range map[string][]string{
		"module.go": {`"github.com/yaboylino/gonestly/sql"`, `"example.com/shop/migrations"`, `Driver: "sqlite"`, "migrations.Files"},
		"config.go": {"DatabaseURL", `env:"DATABASE_URL,required"`},
		"main.go":   {`_ "modernc.org/sqlite"`},
	} {
		content := map[string][]byte{"module.go": module, "config.go": config, "main.go": main}[file]
		for _, want := range wants {
			if !bytes.Contains(content, []byte(want)) {
				t.Errorf("%s misses %s:\n%s", file, want, content)
			}
		}
	}

	if out, code := run(t, shop, "add", "database", "sqlite"); code == 0 || !strings.Contains(out, "already has a database") {
		t.Errorf("adding twice:\n%s", out)
	}

	// The project works with the database: resources, tests and migrations.
	if out, code := run(t, shop, "generate", "resource", "products"); code != 0 {
		t.Fatalf("generate failed:\n%s", out)
	}
	goCmd(t, shop, "vet", "./...")
	goCmd(t, shop, "test", "./...")
	if out, code := run(t, shop, "migrate", "up"); code != 0 || !strings.Contains(out, "create_products") {
		t.Errorf("migrate up:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(shop, "data", "shop.db")); err != nil {
		t.Errorf("the database file was not made by the migration: %v", err)
	}
}

func TestAddDatabaseLeavesProjectAloneOnFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a project")
	}
	work := t.TempDir()
	if out, code := run(t, work, "new", "shop", "--module", "example.com/shop", "--replace", repoRoot(t)); code != 0 {
		t.Fatalf("new failed:\n%s", out)
	}
	shop := filepath.Join(work, "shop")
	files := []string{"main.go", ".env", ".env.example", filepath.Join("internal", "app", "module.go"), filepath.Join("internal", "app", "config.go")}
	before := map[string][]byte{}
	for _, f := range files {
		before[f], _ = os.ReadFile(filepath.Join(shop, f))
	}

	t.Setenv(passwordEnv, "")
	if out, code := run(t, shop, "add", "database", "postgres", "--host", "127.0.0.1", "--port", "1", "--user", "x", "--password=", "--name", "shop"); code == 0 {
		t.Fatalf("an unreachable server worked:\n%s", out)
	}
	for _, f := range files {
		if got, _ := os.ReadFile(filepath.Join(shop, f)); !bytes.Equal(got, before[f]) {
			t.Errorf("%s changed:\n%s", f, got)
		}
	}
	if _, err := os.Stat(filepath.Join(shop, "migrations")); err == nil {
		t.Error("the migrations directory was left behind")
	}
	if out, code := run(t, shop, "add", "database", "sqlite"); code != 0 {
		t.Errorf("adding again after the failure:\n%s", out)
	}
}

func TestAddJobsToProject(t *testing.T) {
	if testing.Short() {
		t.Skip("creates and builds a project")
	}
	work := t.TempDir()
	if out, code := run(t, work, "new", "shop", "--module", "example.com/shop", "--replace", repoRoot(t)); code != 0 {
		t.Fatalf("new failed:\n%s", out)
	}
	shop := filepath.Join(work, "shop")
	if out, code := run(t, shop, "add", "jobs"); code == 0 || !strings.Contains(out, "gonestly add database") {
		t.Errorf("jobs without a database:\n%s", out)
	}
	t.Setenv(passwordEnv, "")
	if out, code := run(t, shop, "add", "database", "sqlite", "--name", "data/shop.db"); code != 0 {
		t.Fatalf("add database failed:\n%s", out)
	}
	if out, code := run(t, shop, "add", "jobs"); code != 0 {
		t.Fatalf("add jobs failed:\n%s", out)
	}
	module, _ := os.ReadFile(filepath.Join(shop, "internal", "app", "module.go"))
	for _, want := range []string{`"github.com/yaboylino/gonestly/jobs"`, "jobs.Module(jobs.Options{})"} {
		if !bytes.Contains(module, []byte(want)) {
			t.Errorf("module.go misses %s:\n%s", want, module)
		}
	}
	up, _ := filepath.Glob(filepath.Join(shop, "migrations", "*_create_jobs.up.sql"))
	if len(up) != 1 {
		t.Fatalf("migrations: %v", up)
	}
	if content, _ := os.ReadFile(up[0]); !bytes.Contains(content, []byte("CREATE TABLE gonestly_jobs")) {
		t.Errorf("migration:\n%s", content)
	}
	if out, code := run(t, shop, "add", "jobs"); code == 0 || !strings.Contains(out, "already has jobs") {
		t.Errorf("adding twice:\n%s", out)
	}

	// A resource made in the same second as the jobs migration gets a version of its own.
	if out, code := run(t, shop, "generate", "resource", "products"); code != 0 {
		t.Fatalf("generate failed:\n%s", out)
	}

	// The project builds, and its migrations create a table that the queue works with.
	test := `package app_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yaboylino/gonestly/jobs"
	"github.com/yaboylino/gonestly/sql"
	_ "modernc.org/sqlite"

	"example.com/shop/migrations"
)

type note struct{ Text string }

func TestJobsTable(t *testing.T) {
	ctx := t.Context()
	db, err := sql.Open(ctx, sql.Options{Driver: "sqlite", DSN: "file:" + filepath.Join(t.TempDir(), "jobs.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migrator, err := sql.NewMigrator(db, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	q, err := jobs.New(db, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	job, err := jobs.Define(q, "note", func(_ context.Context, n note) error { got = n.Text; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := job.Enqueue(ctx, note{"hello"}); err != nil {
		t.Fatal(err)
	}
	if n, err := q.RunDue(ctx); err != nil || n != 1 || got != "hello" {
		t.Fatalf("RunDue = %d, %v, %q", n, err, got)
	}
}
`
	if err := os.WriteFile(filepath.Join(shop, "internal", "app", "jobs_test.go"), []byte(test), 0o600); err != nil {
		t.Fatal(err)
	}
	goCmd(t, shop, "vet", "./...")
	goCmd(t, shop, "test", "./...")
}
