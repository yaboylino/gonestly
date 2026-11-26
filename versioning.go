package gonestly

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// VersioningConfig lets a client pick the version of the API with a header instead of the path.
// Routes are registered under a version with Router.Version, which puts it in the path:
// /v2/users. With a header the client can also ask for GET /users with "API-Version: 2".
type VersioningConfig struct {
	// Header names the request header that carries the version, such as "API-Version". Without
	// it the version is only read from the path.
	Header string
	// Default is the version used for a request that has no version in the header: the
	// routes of that version answer the unversioned path. Without one, an unversioned path
	// only reaches routes that are registered without a version, such as the health checks.
	Default string
}

// APIVersionHeader is set on the response of every route that is registered under a version,
// with the version that answered.
const APIVersionHeader = "API-Version"

var versionPattern = regexp.MustCompile(`^v?([0-9]+(?:\.[0-9]+)?)$`)

// normalizeVersion accepts "1", "v1" and "1.1" and returns the version without the v.
func normalizeVersion(v string) (string, bool) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// DeprecationOption describes when a route or a version is deprecated and removed.
type DeprecationOption func(*deprecation)

type deprecation struct {
	since     time.Time
	sunset    time.Time
	successor string
}

// DeprecatedSince says since when the route is deprecated, for the Deprecation header.
func DeprecatedSince(t time.Time) DeprecationOption {
	return func(d *deprecation) { d.since = t }
}

// SunsetAt says when the route stops working, for the Sunset header. Clients that read it can
// warn their developers before it happens.
func SunsetAt(t time.Time) DeprecationOption {
	return func(d *deprecation) { d.sunset = t }
}

// SuccessorAt names the path or URL that replaces the route, for a Link header with
// rel="successor-version".
func SuccessorAt(location string) DeprecationOption {
	return func(d *deprecation) { d.successor = location }
}

func newDeprecation(opts []DeprecationOption) *deprecation {
	d := &deprecation{}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

func (d *deprecation) problems() []string {
	var problems []string
	if !d.since.IsZero() && !d.sunset.IsZero() && d.sunset.Before(d.since) {
		problems = append(problems, "the sunset date is before the date it was deprecated")
	}
	if d.successor != "" && !strings.HasPrefix(d.successor, "/") && !strings.HasPrefix(d.successor, "http://") && !strings.HasPrefix(d.successor, "https://") {
		problems = append(problems, fmt.Sprintf("the successor %q is not a path or a URL", d.successor))
	}
	return problems
}

// setHeaders adds the headers of RFC 9745 (Deprecation) and RFC 8594 (Sunset), and the link to
// what replaces the route.
func (d *deprecation) setHeaders(h http.Header) {
	if d.since.IsZero() {
		h.Set("Deprecation", "true")
	} else {
		h.Set("Deprecation", "@"+strconv.FormatInt(d.since.Unix(), 10))
	}
	if !d.sunset.IsZero() {
		h.Set("Sunset", d.sunset.UTC().Format(http.TimeFormat))
	}
	if d.successor != "" {
		h.Add("Link", "<"+d.successor+`>; rel="successor-version"`)
	}
}

// describe is the sentence about the deprecation in the OpenAPI document.
func (d *deprecation) describe() string {
	text := "Deprecated"
	if !d.since.IsZero() {
		text += " since " + d.since.UTC().Format("2006-01-02")
	}
	text += "."
	if !d.sunset.IsZero() {
		text += " Stops working on " + d.sunset.UTC().Format("2006-01-02") + "."
	}
	if d.successor != "" {
		text += " Use " + d.successor + " instead."
	}
	return text
}

// routeVersions lists the versions that routes are registered under, in order.
func routeVersions(routes []*route) []string {
	var versions []string
	for _, rt := range routes {
		if rt.version != "" && !slices.Contains(versions, rt.version) {
			versions = append(versions, rt.version)
		}
	}
	slices.SortFunc(versions, compareVersions)
	return versions
}

// compareVersions orders 1, 2, 10 and 1.1 as numbers.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

// checkVersioning reports a header configuration that cannot work.
func (s *server) checkVersioning() []error {
	cfg := s.config.Versioning
	switch {
	case cfg.Header == "" && cfg.Default == "":
		return nil
	case cfg.Header == "":
		return []error{fmt.Errorf("%w: Versioning.Default needs Versioning.Header", ErrInvalidRoute)}
	case cfg.Default == "":
		return nil
	}
	v, ok := normalizeVersion(cfg.Default)
	if !ok {
		return []error{fmt.Errorf("%w: Versioning.Default %q is not a number such as 1 or 1.1", ErrInvalidRoute, cfg.Default)}
	}
	if !slices.Contains(s.versions, v) {
		return []error{fmt.Errorf("%w: Versioning.Default is version %s, but no route is registered under it; the versions are %s", ErrInvalidRoute, v, strings.Join(s.versions, ", "))}
	}
	return nil
}

// negotiateVersion routes an unversioned path to the version that the header names, or to the
// default. A path that already has a version, and paths no version has, are left alone. It
// answers a header with a version that does not exist and returns false.
func (s *server) negotiateVersion(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	cfg := s.config.Versioning
	if cfg.Header == "" {
		return r, true
	}
	requested := strings.TrimSpace(r.Header.Get(cfg.Header))
	version := requested
	if version == "" {
		version = cfg.Default
	}
	if version == "" {
		return r, true
	}
	v, ok := normalizeVersion(version)
	if !ok || !slices.Contains(s.versions, v) {
		if requested == "" {
			return r, true
		}
		writeProblem(w, Problem{
			Type:     "about:blank",
			Title:    http.StatusText(http.StatusBadRequest),
			Status:   http.StatusBadRequest,
			Detail:   fmt.Sprintf("The version %q in the %s header does not exist; use one of: %s.", requested, cfg.Header, strings.Join(s.versions, ", ")),
			Instance: r.URL.Path,
		})
		return r, false
	}
	if first, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/"); versionPattern.MatchString(first) && strings.HasPrefix(first, "v") {
		return r, true
	}
	versioned := r.Clone(r.Context())
	versioned.URL.Path = joinPath("v"+v, r.URL.Path)
	versioned.URL.RawPath = ""
	// Any method counts, so that a method the version does not have is answered with 405 and
	// not as if the path did not exist.
	for _, method := range []string{r.Method, http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		probe := versioned.Clone(r.Context())
		probe.Method = method
		if _, pattern := s.mux.Handler(probe); pattern != "" {
			return versioned, true
		}
	}
	return r, true
}
