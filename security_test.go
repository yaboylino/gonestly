package gonestly_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

func startSecured(t *testing.T, middleware ...gonestly.Middleware) *gonestlytest.App {
	t.Helper()
	return gonestlytest.Start(t, &gonestly.Module{
		Name: "app",
		Providers: gonestly.Providers(func() gonestly.ServerConfig {
			return gonestly.ServerConfig{Middleware: middleware}
		}),
		Controllers: gonestly.Controllers(func() routesFunc {
			return func(r *gonestly.Router) {
				gonestly.Get(r, "/thing", func(context.Context, gonestly.Empty) (string, error) { return "ok", nil })
				gonestly.Post(r, "/thing", func(context.Context, gonestly.Empty) (string, error) { return "made", nil })
				gonestly.Get(r, "/ip", func(ctx context.Context, _ gonestly.Empty) (string, error) { return gonestly.ClientIP(ctx), nil })
			}
		}),
	})
}

func TestSecurityHeadersDefaults(t *testing.T) {
	res := startSecured(t, gonestly.SecurityHeaders(gonestly.SecurityHeadersConfig{})).Get("/thing").ExpectStatus(http.StatusOK)
	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Content-Security-Policy":      "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=(), payment=()",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	}
	for name, value := range want {
		if got := res.Header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if res.Header.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS was sent over plain HTTP")
	}
}

func TestSecurityHeadersCustom(t *testing.T) {
	app := startSecured(t, gonestly.SecurityHeaders(gonestly.SecurityHeadersConfig{
		ContentSecurityPolicy: "default-src 'self'",
		FrameOptions:          "off",
		ReferrerPolicy:        "same-origin",
	}))
	res := app.Get("/thing")
	if res.Header.Get("Content-Security-Policy") != "default-src 'self'" || res.Header.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("headers: %v", res.Header)
	}
	if _, ok := res.Header["X-Frame-Options"]; ok {
		t.Error("a header that was turned off is there")
	}
}

func TestHSTS(t *testing.T) {
	serve := func(config gonestly.SecurityHeadersConfig, prepare func(*http.Request)) string {
		handler := gonestly.SecurityHeaders(config)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		prepare(req)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Header().Get("Strict-Transport-Security")
	}
	tlsRequest := func(r *http.Request) { r.TLS = &tls.ConnectionState{} }
	if got := serve(gonestly.SecurityHeadersConfig{}, tlsRequest); got != "max-age=63072000" {
		t.Errorf("default = %q", got)
	}
	if got := serve(gonestly.SecurityHeadersConfig{HSTSMaxAge: time.Hour, HSTSIncludeSubdomains: true, HSTSPreload: true}, tlsRequest); got != "max-age=3600; includeSubDomains; preload" {
		t.Errorf("custom = %q", got)
	}
	if got := serve(gonestly.SecurityHeadersConfig{HSTSMaxAge: -1}, tlsRequest); got != "" {
		t.Errorf("disabled = %q", got)
	}
	forwarded := func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }
	if got := serve(gonestly.SecurityHeadersConfig{}, forwarded); got != "" {
		t.Errorf("an untrusted X-Forwarded-Proto = %q", got)
	}
	if got := serve(gonestly.SecurityHeadersConfig{TrustForwardedProto: true}, forwarded); got == "" {
		t.Error("a trusted X-Forwarded-Proto gave no HSTS")
	}
}

func TestCrossOriginProtection(t *testing.T) {
	app := startSecured(t, gonestly.CrossOriginProtection("https://app.example.com"))
	for name, tt := range map[string]struct {
		method  string
		headers []string
		want    int
	}{
		"cross-site post":          {http.MethodPost, []string{"Sec-Fetch-Site", "cross-site"}, http.StatusForbidden},
		"cross-origin post":        {http.MethodPost, []string{"Origin", "https://evil.example"}, http.StatusForbidden},
		"same-origin post":         {http.MethodPost, []string{"Sec-Fetch-Site", "same-origin"}, http.StatusCreated},
		"no browser headers":       {http.MethodPost, nil, http.StatusCreated},
		"cross-site get":           {http.MethodGet, []string{"Sec-Fetch-Site", "cross-site"}, http.StatusOK},
		"trusted origin":           {http.MethodPost, []string{"Origin", "https://app.example.com"}, http.StatusCreated},
		"untrusted similar origin": {http.MethodPost, []string{"Origin", "https://app.example.com.evil.example"}, http.StatusForbidden},
	} {
		res := app.Request(tt.method, "/thing", nil, tt.headers...)
		if res.Status != tt.want {
			t.Errorf("%s: status %d, want %d: %s", name, res.Status, tt.want, res.Body)
		}
	}
	if p := app.Post("/thing", nil, "Sec-Fetch-Site", "cross-site").Problem(); p.Status != http.StatusForbidden || p.Title != "Forbidden" {
		t.Errorf("problem = %+v", p)
	}

	defer func() {
		if recover() == nil {
			t.Error("a bad trusted origin did not panic")
		}
	}()
	gonestly.CrossOriginProtection("app.example.com")
}

func TestTrustProxies(t *testing.T) {
	app := startSecured(t, gonestly.TrustProxies(1))
	var ip string
	// The proxy adds the address it saw to the end; what a client put before it is not trusted.
	app.Get("/ip", "X-Forwarded-For", "6.6.6.6, 203.0.113.9").JSON(&ip)
	if ip != "203.0.113.9" {
		t.Errorf("ClientIP = %q", ip)
	}
	app.Get("/ip").JSON(&ip)
	if ip != "192.0.2.1" {
		t.Errorf("without the header, ClientIP = %q, want the connection", ip)
	}
	plain := startSecured(t)
	plain.Get("/ip", "X-Forwarded-For", "6.6.6.6").JSON(&ip)
	if ip != "" {
		t.Errorf("without TrustProxies, ClientIP = %q", ip)
	}
}

func TestTrustProxiesInTheAccessLogAndRateLimit(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	app := startSecured(t,
		gonestly.AccessLog(logger),
		gonestly.TrustProxies(1),
		gonestly.RateLimit(gonestly.RateLimitConfig{Requests: 1, Window: time.Hour}),
	)
	app.Get("/thing", "X-Forwarded-For", "198.51.100.1").ExpectStatus(http.StatusOK)
	// Another client behind the same proxy has a limit of its own.
	app.Get("/thing", "X-Forwarded-For", "198.51.100.2").ExpectStatus(http.StatusOK)
	// A client cannot dodge the limit by putting other addresses first.
	app.Get("/thing", "X-Forwarded-For", "10.9.9.9, 198.51.100.1").ExpectStatus(http.StatusTooManyRequests)
	if !strings.Contains(logs.String(), "remote_addr=198.51.100.1") || strings.Contains(logs.String(), "remote_addr=192.0.2.1") {
		t.Errorf("the access log:\n%s", logs.String())
	}
}

func TestDocsKeepTheirOwnContentSecurityPolicy(t *testing.T) {
	app := startSecured(t, gonestly.SecurityHeaders(gonestly.SecurityHeadersConfig{}))
	res := app.Get("/docs").ExpectStatus(http.StatusOK)
	policy := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(policy, "cdn.jsdelivr.net") || strings.Contains(policy, "script-src 'self'") {
		t.Errorf("policy = %q", policy)
	}
	// The hash in the policy is the hash of the script that starts Swagger UI.
	scripts := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllSubmatch(res.Body, -1)
	if len(scripts) != 1 {
		t.Fatalf("%d inline scripts", len(scripts))
	}
	sum := sha256.Sum256(scripts[0][1])
	if !strings.Contains(policy, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
		t.Errorf("the policy %q does not allow the script %q", policy, scripts[0][1])
	}
	// The API keeps the strict policy.
	if got := app.Get("/thing").Header.Get("Content-Security-Policy"); !strings.HasPrefix(got, "default-src 'none'") {
		t.Errorf("API policy = %q", got)
	}
}
