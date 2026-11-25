package gonestly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"sync"
	"syscall"
	"time"
)

const coreModuleName = "gonestly"

type appState int

const (
	stateIdle appState = iota
	stateStarted
	stateStopped
)

// App is an application built from a root module.
type App struct {
	container       *container
	logger          *slog.Logger
	shutdownTimeout time.Duration

	mu    sync.Mutex
	state appState
	live  []*providerNode

	done        chan struct{}
	doneOnce    sync.Once
	shutdownErr error
}

// New validates the module graph below root and reports every problem in it in one *BuildError.
func New(root *Module, opts ...Option) (*App, error) {
	o := options{logger: slog.Default(), shutdownTimeout: defaultShutdownTimeout}
	for _, opt := range opts {
		opt(&o)
	}

	app := &App{logger: o.logger, shutdownTimeout: o.shutdownTimeout, done: make(chan struct{})}
	core := &Module{
		Name:      coreModuleName,
		Global:    true,
		Providers: Providers(Value(o.logger), Value[Shutdowner](app)),
		Exports:   Exports(Type[*slog.Logger](), Type[Shutdowner]()),
	}
	c, err := buildContainer(root, core, o.overrides)
	if err != nil {
		return nil, err
	}
	app.container = c
	return app, nil
}

// Start constructs every provider in dependency order and calls OnStart on each Starter.
// If one fails, the providers that already started are stopped again.
func (a *App) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stateIdle {
		return errors.New("gonestly: an application can only be started once")
	}
	a.state = stateStarted

	begin := time.Now()
	for _, pn := range a.container.order {
		if err := pn.build(ctx); err != nil {
			return a.rollback(ctx, fmt.Errorf("gonestly: constructing %s: %w", pn, err))
		}
		if s, ok := pn.instance.Interface().(Starter); ok {
			if err := callHook(func() error { return s.OnStart(ctx) }); err != nil {
				return a.rollback(ctx, fmt.Errorf("gonestly: starting %s: %w", pn, err))
			}
		}
		a.live = append(a.live, pn)
	}

	a.logger.InfoContext(ctx, "application started",
		slog.Int("modules", len(a.container.modules)-1),
		slog.Int("providers", len(a.container.order)),
		slog.Duration("duration", time.Since(begin)),
	)
	return nil
}

// Stop calls OnStop on every started Stopper in reverse start order and returns all their errors.
// It does nothing if the application is not running.
func (a *App) Stop(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stateStarted {
		return nil
	}
	err := a.stopLive(ctx)
	a.logger.InfoContext(ctx, "application stopped")
	return err
}

// Run starts the application, waits for an interrupt, a termination signal, a canceled ctx or a
// call to Shutdown, and then stops it within the shutdown timeout.
func (a *App) Run(ctx context.Context) error {
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
	a.mu.Lock()
	defer a.mu.Unlock()
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

func (a *App) rollback(ctx context.Context, cause error) error {
	return errors.Join(cause, a.stopLive(ctx))
}

func (a *App) stopLive(ctx context.Context) error {
	var errs []error
	for _, pn := range slices.Backward(a.live) {
		s, ok := pn.instance.Interface().(Stopper)
		if !ok {
			continue
		}
		if err := callHook(func() error { return s.OnStop(ctx) }); err != nil {
			errs = append(errs, fmt.Errorf("gonestly: stopping %s: %w", pn, err))
		}
	}
	a.state = stateStopped
	a.live = nil
	return errors.Join(errs...)
}
