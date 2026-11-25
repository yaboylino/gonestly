package sql

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const migrationsTable = "gonestly_migrations"

var (
	migrationFile = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.(up|down)\.sql$`)
	// noTransaction is the comment that runs a migration file outside a transaction.
	noTransaction = regexp.MustCompile(`(?m)^--\s*gonestly:no-transaction\s*$`)
)

// Migration is one versioned change to the schema, read from a pair of files such as
// 0001_create_users.up.sql and 0001_create_users.down.sql.
type Migration struct {
	Version int64
	Name    string
	up      string
	down    string
}

// MigrationStatus tells whether a migration has been applied, and when.
type MigrationStatus struct {
	Migration
	Applied   bool
	AppliedAt time.Time
}

// Migrator applies migrations from a file system, usually one embedded with go:embed. Each
// migration runs in its own transaction and is recorded in the gonestly_migrations table.
//
// A file with the comment line "-- gonestly:no-transaction" runs outside a transaction, one
// statement at a time, for statements such as CREATE INDEX CONCURRENTLY in PostgreSQL. When
// such a migration fails halfway, the statements before the failure stay applied.
type Migrator struct {
	db         *DB
	migrations []Migration
}

// NewMigrator reads the migration files at the root of fsys. File names have the form
// <version>_<name>.up.sql, with an optional .down.sql to revert it.
func NewMigrator(db *DB, fsys fs.FS) (*Migrator, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("sql: reading the migrations: %w", err)
	}
	byVersion := map[int64]*Migration{}
	var problems []string
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		match := migrationFile.FindStringSubmatch(entry.Name())
		if match == nil {
			problems = append(problems, entry.Name()+" does not follow <version>_<name>.up.sql or .down.sql, with a lower case name")
			continue
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			problems = append(problems, entry.Name()+" has an invalid version")
			continue
		}
		content, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("sql: reading %s: %w", entry.Name(), err)
		}
		m := byVersion[version]
		if m == nil {
			m = &Migration{Version: version, Name: match[2]}
			byVersion[version] = m
		}
		if m.Name != match[2] {
			problems = append(problems, fmt.Sprintf("version %d is used by %s and %s", version, m.Name, match[2]))
			continue
		}
		if match[3] == "up" {
			m.up = string(content)
		} else {
			m.down = string(content)
		}
	}

	migrations := make([]Migration, 0, len(byVersion))
	for _, m := range byVersion {
		if strings.TrimSpace(m.up) == "" {
			problems = append(problems, fmt.Sprintf("migration %d_%s has no up file, or it is empty", m.Version, m.Name))
		}
		migrations = append(migrations, *m)
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, fmt.Errorf("sql: invalid migrations: %s", strings.Join(problems, "; "))
	}
	slices.SortFunc(migrations, func(a, b Migration) int { return cmp.Compare(a.Version, b.Version) })
	return &Migrator{db: db, migrations: migrations}, nil
}

// Migrations returns every migration, oldest first.
func (m *Migrator) Migrations() []Migration {
	return slices.Clone(m.migrations)
}

type appliedMigration struct {
	Version   int64     `db:"version"`
	Name      string    `db:"name"`
	Checksum  string    `db:"checksum"`
	AppliedAt time.Time `db:"applied_at"`
}

// Up applies every pending migration in order and returns the ones it applied. It refuses to
// run when an applied migration was changed or removed, or when a pending migration is older
// than the latest applied one.
func (m *Migrator) Up(ctx context.Context) ([]Migration, error) {
	var applied []Migration
	err := m.locked(ctx, func(ctx context.Context, done map[int64]appliedMigration) error {
		pending, err := m.pending(done)
		if err != nil {
			return err
		}
		for _, migration := range pending {
			err := m.run(ctx, migration.up, func(ctx context.Context) error {
				_, err := m.db.Exec(ctx, "INSERT INTO "+migrationsTable+" (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
					migration.Version, migration.Name, checksum(migration.up), time.Now().UTC())
				return err
			})
			if err != nil {
				return fmt.Errorf("sql: applying migration %d_%s: %w", migration.Version, migration.Name, err)
			}
			applied = append(applied, migration)
		}
		return nil
	})
	return applied, err
}

// Down reverts the latest applied migration and returns it, or nil when none is applied.
func (m *Migrator) Down(ctx context.Context) (*Migration, error) {
	var reverted *Migration
	err := m.locked(ctx, func(ctx context.Context, done map[int64]appliedMigration) error {
		if err := m.verify(done); err != nil {
			return err
		}
		for _, migration := range slices.Backward(m.migrations) {
			if _, ok := done[migration.Version]; !ok {
				continue
			}
			if strings.TrimSpace(migration.down) == "" {
				return fmt.Errorf("sql: migration %d_%s has no down file", migration.Version, migration.Name)
			}
			err := m.run(ctx, migration.down, func(ctx context.Context) error {
				_, err := m.db.Exec(ctx, "DELETE FROM "+migrationsTable+" WHERE version = ?", migration.Version)
				return err
			})
			if err != nil {
				return fmt.Errorf("sql: reverting migration %d_%s: %w", migration.Version, migration.Name, err)
			}
			reverted = &migration
			return nil
		}
		return nil
	})
	return reverted, err
}

// run executes a migration script and records it: together in one transaction, or statement
// by statement when the script asks to run outside one.
func (m *Migrator) run(ctx context.Context, script string, record func(ctx context.Context) error) error {
	if !noTransaction.MatchString(script) {
		return m.db.Transaction(ctx, func(ctx context.Context) error {
			if _, err := m.db.conn(ctx).ExecContext(ctx, script); err != nil {
				return err
			}
			return record(ctx)
		})
	}
	for _, statement := range m.db.dialect.statements(script) {
		if _, err := m.db.conn(ctx).ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return record(ctx)
}

// Status lists every migration and whether it has been applied.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}
	done, err := m.applied(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]MigrationStatus, len(m.migrations))
	for i, migration := range m.migrations {
		a, ok := done[migration.Version]
		statuses[i] = MigrationStatus{Migration: migration, Applied: ok, AppliedAt: a.AppliedAt}
	}
	return statuses, nil
}

// locked runs fn while holding the migration lock. Everything fn does on its context uses the
// connection that holds the lock, so a pool of a single connection is enough.
func (m *Migrator) locked(ctx context.Context, fn func(ctx context.Context, done map[int64]appliedMigration) error) error {
	conn, err := m.db.std.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sql: connecting for migrations: %w", err)
	}
	defer conn.Close()
	unlock, err := m.db.dialect.lockMigrations(ctx, conn)
	if err != nil {
		return fmt.Errorf("sql: locking the migrations: %w", err)
	}
	defer unlock()

	ctx = m.db.pin(ctx, conn)
	if err := m.ensureTable(ctx); err != nil {
		return err
	}
	done, err := m.applied(ctx)
	if err != nil {
		return err
	}
	return fn(ctx, done)
}

func (m *Migrator) ensureTable(ctx context.Context) error {
	_, err := m.db.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+migrationsTable+
		" (version BIGINT PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TIMESTAMP NOT NULL)")
	if err != nil {
		return fmt.Errorf("sql: creating the %s table: %w", migrationsTable, err)
	}
	return nil
}

func (m *Migrator) applied(ctx context.Context) (map[int64]appliedMigration, error) {
	rows, err := Query[appliedMigration](ctx, m.db, "SELECT version, name, checksum, applied_at FROM "+migrationsTable)
	if err != nil {
		return nil, fmt.Errorf("sql: reading the applied migrations: %w", err)
	}
	done := make(map[int64]appliedMigration, len(rows))
	for _, row := range rows {
		done[row.Version] = row
	}
	return done, nil
}

func (m *Migrator) verify(done map[int64]appliedMigration) error {
	known := map[int64]Migration{}
	for _, migration := range m.migrations {
		known[migration.Version] = migration
	}
	var problems []error
	for version, a := range done {
		migration, ok := known[version]
		switch {
		case !ok:
			problems = append(problems, fmt.Errorf("migration %d_%s is applied but its file is missing", version, a.Name))
		case checksum(migration.up) != a.Checksum:
			problems = append(problems, fmt.Errorf("migration %d_%s was changed after it was applied; add a new migration instead", version, a.Name))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("sql: %w", errors.Join(problems...))
	}
	return nil
}

func (m *Migrator) pending(done map[int64]appliedMigration) ([]Migration, error) {
	if err := m.verify(done); err != nil {
		return nil, err
	}
	var latest int64
	for version := range done {
		latest = max(latest, version)
	}
	var pending []Migration
	for _, migration := range m.migrations {
		if _, ok := done[migration.Version]; ok {
			continue
		}
		if migration.Version < latest {
			return nil, fmt.Errorf("sql: migration %d_%s is pending but the newer migration %d is already applied; give it a higher version", migration.Version, migration.Name, latest)
		}
		pending = append(pending, migration)
	}
	return pending, nil
}

func checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
