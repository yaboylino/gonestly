// Package cli defines the commands of the gonestly tool.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/columns"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/devserver"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/project"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/scaffold"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
)

// Execute runs the command line and returns the exit code.
func Execute(args []string, stdout, stderr io.Writer) int {
	// A closed terminal sends SIGHUP; the dev server must still stop the application it runs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	root := newRoot(stdout, stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		term.New(stderr).Failure("%v", err)
		return 1
	}
	return 0
}

func newRoot(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "gonestly",
		Short:         "Create, generate, run and inspect Gonestly applications",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	printer := term.New(stdout)
	root.AddCommand(
		newCommand(printer),
		generateCommand(printer),
		devCommand(printer, stdout, stderr),
		migrateCommand(printer),
		inspectCommand("routes", "List every route of the application", printer, stdout, stderr),
		inspectCommand("openapi", "Print or write the OpenAPI document of the application", printer, stdout, stderr),
		versionCommand(stdout),
	)
	return root
}

func newCommand(printer *term.Printer) *cobra.Command {
	var opts scaffold.ProjectOptions
	cmd := &cobra.Command{
		Use:   "new <directory>",
		Short: "Create a project that runs immediately",
		Example: `  gonestly new shop
  gonestly new shop --module github.com/acme/shop --database postgres`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Dir = args[0]
			if err := scaffold.NewProject(cmd.Context(), opts, printer); err != nil {
				return err
			}
			printer.Success("created %s", opts.Dir)
			printer.Plain("\n  cd %s\n  gonestly dev\n\n", opts.Dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Module, "module", "", "the Go module path (default: the directory name)")
	cmd.Flags().StringVar(&opts.Database, "database", "", "add a database: postgres, mysql or sqlite")
	cmd.Flags().StringVar(&opts.Replace, "replace", "", "use a local checkout of Gonestly instead of the published modules")
	_ = cmd.Flags().MarkHidden("replace")
	return cmd
}

func generateCommand(printer *term.Printer) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:     "generate <kind> <name>",
		Aliases: []string{"g"},
		Short:   "Generate a resource, module, controller, service, guard or interceptor",
		Long: `Generate code and register it: a new module is imported in the root module, and
controllers and services are added to their module.

Kinds:
  resource     a module with a model, migration, service, controller and tests
  module       an empty module
  controller   a controller and its test
  service      a service
  guard        a guard that checks a bearer token
  interceptor  an interceptor that logs every request`,
		Example: `  gonestly generate resource products
  gonestly g controller reports`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			p, err := project.Find(".")
			if err != nil {
				return err
			}
			opts := scaffold.GenerateOptions{Project: p}
			if dir != "" {
				opts.Dir, _ = filepath.Abs(dir)
			}
			if err := scaffold.Generate(args[0], args[1], opts, printer); err != nil {
				return err
			}
			printer.Success("generated %s %s", args[0], args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "the package directory (default: internal/<name>)")
	cmd.AddCommand(&cobra.Command{
		Use:   "columns [directory]",
		Short: "Generate the typed columns of the models in a package",
		Long: `Write columns_gen.go with a <Model>Columns variable for every struct with db tags, for the
query builder of the sql module. Run it with go generate:

  //go:generate gonestly generate columns`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			models, err := columns.Generate(dir)
			if err != nil {
				return err
			}
			if len(models) == 0 {
				printer.Info("no structs with db tags in %s", dir)
				return nil
			}
			printer.Success("generated columns for %s", strings.Join(models, ", "))
			return nil
		},
	})
	return cmd
}

func devCommand(printer *term.Printer, stdout, stderr io.Writer) *cobra.Command {
	var opts devserver.Options
	cmd := &cobra.Command{
		Use:   "dev [-- args]",
		Short: "Run the application and restart it on every change",
		Long: `Watch the project and check it on every save: build it and run go vet, and with --test
also the tests of the changed packages. When everything passes, the application restarts.
When something fails, the application stops and every problem is listed with its file and
line, so a broken change never looks like it works.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Find(".")
			if err != nil {
				return err
			}
			opts.Root, opts.Args = p.Root, args
			opts.Printer, opts.Stdout, opts.Stderr = printer, stdout, stderr
			return devserver.New(opts).Run(cmd.Context())
		},
	}
	cmd.Flags().BoolVar(&opts.Test, "test", false, "also run the tests of the changed packages")
	cmd.Flags().StringVar(&opts.Main, "main", ".", "the main package to run")
	return cmd
}

// inspectCommand runs the application in inspection mode to read its routes or document.
func inspectCommand(mode, short string, printer *term.Printer, stdout, stderr io.Writer) *cobra.Command {
	var main, output string
	cmd := &cobra.Command{
		Use:   mode,
		Short: short,
		Long: short + `.

The application is built and started without listening, with ` + gonestly.InspectEnv + `=` + mode + `,
so providers such as the database connect as usual.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := project.Find(".")
			if err != nil {
				return err
			}
			data, err := inspect(cmd.Context(), p.Root, main, mode, stderr)
			if err != nil {
				return err
			}
			if mode == "routes" {
				return printRoutes(stdout, data)
			}
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, data, "", "  "); err != nil {
				return fmt.Errorf("the application printed something other than JSON: %w", err)
			}
			pretty.WriteByte('\n')
			if output == "" {
				_, err = stdout.Write(pretty.Bytes())
				return err
			}
			if err := os.WriteFile(output, pretty.Bytes(), 0o644); err != nil { //nolint:gosec // the document is meant to be shared
				return err
			}
			printer.Success("wrote %s", output)
			return nil
		},
	}
	cmd.Flags().StringVar(&main, "main", ".", "the main package of the application")
	if mode == "openapi" {
		cmd.Flags().StringVarP(&output, "output", "o", "", "write the document to a file instead of standard output")
	}
	return cmd
}

func inspect(ctx context.Context, root, main, mode string, stderr io.Writer) ([]byte, error) {
	// The result goes to a file, so that whatever the application prints cannot corrupt it.
	file, err := os.CreateTemp("", "gonestly-inspect-*.json")
	if err != nil {
		return nil, err
	}
	_ = file.Close()
	defer os.Remove(file.Name())

	cmd := exec.CommandContext(ctx, "go", "run", "./"+strings.TrimPrefix(main, "./"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), gonestly.InspectEnv+"="+mode, gonestly.InspectOutputEnv+"="+file.Name())
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		_, _ = stderr.Write(errOut.Bytes())
		return nil, fmt.Errorf("running the application: %w", err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("the application reported nothing; does main call app.Run?")
	}
	return data, nil
}

func printRoutes(w io.Writer, data []byte) error {
	var routes []gonestly.RouteInfo
	if err := json.Unmarshal(data, &routes); err != nil {
		return fmt.Errorf("the application printed something other than routes: %w", err)
	}
	if len(routes) == 0 {
		_, err := fmt.Fprintln(w, "The application has no routes.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "METHOD\tPATH\tCONTROLLER")
	for _, r := range routes {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Method, r.Path, r.Controller)
	}
	return tw.Flush()
}

// version is set by the linker for released binaries; go install records it in the build info.
var version string

func toolVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

func versionCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version of the gonestly tool",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintln(stdout, "gonestly", toolVersion())
		},
	}
}
