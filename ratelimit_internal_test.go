package gonestly

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := newRateLimiter(RateLimitConfig{Requests: 4, Window: time.Minute}, func() time.Time { return now })

	// A quiet client may send its whole quota at once.
	for want := 3; want >= 0; want-- {
		if allowed, remaining, _, _ := limiter.take("a"); !allowed || remaining != want {
			t.Fatalf("allowed = %v, remaining = %d, want %d", allowed, remaining, want)
		}
	}
	allowed, remaining, reset, retry := limiter.take("a")
	if allowed || remaining != 0 || reset != time.Minute || retry != 15*time.Second {
		t.Errorf("over the limit: %v %d %s %s", allowed, remaining, reset, retry)
	}
	if allowed, _, _, _ := limiter.take("b"); !allowed {
		t.Error("another client was refused")
	}

	// Room comes back evenly: one request per quarter of the window.
	now = now.Add(14 * time.Second)
	if allowed, _, _, retry := limiter.take("a"); allowed || retry != time.Second {
		t.Errorf("after 14s: %v %s", allowed, retry)
	}
	now = now.Add(time.Second)
	if allowed, remaining, _, _ := limiter.take("a"); !allowed || remaining != 0 {
		t.Errorf("after 15s: %v %d", allowed, remaining)
	}
	if allowed, _, _, _ := limiter.take("a"); allowed {
		t.Error("a second request after 15s was allowed")
	}

	// Clients whose quota is whole again are forgotten.
	now = now.Add(2 * time.Minute)
	limiter.take("c")
	if len(limiter.clients) != 1 {
		t.Errorf("clients = %v", limiter.clients)
	}
}

func TestClientNetwork(t *testing.T) {
	for address, want := range map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"203.0.113.7:51000":    "203.0.113.7",
		"[2001:db8:1:2::5]:80": "2001:db8:1:2::/64",
		"2001:db8:1:2:aaaa::1": "2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb::9": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":      "2001:db8:1:3::/64",
		"not an address":       "not an address",
	} {
		if got := clientNetwork(address); got != want {
			t.Errorf("clientNetwork(%s) = %s, want %s", address, got, want)
		}
	}
}

func TestClientAddress(t *testing.T) {
	for _, tt := range []struct {
		forwarded []string
		proxies   int
		want      string
	}{
		{nil, 0, "10.0.0.9"},
		{[]string{"1.1.1.1"}, 0, "10.0.0.9"},
		{[]string{"6.6.6.6, 1.1.1.1"}, 1, "1.1.1.1"},
		{[]string{"6.6.6.6, 1.1.1.1", "2.2.2.2"}, 2, "1.1.1.1"},
		{[]string{"1.1.1.1"}, 2, "10.0.0.9"},
		{nil, 1, "10.0.0.9"},
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		r.RemoteAddr = "10.0.0.9:4321"
		for _, value := range tt.forwarded {
			r.Header.Add("X-Forwarded-For", value)
		}
		if got := clientAddress(r, tt.proxies); got != tt.want {
			t.Errorf("clientAddress(%v, %d) = %s, want %s", tt.forwarded, tt.proxies, got, tt.want)
		}
	}
}
