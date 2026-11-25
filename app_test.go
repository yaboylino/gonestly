package gonestly_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
)

type (
	database    struct{ dsn string }
	repository  struct{ db *database }
	service     struct{ repo *repository }
	userStore   interface{ Find() string }
	memoryStore struct{}
)

func (memoryStore) Find() string { return "memory" }

func newDatabase() *database                        { return &database{dsn: "postgres://localhost/app"} }
func newRepository(db *database) *repository        { return &repository{db: db} }
func newService(repo *repository) *service          { return &service{repo: repo} }
func newMemoryStore() *memoryStore                  { return &memoryStore{} }
func newStoreUser(s userStore) *storeUser           { return &storeUser{store: s} }
func newRepositoryFromService(*service) *repository { return &repository{} }

type storeUser struct{ store userStore }

func quietLogger() gonestly.Option {
	return gonestly.WithLogger(slog.New(slog.DiscardHandler))
}

func mustStart(t *testing.T, root *gonestly.Module, opts ...gonestly.Option) *gonestly.App {
	t.Helper()
	app, err := gonestly.New(root, append([]gonestly.Option{quietLogger()}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	return app
}

func mustResolve[T any](t *testing.T, app *gonestly.App) T {
	t.Helper()
	v, err := gonestly.Resolve[T](app)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return v
}

func buildError(t *testing.T, root *gonestly.Module, opts ...gonestly.Option) error {
	t.Helper()
	_, err := gonestly.New(root, append([]gonestly.Option{quietLogger()}, opts...)...)
	if err == nil {
		t.Fatal("New succeeded, want an error")
	}
	var be *gonestly.BuildError
	if !errors.As(err, &be) {
		t.Fatalf("New returned %T, want *gonestly.BuildError", err)
	}
	return err
}

func assertContains(t *testing.T, err error, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error does not contain %q:\n%v", part, err)
		}
	}
}

func TestResolvesAcrossModules(t *testing.T) {
	dbModule := &gonestly.Module{Name: "database", Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}
	usersModule := &gonestly.Module{
		Name:      "users",
		Imports:   gonestly.Imports(dbModule),
		Providers: gonestly.Providers(newRepository, newService),
		Exports:   gonestly.Exports(newService),
	}
	root := &gonestly.Module{Name: "app", Imports: gonestly.Imports(usersModule, dbModule)}

	app := mustStart(t, root)
	svc := mustResolve[*service](t, app)
	db := mustResolve[*database](t, app)
	if svc.repo.db != db {
		t.Error("the database is not shared between modules")
	}
	if db.dsn != "postgres://localhost/app" {
		t.Errorf("dsn = %q", db.dsn)
	}
}

func TestReExportsImportedModule(t *testing.T) {
	dbModule := &gonestly.Module{Name: "database", Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}
	coreModule := &gonestly.Module{Name: "core", Imports: gonestly.Imports(dbModule), Exports: gonestly.Exports(dbModule)}
	root := &gonestly.Module{Name: "app", Imports: gonestly.Imports(coreModule), Providers: gonestly.Providers(newRepository)}

	app := mustStart(t, root)
	if mustResolve[*repository](t, app).db == nil {
		t.Error("the re-exported database was not injected")
	}
}

func TestGlobalModuleIsVisibleEverywhere(t *testing.T) {
	dbModule := &gonestly.Module{Name: "database", Global: true, Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}
	usersModule := &gonestly.Module{Name: "users", Providers: gonestly.Providers(newRepository), Exports: gonestly.Exports(newRepository)}
	root := &gonestly.Module{Name: "app", Imports: gonestly.Imports(dbModule, usersModule)}

	app := mustStart(t, root)
	if mustResolve[*repository](t, app).db == nil {
		t.Error("the global database was not injected")
	}
}

func TestOwnProviderTakesPrecedenceOverImport(t *testing.T) {
	local := &database{dsn: "local"}
	dbModule := &gonestly.Module{Name: "database", Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}
	root := &gonestly.Module{
		Name:      "app",
		Imports:   gonestly.Imports(dbModule),
		Providers: gonestly.Providers(gonestly.Value(local), newRepository),
	}

	app := mustStart(t, root)
	if mustResolve[*repository](t, app).db != local {
		t.Error("the module's own provider did not take precedence")
	}
}

func TestAsProvidesInterface(t *testing.T) {
	root := &gonestly.Module{
		Name:      "app",
		Providers: gonestly.Providers(gonestly.As[userStore](newMemoryStore), newStoreUser),
		Exports:   gonestly.Exports(newMemoryStore),
	}
	app := mustStart(t, root)
	if got := mustResolve[*storeUser](t, app).store.Find(); got != "memory" {
		t.Errorf("Find() = %q", got)
	}
}

func TestValueAsInterface(t *testing.T) {
	root := &gonestly.Module{
		Name:      "app",
		Providers: gonestly.Providers(gonestly.Value[userStore](memoryStore{}), newStoreUser),
	}
	app := mustStart(t, root)
	if mustResolve[*storeUser](t, app).store == nil {
		t.Error("the value was not injected")
	}
}

func TestInjectsContextAndLogger(t *testing.T) {
	type key struct{}
	var gotCtx context.Context
	var gotLogger *slog.Logger
	root := &gonestly.Module{
		Name: "app",
		Providers: gonestly.Providers(func(ctx context.Context, logger *slog.Logger) *database {
			gotCtx, gotLogger = ctx, logger
			return &database{}
		}),
	}
	logger := slog.New(slog.DiscardHandler)
	app, err := gonestly.New(root, gonestly.WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), key{}, "value")
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if gotCtx.Value(key{}) != "value" {
		t.Error("the start context was not injected")
	}
	if gotLogger != logger {
		t.Error("the configured logger was not injected")
	}
}

func TestMissingDependency(t *testing.T) {
	tests := []struct {
		name  string
		root  func() *gonestly.Module
		parts []string
	}{
		{
			name: "not provided anywhere",
			root: func() *gonestly.Module {
				return &gonestly.Module{Name: "app", Providers: gonestly.Providers(newRepository)}
			},
			parts: []string{`gonestly_test.newRepository in module "app" needs *gonestly_test.database`, `no module provides it`},
		},
		{
			name: "exported but not imported",
			root: func() *gonestly.Module {
				db := &gonestly.Module{Name: "database", Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}
				users := &gonestly.Module{Name: "users", Providers: gonestly.Providers(newRepository)}
				return &gonestly.Module{Name: "app", Imports: gonestly.Imports(db, users)}
			},
			parts: []string{`module "database" exports it: add that module to the Imports of "users"`},
		},
		{
			name: "provided but not exported",
			root: func() *gonestly.Module {
				db := &gonestly.Module{Name: "database", Providers: gonestly.Providers(newDatabase)}
				return &gonestly.Module{Name: "app", Imports: gonestly.Imports(db), Providers: gonestly.Providers(newRepository)}
			},
			parts: []string{`module "database" provides it but does not export it: add it to the Exports of "database"`},
		},
		{
			name: "value instead of pointer",
			root: func() *gonestly.Module {
				return &gonestly.Module{
					Name:      "app",
					Providers: gonestly.Providers(gonestly.Value(database{}), newRepository),
				}
			},
			parts: []string{`gonestly_test.database is available: did you mean that type?`},
		},
		{
			name: "implementation provided as concrete type",
			root: func() *gonestly.Module {
				return &gonestly.Module{Name: "app", Providers: gonestly.Providers(newMemoryStore, newStoreUser)}
			},
			parts: []string{`*gonestly_test.memoryStore implements it: provide it with gonestly.As[gonestly_test.userStore](gonestly_test.newMemoryStore)`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := buildError(t, tt.root())
			if !errors.Is(err, gonestly.ErrMissingDependency) {
				t.Errorf("error is not ErrMissingDependency: %v", err)
			}
			assertContains(t, err, tt.parts...)
		})
	}
}

func TestReportsAllProblemsAtOnce(t *testing.T) {
	root := &gonestly.Module{Name: "app", Providers: gonestly.Providers(newRepository, newStoreUser)}
	err := buildError(t, root)
	var be *gonestly.BuildError
	errors.As(err, &be)
	if len(be.Problems) != 2 {
		t.Errorf("got %d problems, want 2:\n%v", len(be.Problems), err)
	}
}

func TestDependencyCycle(t *testing.T) {
	root := &gonestly.Module{Name: "app", Providers: gonestly.Providers(newRepositoryFromService, newService)}
	err := buildError(t, root)
	if !errors.Is(err, gonestly.ErrDependencyCycle) {
		t.Errorf("error is not ErrDependencyCycle: %v", err)
	}
	assertContains(t, err, "gonestly_test.newRepositoryFromService -> gonestly_test.newService -> gonestly_test.newRepositoryFromService")
}

func TestModuleImportCycle(t *testing.T) {
	a := &gonestly.Module{Name: "a"}
	b := &gonestly.Module{Name: "b", Imports: gonestly.Imports(a)}
	a.Imports = gonestly.Imports(b)
	err := buildError(t, &gonestly.Module{Name: "app", Imports: gonestly.Imports(a)})
	if !errors.Is(err, gonestly.ErrDependencyCycle) {
		t.Errorf("error is not ErrDependencyCycle: %v", err)
	}
	assertContains(t, err, `modules import each other: "a" -> "b" -> "a"`)
}

func TestInvalidModules(t *testing.T) {
	tests := []struct {
		name string
		root *gonestly.Module
		part string
	}{
		{"nil root", nil, "the root module is nil"},
		{"root without name", &gonestly.Module{}, "the root module has no name"},
		{"import without name", &gonestly.Module{Name: "app", Imports: gonestly.Imports(&gonestly.Module{})}, `a module imported by "app" has no name`},
		{"nil import", &gonestly.Module{Name: "app", Imports: gonestly.Imports(nil)}, `module "app" imports a nil module`},
		{
			"duplicate module name",
			&gonestly.Module{Name: "app", Imports: gonestly.Imports(&gonestly.Module{Name: "db"}, &gonestly.Module{Name: "db"})},
			`two different modules are named "db"`,
		},
		{"reserved module name", &gonestly.Module{Name: "gonestly"}, `two different modules are named "gonestly"`},
		{"not a function", &gonestly.Module{Name: "app", Providers: gonestly.Providers(42)}, "int is not a constructor function"},
		{"nil function", &gonestly.Module{Name: "app", Providers: gonestly.Providers((func() *database)(nil))}, "constructor func() *gonestly_test.database is nil"},
		{"no result", &gonestly.Module{Name: "app", Providers: gonestly.Providers(func() {})}, "must return a value, or a value and an error"},
		{"only an error", &gonestly.Module{Name: "app", Providers: gonestly.Providers(func() error { return nil })}, "must return a value, or a value and an error"},
		{"variadic", &gonestly.Module{Name: "app", Providers: gonestly.Providers(func(...int) *database { return nil })}, "must not be variadic"},
		{"empty provider", &gonestly.Module{Name: "app", Providers: []gonestly.Provider{{}}}, "has an empty provider"},
		{"nil value", &gonestly.Module{Name: "app", Providers: gonestly.Providers(gonestly.Value[*database](nil))}, "value *gonestly_test.database is nil"},
		{
			"as with unrelated type",
			&gonestly.Module{Name: "app", Providers: gonestly.Providers(gonestly.As[userStore](newDatabase))},
			"returns *gonestly_test.database, which cannot be provided as gonestly_test.userStore",
		},
		{
			"duplicate provider",
			&gonestly.Module{Name: "app", Providers: gonestly.Providers(newDatabase, gonestly.Value(&database{}))},
			`module "app" provides *gonestly_test.database twice`,
		},
		{
			"export of unknown provider",
			&gonestly.Module{Name: "app", Exports: gonestly.Exports(newDatabase)},
			`module "app" exports gonestly_test.newDatabase, which is not one of its providers`,
		},
		{
			"export of unknown type",
			&gonestly.Module{Name: "app", Exports: gonestly.Exports(gonestly.Type[*database]())},
			`module "app" exports *gonestly_test.database, which is not one of its providers`,
		},
		{
			"export of module that is not imported",
			&gonestly.Module{Name: "app", Exports: gonestly.Exports(&gonestly.Module{Name: "db"})},
			`module "app" exports module "db", which it does not import`,
		},
		{"export of nil module", &gonestly.Module{Name: "app", Exports: gonestly.Exports((*gonestly.Module)(nil))}, "cannot export a nil module"},
		{"export of a value", &gonestly.Module{Name: "app", Exports: gonestly.Exports("db")}, "cannot export string"},
		{"empty export", &gonestly.Module{Name: "app", Exports: []gonestly.Export{{}}}, "has an empty export"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := buildError(t, tt.root)
			if !errors.Is(err, gonestly.ErrInvalidModule) {
				t.Errorf("error is not ErrInvalidModule: %v", err)
			}
			assertContains(t, err, tt.part)
		})
	}
}

func TestAmbiguousProviders(t *testing.T) {
	a := &gonestly.Module{Name: "a", Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}
	b := &gonestly.Module{Name: "b", Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}

	err := buildError(t, &gonestly.Module{Name: "app", Imports: gonestly.Imports(a, b)})
	assertContains(t, err, `module "app" imports two providers of *gonestly_test.database`)

	ga := &gonestly.Module{Name: "a", Global: true, Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}
	gb := &gonestly.Module{Name: "b", Global: true, Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}
	err = buildError(t, &gonestly.Module{Name: "app", Imports: gonestly.Imports(ga, gb)})
	assertContains(t, err, "two global modules export *gonestly_test.database")
}

func TestOverride(t *testing.T) {
	fake := &database{dsn: "fake"}
	dbModule := &gonestly.Module{Name: "database", Providers: gonestly.Providers(newDatabase), Exports: gonestly.Exports(newDatabase)}
	root := &gonestly.Module{Name: "app", Imports: gonestly.Imports(dbModule), Providers: gonestly.Providers(newRepository)}

	app := mustStart(t, root, gonestly.Override(gonestly.Value(fake)))
	if mustResolve[*repository](t, app).db != fake {
		t.Error("the override was not used")
	}

	err := buildError(t, root, gonestly.Override(newMemoryStore))
	assertContains(t, err, "override gonestly_test.newMemoryStore replaces no provider of *gonestly_test.memoryStore")

	err = buildError(t, root, gonestly.Override(gonestly.Value(fake), newDatabase))
	assertContains(t, err, "*gonestly_test.database is overridden twice")

	err = buildError(t, root, gonestly.Override(42))
	assertContains(t, err, "override: int is not a constructor function")
}

type recorder struct {
	events *[]string
	name   string
	fail   string
}

func (r *recorder) OnStart(context.Context) error {
	if r.fail == "start" {
		return errors.New("start failed")
	}
	*r.events = append(*r.events, "start "+r.name)
	return nil
}

func (r *recorder) OnStop(context.Context) error {
	*r.events = append(*r.events, "stop "+r.name)
	if r.fail == "stop" {
		return errors.New("stop failed")
	}
	return nil
}

type (
	first  struct{ *recorder }
	second struct{ *recorder }
	third  struct{ *recorder }
)

func lifecycleModule(events *[]string, failSecond, failThird string) *gonestly.Module {
	return &gonestly.Module{
		Name: "app",
		Providers: gonestly.Providers(
			func(*second) *third { return &third{&recorder{events, "third", failThird}} },
			func(*first) *second { return &second{&recorder{events, "second", failSecond}} },
			func() *first { return &first{&recorder{events, "first", ""}} },
		),
	}
}

func TestLifecycleOrder(t *testing.T) {
	var events []string
	app, err := gonestly.New(lifecycleModule(&events, "", ""), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := "start first,start second,start third,stop third,stop second,stop first"
	if got := strings.Join(events, ","); got != want {
		t.Errorf("events = %s\nwant     %s", got, want)
	}
	if err := app.Stop(t.Context()); err != nil {
		t.Errorf("a second Stop returned %v", err)
	}
	if err := app.Start(t.Context()); err == nil {
		t.Error("starting a stopped application succeeded")
	}
}

func TestStartFailureStopsStartedProviders(t *testing.T) {
	var events []string
	app, err := gonestly.New(lifecycleModule(&events, "", "start"), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	err = app.Start(t.Context())
	assertContains(t, err, `starting gonestly_test.lifecycleModule.func1 in module "app": start failed`)
	want := "start first,start second,stop second,stop first"
	if got := strings.Join(events, ","); got != want {
		t.Errorf("events = %s\nwant     %s", got, want)
	}
}

func TestConstructorFailures(t *testing.T) {
	tests := []struct {
		name string
		ctor any
		part string
	}{
		{"error", func() (*database, error) { return nil, errors.New("connection refused") }, "connection refused"},
		{"panic", func() *database { panic("boom") }, "panic: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []string
			root := &gonestly.Module{
				Name:      "app",
				Providers: gonestly.Providers(func() *first { return &first{&recorder{&events, "first", ""}} }, tt.ctor),
			}
			app, err := gonestly.New(root, quietLogger())
			if err != nil {
				t.Fatal(err)
			}
			err = app.Start(t.Context())
			assertContains(t, err, "constructing", `in module "app"`, tt.part)
			if got := strings.Join(events, ","); got != "start first,stop first" {
				t.Errorf("events = %s", got)
			}
		})
	}
}

func TestStopCollectsErrors(t *testing.T) {
	var events []string
	app, err := gonestly.New(lifecycleModule(&events, "stop", "stop"), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	err = app.Stop(t.Context())
	if got := strings.Count(err.Error(), "stop failed"); got != 2 {
		t.Errorf("got %d stop errors, want 2: %v", got, err)
	}
	if got := strings.Join(events, ","); !strings.HasSuffix(got, "stop third,stop second,stop first") {
		t.Errorf("not every provider was stopped: %s", got)
	}
}

func TestResolve(t *testing.T) {
	root := &gonestly.Module{Name: "app", Providers: gonestly.Providers(newDatabase)}
	app, err := gonestly.New(root, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gonestly.Resolve[*database](app); !errors.Is(err, gonestly.ErrNotStarted) {
		t.Errorf("Resolve before Start = %v, want ErrNotStarted", err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := gonestly.Resolve[*service](app); !errors.Is(err, gonestly.ErrMissingDependency) {
		t.Errorf("Resolve of an unknown type = %v, want ErrMissingDependency", err)
	}
}

type shutdownOnStart struct {
	shutdowner gonestly.Shutdowner
	stopped    bool
}

func (s *shutdownOnStart) OnStart(context.Context) error {
	go s.shutdowner.Shutdown(errors.New("server failed"))
	return nil
}

func (s *shutdownOnStart) OnStop(context.Context) error {
	s.stopped = true
	return nil
}

func TestRunStopsOnShutdown(t *testing.T) {
	var component *shutdownOnStart
	root := &gonestly.Module{
		Name: "app",
		Providers: gonestly.Providers(func(sd gonestly.Shutdowner) *shutdownOnStart {
			component = &shutdownOnStart{shutdowner: sd}
			return component
		}),
	}
	app, err := gonestly.New(root, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	err = app.Run(t.Context())
	assertContains(t, err, "server failed")
	if !component.stopped {
		t.Error("the component was not stopped")
	}
}

func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	var events []string
	app, err := gonestly.New(lifecycleModule(&events, "", ""), quietLogger(), gonestly.WithShutdownTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := app.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Join(events, ","); !strings.HasSuffix(got, "stop first") {
		t.Errorf("the application was not stopped: %s", got)
	}
}

func TestRunReturnsStartError(t *testing.T) {
	var events []string
	app, err := gonestly.New(lifecycleModule(&events, "start", ""), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, app.Run(t.Context()), "start failed")
}

type blockingStopper struct{ release chan struct{} }

func (b *blockingStopper) OnStop(context.Context) error {
	<-b.release
	return nil
}

func TestRunEnforcesShutdownTimeout(t *testing.T) {
	blocker := &blockingStopper{release: make(chan struct{})}
	t.Cleanup(func() { close(blocker.release) })
	root := &gonestly.Module{Name: "app", Providers: gonestly.Providers(gonestly.Value(blocker))}
	app, err := gonestly.New(root, quietLogger(), gonestly.WithShutdownTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	begin := time.Now()
	err = app.Run(ctx)
	assertContains(t, err, "the application did not stop within 50ms")
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Errorf("Run took %s, want it to give up after the shutdown timeout", elapsed)
	}
}

type panickingHooks struct{ on string }

func (p *panickingHooks) OnStart(context.Context) error {
	if p.on == "start" {
		panic("start exploded")
	}
	return nil
}

func (p *panickingHooks) OnStop(context.Context) error {
	if p.on == "stop" {
		panic("stop exploded")
	}
	return nil
}

func TestHookPanicsBecomeErrors(t *testing.T) {
	var events []string
	newFirst := func() *first { return &first{&recorder{&events, "first", ""}} }

	root := &gonestly.Module{Name: "app", Providers: gonestly.Providers(newFirst, func(*first) *panickingHooks { return &panickingHooks{on: "start"} })}
	app, err := gonestly.New(root, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, app.Start(t.Context()), "starting", "panic: start exploded")
	if got := strings.Join(events, ","); got != "start first,stop first" {
		t.Errorf("events = %s", got)
	}

	root = &gonestly.Module{Name: "app", Providers: gonestly.Providers(gonestly.Value(&panickingHooks{on: "stop"}))}
	app = mustStart(t, root)
	assertContains(t, app.Stop(t.Context()), "stopping", "panic: stop exploded")
}

func TestConstructorReturningNil(t *testing.T) {
	root := &gonestly.Module{Name: "app", Providers: gonestly.Providers(func() *database { return nil })}
	app, err := gonestly.New(root, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, app.Start(t.Context()), "constructing", "returned a nil *gonestly_test.database")
}

func TestExportProviders(t *testing.T) {
	store := gonestly.As[userStore](newMemoryStore)
	db := gonestly.Value(&database{dsn: "value"})
	lib := &gonestly.Module{Name: "lib", Providers: []gonestly.Provider{store, db}, Exports: gonestly.Exports(store, db)}
	root := &gonestly.Module{Name: "app", Imports: gonestly.Imports(lib), Providers: gonestly.Providers(newStoreUser, newRepository)}

	app := mustStart(t, root)
	if mustResolve[*repository](t, app).db.dsn != "value" || mustResolve[*storeUser](t, app).store == nil {
		t.Error("the exported providers were not injected")
	}

	err := buildError(t, &gonestly.Module{Name: "app", Exports: gonestly.Exports(gonestly.Provide(42))})
	assertContains(t, err, "int is not a constructor function")
}

func TestAmbiguousConstructorExport(t *testing.T) {
	root := &gonestly.Module{
		Name:      "app",
		Providers: gonestly.Providers(newMemoryStore, gonestly.As[userStore](newMemoryStore)),
		Exports:   gonestly.Exports(newMemoryStore),
	}
	assertContains(t, buildError(t, root), "exports gonestly_test.newMemoryStore, which matches 2 providers; export it with gonestly.Type instead")
}

func TestMissingDependencyNotImported(t *testing.T) {
	db := &gonestly.Module{Name: "database", Providers: gonestly.Providers(newDatabase)}
	other := &gonestly.Module{Name: "other", Imports: gonestly.Imports(db)}
	root := &gonestly.Module{Name: "app", Imports: gonestly.Imports(other), Providers: gonestly.Providers(newRepository)}
	assertContains(t, buildError(t, root), `add it to the Exports of "database" and import that module`)
}

func TestEmptyOverride(t *testing.T) {
	root := &gonestly.Module{Name: "app"}
	assertContains(t, buildError(t, root, gonestly.Override(gonestly.Provider{})), "empty override")
}
