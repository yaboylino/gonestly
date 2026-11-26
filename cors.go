package gonestly

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const defaultCORSMaxAge = 10 * time.Minute

// CORSConfig lets pages on other origins, such as a frontend on its own domain, call the API
// from a browser. Without AllowedOrigins the server sends no CORS headers.
type CORSConfig struct {
	// AllowedOrigins are the origins that may call the API, such as "https://app.example.com".
	// "https://*.example.com" allows every subdomain, and "*" allows every origin.
	AllowedOrigins []string
	// AllowedMethods default to GET, HEAD, POST, PUT, PATCH and DELETE.
	AllowedMethods []string
	// AllowedHeaders are the request headers a page may send. By default every header is allowed.
	AllowedHeaders []string
	// ExposedHeaders are the response headers a page may read. It defaults to X-Request-ID.
	ExposedHeaders []string
	// AllowCredentials lets the browser send cookies and HTTP authentication. It cannot be
	// combined with the origin "*".
	AllowCredentials bool
	// MaxAge is how long a browser may remember the answer to a preflight request. It defaults
	// to 10 minutes.
	MaxAge time.Duration
}

type cors struct {
	any         bool
	origins     map[string]bool
	patterns    [][2]string
	methods     []string
	headers     string
	exposed     string
	credentials bool
	maxAge      string
}

// newCORS returns nil when the configuration allows no origin, and every mistake in it.
func newCORS(config CORSConfig) (*cors, []error) {
	if len(config.AllowedOrigins) == 0 {
		return nil, nil
	}
	c := &cors{
		origins:     map[string]bool{},
		methods:     config.AllowedMethods,
		headers:     strings.Join(config.AllowedHeaders, ", "),
		exposed:     RequestIDHeader,
		credentials: config.AllowCredentials,
		maxAge:      strconv.Itoa(int(defaultCORSMaxAge.Seconds())),
	}
	if len(c.methods) == 0 {
		c.methods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	}
	if config.ExposedHeaders != nil {
		c.exposed = strings.Join(config.ExposedHeaders, ", ")
	}
	if config.MaxAge > 0 {
		c.maxAge = strconv.Itoa(int(config.MaxAge.Seconds()))
	}

	var problems []error
	for _, origin := range config.AllowedOrigins {
		switch prefix, suffix, wildcard := strings.Cut(strings.ToLower(origin), "*"); {
		case origin == "*":
			c.any = true
		case !validOrigin(origin):
			problems = append(problems, fmt.Errorf("gonestly: ServerConfig.CORS: the origin %q must be a scheme and a host without a path, such as https://app.example.com", origin))
		case wildcard:
			c.patterns = append(c.patterns, [2]string{prefix, suffix})
		default:
			c.origins[strings.ToLower(origin)] = true
		}
	}
	if c.any && c.credentials {
		problems = append(problems, errors.New(`gonestly: ServerConfig.CORS: AllowCredentials cannot be combined with the origin "*"; list the origins that may send credentials`))
	}
	return c, problems
}

// validOrigin accepts scheme://host[:port], with at most one * for a subdomain.
func validOrigin(origin string) bool {
	u, err := url.Parse(strings.Replace(origin, "*", "wildcard", 1))
	return err == nil && u.Scheme != "" && u.Host != "" && u.Scheme+"://"+u.Host == strings.Replace(origin, "*", "wildcard", 1) &&
		strings.Count(origin, "*") <= 1 && (!strings.Contains(origin, "*") || strings.Contains(origin, "://*."))
}

func (c *cors) allows(origin string) bool {
	origin = strings.ToLower(origin)
	if c.any || c.origins[origin] {
		return true
	}
	for _, p := range c.patterns {
		if len(origin) > len(p[0])+len(p[1]) && strings.HasPrefix(origin, p[0]) && strings.HasSuffix(origin, p[1]) {
			return true
		}
	}
	return false
}

// wrap answers preflight requests and adds the CORS headers to the responses of next.
func (c *cors) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		origin := r.Header.Get("Origin")
		requested := r.Header.Get("Access-Control-Request-Method")
		preflight := r.Method == http.MethodOptions && origin != "" && requested != ""
		// Caches must keep the answers for different origins apart.
		if !c.any || c.credentials {
			header.Add("Vary", "Origin")
		}
		if preflight {
			header.Add("Vary", "Access-Control-Request-Method")
			header.Add("Vary", "Access-Control-Request-Headers")
		}
		if origin == "" || !c.allows(origin) {
			if preflight {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		if c.any {
			header.Set("Access-Control-Allow-Origin", "*")
		} else {
			header.Set("Access-Control-Allow-Origin", origin)
		}
		if c.credentials {
			header.Set("Access-Control-Allow-Credentials", "true")
		}
		if !preflight {
			if c.exposed != "" {
				header.Set("Access-Control-Expose-Headers", c.exposed)
			}
			next.ServeHTTP(w, r)
			return
		}
		if slices.Contains(c.methods, requested) {
			header.Set("Access-Control-Allow-Methods", strings.Join(c.methods, ", "))
			if headers := cmp.Or(c.headers, r.Header.Get("Access-Control-Request-Headers")); headers != "" {
				header.Set("Access-Control-Allow-Headers", headers)
			}
			header.Set("Access-Control-Max-Age", c.maxAge)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
