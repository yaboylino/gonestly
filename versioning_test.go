package gonestly_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

type userByID struct {
	ID int `path:"id"`
}

func reply(s string) func(context.Context, gonestly.Empty) (string, error) {
	return func(context.Context, gonestly.Empty) (string, error) { return s, nil }
}

func startVersioned(t *testing.T, config gonestly.VersioningConfig, routes func(r *gonestly.Router)) *gonestlytest.App {
	t.Helper()
	return gonestlytest.Start(t, &gonestly.Module{
		Name: "app",
		Providers: gonestly.Providers(func() gonestly.ServerConfig {
			return gonestly.ServerConfig{Versioning: config}
		}),
		Controllers: gonestly.Controllers(func() routesFunc { return routes }),
	})
}

func twoVersions(r *gonestly.Router) {
	gonestly.Get(r, "/health", reply("health"))
	r.Group("", func(r *gonestly.Router) {
		r.Version("1").Prefix("/users")
		gonestly.Get(r, "", reply("users v1"))
		gonestly.Post(r, "", reply("created v1"))
	})
	r.Group("", func(r *gonestly.Router) {
		r.Version("v2").Prefix("/users")
		gonestly.Get(r, "", reply("users v2"))
		gonestly.Get(r, "/{id}", func(_ context.Context, req userByID) (string, error) {
			return "user " + string(rune('0'+req.ID)), nil
		})
	})
}

// buildError builds and starts an application with these controllers and returns the error.
func buildVersionError(t *testing.T, config gonestly.ServerConfig, routes func(r *gonestly.Router)) error {
	t.Helper()
	app, err := gonestly.New(&gonestly.Module{
		Name:        "app",
		Providers:   gonestly.Providers(func() gonestly.ServerConfig { return config }),
		Controllers: gonestly.Controllers(func() routesFunc { return routes }),
	}, gonestly.WithoutListener())
	if err != nil {
		return err
	}
	err = app.Start(t.Context())
	if err == nil {
		_ = app.Stop(context.Background())
	}
	return err
}

func body(t *testing.T, res *gonestlytest.Response) string {
	t.Helper()
	var s string
	res.JSON(&s)
	return s
}

func TestVersionInThePath(t *testing.T) {
	app := startVersioned(t, gonestly.VersioningConfig{}, twoVersions)
	if got := body(t, app.Get("/v1/users").ExpectStatus(http.StatusOK)); got != "users v1" {
		t.Errorf("v1 = %q", got)
	}
	res := app.Get("/v2/users").ExpectStatus(http.StatusOK)
	if body(t, res) != "users v2" || res.Header.Get(gonestly.APIVersionHeader) != "2" {
		t.Errorf("v2 = %q, header %q", res.Body, res.Header.Get(gonestly.APIVersionHeader))
	}
	if got := body(t, app.Get("/v2/users/7").ExpectStatus(http.StatusOK)); got != "user 7" {
		t.Errorf("v2 user = %q", got)
	}
	if h := app.Get("/health").Header.Get(gonestly.APIVersionHeader); h != "" {
		t.Errorf("an unversioned route says it is version %q", h)
	}
	app.Get("/users").ExpectStatus(http.StatusNotFound)
}

func TestVersionNames(t *testing.T) {
	for version, want := range map[string]string{"1": "/v1/x", "v3": "/v3/x", "1.1": "/v1.1/x"} {
		app := startVersioned(t, gonestly.VersioningConfig{}, func(r *gonestly.Router) {
			r.Version(version)
			gonestly.Get(r, "/x", reply("x"))
		})
		app.Get(want).ExpectStatus(http.StatusOK)
	}
	for _, bad := range []string{"", "x", "v", "1.2.3", "latest"} {
		err := buildVersionError(t, gonestly.ServerConfig{}, func(r *gonestly.Router) {
			r.Version(bad)
			gonestly.Get(r, "/x", reply("x"))
		})
		var build *gonestly.BuildError
		if !errors.As(err, &build) || !strings.Contains(err.Error(), "is not a number such as 1 or 1.1") {
			t.Errorf("version %q: err = %v", bad, err)
		}
	}
}

func TestVersionWithAHeader(t *testing.T) {
	app := startVersioned(t, gonestly.VersioningConfig{Header: "API-Version", Default: "1"}, twoVersions)
	for name, tt := range map[string]struct {
		path    string
		headers []string
		want    string
	}{
		"the default":               {"/users", nil, "users v1"},
		"a header":                  {"/users", []string{"API-Version", "2"}, "users v2"},
		"a header with a v":         {"/users", []string{"API-Version", "v2"}, "users v2"},
		"a header for version one":  {"/users", []string{"API-Version", "1"}, "users v1"},
		"the path wins":             {"/v2/users", []string{"API-Version", "1"}, "users v2"},
		"a path with parameters":    {"/users/5", []string{"API-Version", "2"}, "user 5"},
		"unversioned routes remain": {"/health", []string{"API-Version", "2"}, "health"},
	} {
		res := app.Get(tt.path, tt.headers...)
		if res.Status != http.StatusOK || body(t, res) != tt.want {
			t.Errorf("%s: %d %s, want %q", name, res.Status, res.Body, tt.want)
		}
	}
	// The default has no such path: version one has no users/{id}.
	app.Get("/users/5").ExpectStatus(http.StatusNotFound)
	// A method that only one version has.
	app.Post("/users", nil, "API-Version", "1").ExpectStatus(http.StatusCreated)
	app.Post("/users", nil, "API-Version", "2").ExpectStatus(http.StatusMethodNotAllowed)

	p := app.Get("/users", "API-Version", "9").Problem()
	if p.Status != http.StatusBadRequest || !strings.Contains(p.Detail, `"9"`) || !strings.Contains(p.Detail, "use one of: 1, 2") {
		t.Errorf("problem = %+v", p)
	}
	if p := app.Get("/users", "API-Version", "latest").Problem(); p.Status != http.StatusBadRequest {
		t.Errorf("problem = %+v", p)
	}
	if v := app.Get("/users", "API-Version", "2").Header.Get(gonestly.APIVersionHeader); v != "2" {
		t.Errorf("the response says version %q", v)
	}
}

func TestHeaderWithoutADefault(t *testing.T) {
	app := startVersioned(t, gonestly.VersioningConfig{Header: "API-Version"}, twoVersions)
	app.Get("/users").ExpectStatus(http.StatusNotFound)
	if got := body(t, app.Get("/users", "API-Version", "2").ExpectStatus(http.StatusOK)); got != "users v2" {
		t.Errorf("got %q", got)
	}
}

func TestVersioningConfigIsChecked(t *testing.T) {
	for name, tt := range map[string]struct {
		config gonestly.VersioningConfig
		want   string
	}{
		"a default without a header":  {gonestly.VersioningConfig{Default: "1"}, "needs Versioning.Header"},
		"an unknown default":          {gonestly.VersioningConfig{Header: "API-Version", Default: "3"}, "no route is registered under it; the versions are 1, 2"},
		"a default that is no number": {gonestly.VersioningConfig{Header: "API-Version", Default: "new"}, "is not a number"},
	} {
		err := buildVersionError(t, gonestly.ServerConfig{Versioning: tt.config}, twoVersions)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.want)
		}
	}
}

func TestDeprecationHeaders(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sunset := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	app := startVersioned(t, gonestly.VersioningConfig{}, func(r *gonestly.Router) {
		r.Group("", func(r *gonestly.Router) {
			r.Version("1").Deprecate(gonestly.DeprecatedSince(since), gonestly.SunsetAt(sunset), gonestly.SuccessorAt("/v2/users"))
			gonestly.Get(r, "/users", reply("old"))
		})
		r.Group("", func(r *gonestly.Router) {
			r.Version("2")
			gonestly.Get(r, "/users", reply("new"))
			gonestly.Get(r, "/legacy", reply("legacy"), gonestly.Deprecated(gonestly.SunsetAt(sunset)))
			gonestly.Get(r, "/quiet", reply("quiet"), gonestly.Deprecated())
		})
	})
	old := app.Get("/v1/users").ExpectStatus(http.StatusOK)
	if old.Header.Get("Deprecation") != "@1767225600" || old.Header.Get("Sunset") != "Thu, 31 Dec 2026 23:59:59 GMT" ||
		old.Header.Get("Link") != `</v2/users>; rel="successor-version"` {
		t.Errorf("headers: %v", old.Header)
	}
	if h := app.Get("/v2/users").Header; h.Get("Deprecation") != "" || h.Get("Sunset") != "" {
		t.Errorf("a current route is deprecated: %v", h)
	}
	if h := app.Get("/v2/legacy").Header; h.Get("Deprecation") != "true" || h.Get("Sunset") == "" || h.Get("Link") != "" {
		t.Errorf("a route with a sunset: %v", h)
	}
	// Deprecated() without options only marks the document.
	if h := app.Get("/v2/quiet").Header; h.Get("Deprecation") != "" {
		t.Errorf("a plain Deprecated sent headers: %v", h)
	}

	var doc struct {
		Paths map[string]map[string]struct {
			Deprecated  bool   `json:"deprecated"`
			Description string `json:"description"`
		} `json:"paths"`
	}
	app.Get("/openapi.json").JSON(&doc)
	v1 := doc.Paths["/v1/users"]["get"]
	if !v1.Deprecated || !strings.Contains(v1.Description, "Deprecated since 2026-01-01. Stops working on 2026-12-31. Use /v2/users instead.") {
		t.Errorf("v1 in the document: %+v", v1)
	}
	if !doc.Paths["/v2/quiet"]["get"].Deprecated || doc.Paths["/v2/users"]["get"].Deprecated {
		t.Errorf("deprecations in the document: %+v", doc.Paths)
	}
}

func TestDeprecationIsChecked(t *testing.T) {
	for name, opts := range map[string][]gonestly.DeprecationOption{
		"sunset before since": {gonestly.DeprecatedSince(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)), gonestly.SunsetAt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
		"a bad successor":     {gonestly.SuccessorAt("v2/users")},
	} {
		err := buildVersionError(t, gonestly.ServerConfig{}, func(r *gonestly.Router) {
			gonestly.Get(r, "/x", reply("x"), gonestly.Deprecated(opts...))
		})
		var build *gonestly.BuildError
		if !errors.As(err, &build) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOpenAPIPerVersion(t *testing.T) {
	app := startVersioned(t, gonestly.VersioningConfig{}, twoVersions)
	paths := func(url string) []string {
		var doc struct {
			Paths map[string]json.RawMessage `json:"paths"`
		}
		app.Get(url).ExpectStatus(http.StatusOK).JSON(&doc)
		var list []string
		for p := range doc.Paths {
			list = append(list, p)
		}
		return list
	}
	if got := paths("/openapi.json"); len(got) != 4 {
		t.Errorf("all versions: %v", got)
	}
	for _, p := range paths("/openapi.json?version=1") {
		if !strings.HasPrefix(p, "/v1/") {
			t.Errorf("version 1 has %s", p)
		}
	}
	for _, p := range paths("/openapi.json?version=v2") {
		if !strings.HasPrefix(p, "/v2/") {
			t.Errorf("version 2 has %s", p)
		}
	}
	if p := app.Get("/openapi.json?version=7").Problem(); p.Status != http.StatusNotFound || !strings.Contains(p.Detail, "1, 2") {
		t.Errorf("problem = %+v", p)
	}
	page := string(app.Get("/docs").ExpectStatus(http.StatusOK).Body)
	for _, want := range []string{`<select id="version">`, `>Version 1</option>`, `value="openapi.json?version=2"`, `ui.specActions.download`} {
		if !strings.Contains(page, want) {
			t.Errorf("the docs page lacks %s:\n%s", want, page)
		}
	}
	// Without versions the page shows the one document as before.
	plain := startVersioned(t, gonestly.VersioningConfig{}, func(r *gonestly.Router) { gonestly.Get(r, "/x", reply("x")) })
	if page := string(plain.Get("/docs").Body); strings.Contains(page, "<select") || !strings.Contains(page, `url: "openapi.json"`) {
		t.Errorf("the plain docs page:\n%s", page)
	}
}
