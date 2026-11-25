package gonestly

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
)

// Controller is implemented by the providers in Module.Controllers. Routes registers the
// controller's routes when the application starts.
type Controller interface {
	Routes(r *Router)
}

// Middleware wraps an http.Handler, the standard way to add behavior to net/http handlers.
type Middleware = func(http.Handler) http.Handler

// Router registers the routes of one controller. Prefix, Guard, Intercept and Use apply to
// the routes registered after them; Group scopes them to a part of the routes.
type Router struct {
	controller   string
	module       string
	prefix       string
	guards       []Guard
	interceptors []Interceptor
	middleware   []Middleware
	routes       *[]*route
}

// RouteInfo describes a registered route.
type RouteInfo struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	Controller string `json:"controller"`
}

type route struct {
	method       string
	path         string
	controller   string
	module       string
	reqType      reflect.Type
	resType      reflect.Type
	handlerName  string
	docs         routeDocs
	binder       *binder
	call         func(ctx context.Context, req reflect.Value) (any, error)
	raw          http.Handler
	status       int
	emptyResult  bool
	guards       []Guard
	interceptors []Interceptor
	middleware   []Middleware
	problems     []string
}

// Prefix adds a path prefix, such as "/users", to the routes registered after it.
func (r *Router) Prefix(prefix string) *Router {
	r.prefix = joinPath(r.prefix, prefix)
	return r
}

// Guard adds guards that run, in order, before every route registered after it.
func (r *Router) Guard(guards ...Guard) *Router {
	r.guards = append(slices.Clip(r.guards), guards...)
	return r
}

// Intercept adds interceptors that wrap every route registered after it; the first is outermost.
func (r *Router) Intercept(interceptors ...Interceptor) *Router {
	r.interceptors = append(slices.Clip(r.interceptors), interceptors...)
	return r
}

// Use adds net/http middleware around every route registered after it; the first is outermost.
func (r *Router) Use(middleware ...Middleware) *Router {
	r.middleware = append(slices.Clip(r.middleware), middleware...)
	return r
}

// Group registers routes under an extra prefix; guards, interceptors and middleware added
// inside fn apply only to those routes.
func (r *Router) Group(prefix string, fn func(r *Router)) {
	fn(&Router{
		controller:   r.controller,
		module:       r.module,
		prefix:       joinPath(r.prefix, prefix),
		guards:       slices.Clip(r.guards),
		interceptors: slices.Clip(r.interceptors),
		middleware:   slices.Clip(r.middleware),
		routes:       r.routes,
	})
}

// RouteOption changes one route.
type RouteOption func(*routeOptions)

type routeOptions struct {
	status       int
	guards       []Guard
	interceptors []Interceptor
	docs         routeDocs
}

// routeDocs describes a route in the OpenAPI document.
type routeDocs struct {
	summary     string
	description string
	tags        []string
	operationID string
	deprecated  bool
}

// Summary sets the one line summary of a route in the OpenAPI document. It defaults to the
// handler's name in words, such as "Find one" for FindOne.
func Summary(summary string) RouteOption {
	return func(o *routeOptions) { o.docs.summary = summary }
}

// Description sets the longer description of a route in the OpenAPI document; Markdown works.
func Description(description string) RouteOption {
	return func(o *routeOptions) { o.docs.description = description }
}

// Tags groups a route in the OpenAPI document. It defaults to the name of its module.
func Tags(tags ...string) RouteOption {
	return func(o *routeOptions) { o.docs.tags = append(o.docs.tags, tags...) }
}

// OperationID sets the operation ID in the OpenAPI document, which client generators use as
// the method name. It defaults to the handler's name.
func OperationID(id string) RouteOption {
	return func(o *routeOptions) { o.docs.operationID = id }
}

// Deprecated marks a route as deprecated in the OpenAPI document.
func Deprecated() RouteOption {
	return func(o *routeOptions) { o.docs.deprecated = true }
}

// Status sets the status code of a successful response. It defaults to 201 for POST, 204 for
// an Empty response and 200 otherwise.
func Status(code int) RouteOption {
	return func(o *routeOptions) { o.status = code }
}

// WithGuards adds guards to one route, after the guards of the router.
func WithGuards(guards ...Guard) RouteOption {
	return func(o *routeOptions) { o.guards = append(o.guards, guards...) }
}

// WithInterceptors adds interceptors to one route, inside the interceptors of the router.
func WithInterceptors(interceptors ...Interceptor) RouteOption {
	return func(o *routeOptions) { o.interceptors = append(o.interceptors, interceptors...) }
}

// Get registers a handler for GET requests, which also answers HEAD requests.
func Get[Req, Res any](r *Router, path string, h Handler[Req, Res], opts ...RouteOption) {
	addRoute(r, http.MethodGet, path, h, opts)
}

// Post registers a handler for POST requests.
func Post[Req, Res any](r *Router, path string, h Handler[Req, Res], opts ...RouteOption) {
	addRoute(r, http.MethodPost, path, h, opts)
}

// Put registers a handler for PUT requests.
func Put[Req, Res any](r *Router, path string, h Handler[Req, Res], opts ...RouteOption) {
	addRoute(r, http.MethodPut, path, h, opts)
}

// Patch registers a handler for PATCH requests.
func Patch[Req, Res any](r *Router, path string, h Handler[Req, Res], opts ...RouteOption) {
	addRoute(r, http.MethodPatch, path, h, opts)
}

// Delete registers a handler for DELETE requests.
func Delete[Req, Res any](r *Router, path string, h Handler[Req, Res], opts ...RouteOption) {
	addRoute(r, http.MethodDelete, path, h, opts)
}

// Handle registers a plain http.Handler, for responses that are not JSON, such as files or
// streams. Guards and middleware apply; binding, validation and interceptors do not.
func Handle(r *Router, method, path string, h http.Handler, opts ...RouteOption) {
	rt := r.newRoute(method, path, opts)
	rt.raw = h
	if h == nil {
		rt.problems = append(rt.problems, "the handler is nil")
	}
}

func addRoute[Req, Res any](r *Router, method, path string, h Handler[Req, Res], opts []RouteOption) {
	rt := r.newRoute(method, path, opts)
	b, problems := newBinder(reflect.TypeFor[Req]())
	rt.binder = b
	rt.reqType = reflect.TypeFor[Req]()
	rt.resType = reflect.TypeFor[Res]()
	rt.emptyResult = rt.resType == emptyType
	rt.problems = append(rt.problems, problems...)
	rt.problems = append(rt.problems, b.pathProblems(rt.path)...)
	if h == nil {
		rt.problems = append(rt.problems, "the handler is nil")
		return
	}
	rt.handlerName = funcName(reflect.ValueOf(h))
	rt.call = func(ctx context.Context, req reflect.Value) (any, error) {
		return h(ctx, req.Interface().(Req)) //nolint:forcetypeassert // the binder creates a Req
	}
}

func (r *Router) newRoute(method, path string, opts []RouteOption) *route {
	var o routeOptions
	for _, opt := range opts {
		opt(&o)
	}
	rt := &route{
		method:       strings.ToUpper(method),
		path:         joinPath(r.prefix, path),
		controller:   r.controller,
		module:       r.module,
		status:       o.status,
		docs:         o.docs,
		guards:       append(slices.Clip(r.guards), o.guards...),
		interceptors: append(slices.Clip(r.interceptors), o.interceptors...),
		middleware:   slices.Clip(r.middleware),
	}
	if o.status != 0 && (o.status < 200 || o.status > 399) {
		rt.problems = append(rt.problems, fmt.Sprintf("the status %d is not a success status", o.status))
	}
	*r.routes = append(*r.routes, rt)
	return rt
}

func (rt *route) String() string {
	return fmt.Sprintf("%s %s in %s", rt.method, rt.path, rt.controller)
}

// muxPattern returns the net/http pattern; "{$}" keeps paths ending in a slash from matching
// everything below them.
func (rt *route) muxPattern() string {
	path := rt.path
	if strings.HasSuffix(path, "/") {
		path += "{$}"
	}
	return rt.method + " " + path
}

// joinPath joins URL paths with single slashes and without a trailing slash, except for "/".
func joinPath(parts ...string) string {
	var segments []string
	for _, part := range parts {
		for _, segment := range strings.Split(part, "/") {
			if segment != "" {
				segments = append(segments, segment)
			}
		}
	}
	return "/" + strings.Join(segments, "/")
}
