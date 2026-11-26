package dbsetup

import (
	"context"
	"crypto/rand"
	stdsql "database/sql"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestValidate(t *testing.T) {
	good := Config{Kind: "postgres", Host: "localhost", Port: 5432, User: "app", Name: "shop_2"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for want, mutate := range map[string]func(*Config){
		`unknown database "oracle"`:      func(c *Config) { c.Kind = "oracle" },
		"the host is empty":              func(c *Config) { c.Host = " " },
		"the port 70000 is not between":  func(c *Config) { c.Port = 70000 },
		"the user is empty":              func(c *Config) { c.User = "" },
		`the database name "shop; DROP"`: func(c *Config) { c.Name = "shop; DROP" },
		`the database name "2shop"`:      func(c *Config) { c.Name = "2shop" },
		`the database name "shop-db"`:    func(c *Config) { c.Name = "shop-db" },
		"the SQLite file needs a name":   func(c *Config) { c.Kind, c.Name = "sqlite", "" },
	} {
		c := good
		mutate(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v", want, err)
		}
	}
	if err := (Config{Kind: "sqlite", Name: "data/shop.db"}).Validate(); err != nil {
		t.Errorf("SQLite: %v", err)
	}
}

func TestDSN(t *testing.T) {
	for name, tt := range map[string]struct {
		cfg     Config
		dsn     string
		example string
		driver  string
	}{
		"postgres": {
			Config{Kind: "postgres", Host: "db.internal", Port: 5432, User: "app user", Password: "p@ss/w:rd?#", Name: "shop"},
			"postgres://app%20user:p%40ss%2Fw%3Ard%3F%23@db.internal:5432/shop?sslmode=require",
			"postgres://app%20user:password@db.internal:5432/shop?sslmode=require", "pgx",
		},
		"postgres with a chosen mode": {
			Config{Kind: "postgres", Host: "db.internal", Port: 5432, User: "u", Password: "p", Name: "shop", SSL: "verify-full"},
			"postgres://u:p@db.internal:5432/shop?sslmode=verify-full",
			"postgres://u:password@db.internal:5432/shop?sslmode=verify-full", "pgx",
		},
		"mysql on another host": {
			Config{Kind: "mysql", Host: "db.internal", Port: 3306, User: "u", Password: "p", Name: "shop"},
			"u:p@tcp(db.internal:3306)/shop?parseTime=true&tls=preferred",
			"u:password@tcp(db.internal:3306)/shop?parseTime=true&tls=preferred", "mysql",
		},
		"postgres on IPv6": {
			Config{Kind: "postgres", Host: "::1", Port: 5433, User: "u", Password: "p", Name: "shop"},
			"postgres://u:p@[::1]:5433/shop?sslmode=disable",
			"postgres://u:password@[::1]:5433/shop?sslmode=disable", "pgx",
		},
		"mysql": {
			Config{Kind: "mysql", Host: "localhost", Port: 3306, User: "root", Password: "p@ss/w:rd", Name: "shop"},
			"root:p@ss/w:rd@tcp(localhost:3306)/shop?parseTime=true",
			"root:password@tcp(localhost:3306)/shop?parseTime=true", "mysql",
		},
		"sqlite": {
			Config{Kind: "sqlite", Name: "data/shop.db"},
			"file:data/shop.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)",
			"file:data/shop.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", "sqlite",
		},
	} {
		if got := tt.cfg.DSN(); got != tt.dsn {
			t.Errorf("%s: DSN = %s, want %s", name, got, tt.dsn)
		}
		if got := tt.cfg.ExampleDSN(); got != tt.example {
			t.Errorf("%s: ExampleDSN = %s, want %s", name, got, tt.example)
		}
		if tt.cfg.Driver() != tt.driver {
			t.Errorf("%s: Driver = %s", name, tt.cfg.Driver())
		}
	}
}

func TestCreateSQLite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "data", "shop.db")
	cfg := Config{Kind: "sqlite", Name: file}
	if result, err := Create(t.Context(), cfg); err != nil || result.Existed {
		t.Fatalf("Create = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Dir(file)); err != nil {
		t.Errorf("the directory was not made: %v", err)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := Create(t.Context(), cfg); err != nil || !result.Existed {
		t.Errorf("Create on an existing file = %+v, %v", result, err)
	}
}

// serverConfig reads the server from the connection string that the tests of the sql module
// use as well: GONESTLY_TEST_POSTGRES as a URL, and GONESTLY_TEST_MYSQL as user:password@tcp(host:port)/db.
func serverConfig(t *testing.T, kind, env string) Config {
	t.Helper()
	raw := os.Getenv(env)
	cfg := Config{Kind: kind}
	switch kind {
	case "postgres":
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Host, cfg.User = u.Hostname(), u.User.Username()
		cfg.Password, _ = u.User.Password()
		cfg.Port, _ = strconv.Atoi(u.Port())
	default:
		c, err := mysql.ParseDSN(raw)
		if err != nil {
			t.Fatal(err)
		}
		host, port, _ := net.SplitHostPort(c.Addr)
		cfg.Host, cfg.User, cfg.Password = host, c.User, c.Passwd
		cfg.Port, _ = strconv.Atoi(port)
	}
	return cfg
}

// The server tests run against the databases of make databases, like the tests of the sql module.
func TestCreateOnServers(t *testing.T) {
	for _, tt := range []struct{ kind, env string }{
		{"postgres", "GONESTLY_TEST_POSTGRES"},
		{"mysql", "GONESTLY_TEST_MYSQL"},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			if os.Getenv(tt.env) == "" {
				t.Skip("set " + tt.env + " to run this test against a server")
			}
			cfg := serverConfig(t, tt.kind, tt.env)
			cfg.Name = "dbsetup_" + strings.ToLower(rand.Text())
			name := cfg.Name
			t.Cleanup(func() {
				db, err := stdsql.Open(cfg.Driver(), cfg.dsn(map[string]string{"postgres": "postgres"}[tt.kind], cfg.Password))
				if err != nil {
					return
				}
				defer db.Close()
				// The context of the test has ended by now, so the cleanup has its own.
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				quote := map[string]string{"postgres": `"`, "mysql": "`"}[tt.kind]
				if _, err := db.ExecContext(ctx, "DROP DATABASE "+quote+name+quote); err != nil {
					t.Errorf("dropping the test database: %v", err)
				}
			})

			if result, err := Create(t.Context(), cfg); err != nil || result.Existed {
				t.Fatalf("Create = %+v, %v", result, err)
			}
			// The database can be used with the connection string that was made.
			db, err := stdsql.Open(cfg.Driver(), cfg.DSN())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.PingContext(t.Context()); err != nil {
				t.Errorf("the new database cannot be used: %v", err)
			}
			if result, err := Create(t.Context(), cfg); err != nil || !result.Existed {
				t.Errorf("Create again = %+v, %v", result, err)
			}

			wrong := cfg
			wrong.Password = "wrong"
			if _, err := Create(t.Context(), wrong); err == nil || !strings.Contains(err.Error(), "cannot connect to "+tt.kind) {
				t.Errorf("a wrong password = %v", err)
			}
			unreachable := cfg
			unreachable.Port = 1
			if _, err := Create(t.Context(), unreachable); err == nil || !strings.Contains(err.Error(), "cannot connect") {
				t.Errorf("an unreachable server = %v", err)
			}
		})
	}
}

func TestValidateRejectsUnknownTLSSetting(t *testing.T) {
	c := Config{Kind: "postgres", Host: "h", Port: 5432, User: "u", Name: "shop", SSL: "maybe"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "TLS setting") {
		t.Errorf("err = %v", err)
	}
	c = Config{Kind: "mysql", Host: "h", Port: 3306, User: "u", Name: "shop", SSL: "require"}
	if err := c.Validate(); err == nil {
		t.Error("require is not a MySQL setting")
	}
}

func TestCreateSQLiteRelativeToDir(t *testing.T) {
	root := t.TempDir()
	if _, err := Create(t.Context(), Config{Kind: "sqlite", Name: "data/shop.db", Dir: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "data")); err != nil {
		t.Errorf("the directory was not made in the project: %v", err)
	}
}
