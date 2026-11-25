package gonestly_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

func startWithMiddleware(t *testing.T) (*gonestlytest.App, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	app := gonestlytest.Start(t, &gonestly.Module{
		Name: "app",
		Providers: gonestly.Providers(func() gonestly.ServerConfig {
			return gonestly.ServerConfig{Middleware: []gonestly.Middleware{gonestly.RequestID(), gonestly.AccessLog(logger)}}
		}),
		Controllers: gonestly.Controllers(func() routesFunc {
			return func(r *gonestly.Router) {
				gonestly.Get(r, "/id", func(ctx context.Context, _ gonestly.Empty) (string, error) {
					return gonestly.RequestIDFrom(ctx), nil
				})
				gonestly.Get(r, "/fail", func(context.Context, gonestly.Empty) (string, error) {
					return "", errors.New("the database is gone")
				})
				gonestly.Get(r, "/panic", func(context.Context, gonestly.Empty) (string, error) { panic("boom") })
			}
		}),
	}, gonestly.WithLogger(logger))
	return app, &logs
}

func TestRequestID(t *testing.T) {
	app, _ := startWithMiddleware(t)

	var generated string
	res := app.Get("/id").ExpectStatus(http.StatusOK)
	res.JSON(&generated)
	if len(generated) < 16 || res.Header.Get(gonestly.RequestIDHeader) != generated {
		t.Errorf("generated ID = %q, header = %q", generated, res.Header.Get(gonestly.RequestIDHeader))
	}

	var given string
	app.Get("/id", gonestly.RequestIDHeader, "trace-42.a:b_c").JSON(&given)
	if given != "trace-42.a:b_c" {
		t.Errorf("the ID of the client was not kept: %q", given)
	}

	// An ID that is not safe to log or to send back is replaced.
	for _, unsafe := range []string{"two words", "line\nbreak", strings.Repeat("x", 129)} {
		var replaced string
		app.Get("/id", gonestly.RequestIDHeader, unsafe).JSON(&replaced)
		if replaced == unsafe || replaced == "" {
			t.Errorf("the unsafe ID %q was accepted", unsafe)
		}
	}
	if gonestly.RequestIDFrom(context.Background()) != "" {
		t.Error("a context without a request has an ID")
	}
}

func TestAccessLog(t *testing.T) {
	app, logs := startWithMiddleware(t)
	app.Get("/id", gonestly.RequestIDHeader, "req-1").ExpectStatus(http.StatusOK)
	app.Get("/missing", gonestly.RequestIDHeader, "req-2").ExpectStatus(http.StatusNotFound)
	app.Get("/fail", gonestly.RequestIDHeader, "req-3").ExpectStatus(http.StatusInternalServerError)
	app.Get("/panic", gonestly.RequestIDHeader, "req-4").ExpectStatus(http.StatusInternalServerError)

	for _, want := range []string{
		`level=INFO msg=request method=GET path=/id request_id=req-1 status=200 bytes=8`,
		`level=INFO msg=request method=GET path=/missing request_id=req-2 status=404`,
		`level=ERROR msg=request method=GET path=/fail request_id=req-3 status=500`,
		`level=ERROR msg="request failed" method=GET path=/fail request_id=req-3 status=500 error="the database is gone"`,
		`level=ERROR msg=request method=GET path=/panic request_id=req-4 status=500`,
		`level=ERROR msg="handler panicked" method=GET path=/panic request_id=req-4 panic=boom`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the logs lack %s\n%s", want, logs.String())
		}
	}
}
