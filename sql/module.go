package sql

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/yaboylino/gonestly"
)

// Module returns a global module that connects to the database when the application starts,
// applies the migrations in Options.Migrations, provides the *DB to every module and closes it
// when the application stops. Import it once, in the root module.
func Module(opts Options) *gonestly.Module {
	return newModule(func(ctx context.Context, logger *slog.Logger) (*DB, error) {
		return connect(ctx, opts, logger)
	})
}

// ModuleFunc is like Module, but builds the options from a provider of a global module,
// typically the configuration from config.Module:
//
//	sql.ModuleFunc(func(cfg Config) sql.Options {
//		return sql.Options{Driver: "pgx", DSN: cfg.DatabaseURL, Migrations: migrations.Files}
//	})
func ModuleFunc[C any](options func(C) Options) *gonestly.Module {
	return newModule(func(ctx context.Context, logger *slog.Logger, c C) (*DB, error) {
		return connect(ctx, options(c), logger)
	})
}

func newModule(constructor any) *gonestly.Module {
	return &gonestly.Module{
		Name:      "sql",
		Global:    true,
		Providers: gonestly.Providers(constructor),
		Exports:   gonestly.Exports(gonestly.Type[*DB]()),
	}
}

func connect(ctx context.Context, opts Options, logger *slog.Logger) (*DB, error) {
	if opts.Logger == nil {
		opts.Logger = logger
	}
	db, err := Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	if opts.Migrations == nil {
		return db, nil
	}
	migrator, err := NewMigrator(db, opts.Migrations)
	if err == nil {
		var applied []Migration
		applied, err = migrator.Up(ctx)
		for _, m := range applied {
			opts.Logger.InfoContext(ctx, "migration applied", slog.Int64("version", m.Version), slog.String("name", m.Name))
		}
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sql: migrating: %w", err)
	}
	return db, nil
}
