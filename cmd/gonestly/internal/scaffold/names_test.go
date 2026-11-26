package scaffold

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNames(t *testing.T) {
	tests := map[string]Names{
		"products":    {Package: "products", Kebab: "products", Snake: "products", Model: "Product", Words: "products", ModelWords: "product"},
		"OrderItems":  {Package: "orderitems", Kebab: "order-items", Snake: "order_items", Model: "OrderItem", Words: "order items", ModelWords: "order item"},
		"categories":  {Package: "categories", Kebab: "categories", Snake: "categories", Model: "Category", Words: "categories", ModelWords: "category"},
		"api_keys":    {Package: "apikeys", Kebab: "api-keys", Snake: "api_keys", Model: "APIKey", Words: "api keys", ModelWords: "api key"},
		"boxes":       {Package: "boxes", Kebab: "boxes", Snake: "boxes", Model: "Box", Words: "boxes", ModelWords: "box"},
		"status":      {Package: "status", Kebab: "status", Snake: "status", Model: "Status", Words: "status", ModelWords: "status"},
		"HTTPClients": {Package: "httpclients", Kebab: "http-clients", Snake: "http_clients", Model: "HTTPClient", Words: "http clients", ModelWords: "http client"},
	}
	for in, want := range tests {
		got, err := NewNames(in)
		if err != nil || got != want {
			t.Errorf("NewNames(%q) = %+v, %v\nwant %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "--", "9lives", "no/slash", "café!"} {
		if _, err := NewNames(bad); err == nil {
			t.Errorf("NewNames(%q) succeeded", bad)
		}
	}
}

func TestCheckGenerated(t *testing.T) {
	for name, kind := range map[string]string{"type": "module", "sql": "service", "pages": "resource", "products": "resource", "services": "controller"} {
		names, err := NewNames(name)
		if err != nil {
			t.Fatal(err)
		}
		wantErr := name == "type" || name == "sql"
		if err := names.checkGenerated(kind); (err != nil) != wantErr {
			t.Errorf("checkGenerated(%s %s) = %v", kind, name, err)
		}
	}
	for _, name := range []string{"services", "modules", "controllers"} {
		names, _ := NewNames(name)
		if err := names.checkGenerated("resource"); err == nil {
			t.Errorf("a resource named %s would not compile, but was accepted", name)
		}
	}
}

func TestFrameworkVersion(t *testing.T) {
	for gomod, want := range map[string]string{
		"module x\n\nrequire github.com/yaboylino/gonestly v0.2.0\n":                     "v0.2.0",
		"module x\n\nrequire (\n\tgithub.com/yaboylino/gonestly v0.3.1 // indirect\n)\n": "v0.3.1",
		"module x\n": "latest",
	} {
		if got := frameworkVersion(gomod); got != want {
			t.Errorf("frameworkVersion(%q) = %s, want %s", gomod, got, want)
		}
	}
}

func TestNextMigrationVersion(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 5, 0, time.UTC)
	write := func(dir string, names ...string) {
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, tt := range map[string]struct {
		files []string
		want  string
	}{
		"an empty directory":      {nil, "20260301100005"},
		"older timestamps":        {[]string{"20250101000000_a.up.sql"}, "20260301100005"},
		"the same second":         {[]string{"20260301100005_a.up.sql", "20260301100005_a.down.sql"}, "20260301100006"},
		"a later second":          {[]string{"20260301100009_a.up.sql"}, "20260301100010"},
		"counters":                {[]string{"0001_a.up.sql", "0002_b.up.sql"}, "0003"},
		"other files are ignored": {[]string{"README.md", "migrations.go", "x_y.sql"}, "20260301100005"},
	} {
		dir := t.TempDir()
		write(dir, tt.files...)
		got, err := NextMigrationVersion(dir, now)
		if err != nil || got != tt.want {
			t.Errorf("%s: %q, %v, want %q", name, got, err, tt.want)
		}
	}
	if _, err := NextMigrationVersion(filepath.Join(t.TempDir(), "missing"), now); err == nil {
		t.Error("a missing directory")
	}
}
