package gonestly

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SecurityHeadersConfig sets the security headers of every response. The zero value is a good
// default for an API; each field changes one header, and "off" leaves it out.
type SecurityHeadersConfig struct {
	// HSTSMaxAge makes browsers use HTTPS only, for this long, after they saw it once. It is sent
	// on HTTPS requests only, 2 years by default; a negative value turns it off.
	HSTSMaxAge time.Duration
	// HSTSIncludeSubdomains and HSTSPreload extend HSTS to subdomains and ask browsers to ship
	// the domain in their list. Turn them on only when every subdomain speaks HTTPS.
	HSTSIncludeSubdomains bool
	HSTSPreload           bool
	// TrustForwardedProto reads X-Forwarded-Proto, so HSTS is sent behind a proxy that ends
	// TLS. Set it only when a proxy you control sets that header.
	TrustForwardedProto bool
	// ContentSecurityPolicy defaults to a policy that lets a response load nothing and be framed
	// nowhere, which is right for JSON.
	ContentSecurityPolicy string
	// FrameOptions defaults to DENY.
	FrameOptions string
	// ReferrerPolicy defaults to no-referrer.
	ReferrerPolicy string
	// PermissionsPolicy defaults to switching off the camera, microphone and location.
	PermissionsPolicy string
	// CrossOriginOpenerPolicy and CrossOriginResourcePolicy default to same-origin.
	CrossOriginOpenerPolicy   string
	CrossOriginResourcePolicy string
}

const (
	off                  = "off"
	defaultHSTSMaxAge    = 2 * 365 * 24 * time.Hour
	defaultCSP           = "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	defaultPermissions   = "camera=(), microphone=(), geolocation=(), payment=()"
	defaultReferrer      = "no-referrer"
	defaultFrameOptions  = "DENY"
	defaultSameOriginVal = "same-origin"
)

// SecurityHeaders is middleware that adds the headers that browsers use to protect their users:
// Strict-Transport-Security on HTTPS, X-Content-Type-Options, Content-Security-Policy,
// X-Frame-Options, Referrer-Policy, Permissions-Policy and the Cross-Origin policies. A handler
// can still change a header for its own response, for example to serve a page with another
// Content-Security-Policy; the API documentation does.
func SecurityHeaders(config SecurityHeadersConfig) Middleware {
	static := map[string]string{"X-Content-Type-Options": "nosniff"}
	set := func(name, value, fallback string) {
		switch value {
		case "":
			static[name] = fallback
		case off:
		default:
			static[name] = value
		}
	}
	set("Content-Security-Policy", config.ContentSecurityPolicy, defaultCSP)
	set("X-Frame-Options", config.FrameOptions, defaultFrameOptions)
	set("Referrer-Policy", config.ReferrerPolicy, defaultReferrer)
	set("Permissions-Policy", config.PermissionsPolicy, defaultPermissions)
	set("Cross-Origin-Opener-Policy", config.CrossOriginOpenerPolicy, defaultSameOriginVal)
	set("Cross-Origin-Resource-Policy", config.CrossOriginResourcePolicy, defaultSameOriginVal)

	hsts := ""
	if config.HSTSMaxAge >= 0 {
		maxAge := config.HSTSMaxAge
		if maxAge == 0 {
			maxAge = defaultHSTSMaxAge
		}
		hsts = "max-age=" + strconv.FormatInt(int64(maxAge/time.Second), 10)
		if config.HSTSIncludeSubdomains {
			hsts += "; includeSubDomains"
		}
		if config.HSTSPreload {
			hsts += "; preload"
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := w.Header()
			for name, value := range static {
				header.Set(name, value)
			}
			if hsts != "" && (r.TLS != nil || (config.TrustForwardedProto && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))) {
				header.Set("Strict-Transport-Security", hsts)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CrossOriginProtection is middleware that stops cross-site request forgery: a browser request
// that changes something (anything but GET, HEAD and OPTIONS) is refused with 403 when it comes
// from another site, judged by the Sec-Fetch-Site and Origin headers that browsers add and
// pages cannot forge. Requests without those headers, such as from curl, a mobile app or
// another server, pass: they are not the browser of a victim.
//
// Use it when the API accepts cookies. Pass the origins of your own frontends on other sites,
// such as "https://app.example.com", as trusted. An API that only takes bearer tokens in a
// header does not need it, since a forged request cannot add the token.
//
// It panics for an origin that is not a scheme and a host, like "https://example.com".
func CrossOriginProtection(trustedOrigins ...string) Middleware {
	protection := http.NewCrossOriginProtection()
	for _, origin := range trustedOrigins {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			panic("gonestly: CrossOriginProtection: " + err.Error())
		}
	}
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, Problem{
			Type:     "about:blank",
			Title:    http.StatusText(http.StatusForbidden),
			Status:   http.StatusForbidden,
			Detail:   "The request came from another site and is refused.",
			Instance: r.URL.Path,
		})
	}))
	return func(next http.Handler) http.Handler { return protection.Handler(next) }
}

type clientIPKey struct{}

// TrustProxies is middleware that finds the address of the client behind reverse proxies, and
// makes it the address that ClientIP returns, that the access log writes and that RateLimit
// counts by. proxies is the number of proxies in front of the application that you control: with
// one, the client is the last address in X-Forwarded-For, which your proxy added. Never set it
// higher than the number of proxies you control, or clients can pick their own address. Add it
// before AccessLog and RateLimit.
func TrustProxies(proxies int) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientAddress(r, proxies)
			if f := logFieldsFrom(r.Context()); f != nil {
				f.mu.Lock()
				f.clientIP = ip
				f.mu.Unlock()
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ip)))
		})
	}
}

// ClientIP returns the address of the client of the request in ctx. Without TrustProxies it is
// the address of the connection, which behind a proxy is the proxy, so it is "" here: use the
// middleware, or read r.RemoteAddr yourself.
func ClientIP(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}
