package jobs

import (
	"embed"
	"fmt"
)

// Table is the table that holds the jobs.
const Table = "gonestly_jobs"

//go:embed schema/*.sql
var schemas embed.FS

// Migration returns the SQL that creates the jobs table and the SQL that drops it again, for
// "postgres", "mysql" or "sqlite". Put them in a migration pair, or use `gonestly add jobs`.
func Migration(dialect string) (up, down string, err error) {
	upSQL, err := schemas.ReadFile("schema/" + dialect + ".up.sql")
	if err != nil {
		return "", "", fmt.Errorf("jobs: no schema for the dialect %q; use postgres, mysql or sqlite", dialect)
	}
	downSQL, err := schemas.ReadFile("schema/" + dialect + ".down.sql")
	if err != nil {
		return "", "", err
	}
	return string(upSQL), string(downSQL), nil
}
