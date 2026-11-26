package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/dbsetup"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/project"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/prompt"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/scaffold"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
)

// passwordEnv holds the password for a non-interactive run, so that it stays out of the
// process list and the shell history.
const passwordEnv = "GONESTLY_DB_PASSWORD" //nolint:gosec // the name of a variable, not a credential

// passwordFromEnv marks, in the set of given details, a password that came from the environment
// instead of a flag; a SQLite database simply has no use for it.
const passwordFromEnv = "password from the environment"

func addCommand(printer *term.Printer, stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add something to an existing project",
	}
	cmd.AddCommand(databaseCommand(printer, stdout))
	return cmd
}

func databaseCommand(printer *term.Printer, stdout io.Writer) *cobra.Command {
	var cfg dbsetup.Config
	var skipCreate bool
	cmd := &cobra.Command{
		Use:     "database [sqlite|mysql|postgres]",
		Aliases: []string{"db"},
		Short:   "Create a database and connect the project to it",
		Long: `Add a database to a project: ask for the type and the connection details, create the
database on its server, and wire it into the project. The connection string goes into .env,
the sql module into the root module, and the project gets a driver and a migrations
directory.

In a terminal the missing details are asked for, the password without showing it. Without a
terminal every detail comes from a flag, and the password from ` + passwordEnv + ` or --password.`,
		Example: `  gonestly add database
  gonestly add database postgres --host db.internal --user app --name shop
  GONESTLY_DB_PASSWORD=secret gonestly add database mysql --user root --name shop`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Find(".")
			if err != nil {
				return err
			}
			if len(args) == 1 {
				cfg.Kind = strings.ToLower(args[0])
			}
			given := map[string]bool{}
			cmd.Flags().Visit(func(f *pflag.Flag) { given[f.Name] = true })
			if !given["password"] {
				if password, ok := os.LookupEnv(passwordEnv); ok {
					cfg.Password, given["password"], given[passwordFromEnv] = password, true, true
				}
			}
			var asker *prompt.Prompter
			if prompt.Interactive(cmd.InOrStdin()) {
				asker = prompt.New(cmd.InOrStdin(), stdout)
			}
			if err := completeConfig(&cfg, given, asker, defaultName(p)); err != nil {
				return err
			}
			if err := scaffold.AddDatabase(cmd.Context(), scaffold.AddDatabaseOptions{Project: p, Config: cfg, SkipCreate: skipCreate}, printer); err != nil {
				return err
			}
			printer.Success("added %s to the project", cfg.Kind)
			printer.Plain("\n  gonestly generate resource products\n  gonestly dev\n\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&cfg.Host, "host", "", "the host of the database server (default: localhost)")
	cmd.Flags().IntVar(&cfg.Port, "port", 0, "the port of the database server (default: the port of the database)")
	cmd.Flags().StringVar(&cfg.User, "user", "", "the user to connect as")
	cmd.Flags().StringVar(&cfg.Password, "password", "", "the password; prefer "+passwordEnv+" or the prompt")
	cmd.Flags().StringVar(&cfg.SSL, "ssl", "", "PostgreSQL sslmode or MySQL tls (default: off for this machine, on for other hosts)")
	cmd.Flags().StringVar(&cfg.Name, "name", "", "the name of the database, or the file of a SQLite database")
	cmd.Flags().BoolVar(&skipCreate, "skip-create", false, "only connect the project; the database exists already or is made another way")
	return cmd
}

// defaultName is the name of the database that fits the project.
func defaultName(p *project.Project) string {
	parts := strings.Split(p.Module, "/")
	last := parts[len(parts)-1]
	// A major version suffix is not the name of the project.
	if len(parts) > 1 && majorVersion.MatchString(last) {
		last = parts[len(parts)-2]
	}
	name := strings.ToLower(invalidNameChars.ReplaceAllString(last, "_"))
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = "app_" + name
	}
	return name
}

var (
	majorVersion     = regexp.MustCompile(`^v[0-9]+$`)
	invalidNameChars = regexp.MustCompile(`[^A-Za-z0-9_]`)
)

var defaultUsers = map[string]string{"mysql": "root", "postgres": "postgres"}

// completeConfig fills in what the flags left out: it asks when it can, and otherwise takes the
// default, or fails when a detail has none.
func completeConfig(cfg *dbsetup.Config, given map[string]bool, ask *prompt.Prompter, project string) error {
	var err error
	if cfg.Kind == "" {
		if ask == nil {
			return fmt.Errorf("say which database to add: %s", strings.Join(dbsetup.Kinds, ", "))
		}
		if cfg.Kind, err = ask.Choose("Which database do you want to add?", dbsetup.Kinds, "sqlite"); err != nil {
			return err
		}
	}
	if !slices.Contains(dbsetup.Kinds, cfg.Kind) {
		return fmt.Errorf("unknown database %q; use %s", cfg.Kind, strings.Join(dbsetup.Kinds, ", "))
	}

	if cfg.Kind == "sqlite" {
		if given["password"] && given[passwordFromEnv] {
			cfg.Password = ""
			given["password"] = false
		}
		if given["host"] || given["port"] || given["user"] || given["password"] || given["ssl"] {
			return errors.New("a SQLite database is a file; it has no host, port, user, password or TLS setting")
		}
		if !given["name"] {
			cfg.Name = project + ".db"
			if ask != nil {
				if cfg.Name, err = ask.Ask("File of the database", cfg.Name); err != nil {
					return err
				}
			}
		}
		return cfg.Validate()
	}

	if !given["host"] {
		cfg.Host = "localhost"
		if ask != nil {
			if cfg.Host, err = ask.Ask("Host", cfg.Host); err != nil {
				return err
			}
		}
	}
	if !given["port"] {
		cfg.Port = dbsetup.DefaultPort(cfg.Kind)
		if ask != nil {
			if cfg.Port, err = ask.AskInt("Port", cfg.Port); err != nil {
				return err
			}
		}
	}
	if !given["user"] {
		cfg.User = defaultUsers[cfg.Kind]
		if ask != nil {
			if cfg.User, err = ask.Ask("User", cfg.User); err != nil {
				return err
			}
		}
	}
	if !given["password"] {
		if ask == nil {
			return fmt.Errorf("give the password with the environment variable %s or --password; use --password= for none", passwordEnv)
		}
		if cfg.Password, err = ask.Password("Password"); err != nil {
			return err
		}
	}
	if !given["name"] {
		cfg.Name = project
		if ask != nil {
			if cfg.Name, err = ask.Ask("Name of the database", cfg.Name); err != nil {
				return err
			}
		}
	}
	return cfg.Validate()
}
