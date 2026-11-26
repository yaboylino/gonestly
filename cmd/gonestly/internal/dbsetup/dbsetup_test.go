package dbsetup

import (
	stdsql "database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			"postgres://app%20user:p%40ss%2Fw%3Ard%3F%23@db.internal:5432/shop?sslmode=disable",
			"postgres://app%20user:password@db.internal:5432/shop?sslmode=disable", "pgx",
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

// The server tests run against the databases of make databases, like the tests of the sql module.
func TestCreateOnServers(t *testing.T) {
	for _, tt := range []struct {
		kind, env string
		port      int
		password  string
		user      string
	}{
		{"postgres", "GONESTLY_TEST_POSTGRES", 55432, "gonestly", "postgres"},
		{"mysql", "GONESTLY_TEST_MYSQL", 53306, "gonestly", "root"},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			if os.Getenv(tt.env) == "" {
				t.Skip("set " + tt.env + " to run this test against a server")
			}
			name := "dbsetup_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")) + "_" + strings.ToLower(filepath.Base(t.TempDir()))
			name = strings.NewReplacer("-", "_").Replace(name[:min(len(name), 60)])
			cfg := Config{Kind: tt.kind, Host: "127.0.0.1", Port: tt.port, User: tt.user, Password: tt.password, Name: name}
			t.Cleanup(func() {
				db, err := stdsql.Open(cfg.Driver(), cfg.dsn(map[string]string{"postgres": "postgres"}[tt.kind], cfg.Password))
				if err == nil {
					quote := map[string]string{"postgres": `"`, "mysql": "`"}[tt.kind]
					_, _ = db.ExecContext(t.Context(), "DROP DATABASE "+quote+name+quote)
					_ = db.Close()
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
