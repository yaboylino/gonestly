package gonestly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const coreModuleName = "gonestly"

// InspectEnv makes Run print information about the application instead of serving it. The
// gonestly CLI sets it to "routes" or "openapi" for the commands of the same name.
const InspectEnv = "GONESTLY_INSPECT"

// InspectOutputEnv names the file that the inspection is written to instead of standard
// output, so that what the application itself prints cannot end up in it.
const InspectOutputEnv = "GONESTLY_INSPECT_OUTPUT"

type appState int

const (
	stateIdle appState = iota
	stateStarting
	stateStarted
	stateStopped
)

// App is an application built from a root module.
type App struct {
	container       *container
	logger          *slog.Logger
	shutdownTimeout time.Duration
	shutdownDelay   time.Duration

	listen bool
	// stopping is set as soon as the application is asked to stop, before the server closes.
	stopping atomic.Bool

	// lifecycle serializes Start and Stop; mu guards the fields below, so that hooks and
	// running requests can still use Resolve and the accessors while the app starts or stops.
	lifecycle sync.Mutex
	mu        sync.RWMutex
	state     appState
	live      []*providerNode
	server    *server

	done        chan struct{}
	doneOnce    sync.Once
	shutdownErr error
}

// New validates the module graph below root and reports every problem in it in one *BuildError.
func New(root *Module, opts ...Option) (*App, error) {
	o := options{logger: slog.Default(), shutdownTimeout: defaultShutdownTimeout, listen: true}
	for _, opt := range opts {
		opt(&o)
	}

	app := &App{logger: o.logger, shutdownTimeout: o.shutdownTimeout, shutdownDelay: o.shutdownDelay, listen: o.listen, done: make(chan struct{})}
	core := &Module{
		Name:      coreModuleName,
		Global:    true,
		Providers: Providers(Value(o.logger), Value[Shutdowner](app), Value[Health](appHealth{app: app})),
		Exports:   Exports(Type[*slog.Logger](), Type[Shutdowner](), Type[Health]()),
	}
	c, err := buildContainer(root, core, o.overrides)
	if err != nil {
		return nil, err
	}
	app.container = c
	return app, nil
}

// Start constructs every provider in dependency order and calls OnStart on each Starter, then
// starts the HTTP server if there are controllers. If anything fails, the providers that
// already started are stopped again.
func (a *App) Start(ctx context.Context) error {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	if a.currentState() != stateIdle {
		return errors.New("gonestly: an application can only be started once")
	}
	a.setState(stateStarting)

	begin := time.Now()
	var live []*providerNode
	for _, pn := range a.container.order {
		if err := pn.build(ctx); err != nil {
			return a.rollback(ctx, live, fmt.Errorf("gonestly: constructing %s: %w", pn, err))
		}
		if s, ok := pn.instance.Interface().(Starter); ok && !pn.provider.alias {
			if err := callHook(func() error { return s.OnStart(ctx) }); err != nil {
				return a.rollback(ctx, live, fmt.Errorf("gonestly: starting %s: %w", pn, err))
			}
		}
		live = append(live, pn)
	}
	server, err := a.startServer(ctx)
	if err != nil {
		return a.rollback(ctx, live, err)
	}
	a.mu.Lock()
	a.state, a.live, a.server = stateStarted, live, server
	a.mu.Unlock()

	a.logger.InfoContext(ctx, "application started",
		slog.Int("modules", len(a.container.modules)-1),
		slog.Int("providers", len(a.container.order)),
		slog.Duration("duration", time.Since(begin)),
	)
	return nil
}

// Stop shuts the HTTP server down, waiting for running requests, then calls OnStop on every
// started Stopper in reverse start order. It returns all errors and does nothing if the
// application is not running.
func (a *App) Stop(ctx context.Context) error {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	a.mu.RLock()
	state, server, live := a.state, a.server, a.live
	a.mu.RUnlock()
	if state != stateStarted {
		return nil
	}
	a.stopping.Store(true)
	var serverErr error
	if server != nil {
		serverErr = server.shutdown(ctx)
	}
	err := errors.Join(serverErr, stopProviders(ctx, live))
	a.mu.Lock()
	a.state, a.live = stateStopped, nil
	a.mu.Unlock()
	a.logger.InfoContext(ctx, "application stopped")
	return err
}

func (a *App) currentState() appState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state
}

func (a *App) setState(state appState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state = state
}

// Handler returns the HTTP handler of a started application, or nil if it has no controllers.
// Use it to serve the application yourself or to test it with net/http/httptest.
func (a *App) Handler() http.Handler {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.server == nil {
		return nil
	}
	return a.server.handler
}

// Addr returns the address the HTTP server listens on, or nil if it does not listen. With
// the port 0 in ServerConfig.Addr, it tells which port was chosen.
func (a *App) Addr() net.Addr {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.server == nil || a.server.listener == nil {
		return nil
	}
	return a.server.listener.Addr()
}

// OpenAPI returns the OpenAPI 3.1 document of a started application as JSON, or nil if it has
// no controllers. It is built even when OpenAPIConfig.Disabled stops serving it.
func (a *App) OpenAPI() []byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.server == nil {
		return nil
	}
	return a.server.openAPI
}

// Routes returns the routes of a started application, in registration order.
func (a *App) Routes() []RouteInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.server == nil {
		return nil
	}
	return a.server.routeInfo()
}

// inspect starts the application without listening, writes its routes or OpenAPI document as
// JSON to w and stops it again.
func (a *App) inspect(ctx context.Context, mode string, w io.Writer) error {
	if mode != "routes" && mode != "openapi" {
		return fmt.Errorf("gonestly: %s must be routes or openapi, not %q", InspectEnv, mode)
	}
	a.listen = false
	if err := a.Start(ctx); err != nil {
		return err
	}
	var output []byte
	var err error
	if mode == "routes" {
		output, err = json.Marshal(a.Routes())
	} else {
		output = a.OpenAPI()
	}
	if err == nil {
		_, err = w.Write(output)
	}
	return errors.Join(err, a.Stop(context.WithoutCancel(ctx)))
}

func (a *App) inspectTo(ctx context.Context, mode, path string) error {
	if path == "" {
		return a.inspect(ctx, mode, os.Stdout)
	}
	file, err := os.Create(path) //nolint:gosec // the path comes from the environment of the process
	if err != nil {
		return fmt.Errorf("gonestly: %s: %w", InspectOutputEnv, err)
	}
	return errors.Join(a.inspect(ctx, mode, file), file.Close())
}

func (a *App) startServer(ctx context.Context) (*server, error) {
	if len(a.container.controllers) == 0 {
		return nil, nil //nolint:nilnil // an application without controllers has no server
	}
	controllers := make([]controllerInstance, 0, len(a.container.controllers))
	for _, pn := range a.container.controllers {
		c, _ := pn.instance.Interface().(Controller)
		controllers = append(controllers, controllerInstance{name: pn.String(), module: pn.module.module.Name, controller: c})
	}
	config, _ := a.rootInstance(reflect.TypeFor[ServerConfig]()).(ServerConfig)
	validator, _ := a.rootInstance(reflect.TypeFor[StructValidator]()).(StructValidator)
	openAPI, _ := a.rootInstance(reflect.TypeFor[OpenAPIConfig]()).(OpenAPIConfig)

	s, err := newServer(controllers, config, openAPI, validator, a.logger)
	if err != nil {
		return nil, err
	}
	if a.listen {
		if err := s.listen(ctx, a.Shutdown); err != nil {
			return nil, err
		}
		a.logger.InfoContext(ctx, "http server listening", slog.String("addr", s.listener.Addr().String()), slog.Int("routes", len(s.routes)))
	}
	return s, nil
}

func (a *App) rootInstance(typ reflect.Type) any {
	if pn, ok := a.container.root.visible[typ]; ok {
		return pn.instance.Interface()
	}
	return nil
}

// Run starts the application, waits for an interrupt, a termination signal, a canceled ctx or a
// call to Shutdown, and then stops it within the shutdown timeout.
func (a *App) Run(ctx context.Context) error {
	if mode := os.Getenv(InspectEnv); mode != "" {
		return a.inspectTo(ctx, mode, os.Getenv(InspectOutputEnv))
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := a.Start(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-a.done:
	}
	// Restore the default signal handling, so a second interrupt terminates the process at once.
	stop()
	// From here Health reports that the application is unavailable, so that whoever sends it
	// traffic can stop doing so before the server closes.
	a.stopping.Store(true)
	if a.shutdownDelay > 0 {
		a.logger.InfoContext(ctx, "application stopping", slog.Duration("delay", a.shutdownDelay))
		time.Sleep(a.shutdownDelay)
	}

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.shutdownTimeout)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- a.Stop(stopCtx) }()
	var stopErr error
	select {
	case stopErr = <-stopped:
	case <-stopCtx.Done():
		stopErr = fmt.Errorf("gonestly: the application did not stop within %s", a.shutdownTimeout)
	}

	select {
	case <-a.done:
		return errors.Join(a.shutdownErr, stopErr)
	default:
		return stopErr
	}
}

// Shutdown makes Run stop the application and return err. Only the first call has effect.
func (a *App) Shutdown(err error) {
	a.doneOnce.Do(func() {
		a.shutdownErr = err
		close(a.done)
	})
}

// Resolve returns the provider of type T that is available in the root module of a started app.
func Resolve[T any](a *App) (T, error) {
	var zero T
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state != stateStarted {
		return zero, fmt.Errorf("gonestly: %w", ErrNotStarted)
	}
	typ := reflect.TypeFor[T]()
	pn, ok := a.container.root.visible[typ]
	if !ok {
		return zero, fmt.Errorf("gonestly: %w: %s is not available in module %q", ErrMissingDependency, typ, a.container.root.module.Name)
	}
	v, _ := pn.instance.Interface().(T)
	return v, nil
}

// callHook runs a lifecycle hook and turns a panic into an error.
func callHook(hook func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return hook()
}

func (a *App) rollback(ctx context.Context, live []*providerNode, cause error) error {
	err := errors.Join(cause, stopProviders(ctx, live))
	a.setState(stateStopped)
	return err
}

func stopProviders(ctx context.Context, live []*providerNode) error {
	var errs []error
	for _, pn := range slices.Backward(live) {
		s, ok := pn.instance.Interface().(Stopper)
		if !ok || pn.provider.alias {
			continue
		}
		if err := callHook(func() error { return s.OnStop(ctx) }); err != nil {
			errs = append(errs, fmt.Errorf("gonestly: stopping %s: %w", pn, err))
		}
	}
	return errors.Join(errs...)
}
