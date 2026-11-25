package gonestly_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

type user struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type findUserRequest struct {
	ID      string `path:"id"`
	Verbose bool   `query:"verbose"`
	Tenant  string `header:"X-Tenant"`
}

type listUsersRequest struct {
	Page int      `query:"page"`
	Tags []string `query:"tag"`
}

type createUserRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (r createUserRequest) Validate() error {
	var errs []gonestly.FieldError
	if r.Name == "" {
		errs = append(errs, gonestly.FieldError{Field: "name", In: "body", Message: "is required"})
	}
	if r.Email != "" && !strings.Contains(r.Email, "@") {
		errs = append(errs, gonestly.FieldError{Field: "email", In: "body", Message: "must be an email address"})
	}
	if len(errs) > 0 {
		return gonestly.Invalid(errs...)
	}
	return nil
}

type updateUserRequest struct {
	ID   string `path:"id"`
	Name string `json:"name"`
}

type userController struct {
	logger *slog.Logger
}

func newUserController(logger *slog.Logger) *userController {
	return &userController{logger: logger}
}

func (c *userController) Routes(r *gonestly.Router) {
	r.Prefix("/users")
	gonestly.Get(r, "/", c.list)
	gonestly.Get(r, "/{id}", c.find)
	gonestly.Post(r, "/", c.create)
	gonestly.Put(r, "/{id}", c.update)
	gonestly.Patch(r, "/{id}", c.update, gonestly.Status(http.StatusAccepted))
	gonestly.Delete(r, "/{id}", c.remove)
}

func (c *userController) list(_ context.Context, req listUsersRequest) (map[string]any, error) {
	return map[string]any{"page": req.Page, "tags": req.Tags}, nil
}

func (c *userController) find(ctx context.Context, req findUserRequest) (*user, error) {
	switch req.ID {
	case "missing":
		return nil, gonestly.NotFound("user %s does not exist", req.ID)
	case "broken":
		return nil, errors.New("database password is hunter2")
	case "panic":
		panic("unexpected")
	}
	gonestly.ResponseHeader(ctx).Set("X-Tenant", req.Tenant)
	name := "Ada"
	if req.Verbose {
		name = "Ada Lovelace (" + gonestly.Request(ctx).Method + ")"
	}
	return &user{ID: req.ID, Name: name}, nil
}

func (c *userController) create(_ context.Context, req createUserRequest) (*user, error) {
	return &user{ID: "1", Name: req.Name, Email: req.Email}, nil
}

func (c *userController) update(_ context.Context, req updateUserRequest) (*user, error) {
	return &user{ID: req.ID, Name: req.Name}, nil
}

func (c *userController) remove(context.Context, findUserRequest) (gonestly.Empty, error) {
	return gonestly.Empty{}, nil
}

func usersModule() *gonestly.Module {
	return &gonestly.Module{Name: "users", Controllers: gonestly.Controllers(newUserController)}
}

func TestBindingAndResponses(t *testing.T) {
	app := gonestlytest.Start(t, usersModule())

	var got user
	res := app.Get("/users/42?verbose=true", "X-Tenant", "acme").ExpectStatus(http.StatusOK)
	res.JSON(&got)
	if got.ID != "42" || got.Name != "Ada Lovelace (GET)" {
		t.Errorf("got %+v", got)
	}
	if res.Header.Get("X-Tenant") != "acme" || res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("headers = %v", res.Header)
	}

	var list map[string]any
	app.Get("/users?page=2&tag=a&tag=b").ExpectStatus(http.StatusOK).JSON(&list)
	if fmt.Sprint(list) != "map[page:2 tags:[a b]]" {
		t.Errorf("list = %v", list)
	}

	app.Post("/users", map[string]string{"name": "Grace", "email": "grace@example.com"}).ExpectStatus(http.StatusCreated).JSON(&got)
	if got.Name != "Grace" || got.Email != "grace@example.com" {
		t.Errorf("created %+v", got)
	}

	app.Put("/users/7", map[string]string{"id": "injected", "name": "Linus"}).ExpectStatus(http.StatusOK).JSON(&got)
	if got.ID != "7" || got.Name != "Linus" {
		t.Errorf("updated %+v, the path value must win over the body", got)
	}

	app.Patch("/users/7", map[string]string{"name": "Linus"}).ExpectStatus(http.StatusAccepted)
	if res := app.Delete("/users/7").ExpectStatus(http.StatusNoContent); len(res.Body) != 0 {
		t.Errorf("a 204 response has a body: %q", res.Body)
	}
	if res := app.Request(http.MethodHead, "/users/7", nil).ExpectStatus(http.StatusOK); len(res.Body) != 0 {
		t.Errorf("a HEAD response has a body: %q", res.Body)
	}
}

func TestRequestErrors(t *testing.T) {
	app := gonestlytest.Start(t, &gonestly.Module{
		Name:      "app",
		Imports:   gonestly.Imports(usersModule()),
		Providers: gonestly.Providers(gonestly.Value(gonestly.ServerConfig{MaxBodyBytes: 64})),
	})

	tests := []struct {
		name   string
		res    *gonestlytest.Response
		status int
		detail string
		errors []gonestly.FieldError
	}{
		{
			name:   "invalid query value",
			res:    app.Get("/users?page=two"),
			status: http.StatusBadRequest,
			detail: "One or more fields are invalid.",
			errors: []gonestly.FieldError{{Field: "page", In: "query", Message: `"two" is not a whole number`}},
		},
		{
			name:   "validation",
			res:    app.Post("/users", map[string]string{"email": "nope"}),
			status: http.StatusBadRequest,
			errors: []gonestly.FieldError{
				{Field: "name", In: "body", Message: "is required"},
				{Field: "email", In: "body", Message: "must be an email address"},
			},
		},
		{
			name:   "wrong JSON type",
			res:    app.Request(http.MethodPost, "/users", `{"name": 5}`, "Content-Type", "application/json"),
			status: http.StatusBadRequest,
			errors: []gonestly.FieldError{{Field: "name", In: "body", Message: "must be a string"}},
		},
		{
			name:   "invalid JSON",
			res:    app.Request(http.MethodPost, "/users", `{"name":`, "Content-Type", "application/json"),
			status: http.StatusBadRequest,
			detail: "the request body is not valid JSON",
		},
		{
			name:   "two JSON values",
			res:    app.Request(http.MethodPost, "/users", `{"name":"a"} {}`, "Content-Type", "application/json"),
			status: http.StatusBadRequest,
			detail: "the request body must contain a single JSON value",
		},
		{
			name:   "body of the wrong JSON type",
			res:    app.Request(http.MethodPost, "/users", `[1]`, "Content-Type", "application/json"),
			status: http.StatusBadRequest,
			detail: "the request body must be a JSON object",
		},
		{
			name:   "wrong content type",
			res:    app.Request(http.MethodPost, "/users", "name=Ada", "Content-Type", "application/x-www-form-urlencoded"),
			status: http.StatusUnsupportedMediaType,
			detail: "send the request body as application/json",
		},
		{
			name:   "body too large",
			res:    app.Post("/users", map[string]string{"name": strings.Repeat("a", 100)}),
			status: http.StatusRequestEntityTooLarge,
			detail: "the request body is larger than 64 bytes",
		},
		{
			name:   "HTTP error from the handler",
			res:    app.Get("/users/missing"),
			status: http.StatusNotFound,
			detail: "user missing does not exist",
		},
		{
			name:   "unexpected error hides its cause",
			res:    app.Get("/users/broken"),
			status: http.StatusInternalServerError,
			detail: "An unexpected error occurred.",
		},
		{
			name:   "panic",
			res:    app.Get("/users/panic"),
			status: http.StatusInternalServerError,
			detail: "An unexpected error occurred.",
		},
		{
			name:   "unknown route",
			res:    app.Get("/nothing"),
			status: http.StatusNotFound,
			detail: "No route matches GET /nothing.",
		},
		{
			name:   "method not allowed",
			res:    app.Request(http.MethodPost, "/users/7", nil),
			status: http.StatusMethodNotAllowed,
			detail: "POST is not allowed for /users/7.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.res.Problem()
			if p.Status != tt.status || tt.res.Status != tt.status {
				t.Errorf("status = %d (body %d), want %d", tt.res.Status, p.Status, tt.status)
			}
			if tt.detail != "" && p.Detail != tt.detail {
				t.Errorf("detail = %q, want %q", p.Detail, tt.detail)
			}
			if tt.errors != nil && !slices.Equal(p.Errors, tt.errors) {
				t.Errorf("errors = %+v\nwant     %+v", p.Errors, tt.errors)
			}
			if p.Type != "about:blank" || p.Title == "" {
				t.Errorf("problem = %+v", p)
			}
			if strings.Contains(string(tt.res.Body), "hunter2") {
				t.Error("the response leaks the cause of an internal error")
			}
		})
	}

	if allow := app.Request(http.MethodPost, "/users/7", nil).Header.Get("Allow"); !strings.Contains(allow, "PUT") {
		t.Errorf("Allow = %q", allow)
	}
}

type ctxKey struct{}

type secureController struct {
	events *[]string
}

func (c *secureController) Routes(r *gonestly.Router) {
	auth := gonestly.GuardFunc(func(ctx context.Context, r *http.Request) (context.Context, error) {
		*c.events = append(*c.events, "guard")
		if r.Header.Get("Authorization") != "Bearer secret" {
			return nil, gonestly.Unauthorized("a valid token is required")
		}
		return context.WithValue(ctx, ctxKey{}, "ada"), nil
	})
	trace := func(name string) gonestly.Interceptor {
		return gonestly.InterceptorFunc(func(ctx context.Context, _ *http.Request, next gonestly.Next) (any, error) {
			*c.events = append(*c.events, "before "+name)
			result, err := next(ctx)
			*c.events = append(*c.events, "after "+name)
			return result, err
		})
	}
	header := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Router", "yes")
			next.ServeHTTP(w, r)
		})
	}

	r.Prefix("/secure").Use(header).Guard(auth).Intercept(trace("router"))
	gonestly.Get(r, "/me", func(ctx context.Context, _ gonestly.Empty) (map[string]string, error) {
		*c.events = append(*c.events, "handler")
		return map[string]string{"user": ctx.Value(ctxKey{}).(string)}, nil
	}, gonestly.WithInterceptors(trace("route")))

	r.Group("/admin", func(r *gonestly.Router) {
		r.Guard(gonestly.GuardFunc(func(context.Context, *http.Request) (context.Context, error) {
			return nil, gonestly.Forbidden("admins only")
		}))
		gonestly.Get(r, "/", func(context.Context, gonestly.Empty) (gonestly.Empty, error) { return gonestly.Empty{}, nil })
	})

	gonestly.Get(r, "/cached", func(context.Context, gonestly.Empty) (string, error) { return "fresh", nil },
		gonestly.WithInterceptors(gonestly.InterceptorFunc(func(context.Context, *http.Request, gonestly.Next) (any, error) {
			return "cached", nil
		})))

	gonestly.Handle(r, http.MethodGet, "/file", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "raw for "+r.Context().Value(ctxKey{}).(string))
	}))
}

func TestGuardsInterceptorsAndMiddleware(t *testing.T) {
	var events []string
	global := gonestly.ServerConfig{
		Guards: []gonestly.Guard{gonestly.GuardFunc(func(ctx context.Context, _ *http.Request) (context.Context, error) {
			events = append(events, "global guard")
			return ctx, nil
		})},
		Interceptors: []gonestly.Interceptor{gonestly.InterceptorFunc(func(ctx context.Context, _ *http.Request, next gonestly.Next) (any, error) {
			events = append(events, "before global")
			return next(ctx)
		})},
		Middleware: []gonestly.Middleware{func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Global", "yes")
				next.ServeHTTP(w, r)
			})
		}},
	}
	root := &gonestly.Module{
		Name:        "app",
		Providers:   gonestly.Providers(gonestly.Value(global)),
		Controllers: gonestly.Controllers(func() *secureController { return &secureController{events: &events} }),
	}
	app := gonestlytest.Start(t, root)

	res := app.Get("/secure/me", "Authorization", "Bearer secret").ExpectStatus(http.StatusOK)
	if string(res.Body) != "{\"user\":\"ada\"}\n" {
		t.Errorf("body = %q", res.Body)
	}
	want := "global guard,guard,before global,before router,before route,handler,after route,after router"
	if got := strings.Join(events, ","); got != want {
		t.Errorf("events = %s\nwant     %s", got, want)
	}
	if res.Header.Get("X-Global") != "yes" || res.Header.Get("X-Router") != "yes" {
		t.Errorf("middleware did not run: %v", res.Header)
	}

	events = nil
	if p := app.Get("/secure/me").Problem(); p.Status != http.StatusUnauthorized || p.Detail != "a valid token is required" {
		t.Errorf("problem = %+v", p)
	}
	if slices.Contains(events, "handler") {
		t.Error("the handler ran for a rejected request")
	}
	if p := app.Get("/secure/admin", "Authorization", "Bearer secret").Problem(); p.Status != http.StatusForbidden {
		t.Errorf("problem = %+v", p)
	}

	var cached string
	app.Get("/secure/cached", "Authorization", "Bearer secret").JSON(&cached)
	if cached != "cached" {
		t.Errorf("the interceptor did not answer: %q", cached)
	}
	if body := string(app.Get("/secure/file", "Authorization", "Bearer secret").ExpectStatus(http.StatusOK).Body); body != "raw for ada" {
		t.Errorf("raw body = %q", body)
	}
	if app.Get("/missing").Header.Get("X-Global") != "yes" {
		t.Error("global middleware does not run for unknown routes")
	}
}

type tagValidator struct{}

func (tagValidator) ValidateStruct(v any) error {
	if r, ok := v.(createUserRequest); ok && r.Name == "forbidden" {
		return errors.New("this name is not allowed")
	}
	return nil
}

func TestStructValidator(t *testing.T) {
	root := &gonestly.Module{
		Name:      "app",
		Imports:   gonestly.Imports(usersModule()),
		Providers: gonestly.Providers(gonestly.Value[gonestly.StructValidator](tagValidator{})),
	}
	app := gonestlytest.Start(t, root)
	if p := app.Post("/users", map[string]string{"name": "forbidden"}).Problem(); p.Status != http.StatusBadRequest || p.Detail != "this name is not allowed" {
		t.Errorf("problem = %+v", p)
	}
	app.Post("/users", map[string]string{"name": "allowed"}).ExpectStatus(http.StatusCreated)
}

type rootController struct{}

func (rootController) Routes(r *gonestly.Router) {
	gonestly.Get(r, "/", func(context.Context, gonestly.Empty) (string, error) { return "home", nil })
	gonestly.Post(r, "/items/", func(_ context.Context, items []string) (int, error) { return len(items), nil }, gonestly.Status(http.StatusOK))
}

func TestPaths(t *testing.T) {
	app := gonestlytest.Start(t, &gonestly.Module{Name: "app", Controllers: gonestly.Controllers(func() rootController { return rootController{} })})
	app.Get("/").ExpectStatus(http.StatusOK)
	app.Get("/other").ExpectStatus(http.StatusNotFound)
	var count int
	app.Post("/items", []string{"a", "b"}).ExpectStatus(http.StatusOK).JSON(&count)
	if count != 2 {
		t.Errorf("count = %d", count)
	}
	app.Post("/items/extra", nil).ExpectStatus(http.StatusNotFound)

	want := []gonestly.RouteInfo{
		{Method: "GET", Path: "/", Controller: `gonestly_test.TestPaths.func1 in module "app"`},
		{Method: "POST", Path: "/items", Controller: `gonestly_test.TestPaths.func1 in module "app"`},
	}
	if got := app.Routes(); !slices.Equal(got, want) {
		t.Errorf("routes = %+v", got)
	}
}

type badController struct {
	routes func(r *gonestly.Router)
}

func (c badController) Routes(r *gonestly.Router) { c.routes(r) }

func startError(t *testing.T, routes func(r *gonestly.Router)) error {
	t.Helper()
	root := &gonestly.Module{Name: "app", Controllers: gonestly.Controllers(func() badController { return badController{routes} })}
	app, err := gonestly.New(root, quietLogger(), gonestly.WithoutListener())
	if err != nil {
		t.Fatal(err)
	}
	err = app.Start(t.Context())
	if err == nil {
		t.Fatal("Start succeeded, want an error")
	}
	return err
}

func TestInvalidRoutes(t *testing.T) {
	type wrongPath struct {
		ID string `path:"id"`
	}
	type badField struct {
		Filter map[string]string `query:"filter"`
		Both   string            `query:"a" header:"b"`
		Empty  string            `path:""`
	}
	handler := func(context.Context, gonestly.Empty) (gonestly.Empty, error) { return gonestly.Empty{}, nil }

	tests := []struct {
		name   string
		routes func(r *gonestly.Router)
		parts  []string
	}{
		{
			name: "path value without wildcard",
			routes: func(r *gonestly.Router) {
				gonestly.Get(r, "/users/{userId}", func(context.Context, wrongPath) (string, error) { return "", nil })
			},
			parts: []string{`GET /users/{userId} in gonestly_test.startError.func1 in module "app": wrongPath.ID reads the path value "id", but the path /users/{userId} has no {id} wildcard`},
		},
		{
			name: "unsupported fields",
			routes: func(r *gonestly.Router) {
				gonestly.Get(r, "/", func(context.Context, badField) (string, error) { return "", nil })
			},
			parts: []string{
				"badField.Filter has type map[string]string, which cannot be read from the query",
				"badField.Both has more than one of the tags query, header",
				"badField.Empty has an empty path tag",
			},
		},
		{
			name: "pointer request",
			routes: func(r *gonestly.Router) {
				gonestly.Get(r, "/", func(context.Context, *wrongPath) (string, error) { return "", nil })
			},
			parts: []string{"the request type *gonestly_test.wrongPath must be a struct, a slice or a map"},
		},
		{
			name:   "status",
			routes: func(r *gonestly.Router) { gonestly.Get(r, "/", handler, gonestly.Status(http.StatusNotFound)) },
			parts:  []string{"the status 404 is not a success status"},
		},
		{
			name: "nil handlers",
			routes: func(r *gonestly.Router) {
				gonestly.Get[gonestly.Empty, gonestly.Empty](r, "/a", nil)
				gonestly.Handle(r, http.MethodGet, "/b", nil)
			},
			parts: []string{"GET /a in", "GET /b in", "the handler is nil"},
		},
		{
			name: "conflicting routes",
			routes: func(r *gonestly.Router) {
				gonestly.Get(r, "/users/{id}", func(context.Context, wrongPath) (string, error) { return "", nil })
				gonestly.Get(r, "/users/{id}", func(context.Context, wrongPath) (string, error) { return "", nil })
			},
			parts: []string{"conflicts with pattern"},
		},
		{
			name:   "panicking Routes",
			routes: func(*gonestly.Router) { panic("boom") },
			parts:  []string{"the Routes method of gonestly_test.startError.func1 in module \"app\" panicked: boom"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := startError(t, tt.routes)
			if !errors.Is(err, gonestly.ErrInvalidRoute) {
				t.Errorf("error is not ErrInvalidRoute: %v", err)
			}
			assertContains(t, err, tt.parts...)
		})
	}
}

func TestInvalidControllers(t *testing.T) {
	tests := []struct {
		name string
		root *gonestly.Module
		part string
	}{
		{
			"without Routes",
			&gonestly.Module{Name: "app", Controllers: gonestly.Controllers(newDatabase)},
			`controller gonestly_test.newDatabase returns *gonestly_test.database, which has no Routes(*gonestly.Router) method`,
		},
		{
			"twice",
			&gonestly.Module{Name: "app", Controllers: gonestly.Controllers(newUserController, newUserController)},
			`module "app" has two controllers of *gonestly_test.userController`,
		},
		{"empty", &gonestly.Module{Name: "app", Controllers: []gonestly.Provider{{}}}, "has an empty controller"},
		{"invalid", &gonestly.Module{Name: "app", Controllers: gonestly.Controllers(42)}, `module "app": controller: int is not a constructor function`},
		{
			"injected",
			&gonestly.Module{
				Name:        "app",
				Providers:   gonestly.Providers(func(*userController) *database { return &database{} }),
				Controllers: gonestly.Controllers(newUserController),
			},
			"needs *gonestly_test.userController, which is not available there",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertContains(t, buildError(t, tt.root), tt.part)
		})
	}
}

func TestControllersInjectDependencies(t *testing.T) {
	db := &gonestly.Module{Name: "database", Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}
	root := &gonestly.Module{
		Name:    "app",
		Imports: gonestly.Imports(db),
		Controllers: gonestly.Controllers(func(d *database) *badController {
			return &badController{routes: func(r *gonestly.Router) {
				gonestly.Get(r, "/dsn", func(context.Context, gonestly.Empty) (string, error) { return d.dsn, nil })
			}}
		}),
	}
	var dsn string
	gonestlytest.Start(t, root).Get("/dsn").ExpectStatus(http.StatusOK).JSON(&dsn)
	if dsn != "postgres://localhost/app" {
		t.Errorf("dsn = %q", dsn)
	}
}

func TestServerListens(t *testing.T) {
	root := &gonestly.Module{
		Name:      "app",
		Imports:   gonestly.Imports(usersModule()),
		Providers: gonestly.Providers(gonestly.Value(gonestly.ServerConfig{Addr: "127.0.0.1:0"})),
	}
	app, err := gonestly.New(root, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	addr := app.Addr().String()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/users/1", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d", res.StatusCode)
	}

	// A second application on the same address fails to start and stops what it started.
	var events []string
	second := &gonestly.Module{
		Name:      "app",
		Imports:   gonestly.Imports(usersModule()),
		Providers: gonestly.Providers(gonestly.Value(gonestly.ServerConfig{Addr: addr}), func() *first { return &first{&recorder{&events, "first", ""}} }),
	}
	other, err := gonestly.New(second, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, other.Start(t.Context()), "listening on "+addr)
	if got := strings.Join(events, ","); got != "start first,stop first" {
		t.Errorf("events = %s", got)
	}

	if err := app.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/users/1", nil)
	if res, err := http.DefaultClient.Do(req); err == nil {
		_ = res.Body.Close()
		t.Error("the server still answers after Stop")
	}
}

func TestAppWithoutControllers(t *testing.T) {
	app := mustStart(t, &gonestly.Module{Name: "app"})
	if app.Handler() != nil || app.Routes() != nil || app.Addr() != nil {
		t.Error("an application without controllers has an HTTP server")
	}
}

func TestRequestHelpersOutsideRequests(t *testing.T) {
	if gonestly.Request(context.Background()) != nil {
		t.Error("Request outside a request is not nil")
	}
	gonestly.ResponseHeader(context.Background()).Set("X", "y")
}

func TestHTTPErrors(t *testing.T) {
	cause := errors.New("cause")
	err := gonestly.Conflict("name %q is taken", "ada").WithCause(cause)
	if err.Error() != `409 Conflict: name "ada" is taken: cause` || !errors.Is(err, cause) {
		t.Errorf("err = %v", err)
	}
	for status, err := range map[int]*gonestly.HTTPError{
		400: gonestly.BadRequest("x"),
		401: gonestly.Unauthorized("x"),
		403: gonestly.Forbidden("x"),
		404: gonestly.NotFound("x"),
		422: gonestly.UnprocessableEntity("x"),
		429: gonestly.TooManyRequests("x"),
		418: gonestly.StatusError(418, "x"),
	} {
		if err.Status != status {
			t.Errorf("status = %d, want %d", err.Status, status)
		}
	}
	if (&gonestly.HTTPError{Status: 599}).Error() != "599 Error" {
		t.Error("unknown status has no fallback title")
	}
}

func TestInspect(t *testing.T) {
	for _, mode := range []string{"routes", "openapi", "nonsense"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(gonestly.InspectEnv, mode)
			app, err := gonestly.New(usersModule(), quietLogger())
			if err != nil {
				t.Fatal(err)
			}
			stdout := captureStdout(t, func() { err = app.Run(t.Context()) })
			switch mode {
			case "routes":
				if err != nil || !strings.Contains(stdout, `{"method":"GET","path":"/users/{id}","controller":"gonestly_test.newUserController in module \"users\""}`) {
					t.Errorf("err = %v, stdout = %s", err, stdout)
				}
			case "openapi":
				if err != nil || !strings.HasPrefix(stdout, `{"openapi":"3.1.0"`) {
					t.Errorf("err = %v, stdout = %.80s", err, stdout)
				}
			default:
				assertContains(t, err, `GONESTLY_INSPECT must be routes or openapi, not "nonsense"`)
			}
			if app.Addr() != nil {
				t.Error("the application listened while inspecting")
			}
		})
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = original }()
	fn()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}
