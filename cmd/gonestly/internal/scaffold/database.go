package scaffold

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/astedit"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/dbsetup"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/project"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
)

// AddDatabaseOptions describe the database to add to a project.
type AddDatabaseOptions struct {
	Project *project.Project
	Config  dbsetup.Config
	// SkipCreate leaves the database to the user, and only wires it into the project.
	SkipCreate bool
	// SkipInstall skips downloading the dependencies, for tests.
	SkipInstall bool
}

var driverModules = map[string]string{
	"postgres": "github.com/jackc/pgx/v5",
	"mysql":    "github.com/go-sql-driver/mysql",
	"sqlite":   "modernc.org/sqlite",
}

var driverImports = map[string]string{
	"postgres": "github.com/jackc/pgx/v5/stdlib",
	"mysql":    "github.com/go-sql-driver/mysql",
	"sqlite":   "modernc.org/sqlite",
}

// AddDatabase creates the database on its server, and wires it into a project that has none:
// the connection string in .env, the sql module in the root module, the driver, and a
// migrations directory.
func AddDatabase(ctx context.Context, opts AddDatabaseOptions, printer *term.Printer) (err error) {
	p, cfg := opts.Project, opts.Config
	cfg.Dir = p.Root
	if err := cfg.Validate(); err != nil {
		return err
	}
	files := map[string]string{
		"root":   filepath.Join(p.Root, "internal", "app", "module.go"),
		"config": filepath.Join(p.Root, "internal", "app", "config.go"),
		"main":   filepath.Join(p.Root, "main.go"),
	}
	if err := checkAddable(p, files); err != nil {
		return err
	}

	// Every file is restored when a step fails, so the project is never left half converted.
	undo := newUndo(append(slices.Collect(maps.Values(files)), filepath.Join(p.Root, ".env"), filepath.Join(p.Root, ".env.example"), filepath.Join(p.Root, "migrations", "migrations.go")), filepath.Join(p.Root, "migrations"))
	committed := false
	defer func() {
		if err != nil && !committed {
			undo.restore()
		}
	}()

	if err := writeMigrationsPackage(p, printer); err != nil {
		return err
	}
	if err := setEnv(p, ".env", "DATABASE_URL", cfg.DSN(), printer); err != nil {
		return err
	}
	if err := setEnv(p, ".env.example", "DATABASE_URL", cfg.ExampleDSN(), printer); err != nil {
		return err
	}

	if changed, err := astedit.AddStructField(files["config"], "Config", "DatabaseURL string `env:\"DATABASE_URL,required\"`"); err != nil {
		return err
	} else if changed {
		printer.Updated(p.Rel(files["config"]))
	}
	expr := fmt.Sprintf("sql.ModuleFunc(func(cfg Config) sql.Options {\n\treturn sql.Options{Driver: %q, DSN: cfg.DatabaseURL, Migrations: migrations.Files}\n})", cfg.Driver())
	if _, err := astedit.AddToModule(files["root"], "Imports", expr, gonestlyModule+"/sql"); err != nil {
		return err
	}
	if _, err := astedit.AddImport(files["root"], p.Module+"/migrations", ""); err != nil {
		return err
	}
	printer.Updated(p.Rel(files["root"]))
	if _, err := astedit.AddImport(files["main"], driverImports[cfg.Kind], "_"); err != nil {
		return err
	}
	printer.Updated(p.Rel(files["main"]))

	if !opts.SkipCreate {
		result, err := dbsetup.Create(ctx, cfg)
		if err != nil {
			return err
		}
		switch {
		case cfg.Kind == "sqlite":
			printer.Info("the SQLite file %s is created by the first migration", cfg.Name)
		case result.Existed:
			printer.Info("the database %s already exists on %s:%d", cfg.Name, cfg.Host, cfg.Port)
		default:
			printer.Success("created the database %s on %s:%d", cfg.Name, cfg.Host, cfg.Port)
		}
	}

	committed = true
	if opts.SkipInstall {
		return nil
	}
	printer.Info("downloading dependencies")
	if err := installDatabase(ctx, p, cfg.Kind); err != nil {
		return fmt.Errorf("the database was added, but its dependencies could not be downloaded; run go mod tidy to try again: %w", err)
	}
	return nil
}

// checkAddable refuses projects that have a database already or that are not shaped like a
// project made by gonestly new, before anything is changed.
func checkAddable(p *project.Project, files map[string]string) error {
	for _, path := range files {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s does not exist, so this project was not made by gonestly new; add the sql module by hand as described in the documentation", p.Rel(path))
		}
	}
	root, err := os.ReadFile(files["root"])
	if err != nil {
		return err
	}
	gomod, _ := os.ReadFile(filepath.Join(p.Root, "go.mod"))
	if bytes.Contains(root, []byte(gonestlyModule+"/sql")) || bytes.Contains(gomod, []byte(gonestlyModule+"/sql")) {
		return errors.New("this project already has a database; change DATABASE_URL in .env to use another one")
	}
	if _, err := os.Stat(filepath.Join(p.Root, "migrations", "migrations.go")); err == nil {
		return errors.New("migrations/migrations.go exists already; remove it or add the sql module by hand")
	}
	return nil
}

func writeMigrationsPackage(p *project.Project, printer *term.Printer) error {
	content, err := render("new/migrations/migrations.go.tmpl", data{Module: p.Module})
	if err != nil {
		return err
	}
	path := filepath.Join(p.Root, "migrations", "migrations.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // project directories are meant to be shared
		return err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil { //nolint:gosec // source files are meant to be readable
		return err
	}
	printer.Created(p.Rel(path))
	return nil
}

var plainEnvValue = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,?&()-]+$`)

// envValue quotes a value for a dotenv file when it holds anything that could be misread.
func envValue(v string) string {
	switch {
	case plainEnvValue.MatchString(v):
		return v
	case !strings.ContainsAny(v, "'\n\r"):
		return "'" + v + "'"
	default:
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)
		return `"` + r.Replace(v) + `"`
	}
}

// setEnv sets key in a dotenv file, replacing a line that has it and keeping the rest.
func setEnv(p *project.Project, name, key, value string, printer *term.Printer) error {
	path := filepath.Join(p.Root, name)
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	line := key + "=" + envValue(value)
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	replaced := false
	for i, l := range lines {
		if k, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(l), "export "), "="); ok && strings.TrimSpace(k) == key {
			lines[i], replaced = line, true
		}
	}
	if !replaced {
		if len(content) == 0 {
			lines = nil
		}
		lines = append(lines, line)
	}
	// The file holds the password, so only its owner may read it.
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil { //nolint:gosec // the path is in the project the user chose
		return err
	}
	printer.Updated(name)
	return nil
}

// installDatabase requires the sql module and the driver, following a replace of the framework
// that a project uses against a local checkout.
func installDatabase(ctx context.Context, p *project.Project, kind string) error {
	gomod, err := os.ReadFile(filepath.Join(p.Root, "go.mod"))
	if err != nil {
		return err
	}
	if local := localFramework(string(gomod)); local != "" {
		if err := run(ctx, p.Root, "go", "mod", "edit", "-require="+gonestlyModule+"/sql@v0.0.0", "-replace="+gonestlyModule+"/sql="+filepath.Join(local, "sql")); err != nil {
			return err
		}
	} else if err := run(ctx, p.Root, "go", "get", gonestlyModule+"/sql@"+frameworkVersion(string(gomod))); err != nil {
		return err
	}
	if err := run(ctx, p.Root, "go", "get", driverModules[kind]+"@latest"); err != nil {
		return err
	}
	return run(ctx, p.Root, "go", "mod", "tidy")
}

var replaceFramework = regexp.MustCompile(`(?m)^\s*(?:replace\s+)?` + regexp.QuoteMeta(gonestlyModule) + `\s+=>\s+(\S+)\s*$`)

// localFramework returns the directory that go.mod replaces the framework with, or "".
func localFramework(gomod string) string {
	if m := replaceFramework.FindStringSubmatch(gomod); m != nil {
		return m[1]
	}
	return ""
}

var requireFramework = regexp.MustCompile(`(?m)^\s*(?:require\s+)?` + regexp.QuoteMeta(gonestlyModule) + `\s+(v\S+)`)

// frameworkVersion is the version of the framework that the project requires, so that the sql
// module that goes with it is added and the framework is not upgraded on the way.
func frameworkVersion(gomod string) string {
	if m := requireFramework.FindStringSubmatch(gomod); m != nil {
		return m[1]
	}
	return "latest"
}

// undo restores files to what they were, and removes what did not exist.
type undo struct {
	files map[string][]byte
	gone  []string
	dirs  []string
}

func newUndo(paths []string, newDirs ...string) *undo {
	u := &undo{files: map[string][]byte{}}
	for _, path := range paths {
		if content, err := os.ReadFile(path); err == nil {
			u.files[path] = content
		} else {
			u.gone = append(u.gone, path)
		}
	}
	for _, dir := range newDirs {
		if _, err := os.Stat(dir); err != nil {
			u.dirs = append(u.dirs, dir)
		}
	}
	return u
}

func (u *undo) restore() {
	for path, content := range u.files {
		info, err := os.Stat(path)
		mode := os.FileMode(0o644)
		if err == nil {
			mode = info.Mode().Perm()
		}
		_ = os.WriteFile(path, content, mode)
	}
	for _, path := range u.gone {
		_ = os.Remove(path)
	}
	for _, dir := range u.dirs {
		_ = os.Remove(dir)
	}
}
