package gonestly

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimitConfig limits how many requests one client may send.
type RateLimitConfig struct {
	// Requests is how many requests a client may send per Window. A client that was quiet may
	// send them all at once; after that, room comes back evenly over the window.
	Requests int
	// Window is the period that Requests applies to, such as time.Minute.
	Window time.Duration
	// TrustedProxies is the number of reverse proxies in front of the application. With zero
	// the client is the address of the connection; with one it is the last address in the
	// X-Forwarded-For header, which your own proxy added. Never set it higher than the number
	// of proxies you control, or clients can pick their own address.
	TrustedProxies int
	// Key identifies the client instead of its address, for example by API key or user. An
	// empty key lets the request through without counting it.
	Key func(r *http.Request) string
}

// RateLimit is middleware that answers 429 Too Many Requests when a client sends more than
// the configured number of requests. Every response tells the client where it stands with the
// RateLimit-Limit, RateLimit-Remaining and RateLimit-Reset headers, and a 429 has Retry-After.
//
// The counts live in memory, so each instance of the application counts for itself. Use it in
// ServerConfig.Middleware for the whole API, or on a router for routes such as a login. It
// panics when Requests or Window is not above zero.
func RateLimit(config RateLimitConfig) Middleware {
	limiter := newRateLimiter(config, time.Now)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := limiter.key(r)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			allowed, remaining, reset, retry := limiter.take(key)
			header := w.Header()
			header.Set("RateLimit-Limit", strconv.Itoa(config.Requests))
			header.Set("RateLimit-Remaining", strconv.Itoa(remaining))
			header.Set("RateLimit-Reset", seconds(reset))
			if allowed {
				next.ServeHTTP(w, r)
				return
			}
			header.Set("Retry-After", seconds(retry))
			writeProblem(w, Problem{
				Type:     "about:blank",
				Title:    http.StatusText(http.StatusTooManyRequests),
				Status:   http.StatusTooManyRequests,
				Detail:   "Too many requests; try again in " + seconds(retry) + " seconds.",
				Instance: r.URL.Path,
			})
		})
	}
}

func seconds(d time.Duration) string {
	return strconv.Itoa(int(math.Ceil(d.Seconds())))
}

// rateLimiter counts with the generic cell rate algorithm: per client it keeps one moment,
// the time at which the client has used nothing of its quota again.
type rateLimiter struct {
	config   RateLimitConfig
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	clients map[string]time.Time
	swept   time.Time
}

func newRateLimiter(config RateLimitConfig, now func() time.Time) *rateLimiter {
	if config.Requests <= 0 || config.Window <= 0 || config.Window < time.Duration(config.Requests) {
		panic("gonestly: RateLimit needs Requests and Window above zero")
	}
	return &rateLimiter{
		config:   config,
		interval: config.Window / time.Duration(config.Requests),
		now:      now,
		clients:  map[string]time.Time{},
		swept:    now(),
	}
}

// take counts one request and reports whether it is allowed, how many more are, how long
// until the quota is whole again, and how long to wait after a refusal.
func (l *rateLimiter) take(key string) (allowed bool, remaining int, reset, retry time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	free := l.clients[key]
	if free.Before(now) {
		free = now
	}
	used := free.Sub(now)
	if next := used + l.interval; next <= l.config.Window {
		l.clients[key] = now.Add(next)
		return true, int((l.config.Window - next) / l.interval), next, 0
	}
	return false, 0, used, used + l.interval - l.config.Window
}

// sweep forgets the clients whose quota is whole again, once per window.
func (l *rateLimiter) sweep(now time.Time) {
	if now.Sub(l.swept) < l.config.Window {
		return
	}
	l.swept = now
	for key, free := range l.clients {
		if !free.After(now) {
			delete(l.clients, key)
		}
	}
}

func (l *rateLimiter) key(r *http.Request) string {
	if l.config.Key != nil {
		return l.config.Key(r)
	}
	if ip := ClientIP(r.Context()); ip != "" && l.config.TrustedProxies == 0 {
		return clientNetwork(ip)
	}
	return clientNetwork(clientAddress(r, l.config.TrustedProxies))
}

// clientAddress returns the address of the client as seen by the outermost trusted proxy.
func clientAddress(r *http.Request, trustedProxies int) string {
	if trustedProxies > 0 {
		forwarded := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		if i := len(forwarded) - trustedProxies; i >= 0 {
			if address := strings.TrimSpace(forwarded[i]); address != "" {
				return address
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientNetwork is what a limit counts by: an IPv4 address, or the /64 network of an IPv6
// address, because one client usually has all the addresses of such a network.
func clientNetwork(address string) string {
	ip, err := netip.ParseAddr(address)
	if err != nil {
		// Some proxies add the port; without it the address stays the same for one client.
		withPort, portErr := netip.ParseAddrPort(address)
		if portErr != nil {
			return address
		}
		ip = withPort.Addr()
	}
	if ip.Unmap().Is4() {
		return ip.Unmap().String()
	}
	prefix, err := ip.Prefix(64)
	if err != nil {
		return address
	}
	return prefix.String()
}
