package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/project"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/schema"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
	"github.com/yaboylino/gonestly/config"
	"github.com/yaboylino/gonestly/sql"
)

var migrationName = regexp.MustCompile(`^[a-z0-9_]+$`)

type migrateOptions struct {
	dsn    string
	driver string
	dir    string
}

func migrateCommand(printer *term.Printer) *cobra.Command {
	var opts migrateOptions
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply, revert, list and create migrations",
		Long: `Manage the SQL migrations in the migrations directory. The database is read from
DATABASE_URL in the environment or the .env file, unless --dsn is given.`,
	}
	cmd.PersistentFlags().StringVar(&opts.dsn, "dsn", "", "the connection string (default: DATABASE_URL)")
	cmd.PersistentFlags().StringVar(&opts.driver, "driver", "", "pgx, mysql or sqlite (default: inferred from the connection string)")
	cmd.PersistentFlags().StringVar(&opts.dir, "dir", "migrations", "the migrations directory")

	cmd.AddCommand(
		migrateDevCommand(printer, &opts),
		diffCommand(printer, &opts),
		baselineCommand(printer),
		&cobra.Command{
			Use:   "up",
			Short: "Apply every pending migration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return withMigrator(cmd.Context(), opts, func(m *sql.Migrator) error {
					applied, err := m.Up(cmd.Context())
					for _, a := range applied {
						printer.Success("applied %d_%s", a.Version, a.Name)
					}
					if err == nil && len(applied) == 0 {
						printer.Info("the database is up to date")
					}
					return err
				})
			},
		},
		&cobra.Command{
			Use:   "down",
			Short: "Revert the latest applied migration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return withMigrator(cmd.Context(), opts, func(m *sql.Migrator) error {
					reverted, err := m.Down(cmd.Context())
					switch {
					case err != nil:
						return err
					case reverted == nil:
						printer.Info("no migration is applied")
					default:
						printer.Success("reverted %d_%s", reverted.Version, reverted.Name)
					}
					return nil
				})
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "List every migration and whether it is applied",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return withMigrator(cmd.Context(), opts, func(m *sql.Migrator) error {
					statuses, err := m.Status(cmd.Context())
					if err != nil {
						return err
					}
					tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
					fmt.Fprintln(tw, "VERSION\tNAME\tAPPLIED")
					for _, s := range statuses {
						applied := "pending"
						if s.Applied {
							applied = s.AppliedAt.Local().Format("2006-01-02 15:04")
						}
						fmt.Fprintf(tw, "%d\t%s\t%s\n", s.Version, s.Name, applied)
					}
					return tw.Flush()
				})
			},
		},
		&cobra.Command{
			Use:     "create <name>",
			Short:   "Create an empty up and down migration",
			Example: `  gonestly migrate create add_user_email`,
			Args:    cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				p, err := project.Find(".")
				if err != nil {
					return err
				}
				return createMigration(filepath.Join(p.Root, opts.dir), args[0], time.Now(), printer, p)
			},
		},
	)
	return cmd
}

func withMigrator(ctx context.Context, opts migrateOptions, fn func(*sql.Migrator) error) error {
	p, err := project.Find(".")
	if err != nil {
		return err
	}
	dsn := opts.dsn
	if dsn == "" {
		type settings struct {
			DatabaseURL string `env:"DATABASE_URL"`
		}
		s, err := config.Load[settings](config.Options{Files: []string{filepath.Join(p.Root, ".env")}})
		if err != nil {
			return err
		}
		dsn = s.DatabaseURL
	}
	if dsn == "" {
		return errors.New("no database: set DATABASE_URL in the environment or the .env file, or pass --dsn")
	}
	if strings.HasPrefix(dsn, "mysql://") {
		return errors.New("a MySQL connection string is not a URL; write it as user:password@tcp(host:3306)/database")
	}
	driver := cmp.Or(opts.driver, driverFor(dsn))
	if driver == "" {
		return errors.New("cannot tell the driver from the connection string; pass --driver pgx, mysql or sqlite")
	}
	db, err := sql.Open(ctx, sql.Options{Driver: driver, DSN: dsn})
	if err != nil {
		return err
	}
	defer db.Close()
	migrator, err := sql.NewMigrator(db, os.DirFS(filepath.Join(p.Root, opts.dir)))
	if err != nil {
		return err
	}
	return fn(migrator)
}

func driverFor(dsn string) string {
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"), strings.Contains(dsn, "host="):
		return "pgx"
	case strings.Contains(dsn, "@tcp("), strings.Contains(dsn, "@unix("):
		return "mysql"
	case strings.HasPrefix(dsn, "file:"), strings.HasPrefix(dsn, "sqlite:"), strings.HasSuffix(dsn, ".db"), strings.Contains(dsn, ".db?"):
		return "sqlite"
	default:
		return ""
	}
}

// createMigration writes an empty pair, numbered like the existing migrations.
func createMigration(dir, name string, now time.Time, printer *term.Printer, p *project.Project) error {
	if !migrationName.MatchString(name) {
		return fmt.Errorf("%q is not a valid migration name; use lower case letters, digits and underscores", name)
	}
	version, err := schema.NextVersion(dir, now)
	if err != nil {
		return err
	}
	for _, direction := range []string{"up", "down"} {
		path := filepath.Join(dir, fmt.Sprintf("%s_%s.%s.sql", version, name, direction))
		content := fmt.Sprintf("-- Write the SQL that %s %s here.\n", map[string]string{"up": "applies", "down": "reverts"}[direction], name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // migrations are meant to be readable
			return err
		}
		printer.Created(p.Rel(path))
	}
	return nil
}
