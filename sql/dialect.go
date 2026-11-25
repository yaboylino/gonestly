package sql

import (
	"context"
	stdsql "database/sql"
	"strconv"
	"strings"
)

// Dialect describes the SQL differences between databases.
type Dialect interface {
	// Name identifies the dialect, such as "postgres".
	Name() string
	// Placeholder returns the placeholder for the nth argument, counting from 1.
	Placeholder(n int) string
	// Quote quotes an identifier such as a table or column name.
	Quote(identifier string) string

	lockMigrations(ctx context.Context, conn *stdsql.Conn) (unlock func(), err error)
}

var (
	// Postgres is the dialect of PostgreSQL.
	Postgres Dialect = postgresDialect{}
	// SQLite is the dialect of SQLite.
	SQLite Dialect = sqliteDialect{}
)

// migrationLockID is an arbitrary constant that identifies the migration lock in PostgreSQL.
const migrationLockID = 7464327061

type postgresDialect struct{}

func (postgresDialect) Name() string             { return "postgres" }
func (postgresDialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (postgresDialect) Quote(id string) string   { return quoteIdentifier(id) }

func (postgresDialect) lockMigrations(ctx context.Context, conn *stdsql.Conn) (func(), error) {
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return nil, err
	}
	return func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID)
	}, nil
}

type sqliteDialect struct{}

func (sqliteDialect) Name() string           { return "sqlite" }
func (sqliteDialect) Placeholder(int) string { return "?" }
func (sqliteDialect) Quote(id string) string { return quoteIdentifier(id) }

// SQLite allows one writer at a time, so the migration transactions already serialize.
func (sqliteDialect) lockMigrations(context.Context, *stdsql.Conn) (func(), error) {
	return func() {}, nil
}

// quoteIdentifier quotes each part of a possibly qualified name, such as users.email.
func quoteIdentifier(id string) string {
	parts := strings.Split(id, ".")
	for i, part := range parts {
		parts[i] = `"` + strings.ReplaceAll(part, `"`, `""`) + `"`
	}
	return strings.Join(parts, ".")
}

// dialectFor infers the dialect from the name of a database/sql driver.
func dialectFor(driver string) Dialect {
	switch driver {
	case "pgx", "pgx/v5", "postgres", "postgresql":
		return Postgres
	case "sqlite", "sqlite3":
		return SQLite
	default:
		return nil
	}
}
