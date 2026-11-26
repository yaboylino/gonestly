package schema

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Env is a project whose models are its schema.
type Env struct {
	Root    string
	Module  string
	Dialect Dialect
}

// Dir is the directory with the migrations.
func (e Env) Dir() string { return filepath.Join(e.Root, "migrations") }

// Models reads the schema that the models in the project describe.
func (e Env) Models() (Schema, error) { return Extract(e.Root, e.Module) }

// Snapshot reads the schema that the migrations made.
func (e Env) Snapshot() (Schema, error) { return LoadSnapshot(e.Dir()) }

// Baseline records the current models as the schema that the existing migrations made, without
// writing a migration. Use it for a project whose migrations were written by hand, once, when its
// database matches its models.
func (e Env) Baseline() (Schema, error) {
	models, err := e.Models()
	if err != nil {
		return Schema{}, err
	}
	return models, SaveSnapshot(e.Dir(), models)
}

// Change is a migration that has been planned but not written.
type Change struct {
	Plan
	Name     string
	Renames  []Rename
	Snapshot Schema
	Models   Schema
}

// Plan diffs the snapshot with the models, with the renames that the author confirmed.
func (e Env) Plan(snapshot, models Schema, renames []Rename) (Change, error) {
	plan, err := NewPlan(e.Dialect, snapshot, models, renames)
	if err != nil {
		return Change{}, err
	}
	return Change{Plan: plan, Name: SuggestName(plan.Ops), Renames: renames, Snapshot: snapshot, Models: models}, nil
}

var migrationName = regexp.MustCompile(`^[a-z0-9_]+$`)

// Write puts the files of a migration in the migrations directory and updates the snapshot, so
// that the next change is the difference with this one.
func (e Env) Write(c Change, name string, now time.Time) (Written, error) {
	if name == "" {
		name = c.Name
	}
	if !migrationName.MatchString(name) {
		return Written{}, fmt.Errorf("%q is not a valid migration name; use lower case letters, digits and underscores", name)
	}
	version, err := NextVersion(e.Dir(), now)
	if err != nil {
		return Written{}, err
	}
	written, err := WriteMigration(e.Dir(), version, name, c.Up, c.Down)
	if err != nil {
		return Written{}, err
	}
	return written, SaveSnapshot(e.Dir(), c.Models)
}

// WithTable returns the snapshot with one table of the models added, for a command that makes the
// table of one model and leaves other changes of the models to the author.
func WithTable(snapshot, models Schema, name string) (Schema, error) {
	t, ok := models.table(name)
	if !ok {
		return Schema{}, fmt.Errorf("the models have no table %s", name)
	}
	if _, exists := snapshot.table(name); exists {
		return Schema{}, fmt.Errorf("the table %s exists already", name)
	}
	next := Schema{Version: snapshot.Version, Tables: append(append([]Table(nil), snapshot.Tables...), t)}
	next.normalize()
	return next, nil
}

// ParseRenames reads renames as people write them on a command line: a table as users=people and
// a column as users.name=full_name.
func ParseRenames(values []string) ([]Rename, error) {
	var renames []Rename
	for _, v := range values {
		from, to, ok := strings.Cut(v, "=")
		if !ok || from == "" || to == "" {
			return nil, fmt.Errorf("the rename %q is not like users=people or users.name=full_name", v)
		}
		table, column, isColumn := strings.Cut(from, ".")
		if isColumn {
			renames = append(renames, Rename{Table: table, From: column, To: to})
		} else {
			renames = append(renames, Rename{From: from, To: to})
		}
	}
	return renames, nil
}

// CheckRenames reports a rename that does not match the schemas.
func CheckRenames(from, to Schema, renames []Rename) error {
	for _, r := range renames {
		if r.Table == "" {
			if _, ok := from.table(r.From); !ok {
				return fmt.Errorf("cannot rename the table %s, which the migrations do not have", r.From)
			}
			if _, ok := to.table(r.To); !ok {
				return fmt.Errorf("cannot rename %s to %s, which the models do not have", r.From, r.To)
			}
			continue
		}
		old, ok := from.table(r.Table)
		if !ok {
			return fmt.Errorf("cannot rename the column %s.%s, because the migrations have no table %s", r.Table, r.From, r.Table)
		}
		if _, ok := old.column(r.From); !ok {
			return fmt.Errorf("cannot rename the column %s.%s, which the migrations do not have", r.Table, r.From)
		}
		newTable, ok := to.table(renamedTable(renames, r.Table))
		if !ok {
			return errors.New("cannot rename a column of a table that the models do not have")
		}
		if _, ok := newTable.column(r.To); !ok {
			return fmt.Errorf("cannot rename %s.%s to %s, which the models do not have", r.Table, r.From, r.To)
		}
	}
	return nil
}
