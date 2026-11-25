package config_test

import (
	"context"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/config"
)

func env(vars map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type redis struct {
	Host string `default:"localhost"`
	Port int    `default:"6379"`
}

type Embedded struct {
	Region string
}

type appConfig struct {
	Embedded
	Port        int    `default:"3000"`
	DatabaseURL string `env:"DATABASE_URL,required"`
	Debug       bool
	Timeout     time.Duration `default:"5s"`
	Ratio       float64
	MaxBytes    uint32
	Origins     []string
	Weights     []int
	Address     netip.Addr
	Redis       redis
	Cache       redis `env:"CACHE_STORE"`
	APIKey      string
	Internal    string `env:"-"`
}

func TestLoad(t *testing.T) {
	cfg, err := config.Load[appConfig](config.Options{
		Files: []string{},
		LookupEnv: env(map[string]string{
			"DATABASE_URL":     "postgres://localhost/app",
			"DEBUG":            "true",
			"RATIO":            "0.75",
			"MAX_BYTES":        "1024",
			"ORIGINS":          "https://a.example, https://b.example",
			"WEIGHTS":          "1,2,3",
			"ADDRESS":          "10.0.0.1",
			"REDIS_HOST":       "redis",
			"CACHE_STORE_PORT": "7000",
			"API_KEY":          "secret",
			"REGION":           "eu-west",
			"INTERNAL":         "ignored",
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := appConfig{
		Embedded:    Embedded{Region: "eu-west"},
		Port:        3000,
		DatabaseURL: "postgres://localhost/app",
		Debug:       true,
		Timeout:     5 * time.Second,
		Ratio:       0.75,
		MaxBytes:    1024,
		Origins:     []string{"https://a.example", "https://b.example"},
		Weights:     []int{1, 2, 3},
		Address:     netip.MustParseAddr("10.0.0.1"),
		Redis:       redis{Host: "redis", Port: 6379},
		Cache:       redis{Host: "localhost", Port: 7000},
		APIKey:      "secret",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got  %+v\nwant %+v", cfg, want)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	type cfg struct {
		Port     int    `env:"PORT"`
		Token    string `env:"TOKEN,required"`
		Secret   string `env:"SECRET,required"`
		Debug    bool
		Timeout  time.Duration
		Ratio    float32
		Count    uint8
		Weights  []int
		Address  netip.Addr
		Settings map[string]string
	}
	_, err := config.Load[cfg](config.Options{
		Files: []string{},
		LookupEnv: env(map[string]string{
			"PORT":    "abc",
			"SECRET":  "",
			"DEBUG":   "maybe",
			"TIMEOUT": "5",
			"RATIO":   "x",
			"COUNT":   "300",
			"WEIGHTS": "1,two",
			"ADDRESS": "not-an-ip",
		}),
	})
	if err == nil {
		t.Fatal("Load succeeded, want an error")
	}
	for _, part := range []string{
		`config: PORT: "abc" is not a valid int`,
		"config: TOKEN is required",
		"config: SECRET is required",
		`config: DEBUG: "maybe" is not a valid boolean`,
		`config: TIMEOUT: "5" is not a valid duration`,
		`config: RATIO: "x" is not a valid float32`,
		`config: COUNT: "300" is not a valid uint8`,
		`config: WEIGHTS: item 2: "two" is not a valid int`,
		"config: ADDRESS: ",
		"config: SETTINGS: field Settings has unsupported type map[string]string",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error does not contain %q:\n%v", part, err)
		}
	}
}

func TestLoadPrefixAndEmptySlice(t *testing.T) {
	type cfg struct {
		Port    int
		Origins []string `default:"a,b"`
	}
	got, err := config.Load[cfg](config.Options{
		Files:     []string{},
		Prefix:    "APP_",
		LookupEnv: env(map[string]string{"APP_PORT": "8080", "PORT": "1", "APP_ORIGINS": ""}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 8080 || len(got.Origins) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestLoadRejectsNonStruct(t *testing.T) {
	if _, err := config.Load[string](config.Options{}); err == nil || !strings.Contains(err.Error(), "string is not a struct") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadFiles(t *testing.T) {
	type cfg struct {
		Name    string
		Port    int
		Message string
		Raw     string
		Comment string
		Token   string
	}
	base := writeFile(t, `
# comment
export NAME=base
PORT=1000
MESSAGE="line one\nline \"two\"" # trailing comment
RAW='no \n escapes'
COMMENT=value # comment
TOKEN=from-file
`)
	local := writeFile(t, "PORT=2000\n")

	got, err := config.Load[cfg](config.Options{
		Files:     []string{base, local, filepath.Join(t.TempDir(), "missing.env")},
		LookupEnv: env(map[string]string{"TOKEN": "from-env"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := cfg{
		Name:    "base",
		Port:    2000,
		Message: "line one\nline \"two\"",
		Raw:     `no \n escapes`,
		Comment: "value",
		Token:   "from-env",
	}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestLoadReadsDotenvByDefault(t *testing.T) {
	type cfg struct{ Name string }
	t.Chdir(filepath.Dir(writeFile(t, "NAME=default-file\n")))
	got, err := config.Load[cfg](config.Options{LookupEnv: env(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "default-file" {
		t.Errorf("Name = %q", got.Name)
	}
}

func TestInvalidDotenv(t *testing.T) {
	tests := []struct {
		content string
		want    string
	}{
		{"NAME\n", ":1: expected KEY=VALUE"},
		{"\n1NAME=x\n", ":2: expected KEY=VALUE"},
		{"NA-ME=x\n", ":1: expected KEY=VALUE"},
		{`NAME="open` + "\n", ":1: unterminated double quoted value"},
		{"NAME='open\n", ":1: unterminated single quoted value"},
		{`NAME="a" b` + "\n", `:1: unexpected "b" after the closing quote`},
	}
	type cfg struct{ Name string }
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			_, err := config.Load[cfg](config.Options{Files: []string{writeFile(t, tt.content)}, LookupEnv: env(nil)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestUnreadableFile(t *testing.T) {
	type cfg struct{ Name string }
	_, err := config.Load[cfg](config.Options{Files: []string{t.TempDir()}, LookupEnv: env(nil)})
	if err == nil {
		t.Error("reading a directory as a dotenv file succeeded")
	}
}

func TestModule(t *testing.T) {
	type cfg struct {
		Port int `env:"PORT,required"`
	}
	quiet := gonestly.WithLogger(slog.New(slog.DiscardHandler))

	t.Setenv("PORT", "4000")
	root := &gonestly.Module{Name: "app", Imports: gonestly.Imports(config.Module[cfg](config.Options{Files: []string{}}))}
	app, err := gonestly.New(root, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop(context.Background()) }()
	got, err := gonestly.Resolve[cfg](app)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 4000 {
		t.Errorf("Port = %d", got.Port)
	}

	t.Setenv("PORT", "")
	root = &gonestly.Module{Name: "app", Imports: gonestly.Imports(config.Module[cfg](config.Options{Files: []string{}}))}
	app, err = gonestly.New(root, quiet)
	if err != nil {
		t.Fatal(err)
	}
	err = app.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "config: PORT is required") {
		t.Errorf("Start = %v, want a configuration error", err)
	}
}
