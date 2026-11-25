package gonestly_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

type paging struct {
	Page int `query:"page"`
}

type embeddedPointerRequest struct {
	*paging
}

type embeddedPointerController struct{}

func (embeddedPointerController) Routes(r *gonestly.Router) {
	gonestly.Get(r, "/items", func(context.Context, embeddedPointerRequest) (int, error) { return 0, nil })
}

func TestEmbeddedPointerRequestIsRejected(t *testing.T) {
	app, err := gonestly.New(&gonestly.Module{
		Name:        "app",
		Controllers: gonestly.Controllers(func() embeddedPointerController { return embeddedPointerController{} }),
	}, gonestly.WithoutListener())
	if err != nil {
		t.Fatal(err)
	}
	err = app.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "embedded through a pointer") {
		t.Fatalf("Start() = %v, want an embedded pointer problem", err)
	}
}

type firstController struct{}

func (c firstController) Routes(r *gonestly.Router) { gonestly.Get(r, "/first", c.FindAll) }

func (firstController) FindAll(context.Context, gonestly.Empty) ([]string, error) { return nil, nil }

type secondController struct{}

func (c secondController) Routes(r *gonestly.Router) { gonestly.Get(r, "/second", c.FindAll) }

func (secondController) FindAll(context.Context, gonestly.Empty) ([]string, error) { return nil, nil }

func TestOperationIDsAreUniqueWithinAModule(t *testing.T) {
	app := gonestlytest.Start(t, &gonestly.Module{
		Name: "app",
		Controllers: gonestly.Controllers(
			func() firstController { return firstController{} },
			func() secondController { return secondController{} },
		),
	})
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(app.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	first, second := doc.Paths["/first"]["get"].OperationID, doc.Paths["/second"]["get"].OperationID
	if first == "" || first == second {
		t.Errorf("operation IDs = %q and %q, want two different IDs", first, second)
	}
}

type panicController struct{}

func (panicController) Routes(r *gonestly.Router) {
	r.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("middleware failed") })
	})
	gonestly.Get(r, "/panic", func(context.Context, gonestly.Empty) (string, error) { return "ok", nil })
}

func TestMiddlewarePanicBecomesProblem(t *testing.T) {
	app := gonestlytest.Start(t, &gonestly.Module{
		Name:        "app",
		Controllers: gonestly.Controllers(func() panicController { return panicController{} }),
	})
	problem := app.Get("/panic").Problem()
	if problem.Status != http.StatusInternalServerError || strings.Contains(problem.Detail, "middleware failed") {
		t.Errorf("problem = %+v", problem)
	}
}

type optionalRequest struct {
	Limit *int `query:"limit"`
}

type edgeController struct {
	app func() *gonestly.App
}

func (c edgeController) Routes(r *gonestly.Router) {
	gonestly.Get(r, "/optional", func(_ context.Context, req optionalRequest) (string, error) {
		if req.Limit == nil {
			return "absent", nil
		}
		return "present", nil
	})
	gonestly.Get(r, "/cause", func(context.Context, gonestly.Empty) (string, error) {
		return "", gonestly.BadRequest("try again").WithCause(errors.New("the hidden cause"))
	})
	gonestly.Get(r, "/resolve", func(context.Context, gonestly.Empty) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return gonestly.Resolve[string](c.app())
	})
}

func TestPointerParameterTellsAbsentFromZero(t *testing.T) {
	app := gonestlytest.Start(t, &gonestly.Module{
		Name:        "app",
		Controllers: gonestly.Controllers(func() edgeController { return edgeController{} }),
	})
	for path, want := range map[string]string{"/optional": "absent", "/optional?limit=0": "present"} {
		var got string
		app.Get(path).ExpectStatus(http.StatusOK).JSON(&got)
		if got != want {
			t.Errorf("GET %s = %q, want %q", path, got, want)
		}
	}
	if doc := string(app.OpenAPI()); strings.Contains(doc, `"null"`) {
		t.Errorf("the optional parameter is described as nullable: %s", doc)
	}
}

func TestClientErrorCauseIsLoggedAtDebugLevel(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app := gonestlytest.Start(t, &gonestly.Module{
		Name:        "app",
		Controllers: gonestly.Controllers(func() edgeController { return edgeController{} }),
	}, gonestly.WithLogger(logger))

	problem := app.Get("/cause").Problem()
	if strings.Contains(problem.Detail, "hidden") {
		t.Errorf("the cause reached the client: %+v", problem)
	}
	if !strings.Contains(logs.String(), "level=DEBUG") || !strings.Contains(logs.String(), "the hidden cause") {
		t.Errorf("the cause was not logged at debug level:\n%s", logs.String())
	}
}

func TestStatusWithoutBodyNeedsEmptyResult(t *testing.T) {
	app, err := gonestly.New(&gonestly.Module{
		Name: "app",
		Controllers: gonestly.Controllers(func() routesFunc {
			return func(r *gonestly.Router) {
				gonestly.Delete(r, "/items", func(context.Context, gonestly.Empty) (string, error) { return "", nil }, gonestly.Status(http.StatusNoContent))
			}
		}),
	}, gonestly.WithoutListener())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(t.Context()); !errors.Is(err, gonestly.ErrInvalidRoute) || !strings.Contains(err.Error(), "the status 204 has no body") {
		t.Errorf("Start() = %v", err)
	}
}

type routesFunc func(r *gonestly.Router)

func (f routesFunc) Routes(r *gonestly.Router) { f(r) }

type resolvingStarter struct {
	app func() *gonestly.App
	err error
}

func (s *resolvingStarter) OnStart(context.Context) error {
	_, s.err = gonestly.Resolve[string](s.app())
	return nil
}

func TestResolveNeverBlocksStartOrStop(t *testing.T) {
	var app *gonestly.App
	current := func() *gonestly.App { return app }
	starter := &resolvingStarter{app: current}
	app, err := gonestly.New(&gonestly.Module{
		Name:        "app",
		Providers:   gonestly.Providers(gonestly.Value("resolved"), gonestly.Value(starter), gonestly.Value(gonestly.ServerConfig{Addr: "127.0.0.1:0"})),
		Controllers: gonestly.Controllers(func() edgeController { return edgeController{app: current} }),
	}, gonestly.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(starter.err, gonestly.ErrNotStarted) {
		t.Errorf("Resolve in OnStart = %v, want ErrNotStarted", starter.err)
	}

	// A request that is still running while the application stops can resolve providers.
	status := make(chan int, 1)
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+app.Addr().String()+"/resolve", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		_ = res.Body.Close()
		status <- res.StatusCode
	}()
	time.Sleep(20 * time.Millisecond)
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if got := <-status; got != http.StatusOK {
		t.Errorf("status of the running request = %d, want 200", got)
	}
}

func TestInspectWritesToFile(t *testing.T) {
	output := t.TempDir() + "/routes.json"
	t.Setenv(gonestly.InspectEnv, "routes")
	t.Setenv(gonestly.InspectOutputEnv, output)
	app, err := gonestly.New(usersModule(), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	stdout := captureStdout(t, func() { err = app.Run(t.Context()) })
	if err != nil || stdout != "" {
		t.Fatalf("err = %v, stdout = %q", err, stdout)
	}
	var routes []gonestly.RouteInfo
	data, err := os.ReadFile(output)
	if err != nil || json.Unmarshal(data, &routes) != nil || len(routes) == 0 {
		t.Errorf("routes = %v, %v: %s", routes, err, data)
	}
}

type money struct{ cents int64 }

func (m *money) JSONSchema() *gonestly.Schema {
	return &gonestly.Schema{Type: "string", Format: "money"}
}

func (m money) MarshalJSON() ([]byte, error) { return json.Marshal(m.cents) }

type schemaResponse struct {
	Count uint32  `json:"count"`
	Hash  [4]byte `json:"hash"`
	Price money   `json:"price"`
}

func TestSchemaDetails(t *testing.T) {
	app := gonestlytest.Start(t, &gonestly.Module{
		Name: "app",
		Controllers: gonestly.Controllers(func() routesFunc {
			return func(r *gonestly.Router) {
				gonestly.Get(r, "/schema", func(context.Context, gonestly.Empty) (schemaResponse, error) { return schemaResponse{}, nil })
			}
		}),
	})
	doc := string(app.OpenAPI())
	for _, want := range []string{
		`"count":{"type":"integer","format":"int64","minimum":0}`,
		`"hash":{"type":"array","items":{"type":"integer","format":"int32","minimum":0}}`,
		`"price":{"type":"string","format":"money"}`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document lacks %s:\n%s", want, doc)
		}
	}
}

func TestContextCannotBeProvided(t *testing.T) {
	_, err := gonestly.New(&gonestly.Module{
		Name:      "app",
		Providers: gonestly.Providers(gonestly.Value(context.Background()), func() context.Context { return context.Background() }),
	})
	if !errors.Is(err, gonestly.ErrInvalidModule) || strings.Count(err.Error(), "reserved for the context of App.Start") != 2 {
		t.Errorf("New() = %v", err)
	}
}
