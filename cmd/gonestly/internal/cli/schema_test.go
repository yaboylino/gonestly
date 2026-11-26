package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newShop(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	if out, code := run(t, work, "new", "shop", "--module", "example.com/shop", "--database", "sqlite", "--replace", repoRoot(t)); code != 0 {
		t.Fatalf("new failed:\n%s", out)
	}
	return filepath.Join(work, "shop")
}

func migrationFiles(t *testing.T, shop string) []string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(shop, "migrations", "*.sql"))
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = filepath.Base(m)
	}
	return names
}

func edit(t *testing.T, path, old, replacement string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s lacks %q:\n%s", path, old, data)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), old, replacement, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateDevFollowsTheModels(t *testing.T) {
	if testing.Short() {
		t.Skip("creates and builds a project")
	}
	shop := newShop(t)
	if out, code := run(t, shop, "generate", "resource", "products"); code != 0 {
		t.Fatalf("generate failed:\n%s", out)
	}
	files := migrationFiles(t, shop)
	if len(files) != 2 || !strings.HasSuffix(files[0], "_create_products.down.sql") {
		t.Fatalf("migrations = %v", files)
	}
	up, _ := os.ReadFile(filepath.Join(shop, "migrations", files[1]))
	if !strings.Contains(string(up), "CREATE TABLE products (") || !strings.Contains(string(up), "id         INTEGER PRIMARY KEY AUTOINCREMENT") {
		t.Errorf("up:\n%s", up)
	}
	if _, err := os.Stat(filepath.Join(shop, "migrations", "schema.snapshot.json")); err != nil {
		t.Errorf("no snapshot: %v", err)
	}
	if out, code := run(t, shop, "migrate", "dev"); code != 0 || !strings.Contains(out, "in sync") {
		t.Fatalf("dev without changes:\n%s", out)
	}

	model := filepath.Join(shop, "internal", "products", "model.go")
	edit(t, model, "\tName      string    `db:\"name\" json:\"name\"`\n", "\tName      string    `db:\"name\" json:\"name\"`\n\tNote      *string   `db:\"note\" json:\"note\" schema:\"size=200,index\"`\n")
	if out, code := run(t, shop, "migrate", "diff"); code != 0 || !strings.Contains(out, "ALTER TABLE products ADD COLUMN note TEXT") || !strings.Contains(out, "CREATE INDEX products_note_idx ON products (note)") {
		t.Fatalf("diff:\n%s", out)
	}
	if got := migrationFiles(t, shop); len(got) != 2 {
		t.Errorf("diff wrote files: %v", got)
	}
	out, code := run(t, shop, "migrate", "dev", "--name", "add_product_note")
	if code != 0 || !strings.Contains(out, "applied") {
		t.Fatalf("dev:\n%s", out)
	}
	if got := migrationFiles(t, shop); len(got) != 4 {
		t.Errorf("migrations = %v", got)
	}
	if out, code := run(t, shop, "migrate", "dev"); code != 0 || !strings.Contains(out, "in sync") {
		t.Errorf("dev after dev:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "status"); code != 0 || strings.Contains(out, "pending") {
		t.Errorf("status:\n%s", out)
	}

	// A field that was renamed is not a drop, when the author says so.
	edit(t, model, "\tNote      *string   `db:\"note\" json:\"note\" schema:\"size=200,index\"`\n", "\tRemark    *string   `db:\"remark\" json:\"note\" schema:\"size=200,index\"`\n")
	out, code = run(t, shop, "migrate", "diff")
	if code != 0 || !strings.Contains(out, "may be a rename") || !strings.Contains(out, "DROP COLUMN note") {
		t.Errorf("diff without the rename:\n%s", out)
	}
	out, code = run(t, shop, "migrate", "diff", "--rename", "products.note=remark")
	if code != 0 || !strings.Contains(out, "RENAME COLUMN note TO remark") || strings.Contains(out, "DROP COLUMN") {
		t.Errorf("diff with the rename:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "diff", "--rename", "products.nothing=remark"); code == 0 || !strings.Contains(out, "do not have") {
		t.Errorf("a wrong rename:\n%s", out)
	}

	// Dropping the column loses data, so it has to be accepted.
	edit(t, model, "\tRemark    *string   `db:\"remark\" json:\"note\" schema:\"size=200,index\"`\n", "")
	out, code = run(t, shop, "migrate", "dev")
	if code == 0 || !strings.Contains(out, "--accept-data-loss") || !strings.Contains(out, "the column products.note and its data are dropped") {
		t.Errorf("dev with a drop:\n%s", out)
	}
	if got := migrationFiles(t, shop); len(got) != 4 {
		t.Errorf("a refused migration wrote files: %v", got)
	}
	if out, code := run(t, shop, "migrate", "dev", "--accept-data-loss"); code != 0 {
		t.Fatalf("dev with --accept-data-loss:\n%s", out)
	}
	if got := migrationFiles(t, shop); len(got) != 6 || !strings.Contains(strings.Join(got, " "), "drop_products_note") {
		t.Errorf("migrations = %v", got)
	}

	goCmd(t, shop, "vet", "./...")
	goCmd(t, shop, "test", "./...")
	if out, code := run(t, shop, "migrate", "down"); code != 0 {
		t.Errorf("down:\n%s", out)
	}
}

func TestMigrateDevCreateOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("creates and builds a project")
	}
	shop := newShop(t)
	if out, code := run(t, shop, "generate", "resource", "products"); code != 0 {
		t.Fatalf("generate failed:\n%s", out)
	}
	edit(t, filepath.Join(shop, "internal", "products", "model.go"), "\tName      string    `db:\"name\" json:\"name\"`\n", "\tName      string    `db:\"name\" json:\"name\" schema:\"unique\"`\n")
	out, code := run(t, shop, "migrate", "dev", "--create-only", "--name", "unique_names")
	if code != 0 || !strings.Contains(out, "not applied yet") {
		t.Fatalf("create only:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "status"); code != 0 || !strings.Contains(out, "pending") {
		t.Errorf("status:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "dev", "--name", "Not A Name"); code != 0 || !strings.Contains(out, "in sync") {
		t.Errorf("after create only:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "up"); code != 0 || !strings.Contains(out, "unique_names") {
		t.Errorf("up:\n%s", out)
	}
}

func TestMigrateDevOnHandWrittenMigrations(t *testing.T) {
	if testing.Short() {
		t.Skip("creates and builds a project")
	}
	shop := newShop(t)
	if err := os.WriteFile(filepath.Join(shop, "migrations", "0001_create_tracks.up.sql"), []byte("CREATE TABLE tracks (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shop, "migrations", "0001_create_tracks.down.sql"), []byte("DROP TABLE tracks;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(shop, "internal", "tracks"), 0o755); err != nil {
		t.Fatal(err)
	}
	model := "package tracks\n\ntype Track struct {\n\tID    int64  `db:\"id\"`\n\tTitle string `db:\"title\"`\n}\n"
	if err := os.WriteFile(filepath.Join(shop, "internal", "tracks", "model.go"), []byte(model), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := run(t, shop, "migrate", "dev"); code == 0 || !strings.Contains(out, "gonestly migrate baseline") {
		t.Fatalf("dev without a baseline:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "baseline"); code != 0 || !strings.Contains(out, "recorded 1 tables") {
		t.Fatalf("baseline:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "baseline"); code == 0 || !strings.Contains(out, "--force") {
		t.Errorf("a second baseline:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "dev"); code != 0 || !strings.Contains(out, "in sync") {
		t.Fatalf("dev after the baseline:\n%s", out)
	}
	// A resource in a project with counters follows the counters.
	if out, code := run(t, shop, "generate", "resource", "products"); code != 0 {
		t.Fatalf("generate failed:\n%s", out)
	}
	files := strings.Join(migrationFiles(t, shop), " ")
	if !strings.Contains(files, "0002_create_products.up.sql") {
		t.Errorf("migrations = %s", files)
	}
	if out, code := run(t, shop, "migrate", "dev"); code != 0 || !strings.Contains(out, "in sync") {
		t.Errorf("dev after generate:\n%s", out)
	}
}

func TestGenerateResourceLeavesOtherModelChangesAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("creates and builds a project")
	}
	shop := newShop(t)
	if out, code := run(t, shop, "generate", "resource", "products"); code != 0 {
		t.Fatalf("generate failed:\n%s", out)
	}
	edit(t, filepath.Join(shop, "internal", "products", "model.go"), "\tName      string    `db:\"name\" json:\"name\"`\n", "\tName      string    `db:\"name\" json:\"name\"`\n\tNote      *string   `db:\"note\" json:\"note\"`\n")
	if out, code := run(t, shop, "generate", "resource", "orders"); code != 0 {
		t.Fatalf("generate failed:\n%s", out)
	}
	for _, f := range migrationFiles(t, shop) {
		if strings.HasSuffix(f, "_create_orders.up.sql") {
			data, _ := os.ReadFile(filepath.Join(shop, "migrations", f))
			if strings.Contains(string(data), "note") {
				t.Errorf("the resource migration holds a change that is not its own:\n%s", data)
			}
		}
	}
	out, code := run(t, shop, "migrate", "diff")
	if code != 0 || !strings.Contains(out, "ADD COLUMN note") {
		t.Errorf("the change to products is still to come:\n%s", out)
	}
}
