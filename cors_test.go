package gonestly_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

func corsApp(t *testing.T, config gonestly.CORSConfig) *gonestlytest.App {
	t.Helper()
	root := usersModule()
	root.Providers = append(root.Providers, gonestly.Providers(gonestly.Value(gonestly.ServerConfig{CORS: config}))...)
	return gonestlytest.Start(t, root)
}

func preflight(app *gonestlytest.App, origin, method, headers string) *gonestlytest.Response {
	pairs := []string{"Origin", origin, "Access-Control-Request-Method", method}
	if headers != "" {
		pairs = append(pairs, "Access-Control-Request-Headers", headers)
	}
	return app.Request(http.MethodOptions, "/users/1", nil, pairs...)
}

func TestCORS(t *testing.T) {
	app := corsApp(t, gonestly.CORSConfig{AllowedOrigins: []string{"https://app.example.com", "https://*.preview.example.com"}})

	for origin, allowed := range map[string]bool{
		"https://app.example.com":                true,
		"https://APP.example.com":                true,
		"https://pr-7.preview.example.com":       true,
		"https://preview.example.com":            false,
		"https://.preview.example.com":           false,
		"https://evil.com":                       false,
		"http://app.example.com":                 false,
		"https://app.example.com.evil.com":       false,
		"https://evil.com/.preview.example.com":  false,
		"https://evil.com:1.preview.example.com": false,
	} {
		res := app.Get("/users/1", "Origin", origin)
		got := res.Header.Get("Access-Control-Allow-Origin")
		if allowed != (got == origin) || res.Status != http.StatusOK {
			t.Errorf("origin %s: Access-Control-Allow-Origin = %q, status %d", origin, got, res.Status)
		}
		if res.Header.Get("Vary") != "Origin" {
			t.Errorf("origin %s: Vary = %q", origin, res.Header.Values("Vary"))
		}
	}

	res := app.Get("/users/1", "Origin", "https://app.example.com")
	if res.Header.Get("Access-Control-Expose-Headers") != "X-Request-ID" || res.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("headers = %v", res.Header)
	}
	if res := app.Get("/users/1"); res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Vary") != "Origin" {
		t.Errorf("without an origin: %v", res.Header)
	}
	// An error is readable for the page too.
	if res := app.Get("/nowhere", "Origin", "https://app.example.com"); res.Status != http.StatusNotFound || res.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Errorf("a 404: %d %v", res.Status, res.Header)
	}

	res = preflight(app, "https://app.example.com", "PUT", "content-type, authorization")
	if res.Status != http.StatusNoContent ||
		res.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		res.Header.Get("Access-Control-Allow-Methods") != "GET, HEAD, POST, PUT, PATCH, DELETE" ||
		res.Header.Get("Access-Control-Allow-Headers") != "content-type, authorization" ||
		res.Header.Get("Access-Control-Max-Age") != "600" ||
		strings.Join(res.Header.Values("Vary"), ",") != "Origin,Access-Control-Request-Method,Access-Control-Request-Headers" {
		t.Errorf("preflight: %d %v", res.Status, res.Header)
	}
	if res := preflight(app, "https://evil.com", "PUT", ""); res.Status != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Access-Control-Allow-Methods") != "" {
		t.Errorf("preflight from another origin: %d %v", res.Status, res.Header)
	}
	if res := preflight(app, "https://app.example.com", "TRACE", ""); res.Header.Get("Access-Control-Allow-Methods") != "" {
		t.Errorf("preflight for a method that is not allowed: %v", res.Header)
	}
	// OPTIONS without a requested method is an ordinary request, not a preflight.
	if res := app.Request(http.MethodOptions, "/users/1", nil, "Origin", "https://app.example.com"); res.Status != http.StatusMethodNotAllowed {
		t.Errorf("a plain OPTIONS request = %d", res.Status)
	}
}

func TestCORSOptions(t *testing.T) {
	app := corsApp(t, gonestly.CORSConfig{
		AllowedOrigins:   []string{"https://app.example.com"},
		AllowedMethods:   []string{"get"},
		AllowedHeaders:   []string{"Authorization"},
		ExposedHeaders:   []string{},
		AllowCredentials: true,
		MaxAge:           time.Hour,
	})
	res := preflight(app, "https://app.example.com", "GET", "x-anything")
	if res.Header.Get("Access-Control-Allow-Methods") != "GET" || res.Header.Get("Access-Control-Allow-Headers") != "Authorization" ||
		res.Header.Get("Access-Control-Max-Age") != "3600" || res.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("preflight: %v", res.Header)
	}
	res = app.Get("/users/1", "Origin", "https://app.example.com")
	if _, exposed := res.Header["Access-Control-Expose-Headers"]; exposed || res.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("headers = %v", res.Header)
	}

	everyone := corsApp(t, gonestly.CORSConfig{AllowedOrigins: []string{"*"}})
	res = everyone.Get("/users/1", "Origin", "https://anything.example")
	if res.Header.Get("Access-Control-Allow-Origin") != "*" || res.Header.Get("Vary") != "" {
		t.Errorf("every origin: %v", res.Header)
	}

	// Without origins the server stays out of CORS altogether.
	none := corsApp(t, gonestly.CORSConfig{AllowCredentials: true})
	if res := none.Get("/users/1", "Origin", "https://app.example.com"); res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Vary") != "" {
		t.Errorf("no origins: %v", res.Header)
	}
}

func TestCORSMistakes(t *testing.T) {
	for _, tt := range []struct {
		config gonestly.CORSConfig
		want   string
	}{
		{gonestly.CORSConfig{AllowedOrigins: []string{"https://app.example.com/"}}, `the origin "https://app.example.com/" must be a scheme and a host without a path`},
		{gonestly.CORSConfig{AllowedOrigins: []string{"app.example.com"}}, `the origin "app.example.com" must be a scheme and a host`},
		{gonestly.CORSConfig{AllowedOrigins: []string{"https://*example.com"}}, `the origin "https://*example.com" must be`},
		{gonestly.CORSConfig{AllowedOrigins: []string{"https://*.*.example.com"}}, `the origin "https://*.*.example.com" must be`},
		{gonestly.CORSConfig{AllowedOrigins: []string{"*"}, AllowCredentials: true}, `AllowCredentials cannot be combined with the origin "*"`},
	} {
		root := usersModule()
		root.Providers = append(root.Providers, gonestly.Providers(gonestly.Value(gonestly.ServerConfig{CORS: tt.config}))...)
		app, err := gonestly.New(root, quietLogger())
		if err == nil {
			err = app.Start(t.Context())
			_ = app.Stop(t.Context())
		}
		assertContains(t, err, tt.want)
	}
}
