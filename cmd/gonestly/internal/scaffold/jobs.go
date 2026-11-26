package scaffold

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/astedit"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/project"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/schema"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
	"github.com/yaboylino/gonestly/jobs"
)

// AddJobsOptions describe how to add background jobs to a project.
type AddJobsOptions struct {
	Project *project.Project
	// Now is the time used for the migration version; the current time when zero.
	Now time.Time
	// SkipInstall skips downloading the dependencies, for tests.
	SkipInstall bool
}

// AddJobs adds the jobs table as a migration and the jobs module to the root module of a
// project that has a database.
func AddJobs(ctx context.Context, opts AddJobsOptions, printer *term.Printer) (err error) {
	p := opts.Project
	database, err := DetectDatabase(p)
	if err != nil {
		return errors.New("jobs are stored in the database, and go.mod has no PostgreSQL, MySQL or SQLite driver; add one with gonestly add database")
	}
	root := filepath.Join(p.Root, "internal", "app", "module.go")
	migrations := filepath.Join(p.Root, "migrations")
	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("%s does not exist, so this project was not made by gonestly new; add the jobs module by hand as described in the documentation", p.Rel(root))
	}
	if _, err := os.Stat(migrations); err != nil {
		return errors.New("the project has no migrations directory; add a database with gonestly add database first")
	}
	source, err := os.ReadFile(root)
	if err != nil {
		return err
	}
	if bytes.Contains(source, []byte(gonestlyModule+"/jobs")) {
		return errors.New("this project already has jobs")
	}
	if !bytes.Contains(source, []byte(gonestlyModule+"/sql")) {
		return fmt.Errorf("%s does not use the sql module, so the project has no database to keep jobs in", p.Rel(root))
	}
	if existing, _ := filepath.Glob(filepath.Join(migrations, "*_create_jobs.up.sql")); len(existing) > 0 {
		return fmt.Errorf("%s exists already; remove it or add the jobs module by hand", p.Rel(existing[0]))
	}
	up, down, err := jobs.Migration(database)
	if err != nil {
		return err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	version, err := schema.NextVersion(migrations, now)
	if err != nil {
		return err
	}

	undo := newUndo([]string{root})
	created := []string{
		filepath.Join(migrations, version+"_create_jobs.up.sql"),
		filepath.Join(migrations, version+"_create_jobs.down.sql"),
	}
	committed := false
	defer func() {
		if err != nil && !committed {
			undo.restore()
			for _, path := range created {
				_ = os.Remove(path)
			}
		}
	}()
	for i, content := range []string{up, down} {
		if err := os.WriteFile(created[i], []byte(content), 0o644); err != nil { //nolint:gosec // migrations are meant to be readable
			return err
		}
		printer.Created(p.Rel(created[i]))
	}
	if _, err := astedit.AddToModule(root, "Imports", "jobs.Module(jobs.Options{})", gonestlyModule+"/jobs"); err != nil {
		return err
	}
	printer.Updated(p.Rel(root))

	committed = true
	if opts.SkipInstall {
		return nil
	}
	printer.Info("downloading dependencies")
	if err := requireModule(ctx, p, "jobs"); err != nil {
		return fmt.Errorf("jobs were added, but their module could not be downloaded; run go mod tidy to try again: %w", err)
	}
	if err := run(ctx, p.Root, "go", "mod", "tidy"); err != nil {
		return fmt.Errorf("jobs were added, but go mod tidy failed; run it to try again: %w", err)
	}
	return nil
}
