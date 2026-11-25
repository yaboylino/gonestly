package gonestlytest_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

type echoRequest struct {
	Header string `header:"X-Echo"`
	Text   string `json:"text"`
}

type echoController struct{ prefix string }

func newEchoController(prefix string) *echoController { return &echoController{prefix: prefix} }

func (c *echoController) Routes(r *gonestly.Router) {
	echo := func(_ context.Context, req echoRequest) (string, error) { return c.prefix + req.Text + req.Header, nil }
	gonestly.Get(r, "/echo", echo)
	gonestly.Post(r, "/echo", echo)
	gonestly.Put(r, "/echo", echo)
	gonestly.Patch(r, "/echo", echo)
	gonestly.Delete(r, "/echo", echo)
	gonestly.Handle(r, http.MethodPost, "/raw", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	}))
}

var module = &gonestly.Module{
	Name:        "echo",
	Providers:   gonestly.Providers(gonestly.Value("> ")),
	Controllers: gonestly.Controllers(newEchoController),
}

func TestRequests(t *testing.T) {
	app := gonestlytest.Start(t, module)
	var got string

	app.Get("/echo", "X-Echo", "!").ExpectStatus(http.StatusOK).JSON(&got)
	if got != "> !" {
		t.Errorf("GET = %q", got)
	}
	for _, res := range []*gonestlytest.Response{
		app.Post("/echo", map[string]string{"text": "hi"}),
		app.Put("/echo", map[string]string{"text": "hi"}),
		app.Patch("/echo", map[string]string{"text": "hi"}),
	} {
		res.JSON(&got)
		if got != "> hi" {
			t.Errorf("body = %q", got)
		}
	}
	app.Delete("/echo").ExpectStatus(http.StatusOK)

	for _, body := range []any{"text", []byte("text"), strings.NewReader("text")} {
		if res := app.Request(http.MethodPost, "/raw", body); string(res.Body) != "text" {
			t.Errorf("raw body = %q", res.Body)
		}
	}

	if p := app.Get("/missing").Problem(); p.Status != http.StatusNotFound {
		t.Errorf("problem = %+v", p)
	}
	if prefix := gonestlytest.Resolve[string](app); prefix != "> " {
		t.Errorf("Resolve = %q", prefix)
	}
}

func TestOverrides(t *testing.T) {
	var got string
	gonestlytest.Start(t, module, gonestly.Override(gonestly.Value("< "))).Get("/echo").JSON(&got)
	if got != "< " {
		t.Errorf("got %q", got)
	}
}
