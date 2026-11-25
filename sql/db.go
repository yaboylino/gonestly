package sql

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"
)

// Options configures a database connection.
type Options struct {
	// Driver is the name of a registered database/sql driver, such as "pgx" or "sqlite".
	// Import the driver package, for example _ "github.com/jackc/pgx/v5/stdlib".
	Driver string
	// DSN is the connection string for the driver.
	DSN string
	// Dialect is inferred from Driver for the drivers above; set it for other driver names.
	Dialect Dialect
	// MaxOpenConns, MaxIdleConns, ConnMaxLifetime and ConnMaxIdleTime tune the connection pool;
	// zero keeps the database/sql defaults.
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	// Migrations holds migration files that Module applies when the application starts.
	Migrations fs.FS
	// Logger receives every query at debug level; Module uses the application's logger.
	Logger *slog.Logger
}

// DB is a database connection pool with a dialect. It is safe for concurrent use.
type DB struct {
	std     *stdsql.DB
	dialect Dialect
	logger  *slog.Logger
}

// Open connects to the database and checks the connection.
func Open(ctx context.Context, opts Options) (*DB, error) {
	dialect := opts.Dialect
	if dialect == nil {
		dialect = dialectFor(opts.Driver)
	}
	if dialect == nil {
		return nil, fmt.Errorf("sql: unknown dialect for the driver %q; set Options.Dialect", opts.Driver)
	}
	std, err := stdsql.Open(opts.Driver, opts.DSN)
	if err != nil {
		if strings.Contains(err.Error(), "unknown driver") {
			return nil, fmt.Errorf("sql: the driver %q is not registered; import it, for example _ \"github.com/jackc/pgx/v5/stdlib\" for pgx or _ \"modernc.org/sqlite\" for sqlite: %w", opts.Driver, err)
		}
		return nil, fmt.Errorf("sql: opening the database: %w", err)
	}
	if opts.MaxOpenConns != 0 {
		std.SetMaxOpenConns(opts.MaxOpenConns)
	}
	if opts.MaxIdleConns != 0 {
		std.SetMaxIdleConns(opts.MaxIdleConns)
	}
	if opts.ConnMaxLifetime != 0 {
		std.SetConnMaxLifetime(opts.ConnMaxLifetime)
	}
	if opts.ConnMaxIdleTime != 0 {
		std.SetConnMaxIdleTime(opts.ConnMaxIdleTime)
	}
	if err := std.PingContext(ctx); err != nil {
		_ = std.Close()
		return nil, fmt.Errorf("sql: connecting to the database: %w", err)
	}
	return &DB{std: std, dialect: dialect, logger: opts.Logger}, nil
}

// New wraps an existing connection pool, for example one opened by another library.
func New(std *stdsql.DB, dialect Dialect) *DB {
	return &DB{std: std, dialect: dialect}
}

// Std returns the underlying *sql.DB from database/sql.
func (db *DB) Std() *stdsql.DB { return db.std }

// Dialect returns the dialect of the database.
func (db *DB) Dialect() Dialect { return db.dialect }

// Close closes the connection pool.
func (db *DB) Close() error { return db.std.Close() }

// OnStop closes the connection pool when the application stops.
func (db *DB) OnStop(context.Context) error { return db.Close() }

// Exec runs a statement, such as an insert or update, and returns its result.
func (db *DB) Exec(ctx context.Context, query string, args ...any) (stdsql.Result, error) {
	query, args, err := rebind(db.dialect, query, args)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	result, err := db.conn(ctx).ExecContext(ctx, query, args...)
	db.log(ctx, query, start, err)
	return result, db.wrap(err)
}

func (db *DB) query(ctx context.Context, query string, args []any) (*stdsql.Rows, error) {
	query, args, err := rebind(db.dialect, query, args)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	rows, err := db.conn(ctx).QueryContext(ctx, query, args...)
	db.log(ctx, query, start, err)
	return rows, db.wrap(err)
}

func (db *DB) log(ctx context.Context, query string, start time.Time, err error) {
	if db.logger == nil || !db.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	db.logger.DebugContext(ctx, "sql query", slog.String("query", query), slog.Duration("duration", time.Since(start)), slog.Any("error", err))
}

type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (stdsql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*stdsql.Rows, error)
}

type txKey struct{}

type txState struct {
	db    *DB
	tx    *stdsql.Tx
	depth int
}

type connKey struct{}

// pinnedConn is one connection that every query on its context uses, instead of the pool.
type pinnedConn struct {
	db   *DB
	conn *stdsql.Conn
}

// pin makes the queries on the returned context use conn, so that work which must stay on
// one connection, such as migrating under a session lock, needs no second one from the pool.
func (db *DB) pin(ctx context.Context, conn *stdsql.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, &pinnedConn{db: db, conn: conn})
}

func (db *DB) pinned(ctx context.Context) *stdsql.Conn {
	if p, ok := ctx.Value(connKey{}).(*pinnedConn); ok && p.db == db {
		return p.conn
	}
	return nil
}

// conn returns the transaction in ctx when it belongs to this database, then the pinned
// connection, then the pool.
func (db *DB) conn(ctx context.Context) executor {
	if state, ok := ctx.Value(txKey{}).(*txState); ok && state.db == db {
		return state.tx
	}
	if conn := db.pinned(ctx); conn != nil {
		return conn
	}
	return db.std
}

// begin starts a transaction and returns what to call once it has ended.
func (db *DB) begin(ctx context.Context, opts *stdsql.TxOptions) (*stdsql.Tx, func(), error) {
	conn := db.pinned(ctx)
	on, off := db.dialect.readOnlySession()
	if opts == nil || !opts.ReadOnly || on == "" {
		if conn != nil {
			tx, err := conn.BeginTx(ctx, opts)
			return tx, func() {}, err
		}
		tx, err := db.std.BeginTx(ctx, opts)
		return tx, func() {}, err
	}

	// The setting belongs to the connection, so the transaction keeps one and resets it after.
	release := func() {}
	if conn == nil {
		var err error
		if conn, err = db.std.Conn(ctx); err != nil {
			return nil, nil, err
		}
		release = func() { _ = conn.Close() }
	}
	if _, err := conn.ExecContext(ctx, on); err != nil {
		release()
		return nil, nil, err
	}
	end := func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), off)
		release()
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		end()
		return nil, nil, err
	}
	return tx, end, nil
}

// TxOption changes how Transaction begins a transaction.
type TxOption func(*stdsql.TxOptions)

// The isolation levels for Isolation; which ones exist depends on the database.
const (
	LevelReadCommitted  = stdsql.LevelReadCommitted
	LevelRepeatableRead = stdsql.LevelRepeatableRead
	LevelSerializable   = stdsql.LevelSerializable
)

// Isolation sets the isolation level of a transaction, such as LevelSerializable.
func Isolation(level stdsql.IsolationLevel) TxOption {
	return func(o *stdsql.TxOptions) { o.Isolation = level }
}

// ReadOnly makes a transaction read only, so the database rejects every write in it.
func ReadOnly() TxOption {
	return func(o *stdsql.TxOptions) { o.ReadOnly = true }
}

// Transaction runs fn in a transaction that commits when fn returns nil and rolls back when it
// returns an error or panics. Queries that use the context passed to fn take part in it. A
// nested call runs in a savepoint, so its failure undoes only its own work.
//
// Options such as ReadOnly and Isolation apply to the outermost transaction only; a nested
// call with options returns an error, because a savepoint cannot change them.
//
// Do not run queries on the transaction's context from several goroutines at once.
func (db *DB) Transaction(ctx context.Context, fn func(ctx context.Context) error, opts ...TxOption) (err error) {
	if state, ok := ctx.Value(txKey{}).(*txState); ok && state.db == db {
		if len(opts) > 0 {
			return errors.New("sql: a nested transaction cannot set options; pass them to the outermost Transaction")
		}
		return db.savepoint(ctx, state, fn)
	}
	var txOptions *stdsql.TxOptions
	if len(opts) > 0 {
		txOptions = &stdsql.TxOptions{}
		for _, opt := range opts {
			opt(txOptions)
		}
	}
	tx, end, err := db.begin(ctx, txOptions)
	if err != nil {
		return fmt.Errorf("sql: beginning a transaction: %w", err)
	}
	defer end()
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
			panic(r)
		}
	}()
	if err := fn(context.WithValue(ctx, txKey{}, &txState{db: db, tx: tx})); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("sql: rolling back: %w", rollbackErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sql: committing: %w", db.wrap(err))
	}
	return nil
}

func (db *DB) savepoint(ctx context.Context, parent *txState, fn func(ctx context.Context) error) error {
	state := &txState{db: db, tx: parent.tx, depth: parent.depth + 1}
	name := fmt.Sprintf("gonestly_savepoint_%d", state.depth)
	if _, err := parent.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return fmt.Errorf("sql: creating a savepoint: %w", err)
	}
	defer func() {
		if r := recover(); r != nil {
			_, _ = parent.tx.ExecContext(context.WithoutCancel(ctx), "ROLLBACK TO SAVEPOINT "+name)
			panic(r)
		}
	}()
	if err := fn(context.WithValue(ctx, txKey{}, state)); err != nil {
		if _, rollbackErr := parent.tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+name); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("sql: rolling back to a savepoint: %w", rollbackErr))
		}
		return err
	}
	if _, err := parent.tx.ExecContext(ctx, "RELEASE SAVEPOINT "+name); err != nil {
		return fmt.Errorf("sql: releasing a savepoint: %w", err)
	}
	return nil
}
