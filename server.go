package gonestly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"runtime/debug"
	"slices"
	"time"
)

const (
	defaultAddr              = ":3000"
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	defaultMaxBodyBytes      = 1 << 20
)

// ServerConfig configures the HTTP server that serves the controllers. Provide one in the root
// module, for example built from your configuration, to change the defaults.
type ServerConfig struct {
	// Addr is the address to listen on. It defaults to ":3000".
	Addr string
	// ReadHeaderTimeout, ReadTimeout, WriteTimeout and IdleTimeout default to 10s, 30s, 30s and 120s.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	// MaxBodyBytes limits the size of a request body. It defaults to 1 MiB.
	MaxBodyBytes int64
	// Middleware wraps every request, including unknown routes; the first is outermost.
	Middleware []Middleware
	// Guards run before the guards of every route.
	Guards []Guard
	// Interceptors wrap the interceptors of every route; the first is outermost.
	Interceptors []Interceptor
}

func (c ServerConfig) withDefaults() ServerConfig {
	if c.Addr == "" {
		c.Addr = defaultAddr
	}
	if c.ReadHeaderTimeout == 0 {
		c.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = defaultReadTimeout
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = defaultWriteTimeout
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = defaultIdleTimeout
	}
	if c.MaxBodyBytes == 0 {
		c.MaxBodyBytes = defaultMaxBodyBytes
	}
	return c
}

type server struct {
	config    ServerConfig
	openAPI   []byte
	logger    *slog.Logger
	validator StructValidator
	routes    []*route
	mux       *http.ServeMux
	handler   http.Handler
	http      *http.Server
	listener  net.Listener
}

type controllerInstance struct {
	name       string
	module     string
	controller Controller
}

// newServer collects the routes of every controller and reports all route problems at once.
func newServer(controllers []controllerInstance, config ServerConfig, openAPI OpenAPIConfig, validator StructValidator, logger *slog.Logger) (*server, error) {
	openAPI = openAPI.withDefaults()
	s := &server{config: config.withDefaults(), logger: logger, validator: validator, mux: http.NewServeMux()}

	var problems []error
	for _, c := range controllers {
		if err := s.collectRoutes(c); err != nil {
			problems = append(problems, err)
		}
	}
	for _, rt := range s.routes {
		for _, p := range rt.problems {
			problems = append(problems, fmt.Errorf("%w: %s: %s", ErrInvalidRoute, rt, p))
		}
	}
	if !openAPI.Disabled {
		problems = append(problems, openAPIRouteConflict(s.routes, openAPI)...)
	}
	if len(problems) > 0 {
		return nil, &BuildError{Problems: problems}
	}
	document, err := json.Marshal(buildOpenAPI(s.routes, openAPI, s.config.Guards))
	if err != nil {
		return nil, fmt.Errorf("gonestly: encoding the OpenAPI document: %w", err)
	}
	s.openAPI = document
	if !openAPI.Disabled {
		s.serveOpenAPI(openAPI)
	}
	for _, rt := range s.routes {
		if err := s.register(rt); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		return nil, &BuildError{Problems: problems}
	}

	var handler http.Handler = http.HandlerFunc(s.serveHTTP)
	for _, mw := range slices.Backward(s.config.Middleware) {
		handler = mw(handler)
	}
	s.handler = s.recovered(handler)
	return s, nil
}

// recovered answers a panic in a handler, guard, interceptor or middleware with a 500.
func (s *server) recovered(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // net/http compares the sentinel directly
				panic(v)
			}
			s.logger.ErrorContext(r.Context(), "handler panicked", slog.Any("panic", v), slog.String("stack", string(debug.Stack())))
			writeError(w, r, fmt.Errorf("panic: %v", v), s.logger)
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *server) serveOpenAPI(config OpenAPIConfig) {
	ui := renderSwaggerUI(config.Title, relativeURL(config.DocsPath, config.JSONPath))
	s.mux.HandleFunc("GET "+config.JSONPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(s.openAPI)
	})
	s.mux.HandleFunc("GET "+config.DocsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(ui)
	})
}

func (s *server) collectRoutes(c controllerInstance) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: the Routes method of %s panicked: %v", ErrInvalidRoute, c.name, r)
		}
	}()
	c.controller.Routes(&Router{controller: c.name, module: c.module, routes: &s.routes})
	return nil
}

func (s *server) register(rt *route) (err error) {
	defer func() {
		// ServeMux panics on invalid and conflicting patterns; its message names both routes.
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %s: %v", ErrInvalidRoute, rt, r)
		}
	}()
	s.mux.Handle(rt.muxPattern(), s.routeHandler(rt))
	return nil
}

func (s *server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if _, pattern := s.mux.Handler(r); pattern == "" {
		s.mux.ServeHTTP(&problemWriter{ResponseWriter: w, request: r}, r)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *server) routeHandler(rt *route) http.Handler {
	guards := append(slices.Clip(s.config.Guards), rt.guards...)
	interceptors := append(slices.Clip(s.config.Interceptors), rt.interceptors...)

	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), requestKey{}, &requestState{request: r, header: w.Header()})
		for _, guard := range guards {
			next, err := guard.Check(ctx, r)
			if err != nil {
				writeError(w, r, err, s.logger)
				return
			}
			if next != nil {
				ctx = next
			}
		}
		r = r.WithContext(ctx)
		if rt.raw != nil {
			rt.raw.ServeHTTP(w, r)
			return
		}

		req, err := rt.binder.bind(w, r, s.config.MaxBodyBytes)
		if err == nil {
			err = s.validate(req)
		}
		if err != nil {
			writeError(w, r, err, s.logger)
			return
		}

		next := Next(func(ctx context.Context) (any, error) { return rt.call(ctx, req) })
		for _, interceptor := range slices.Backward(interceptors) {
			inner := next
			next = func(ctx context.Context) (any, error) { return interceptor.Intercept(ctx, r, inner) }
		}
		result, err := next(ctx)
		if err != nil {
			writeError(w, r, err, s.logger)
			return
		}
		s.writeResult(w, r, rt, result)
	})
	for _, mw := range slices.Backward(rt.middleware) {
		handler = mw(handler)
	}
	return handler
}

func (s *server) validate(req reflect.Value) error {
	var err error
	if s.validator != nil {
		err = s.validator.ValidateStruct(req.Interface())
	}
	if v, ok := req.Addr().Interface().(Validatable); ok && err == nil {
		err = v.Validate()
	}
	if err == nil {
		return nil
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return err
	}
	return BadRequest("%s", err.Error()).WithCause(err)
}

func (s *server) writeResult(w http.ResponseWriter, r *http.Request, rt *route, result any) {
	status := rt.status
	if _, empty := result.(Empty); empty || rt.emptyResult {
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
		return
	}
	if status == 0 {
		status = http.StatusOK
		if rt.method == http.MethodPost {
			status = http.StatusCreated
		}
	}
	if err := writeJSON(w, r, status, result); err != nil {
		writeError(w, r, fmt.Errorf("encoding the response: %w", err), s.logger)
	}
}

// listen opens the listener so that an address in use fails Start, then serves in the background.
func (s *server) listen(ctx context.Context, onError func(error)) error {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", s.config.Addr)
	if err != nil {
		return fmt.Errorf("gonestly: listening on %s: %w", s.config.Addr, err)
	}
	s.listener = listener
	s.http = &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: s.config.ReadHeaderTimeout,
		ReadTimeout:       s.config.ReadTimeout,
		WriteTimeout:      s.config.WriteTimeout,
		IdleTimeout:       s.config.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
	}
	go func() {
		if err := s.http.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			onError(fmt.Errorf("gonestly: the HTTP server stopped: %w", err))
		}
	}()
	return nil
}

func (s *server) shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	if err := s.http.Shutdown(ctx); err != nil {
		return fmt.Errorf("gonestly: stopping the HTTP server: %w", err)
	}
	return nil
}

func (s *server) routeInfo() []RouteInfo {
	infos := make([]RouteInfo, 0, len(s.routes))
	for _, rt := range s.routes {
		infos = append(infos, RouteInfo{Method: rt.method, Path: rt.path, Controller: rt.controller})
	}
	return infos
}

// problemWriter replaces the plain text 404 and 405 responses of ServeMux with problem details.
type problemWriter struct {
	http.ResponseWriter
	request   *http.Request
	rewritten bool
}

func (p *problemWriter) WriteHeader(status int) {
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		p.ResponseWriter.WriteHeader(status)
		return
	}
	p.rewritten = true
	detail := fmt.Sprintf("No route matches %s %s.", p.request.Method, p.request.URL.Path)
	if status == http.StatusMethodNotAllowed {
		detail = fmt.Sprintf("%s is not allowed for %s.", p.request.Method, p.request.URL.Path)
	}
	p.Header().Del("Content-Type")
	writeProblem(p.ResponseWriter, Problem{
		Type:     "about:blank",
		Title:    http.StatusText(status),
		Status:   status,
		Detail:   detail,
		Instance: p.request.URL.Path,
	})
}

func (p *problemWriter) Write(b []byte) (int, error) {
	if p.rewritten {
		return len(b), nil
	}
	return p.ResponseWriter.Write(b)
}
