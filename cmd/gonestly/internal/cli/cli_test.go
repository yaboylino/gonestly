package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/project"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
)

// run executes the CLI in dir and returns its output.
func run(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	t.Chdir(dir)
	var out bytes.Buffer
	code := Execute(args, &out, &out)
	return out.String(), code
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "app.go")); err != nil {
		t.Fatalf("the framework is not at %s", root)
	}
	return root
}

func goCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestProjectLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("creates and builds a project")
	}
	work := t.TempDir()
	out, code := run(t, work, "new", "shop", "--module", "example.com/shop", "--database", "sqlite", "--replace", repoRoot(t))
	if code != 0 {
		t.Fatalf("new failed:\n%s", out)
	}
	shop := filepath.Join(work, "shop")
	for _, kind := range []string{"resource products", "controller reports", "service reports", "guard auth", "interceptor logging", "module billing"} {
		if out, code := run(t, shop, append([]string{"generate"}, strings.Fields(kind)...)...); code != 0 {
			t.Fatalf("generate %s failed:\n%s", kind, out)
		}
	}
	if out, code := run(t, shop, "generate", "resource", "products"); code == 0 || !strings.Contains(out, "already exists") {
		t.Errorf("generating twice: %s", out)
	}
	goCmd(t, shop, "vet", "./...")
	goCmd(t, shop, "test", "./...")

	out, code = run(t, shop, "routes")
	if code != 0 || !strings.Contains(out, "DELETE   /products/{id}   products.NewController in module \"products\"") {
		t.Errorf("routes:\n%s", out)
	}
	t.Setenv("LOG_LEVEL", "loud")
	if out, code := run(t, shop, "routes"); code == 0 || !strings.Contains(out, `the log level "loud"`) {
		t.Errorf("a bad log level:\n%s", out)
	}
	t.Setenv("LOG_LEVEL", "")
	out, code = run(t, shop, "openapi", "-o", "openapi.json")
	if code != 0 {
		t.Fatalf("openapi:\n%s", out)
	}
	document, _ := os.ReadFile(filepath.Join(shop, "openapi.json"))
	if !bytes.Contains(document, []byte(`"/products/{id}"`)) || !bytes.Contains(document, []byte(`"title": "Shop"`)) {
		t.Errorf("document:\n%.300s", document)
	}

	out, code = run(t, shop, "migrate", "status")
	if code != 0 || !strings.Contains(out, "create_products") {
		t.Errorf("status:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "down"); code != 0 || !strings.Contains(out, "reverted") {
		t.Errorf("down:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "up"); code != 0 || !strings.Contains(out, "applied") {
		t.Errorf("up:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "create", "add_price"); code != 0 || !strings.Contains(out, "_add_price.up.sql") {
		t.Errorf("create:\n%s", out)
	}
	if out, code := run(t, shop, "migrate", "status", "--dsn", "mysql://localhost/shop"); code == 0 || !strings.Contains(out, "write it as user:password@tcp(host:3306)/database") {
		t.Errorf("status with a MySQL URL:\n%s", out)
	}
	if out, code := run(t, shop, "version"); code != 0 || !strings.HasPrefix(out, "gonestly ") {
		t.Errorf("version: %s", out)
	}
}

func TestErrors(t *testing.T) {
	dir := t.TempDir()
	for args, want := range map[string]string{
		"generate resource products": "no go.mod found",
		"routes":                     "no go.mod found",
		"generate widget x":          "no go.mod found",
		"new":                        "accepts 1 arg",
		"nonsense":                   "unknown command",
	} {
		out, code := run(t, dir, strings.Fields(args)...)
		if code == 0 || !strings.Contains(out, want) {
			t.Errorf("gonestly %s: code %d, output %q, want %q", args, code, out, want)
		}
	}
	if out, code := run(t, dir, "new", "app", "--database", "oracle"); code == 0 || !strings.Contains(out, `unknown database "oracle"; use postgres, mysql or sqlite`) {
		t.Errorf("new with oracle: %s", out)
	}
}

func TestCreateMigration(t *testing.T) {
	dir := t.TempDir()
	p := &project.Project{Root: dir}
	printer := term.New(&bytes.Buffer{})
	now := time.Date(2026, 11, 25, 10, 30, 0, 0, time.UTC)

	if err := createMigration(dir, "create_users", now, printer, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "20261125103000_create_users.up.sql")); err != nil {
		t.Error("an empty directory does not get a timestamp version")
	}

	counter := t.TempDir()
	_ = os.WriteFile(filepath.Join(counter, "0007_first.up.sql"), nil, 0o600)
	if err := createMigration(counter, "second", now, printer, p); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0008_second.up.sql", "0008_second.down.sql"} {
		if _, err := os.Stat(filepath.Join(counter, name)); err != nil {
			t.Errorf("%s was not created", name)
		}
	}
	if err := createMigration(dir, "Bad Name", now, printer, p); err == nil {
		t.Error("an invalid name was accepted")
	}
	if err := createMigration(filepath.Join(dir, "missing"), "x", now, printer, p); err == nil {
		t.Error("a missing directory was accepted")
	}
}

func TestDriverFor(t *testing.T) {
	for dsn, want := range map[string]string{
		"postgres://localhost/shop":            "pgx",
		"postgresql://localhost/shop":          "pgx",
		"host=localhost dbname=shop":           "pgx",
		"file:shop.db?_pragma=foreign_keys(1)": "sqlite",
		"shop.db":                              "sqlite",
		"root:pw@tcp(localhost:3306)/shop":     "mysql",
		"root@unix(/tmp/mysql.sock)/shop":      "mysql",
		"mysql://localhost":                    "",
	} {
		if got := driverFor(dsn); got != want {
			t.Errorf("driverFor(%q) = %q, want %q", dsn, got, want)
		}
	}
}

func TestPrintRoutes(t *testing.T) {
	var out bytes.Buffer
	if err := printRoutes(&out, []byte(`[{"method":"GET","path":"/a","controller":"c"}]`)); err != nil || out.String() != "METHOD   PATH   CONTROLLER\nGET      /a     c\n" {
		t.Errorf("out = %q, err = %v", out.String(), err)
	}
	out.Reset()
	if err := printRoutes(&out, []byte(`[]`)); err != nil || !strings.Contains(out.String(), "no routes") {
		t.Errorf("out = %q", out.String())
	}
	if err := printRoutes(&out, []byte(`nope`)); err == nil {
		t.Error("invalid JSON was accepted")
	}
}
