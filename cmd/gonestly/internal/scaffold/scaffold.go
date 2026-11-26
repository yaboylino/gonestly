// Package scaffold creates projects and generates modules, controllers, services and resources.
package scaffold

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/astedit"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/columns"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/project"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
)

//go:embed all:templates
var templates embed.FS

// Kinds are the things Generate can create.
var Kinds = []string{"resource", "module", "controller", "service", "guard", "interceptor"}

// Databases are the databases a project can use.
var Databases = map[string]string{"postgres": "pgx", "mysql": "mysql", "sqlite": "sqlite"}

const gonestlyModule = "github.com/yaboylino/gonestly"

// data is what the templates see.
type data struct {
	Module   string
	Title    string
	Database string
	Driver   string
	Import   string
	Names    Names
}

type output struct {
	template string
	target   string
}

func render(name string, d data) ([]byte, error) {
	source, err := fs.ReadFile(templates, "templates/"+name)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(source))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		return nil, err
	}
	if strings.HasSuffix(name, ".go.tmpl") {
		formatted, err := format.Source(buf.Bytes())
		if err != nil {
			return nil, fmt.Errorf("the template %s does not produce valid Go: %w", name, err)
		}
		return formatted, nil
	}
	return buf.Bytes(), nil
}

// writer writes rendered files, never overwriting one that exists.
type writer struct {
	root    string
	printer *term.Printer
	created []string
}

// undo removes the files this writer created, so a failed generation leaves nothing behind.
func (w *writer) undo() {
	for _, path := range slices.Backward(w.created) {
		_ = os.Remove(path)
	}
	w.created = nil
}

func (w *writer) check(outputs []output) error {
	var existing []string
	for _, o := range outputs {
		if _, err := os.Stat(filepath.Join(w.root, o.target)); err == nil {
			existing = append(existing, o.target)
		}
	}
	if len(existing) > 0 {
		return fmt.Errorf("these files already exist, so nothing was generated: %s", strings.Join(existing, ", "))
	}
	return nil
}

func (w *writer) write(outputs []output, d data) error {
	if err := w.check(outputs); err != nil {
		return err
	}
	for _, o := range outputs {
		content, err := render(o.template, d)
		if err != nil {
			return err
		}
		path := filepath.Join(w.root, o.target)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // project directories are meant to be shared
			return err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil { //nolint:gosec // source files are meant to be readable
			return err
		}
		w.created = append(w.created, path)
		w.printer.Created(filepath.ToSlash(o.target))
	}
	return nil
}

// ProjectOptions describe a new project.
type ProjectOptions struct {
	// Dir is the directory to create; its base name names the project.
	Dir string
	// Module is the Go module path; it defaults to the project name.
	Module string
	// Database is "postgres", "mysql", "sqlite" or empty for none.
	Database string
	// Replace points at a local checkout of Gonestly, used instead of the published modules.
	Replace string
	// SkipInstall skips downloading the dependencies, for tests.
	SkipInstall bool
}

// NewProject creates a project that runs immediately.
func NewProject(ctx context.Context, opts ProjectOptions, printer *term.Printer) error {
	name := filepath.Base(filepath.Clean(opts.Dir))
	names, err := NewNames(name)
	if err != nil {
		return err
	}
	driver, ok := Databases[opts.Database]
	if opts.Database != "" && !ok {
		return fmt.Errorf("unknown database %q; use postgres, mysql or sqlite", opts.Database)
	}
	if entries, err := os.ReadDir(opts.Dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s already exists and is not empty", opts.Dir)
	}
	module := opts.Module
	if module == "" {
		module = names.Kebab
	}
	d := data{Module: module, Title: titleCase(names.Words), Database: opts.Database, Driver: driver, Names: names}

	outputs := []output{
		{"new/main.go.tmpl", "main.go"},
		{"new/internal/app/module.go.tmpl", "internal/app/module.go"},
		{"new/internal/app/config.go.tmpl", "internal/app/config.go"},
		{"new/internal/app/logger.go.tmpl", "internal/app/logger.go"},
		{"new/internal/health/module.go.tmpl", "internal/health/module.go"},
		{"new/internal/health/controller.go.tmpl", "internal/health/controller.go"},
		{"new/internal/health/controller_test.go.tmpl", "internal/health/controller_test.go"},
		{"new/env.tmpl", ".env"},
		{"new/env.tmpl", ".env.example"},
		{"new/gitignore.tmpl", ".gitignore"},
		{"new/editorconfig.tmpl", ".editorconfig"},
		{"new/vscode-settings.json.tmpl", ".vscode/settings.json"},
		{"new/Makefile.tmpl", "Makefile"},
		{"new/README.md.tmpl", "README.md"},
	}
	if opts.Database != "" {
		outputs = append(outputs, output{"new/migrations/migrations.go.tmpl", "migrations/migrations.go"})
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil { //nolint:gosec // project directories are meant to be shared
		return err
	}
	w := &writer{root: opts.Dir, printer: printer}
	if err := w.write(outputs, d); err != nil {
		return err
	}
	if err := writeGoMod(opts, module); err != nil {
		return err
	}
	printer.Created("go.mod")

	if opts.SkipInstall {
		return nil
	}
	printer.Info("downloading dependencies")
	if err := install(ctx, opts); err != nil {
		return fmt.Errorf("the project was created in %s, but its dependencies could not be downloaded; run go mod tidy there to try again: %w", opts.Dir, err)
	}
	if _, err := exec.LookPath("git"); err == nil {
		_ = run(ctx, opts.Dir, "git", "init", "--quiet")
	}
	return nil
}

func install(ctx context.Context, opts ProjectOptions) error {
	if opts.Replace == "" {
		modules := []string{gonestlyModule + "@latest", gonestlyModule + "/validator@latest"}
		if opts.Database != "" {
			modules = append(modules, gonestlyModule+"/sql@latest")
		}
		if err := run(ctx, opts.Dir, "go", append([]string{"get"}, modules...)...); err != nil {
			return err
		}
	}
	return run(ctx, opts.Dir, "go", "mod", "tidy")
}

func writeGoMod(opts ProjectOptions, module string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "module %s\n\ngo 1.26.0\n", module)
	if opts.Replace != "" {
		replace, err := filepath.Abs(opts.Replace)
		if err != nil {
			return err
		}
		required := []string{"", "/validator"}
		if opts.Database != "" {
			required = append(required, "/sql")
		}
		b.WriteString("\nrequire (\n")
		for _, r := range required {
			fmt.Fprintf(&b, "\t%s%s v0.0.0\n", gonestlyModule, r)
		}
		b.WriteString(")\n\nreplace (\n")
		for _, r := range required {
			fmt.Fprintf(&b, "\t%s%s => %s\n", gonestlyModule, r, filepath.Join(replace, r))
		}
		b.WriteString(")\n")
	}
	return os.WriteFile(filepath.Join(opts.Dir, "go.mod"), []byte(b.String()), 0o644) //nolint:gosec // source files are meant to be readable
}

// GenerateOptions describe where to generate.
type GenerateOptions struct {
	Project *project.Project
	// Dir is the package directory; it defaults to internal/<package>.
	Dir string
	// Now is the time used for migration versions; it defaults to the current time.
	Now time.Time
}

// Generate creates something of the given kind and registers it in its module, and a new
// module in the root module.
func Generate(kind, name string, opts GenerateOptions, printer *term.Printer) error {
	names, err := NewNames(name)
	if err != nil {
		return err
	}
	if err := names.checkGenerated(kind); err != nil {
		return err
	}
	p := opts.Project
	dir := opts.Dir
	if dir == "" {
		dir = filepath.Join(p.Root, "internal", names.Package)
	}
	importPath, err := p.ImportPath(dir)
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(p.Root, dir)
	d := data{Module: p.Module, Import: importPath, Names: names}
	w := &writer{root: p.Root, printer: printer}
	moduleFile := filepath.Join(dir, "module.go")
	_, moduleErr := os.Stat(moduleFile)
	moduleExists := moduleErr == nil
	in := func(file string) string { return filepath.Join(rel, file) }

	switch kind {
	case "resource":
		return generateResource(w, d, p, dir, rel, moduleExists, opts.Now, printer)
	case "module":
		if moduleExists {
			return fmt.Errorf("%s already exists", p.Rel(moduleFile))
		}
		if err := w.write([]output{{"module/module.go.tmpl", in("module.go")}}, d); err != nil {
			return err
		}
		return registerInRoot(p, importPath, printer)
	}

	var outputs []output
	var register []struct{ field, expr string }
	switch kind {
	case "controller":
		outputs = []output{{"controller/controller.go.tmpl", in("controller.go")}, {"controller/controller_test.go.tmpl", in("controller_test.go")}}
		register = []struct{ field, expr string }{{"Controllers", "NewController"}}
	case "service":
		outputs = []output{{"service/service.go.tmpl", in("service.go")}}
		register = []struct{ field, expr string }{{"Providers", "NewService"}}
	case "guard":
		outputs = []output{{"guard/guard.go.tmpl", in("guard.go")}}
		register = []struct{ field, expr string }{{"Providers", "NewGuard"}, {"Exports", "NewGuard"}}
	case "interceptor":
		outputs = []output{{"interceptor/interceptor.go.tmpl", in("interceptor.go")}}
		register = []struct{ field, expr string }{{"Providers", "NewInterceptor"}, {"Exports", "NewInterceptor"}}
	default:
		return fmt.Errorf("unknown kind %q; use one of %s", kind, strings.Join(Kinds, ", "))
	}

	if !moduleExists {
		outputs = append([]output{{"module/module.go.tmpl", in("module.go")}}, outputs...)
	}
	if err := w.write(outputs, d); err != nil {
		w.undo()
		return err
	}
	for _, r := range register {
		if _, err := astedit.AddToModule(moduleFile, r.field, r.expr, ""); err != nil {
			w.undo()
			return err
		}
	}
	printer.Updated(p.Rel(moduleFile))
	if !moduleExists {
		return registerInRoot(p, importPath, printer)
	}
	return nil
}

func generateResource(w *writer, d data, p *project.Project, dir, rel string, moduleExists bool, now time.Time, printer *term.Printer) error {
	if moduleExists {
		return fmt.Errorf("%s already exists; a resource creates its own module", p.Rel(filepath.Join(dir, "module.go")))
	}
	database, err := detectDatabase(p)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(p.Root, "migrations")); err != nil {
		return errors.New("a resource needs a migrations directory; create the project with --database, or add one as described in the documentation")
	}
	d.Database = database
	if now.IsZero() {
		now = time.Now()
	}
	version := now.UTC().Format("20060102150405")
	in := func(file string) string { return filepath.Join(rel, file) }
	outputs := []output{
		{"resource/module.go.tmpl", in("module.go")},
		{"resource/model.go.tmpl", in("model.go")},
		{"resource/requests.go.tmpl", in("requests.go")},
		{"resource/service.go.tmpl", in("service.go")},
		{"resource/controller.go.tmpl", in("controller.go")},
		{"resource/up.sql.tmpl", filepath.Join("migrations", version+"_create_"+d.Names.Snake+".up.sql")},
		{"resource/down.sql.tmpl", filepath.Join("migrations", version+"_create_"+d.Names.Snake+".down.sql")},
	}
	if database == "sqlite" {
		outputs = append(outputs, output{"resource/controller_test.go.tmpl", in("controller_test.go")})
	}
	if err := w.write(outputs, d); err != nil {
		w.undo()
		return err
	}
	if _, err := columns.Generate(dir); err != nil {
		w.undo()
		return fmt.Errorf("generating the columns: %w", err)
	}
	printer.Created(filepath.ToSlash(in(columns.FileName)))
	if err := registerInRoot(p, d.Import, printer); err != nil {
		w.undo()
		_ = os.Remove(filepath.Join(dir, columns.FileName))
		return err
	}
	return nil
}

// registerInRoot imports a new module in the root module, internal/app/module.go.
func registerInRoot(p *project.Project, importPath string, printer *term.Printer) error {
	root := filepath.Join(p.Root, "internal", "app", "module.go")
	expr := astedit.Name(importPath) + ".Module"
	if _, err := os.Stat(root); err != nil {
		printer.Info("add %s to the Imports of your root module", expr)
		return nil
	}
	changed, err := astedit.AddToModule(root, "Imports", expr, importPath)
	if err != nil {
		return err
	}
	if changed {
		printer.Updated(p.Rel(root))
	}
	return nil
}

// detectDatabase reads the driver from go.mod.
func detectDatabase(p *project.Project) (string, error) {
	gomod, err := os.ReadFile(filepath.Join(p.Root, "go.mod"))
	if err != nil {
		return "", err
	}
	switch {
	case bytes.Contains(gomod, []byte("github.com/jackc/pgx")), bytes.Contains(gomod, []byte("github.com/lib/pq")):
		return "postgres", nil
	case bytes.Contains(gomod, []byte("github.com/go-sql-driver/mysql")):
		return "mysql", nil
	case bytes.Contains(gomod, []byte("modernc.org/sqlite")), bytes.Contains(gomod, []byte("github.com/mattn/go-sqlite3")):
		return "sqlite", nil
	default:
		return "", errors.New("a resource needs a database, and go.mod has no PostgreSQL, MySQL or SQLite driver; create the project with --database postgres, mysql or sqlite")
	}
}

func run(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

func titleCase(words string) string {
	parts := strings.Fields(words)
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
