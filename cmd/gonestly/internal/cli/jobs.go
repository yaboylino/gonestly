package cli

import (
	"github.com/spf13/cobra"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/project"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/scaffold"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
)

func jobsCommand(printer *term.Printer) *cobra.Command {
	return &cobra.Command{
		Use:   "jobs",
		Short: "Add background jobs and cron to the project",
		Long: `Add background jobs to a project that has a database: a migration that creates the
gonestly_jobs table, and the jobs module in the root module. Run gonestly migrate up afterwards;
the migrations also run when the application starts.`,
		Example: `  gonestly add jobs`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := project.Find(".")
			if err != nil {
				return err
			}
			if err := scaffold.AddJobs(cmd.Context(), scaffold.AddJobsOptions{Project: p}, printer); err != nil {
				return err
			}
			printer.Success("added jobs to the project")
			printer.Plain("\n  Define a job in the constructor of a service with jobs.Define(queue, ...)\n  and enqueue it with job.Enqueue(ctx, payload).\n\n")
			return nil
		},
	}
}
