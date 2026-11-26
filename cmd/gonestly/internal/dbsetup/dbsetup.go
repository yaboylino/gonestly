// Package dbsetup describes a database server, builds its connection string and creates the
// database on it.
package dbsetup

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Kinds are the databases that can be set up.
var Kinds = []string{"sqlite", "mysql", "postgres"}

var name = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// Config describes a database and the server it lives on.
type Config struct {
	// Kind is "sqlite", "mysql" or "postgres".
	Kind     string
	Host     string
	Port     int
	User     string
	Password string
	// Name is the name of the database, or for SQLite the path of its file.
	Name string
	// SSL is the sslmode of PostgreSQL or the tls setting of MySQL. When empty, a server on this
	// machine is reached without TLS and any other server with TLS.
	SSL string
	// Dir is what a relative SQLite name is relative to; the working directory when empty.
	Dir string
}

var sslModes = map[string][]string{
	"postgres": {"disable", "allow", "prefer", "require", "verify-ca", "verify-full"},
	"mysql":    {"false", "preferred", "true", "skip-verify"},
}

func isLocal(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ssl returns the TLS setting to use.
func (c Config) ssl() string {
	if c.SSL != "" {
		return c.SSL
	}
	switch {
	case isLocal(c.Host):
		return map[string]string{"postgres": "disable", "mysql": "false"}[c.Kind]
	default:
		return map[string]string{"postgres": "require", "mysql": "preferred"}[c.Kind]
	}
}

// DefaultPort returns the port a server of this kind listens on, or 0 for SQLite.
func DefaultPort(kind string) int {
	switch kind {
	case "mysql":
		return 3306
	case "postgres":
		return 5432
	default:
		return 0
	}
}

// Validate reports what is wrong with the configuration.
func (c Config) Validate() error {
	switch c.Kind {
	case "sqlite":
		if strings.TrimSpace(c.Name) == "" {
			return errors.New("the SQLite file needs a name, such as shop.db")
		}
		return nil
	case "mysql", "postgres":
	default:
		return fmt.Errorf("unknown database %q; use %s", c.Kind, strings.Join(Kinds, ", "))
	}
	switch {
	case strings.TrimSpace(c.Host) == "":
		return errors.New("the host is empty")
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("the port %d is not between 1 and 65535", c.Port)
	case c.User == "":
		return errors.New("the user is empty")
	case c.SSL != "" && !slices.Contains(sslModes[c.Kind], c.SSL):
		return fmt.Errorf("the TLS setting %q is not one of %s", c.SSL, strings.Join(sslModes[c.Kind], ", "))
	case !name.MatchString(c.Name):
		return fmt.Errorf("the database name %q must start with a letter or underscore and have only letters, digits and underscores", c.Name)
	}
	return nil
}

// Driver returns the database/sql driver name, as gonestly's sql module knows it.
func (c Config) Driver() string {
	return map[string]string{"postgres": "pgx", "mysql": "mysql", "sqlite": "sqlite"}[c.Kind]
}

// DSN returns the connection string for the application.
func (c Config) DSN() string { return c.dsn(c.Name, c.Password) }

// ExampleDSN is the connection string with a made up password, for .env.example.
func (c Config) ExampleDSN() string {
	if c.Kind == "sqlite" {
		return c.DSN()
	}
	return c.dsn(c.Name, "password")
}

func (c Config) dsn(database, password string) string {
	address := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	switch c.Kind {
	case "postgres":
		u := url.URL{Scheme: "postgres", User: url.UserPassword(c.User, password), Host: address, Path: "/" + database}
		u.RawQuery = "sslmode=" + c.ssl()
		return u.String()
	case "mysql":
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = c.User, password, "tcp", address, database
		cfg.ParseTime = true
		if tls := c.ssl(); tls != "false" {
			cfg.TLSConfig = tls
		}
		return cfg.FormatDSN()
	default:
		return "file:" + filepath.ToSlash(database) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	}
}

// Result tells what Create did.
type Result struct {
	// Existed reports that the database was already there.
	Existed bool
}

// Create makes the database when it does not exist yet, after checking that the server can be
// reached with the user and password. A SQLite file is created by the first migration.
func Create(ctx context.Context, c Config) (Result, error) {
	if err := c.Validate(); err != nil {
		return Result{}, err
	}
	if c.Kind == "sqlite" {
		file := c.Name
		if c.Dir != "" && !filepath.IsAbs(file) {
			file = filepath.Join(c.Dir, file)
		}
		if dir := filepath.Dir(file); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the directory is meant to be shared
				return Result{}, err
			}
		}
		_, err := os.Stat(file)
		return Result{Existed: err == nil}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Postgres has no database to connect to without one; postgres always exists. MySQL
	// connects without a database.
	admin := ""
	if c.Kind == "postgres" {
		admin = "postgres"
	}
	db, err := stdsql.Open(c.Driver(), c.dsn(admin, c.Password))
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return Result{}, fmt.Errorf("cannot connect to %s at %s:%d as %s: %w", c.Kind, c.Host, c.Port, c.User, err)
	}

	if c.Kind == "postgres" {
		var exists bool
		if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", c.Name).Scan(&exists); err != nil {
			return Result{}, err
		}
		if exists {
			return Result{Existed: true}, nil
		}
		// The name was validated, and an identifier cannot be a parameter.
		_, err := db.ExecContext(ctx, `CREATE DATABASE "`+c.Name+`"`)
		return Result{}, wrapCreate(c, err)
	}
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = ?)", c.Name).Scan(&exists); err != nil {
		return Result{}, err
	}
	if exists {
		return Result{Existed: true}, nil
	}
	_, err = db.ExecContext(ctx, "CREATE DATABASE `"+c.Name+"` CHARACTER SET utf8mb4")
	return Result{}, wrapCreate(c, err)
}

func wrapCreate(c Config, err error) error {
	if err != nil {
		return fmt.Errorf("creating the database %s: %w; the user %s needs the right to create databases", c.Name, err, c.User)
	}
	return nil
}
