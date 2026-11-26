package sql

import (
	"context"
	stdsql "database/sql"
	"errors"
	"reflect"
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
	// migrationGuard is a statement that makes a migration wait for the one another process is
	// running, for databases without a lock that lockMigrations can take.
	migrationGuard() string
	isUniqueViolation(err error) bool
	// readOnlySession returns the statements that switch a connection to read only and back,
	// for databases whose driver does not make a read only transaction read only by itself.
	readOnlySession() (on, off string)
	// statements splits a migration script into what must be sent to the database one by one.
	statements(script string, transaction bool) []string
	// returning reports whether INSERT and UPDATE can return the stored row.
	returning() bool
	// emptyInsert is the tail of an INSERT that leaves every column to its default.
	emptyInsert() string
	// noLimit is the LIMIT that an OFFSET without a limit needs, if any.
	noLimit() string
	// timestamp is the column type for a moment in time with sub-second precision.
	timestamp() string
}

var (
	// Postgres is the dialect of PostgreSQL.
	Postgres Dialect = postgresDialect{}
	// SQLite is the dialect of SQLite.
	SQLite Dialect = sqliteDialect{}
	// MySQL is the dialect of MySQL and MariaDB.
	MySQL Dialect = mysqlDialect{}
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

func (postgresDialect) migrationGuard() string { return "" }

// The drivers are recognized by their SQLSTATE method, so none of them has to be imported.
func (postgresDialect) isUniqueViolation(err error) bool {
	var state interface{ SQLState() string }
	return errors.As(err, &state) && state.SQLState() == "23505"
}

func (postgresDialect) readOnlySession() (on, off string) { return "", "" }

// PostgreSQL runs several statements in one message as a single transaction, so outside a
// transaction they are sent one by one.
func (d postgresDialect) statements(script string, transaction bool) []string {
	if transaction {
		return []string{script}
	}
	return splitStatements(d, script)
}

func (postgresDialect) returning() bool     { return true }
func (postgresDialect) emptyInsert() string { return " DEFAULT VALUES" }
func (postgresDialect) noLimit() string     { return "" }
func (postgresDialect) timestamp() string   { return "TIMESTAMP" }

type sqliteDialect struct{}

func (sqliteDialect) Name() string           { return "sqlite" }
func (sqliteDialect) Placeholder(int) string { return "?" }
func (sqliteDialect) Quote(id string) string { return quoteIdentifier(id) }

// SQLite has no lock that outlives a transaction; migrationGuard serializes the migrations.
func (sqliteDialect) lockMigrations(context.Context, *stdsql.Conn) (func(), error) {
	return func() {}, nil
}

// A write that changes nothing still takes the write lock of SQLite until the transaction ends.
func (sqliteDialect) migrationGuard() string {
	return "UPDATE " + migrationsTable + " SET version = version WHERE 1 = 0"
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

func (sqliteDialect) statements(script string, _ bool) []string { return []string{script} }

func (sqliteDialect) returning() bool     { return true }
func (sqliteDialect) emptyInsert() string { return " DEFAULT VALUES" }

// SQLite only accepts OFFSET after a LIMIT; -1 means no limit.
func (sqliteDialect) noLimit() string   { return "-1" }
func (sqliteDialect) timestamp() string { return "TIMESTAMP" }

type mysqlDialect struct{}

func (mysqlDialect) Name() string           { return "mysql" }
func (mysqlDialect) Placeholder(int) string { return "?" }

func (mysqlDialect) Quote(id string) string {
	parts := strings.Split(id, ".")
	for i, part := range parts {
		parts[i] = "`" + strings.ReplaceAll(part, "`", "``") + "`"
	}
	return strings.Join(parts, ".")
}

// mysqlMigrationLock names the lock after the database, because MySQL locks span the server.
const mysqlMigrationLock = "CONCAT('gonestly_migrations.', MD5(IFNULL(DATABASE(), '')))"

// The lock is asked for in short waits, because MariaDB has no timeout that means forever.
func (mysqlDialect) lockMigrations(ctx context.Context, conn *stdsql.Conn) (func(), error) {
	for {
		var locked stdsql.NullInt64
		if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK("+mysqlMigrationLock+", 5)").Scan(&locked); err != nil {
			return nil, err
		}
		if locked.Int64 == 1 {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "DO RELEASE_LOCK("+mysqlMigrationLock+")")
	}, nil
}

func (mysqlDialect) migrationGuard() string { return "" }

const (
	mysqlDuplicateEntry        = 1062
	mysqlDuplicateEntryWithKey = 1586
)

// The driver exposes the error number as a field, so it is read without importing the driver.
func (mysqlDialect) isUniqueViolation(err error) bool {
	number, ok := errorNumber(err)
	if !ok {
		return strings.Contains(err.Error(), "Error 1062 ")
	}
	return number == mysqlDuplicateEntry || number == mysqlDuplicateEntryWithKey
}

// errorNumber finds the Number field of a driver error anywhere in the chain of err.
func errorNumber(err error) (uint64, bool) {
	for err != nil {
		v := reflect.Indirect(reflect.ValueOf(err))
		if v.Kind() == reflect.Struct {
			if number := v.FieldByName("Number"); number.IsValid() && number.CanUint() {
				return number.Uint(), true
			}
		}
		switch e := err.(type) { //nolint:errorlint // the chain is walked by hand to inspect every error
		case interface{ Unwrap() error }:
			err = e.Unwrap()
		case interface{ Unwrap() []error }:
			for _, inner := range e.Unwrap() {
				if number, ok := errorNumber(inner); ok {
					return number, true
				}
			}
			return 0, false
		default:
			return 0, false
		}
	}
	return 0, false
}

func (mysqlDialect) readOnlySession() (on, off string) { return "", "" }

// The driver sends one statement at a time unless the connection string allows more.
func (d mysqlDialect) statements(script string, _ bool) []string { return splitStatements(d, script) }

func (mysqlDialect) returning() bool     { return false }
func (mysqlDialect) emptyInsert() string { return " () VALUES ()" }

// MySQL only accepts OFFSET after a LIMIT; this is the largest one.
func (mysqlDialect) noLimit() string   { return "18446744073709551615" }
func (mysqlDialect) timestamp() string { return "DATETIME(6)" }

// mysqlDSN adds parseTime=true to a connection string that does not set it, because without
// it the driver cannot scan a date or time column into a time.Time.
func mysqlDSN(dsn string) string {
	slash := strings.LastIndexByte(dsn, '/')
	if slash < 0 {
		return dsn
	}
	query := strings.IndexByte(dsn[slash:], '?')
	switch {
	case query < 0:
		return dsn + "?parseTime=true"
	case strings.Contains(dsn[slash+query:], "parseTime="):
		return dsn
	default:
		return dsn + "&parseTime=true"
	}
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
	case "mysql":
		return MySQL
	default:
		return nil
	}
}
