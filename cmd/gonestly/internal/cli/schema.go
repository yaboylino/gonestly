package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/project"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/prompt"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/scaffold"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/schema"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
	"github.com/yaboylino/gonestly/sql"
)

type devOptions struct {
	migrateOptions
	name           string
	createOnly     bool
	acceptDataLoss bool
	renames        []string
}

func (o *devOptions) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&o.renames, "rename", nil, "a rename, as users=people for a table or users.name=full_name for a column")
}

func migrateDevCommand(printer *term.Printer, opts *migrateOptions) *cobra.Command {
	dev := devOptions{migrateOptions: *opts}
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Write a migration for the changes in your models and apply it",
		Long: `Compare the models in your code with the schema that the migrations made, write the
SQL that closes the difference as a new migration, and apply it. The structs with db tags are
the schema: add a field, a table, an index or a relation, run this command, and the migration
is written for your database, with the SQL to undo it.

A field that disappeared while another of the same type appeared may be a rename; the command
asks, so that the data is kept. A change that can lose data, such as dropping a column, has to be
accepted. The schema is remembered in migrations/` + schema.SnapshotFile + `, which belongs in git.`,
		Example: `  gonestly migrate dev
  gonestly migrate dev --name add_track_slug
  gonestly migrate dev --create-only --rename tracks.name=title`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dev.migrateOptions = *opts
			return runDev(cmd, printer, &dev, false)
		},
	}
	cmd.Flags().StringVar(&dev.name, "name", "", "the name of the migration (default: made from the changes)")
	cmd.Flags().BoolVar(&dev.createOnly, "create-only", false, "write the migration without applying it")
	cmd.Flags().BoolVar(&dev.acceptDataLoss, "accept-data-loss", false, "allow changes that can lose data")
	dev.addFlags(cmd)
	return cmd
}

func diffCommand(printer *term.Printer, opts *migrateOptions) *cobra.Command {
	dev := devOptions{migrateOptions: *opts}
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show the SQL for the changes in your models, without writing it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dev.migrateOptions = *opts
			return runDev(cmd, printer, &dev, true)
		},
	}
	dev.addFlags(cmd)
	return cmd
}

func baselineCommand(printer *term.Printer) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "baseline",
		Short: "Record your models as the schema of migrations that you wrote by hand",
		Long: `Adopt a project whose migrations were written by hand: record the models as the schema
that those migrations made, without writing a migration. Only do this when the database matches the
models, because every change from now on is the difference with them.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			env, err := schemaEnv()
			if err != nil {
				return err
			}
			if _, err := env.Snapshot(); err == nil && !force {
				return errors.New("the migrations have a schema snapshot already; use gonestly migrate dev, or --force to record the models again")
			}
			models, err := env.Baseline()
			if err != nil {
				return err
			}
			printer.Success("recorded %d tables as the schema of the migrations", len(models.Tables))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "record the models even when a snapshot exists")
	return cmd
}

func schemaEnv() (schema.Env, error) {
	p, err := project.Find(".")
	if err != nil {
		return schema.Env{}, err
	}
	database, err := scaffold.DetectDatabase(p)
	if err != nil {
		return schema.Env{}, errors.New("the project has no database; add one with gonestly add database")
	}
	return schema.Env{Root: p.Root, Module: p.Module, Dialect: schema.Dialect(database)}, nil
}

func runDev(cmd *cobra.Command, printer *term.Printer, opts *devOptions, dryRun bool) error {
	env, err := schemaEnv()
	if err != nil {
		return err
	}
	p, err := project.Find(".")
	if err != nil {
		return err
	}
	snapshot, err := env.Snapshot()
	if errors.Is(err, schema.ErrNoSnapshot) {
		return errors.New("the migrations were written by hand, so the schema they make is not known; run gonestly migrate baseline when the database matches your models")
	}
	if err != nil {
		return err
	}
	models, err := env.Models()
	if err != nil {
		return err
	}

	var asker *prompt.Prompter
	if prompt.Interactive(cmd.InOrStdin()) {
		asker = prompt.New(cmd.InOrStdin(), cmd.OutOrStdout())
	}
	renames, err := chooseRenames(printer, asker, snapshot, models, opts.renames)
	if err != nil {
		return err
	}
	change, err := env.Plan(snapshot, models, renames)
	if err != nil {
		return err
	}
	if change.Empty() {
		printer.Info("the database is in sync with your models")
		return nil
	}

	if dryRun {
		describe(printer, change)
		warn(printer, change)
		fmt.Fprint(cmd.OutOrStdout(), "\n"+change.Up)
		return nil
	}
	describe(printer, change)
	warn(printer, change)
	if len(change.DataLoss) > 0 && !opts.acceptDataLoss && !opts.createOnly {
		if asker == nil {
			return errors.New("this migration deletes data; read the warnings above and run again with --accept-data-loss")
		}
		ok, err := asker.Confirm("Write the migration anyway?", false)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("no migration was written")
		}
	}
	written, err := env.Write(change, opts.name, time.Now())
	if err != nil {
		return err
	}
	printer.Created(p.Rel(written.Up))
	printer.Created(p.Rel(written.Down))
	if opts.createOnly {
		printer.Info("the migration is not applied yet; run gonestly migrate up")
		return nil
	}
	return withMigrator(cmd.Context(), opts.migrateOptions, func(m *sql.Migrator) error {
		applied, err := m.Up(cmd.Context())
		for _, a := range applied {
			printer.Success("applied %d_%s", a.Version, a.Name)
		}
		return err
	})
}

// describe lists what the migration does, and what the author should know first.
func describe(printer *term.Printer, c schema.Change) {
	printer.Info("%s:", c.Name)
	for _, line := range schema.Describe(c.Ops) {
		printer.Detail("%s", line)
	}
}

func warn(printer *term.Printer, c schema.Change) {
	for _, w := range c.DataLoss {
		printer.Warning("%s", w)
	}
	for _, w := range c.Risks {
		printer.Warning("%s", w)
	}
}

// chooseRenames asks which of the tables and columns that look renamed are, and adds the renames
// from the command line.
func chooseRenames(printer *term.Printer, asker *prompt.Prompter, from, to schema.Schema, flags []string) ([]schema.Rename, error) {
	renames, err := schema.ParseRenames(flags)
	if err != nil {
		return nil, err
	}
	if err := schema.CheckRenames(from, to, renames); err != nil {
		return nil, err
	}
	decided := func(c schema.Candidate) bool {
		for _, r := range renames {
			if r == c.Rename || (r.Table == c.Rename.Table && r.From == c.Rename.From) {
				return true
			}
		}
		return false
	}
	ask := func(candidates []schema.Candidate) error {
		for _, c := range candidates {
			if decided(c) {
				continue
			}
			if asker == nil {
				printer.Warning("%s may be a rename, and is dropped and added instead; pass --rename to keep the data", c)
				continue
			}
			ok, err := asker.Confirm(fmt.Sprintf("Did you rename %s?", c), false)
			if err != nil {
				return err
			}
			if ok {
				renames = append(renames, c.Rename)
			}
		}
		return nil
	}
	var tableRenames []schema.Rename
	for _, r := range renames {
		if r.Table == "" {
			tableRenames = append(tableRenames, r)
		}
	}
	tables := schema.TableCandidates(from, to)
	if err := ask(tables); err != nil {
		return nil, err
	}
	tableRenames = tableRenames[:0]
	for _, r := range renames {
		if r.Table == "" {
			tableRenames = append(tableRenames, r)
		}
	}
	if err := ask(schema.ColumnCandidates(from, to, tableRenames)); err != nil {
		return nil, err
	}
	return renames, nil
}
