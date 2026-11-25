package sql

import (
	"context"
	stdsql "database/sql"
	"errors"
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
	isUniqueViolation(err error) bool
	// readOnlySession returns the statements that switch a connection to read only and back,
	// for databases whose driver does not make a read only transaction read only by itself.
	readOnlySession() (on, off string)
	// statements splits a script into what must be sent one by one to run outside a transaction.
	statements(script string) []string
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

// The drivers are recognized by their SQLSTATE method, so none of them has to be imported.
func (postgresDialect) isUniqueViolation(err error) bool {
	var state interface{ SQLState() string }
	return errors.As(err, &state) && state.SQLState() == "23505"
}

func (postgresDialect) readOnlySession() (on, off string) { return "", "" }

// PostgreSQL runs several statements in one message as a single transaction.
func (d postgresDialect) statements(script string) []string { return splitStatements(d, script) }

type sqliteDialect struct{}

func (sqliteDialect) Name() string           { return "sqlite" }
func (sqliteDialect) Placeholder(int) string { return "?" }
func (sqliteDialect) Quote(id string) string { return quoteIdentifier(id) }

// SQLite allows one writer at a time, so the migration transactions already serialize.
func (sqliteDialect) lockMigrations(context.Context, *stdsql.Conn) (func(), error) {
	return func() {}, nil
}

const (
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
)

func (sqliteDialect) isUniqueViolation(err error) bool {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code() == sqliteConstraintUnique || coded.Code() == sqliteConstraintPrimaryKey
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (sqliteDialect) readOnlySession() (on, off string) {
	return "PRAGMA query_only = ON", "PRAGMA query_only = OFF"
}

func (sqliteDialect) statements(script string) []string { return []string{script} }

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
