package gonestly_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

func TestRateLimit(t *testing.T) {
	root := usersModule()
	root.Providers = append(root.Providers, gonestly.Providers(gonestly.Value(gonestly.ServerConfig{
		Middleware: []gonestly.Middleware{gonestly.RateLimit(gonestly.RateLimitConfig{
			Requests: 2,
			Window:   time.Hour,
			Key:      func(r *http.Request) string { return r.Header.Get("X-API-Key") },
		})},
	}))...)
	app := gonestlytest.Start(t, root)

	first := app.Get("/users/1", "X-API-Key", "a").ExpectStatus(http.StatusOK)
	if first.Header.Get("RateLimit-Limit") != "2" || first.Header.Get("RateLimit-Remaining") != "1" || first.Header.Get("RateLimit-Reset") != "1800" {
		t.Errorf("headers = %v", first.Header)
	}
	app.Get("/users/1", "X-API-Key", "a").ExpectStatus(http.StatusOK)
	refused := app.Get("/users/1", "X-API-Key", "a")
	problem := refused.Problem()
	if problem.Status != http.StatusTooManyRequests || problem.Detail != "Too many requests; try again in 1800 seconds." ||
		refused.Header.Get("Retry-After") != "1800" || refused.Header.Get("RateLimit-Remaining") != "0" {
		t.Errorf("refused = %+v %v", problem, refused.Header)
	}
	// Another client has its own count, and a request without a key is not counted.
	app.Get("/users/1", "X-API-Key", "b").ExpectStatus(http.StatusOK)
	for range 3 {
		if res := app.Get("/users/1").ExpectStatus(http.StatusOK); res.Header.Get("RateLimit-Limit") != "" {
			t.Errorf("a request without a key was counted: %v", res.Header)
		}
	}

	defer func() {
		if recover() == nil {
			t.Error("a limit of zero requests was accepted")
		}
	}()
	gonestly.RateLimit(gonestly.RateLimitConfig{Window: time.Minute})
}
