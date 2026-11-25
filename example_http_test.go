package gonestly_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"

	"github.com/yaboylino/gonestly"
)

type GreetRequest struct {
	Name string `path:"name"`
	Lang string `query:"lang"`
}

type Greeting struct {
	Message string `json:"message"`
}

type GreetController struct{}

func NewGreetController() *GreetController { return &GreetController{} }

func (c *GreetController) Routes(r *gonestly.Router) {
	r.Prefix("/greet")
	gonestly.Get(r, "/{name}", c.Greet)
}

func (c *GreetController) Greet(_ context.Context, req GreetRequest) (Greeting, error) {
	if req.Lang == "nl" {
		return Greeting{Message: "Hallo, " + req.Name}, nil
	}
	return Greeting{Message: "Hello, " + req.Name}, nil
}

func ExampleGet() {
	module := &gonestly.Module{
		Name:        "greet",
		Controllers: gonestly.Controllers(NewGreetController),
	}
	app, _ := gonestly.New(module, gonestly.WithoutListener(), gonestly.WithLogger(slog.New(slog.DiscardHandler)))
	_ = app.Start(context.Background())
	defer func() { _ = app.Stop(context.Background()) }()

	for _, path := range []string{"/greet/Ada", "/greet/Ada?lang=nl", "/greet"} {
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		fmt.Print(recorder.Code, " ", recorder.Body.String())
	}
	fmt.Println()
	// Output:
	// 200 {"message":"Hello, Ada"}
	// 200 {"message":"Hallo, Ada"}
	// 404 {"type":"about:blank","title":"Not Found","status":404,"detail":"No route matches GET /greet.","instance":"/greet"}
}
