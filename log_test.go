package gonestly_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

type logService struct{ logger *slog.Logger }

func newLogService(logger *slog.Logger) *logService { return &logService{logger: logger} }

func startLogging(t *testing.T, access ...gonestly.AccessLogOption) (*gonestlytest.App, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	app := gonestlytest.Start(t, &gonestly.Module{
		Name: "app",
		Providers: gonestly.Providers(newLogService, func() gonestly.ServerConfig {
			return gonestly.ServerConfig{Middleware: []gonestly.Middleware{gonestly.RequestID(), gonestly.AccessLog(logger, access...)}}
		}),
		Controllers: gonestly.Controllers(func(s *logService) routesFunc {
			return func(r *gonestly.Router) {
				gonestly.Get(r, "/work/{id}", func(ctx context.Context, _ gonestly.Empty) (string, error) {
					s.logger.InfoContext(ctx, "before")
					gonestly.LogAttrs(ctx, slog.String("user_id", "u-7"))
					s.logger.InfoContext(ctx, "after")
					return "ok", nil
				})
				gonestly.Get(r, "/health/live", func(context.Context, gonestly.Empty) (string, error) { return "ok", nil })
				gonestly.Get(r, "/slow", func(context.Context, gonestly.Empty) (string, error) {
					time.Sleep(30 * time.Millisecond)
					return "ok", nil
				})
			}
		}),
	}, gonestly.WithLogger(logger))
	return app, &logs
}

func TestLogsCarryTheRequestID(t *testing.T) {
	app, logs := startLogging(t)
	app.Get("/work/3", gonestly.RequestIDHeader, "req-9").ExpectStatus(http.StatusOK)
	for _, want := range []string{
		`msg=before request_id=req-9`,
		`msg=after request_id=req-9 user_id=u-7`,
		`msg=request method=GET path=/work/3 request_id=req-9 status=200`,
		`route=/work/{id}`,
		`user_id=u-7`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the logs lack %s\n%s", want, logs.String())
		}
	}
	if strings.Count(logs.String(), "request_id=req-9") != 3 || strings.Contains(logs.String(), "request_id=req-9 request_id") {
		t.Errorf("the request ID is duplicated or missing:\n%s", logs.String())
	}
	if strings.Contains(strings.SplitN(logs.String(), "msg=before", 2)[1], "user_id=u-7 user_id") {
		t.Errorf("an attribute is duplicated:\n%s", logs.String())
	}
}

func TestAccessLogDetails(t *testing.T) {
	app, logs := startLogging(t)
	app.Get("/work/1", "User-Agent", "probe/1.0").ExpectStatus(http.StatusOK)
	if !strings.Contains(logs.String(), `remote_addr=192.0.2.1`) || !strings.Contains(logs.String(), `user_agent=probe/1.0`) {
		t.Errorf("the logs lack the client:\n%s", logs.String())
	}
}

func TestAccessLogSkipPaths(t *testing.T) {
	app, logs := startLogging(t, gonestly.SkipPaths("/health/*"))
	app.Get("/health/live").ExpectStatus(http.StatusOK)
	app.Get("/work/1").ExpectStatus(http.StatusOK)
	if strings.Contains(logs.String(), "path=/health/live") {
		t.Errorf("a skipped path was logged:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "path=/work/1") {
		t.Errorf("another path was not logged:\n%s", logs.String())
	}
}

func TestAccessLogSlowRequests(t *testing.T) {
	app, logs := startLogging(t, gonestly.SlowAfter(10*time.Millisecond))
	app.Get("/slow").ExpectStatus(http.StatusOK)
	app.Get("/work/1").ExpectStatus(http.StatusOK)
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	var slow, fast string
	for _, line := range lines {
		switch {
		case strings.Contains(line, "path=/slow"):
			slow = line
		case strings.Contains(line, "msg=request") && strings.Contains(line, "path=/work/1"):
			fast = line
		}
	}
	if !strings.Contains(slow, "level=WARN") || !strings.Contains(slow, "slow=true") {
		t.Errorf("the slow request: %s", slow)
	}
	if !strings.Contains(fast, "level=INFO") || strings.Contains(fast, "slow=") {
		t.Errorf("the fast request: %s", fast)
	}
}

func TestLogHandler(t *testing.T) {
	var logs bytes.Buffer
	handler := gonestly.LogHandler(slog.NewTextHandler(&logs, nil))
	if gonestly.LogHandler(handler) != handler {
		t.Error("a wrapped handler was wrapped again")
	}
	logger := slog.New(handler).With("service", "billing").WithGroup("g")
	ctx := gonestly.WithRequestID(context.Background(), "abc")
	logger.InfoContext(ctx, "hello", "n", 1)
	logger.Info("no context")
	if !strings.Contains(logs.String(), `service=billing g.n=1 g.request_id=abc`) {
		t.Errorf("a record with a request:\n%s", logs.String())
	}
	if strings.Count(logs.String(), "request_id") != 1 {
		t.Errorf("a record without a request got an ID:\n%s", logs.String())
	}
	// Outside a request there is nothing to add to.
	gonestly.LogAttrs(context.Background(), slog.String("x", "y"))
}

func TestNewLogger(t *testing.T) {
	var out bytes.Buffer
	logger, err := gonestly.NewLogger(gonestly.LogOptions{Level: "warn", Format: "json", Output: &out})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("hidden")
	logger.Warn("shown", "k", "v")
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &line); err != nil || line["msg"] != "shown" || line["k"] != "v" || line["level"] != "WARN" {
		t.Errorf("output = %q, %v", out.String(), err)
	}

	out.Reset()
	logger, _ = gonestly.NewLogger(gonestly.LogOptions{Level: "DEBUG", Format: "text", Output: &out})
	logger.Debug("deep")
	if !strings.Contains(out.String(), "level=DEBUG msg=deep") {
		t.Errorf("text output = %q", out.String())
	}

	// Output that is not a terminal gets JSON.
	out.Reset()
	logger, _ = gonestly.NewLogger(gonestly.LogOptions{Output: &out})
	logger.Info("auto")
	if !strings.HasPrefix(out.String(), "{") {
		t.Errorf("auto output = %q", out.String())
	}

	if _, err := gonestly.NewLogger(gonestly.LogOptions{Level: "loud"}); err == nil || !strings.Contains(err.Error(), "loud") {
		t.Errorf("a bad level: %v", err)
	}
	if _, err := gonestly.NewLogger(gonestly.LogOptions{Format: "xml"}); err == nil || !strings.Contains(err.Error(), "xml") {
		t.Errorf("a bad format: %v", err)
	}
}
