package gonestly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime/debug"
	"sync"
)

// EventBus carries events between the parts of one application, so a part can announce what
// happened without knowing who cares. Every module can inject the *EventBus.
//
// Subscribe in the constructor of a provider and publish from anywhere:
//
//	gonestly.Subscribe(bus, func(ctx context.Context, e UserCreated) error {
//		return mailer.Welcome(ctx, e.UserID)
//	})
//
//	err := bus.Publish(ctx, UserCreated{UserID: 7})
//
// Events stay inside the process and are not stored: use the jobs module for work that must
// survive a restart, and the bus for reacting in the same request.
type EventBus struct {
	logger *slog.Logger

	mu    sync.RWMutex
	subs  []*subscription
	cache map[reflect.Type][]*subscription
	next  int

	async sync.WaitGroup
}

type subscription struct {
	id     int
	typ    reflect.Type
	name   string
	async  bool
	handle func(ctx context.Context, event any) error
}

func newEventBus(logger *slog.Logger) *EventBus {
	return &EventBus{logger: logger, cache: map[reflect.Type][]*subscription{}}
}

// SubscribeOption changes how a subscription is handled.
type SubscribeOption func(*subscription)

// Async runs the handler in a goroutine of its own when an event is published, so the publisher
// does not wait for it and does not see its error: the error is logged. The handler gets the
// values of the context of the publisher, such as the request ID, but is not canceled when the
// request ends. The application waits for running handlers when it stops.
func Async() SubscribeOption {
	return func(s *subscription) { s.async = true }
}

// Subscribe registers a handler for events of type T. T is the type of the published value,
// such as UserCreated, or an interface: a handler for fmt.Stringer receives every event that
// implements it. A pointer is another type than the value it points at. It returns a function
// that removes the subscription.
//
// Handlers run in the order they were subscribed.
func Subscribe[T any](bus *EventBus, handler func(ctx context.Context, event T) error, opts ...SubscribeOption) (unsubscribe func()) {
	typ := reflect.TypeFor[T]()
	s := &subscription{
		typ:  typ,
		name: typ.String(),
		handle: func(ctx context.Context, event any) error {
			return handler(ctx, event.(T)) //nolint:forcetypeassert // matched on the type before
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.next++
	s.id = bus.next
	bus.subs = append(bus.subs, s)
	clear(bus.cache)
	return func() {
		bus.mu.Lock()
		defer bus.mu.Unlock()
		for i, existing := range bus.subs {
			if existing.id == s.id {
				bus.subs = append(bus.subs[:i:i], bus.subs[i+1:]...)
				clear(bus.cache)
				return
			}
		}
	}
}

// Publish hands the event to every subscription for its type, in order. The handlers that are
// not Async run before Publish returns, in the goroutine and with the context of the caller, so a
// handler takes part in the transaction of ctx. Every handler runs, even when one fails, and the
// errors come back joined; a panic in a handler becomes an error.
//
// Publish returns an error for a nil event. An event without subscribers is fine.
func (b *EventBus) Publish(ctx context.Context, event any) error {
	if event == nil {
		return errors.New("gonestly: cannot publish a nil event")
	}
	var errs []error
	for _, s := range b.subscribers(reflect.TypeOf(event)) {
		if !s.async {
			if err := b.run(ctx, s, event); err != nil {
				errs = append(errs, fmt.Errorf("handling %T: %w", event, err))
			}
			continue
		}
		detached := context.WithoutCancel(ctx)
		b.async.Add(1)
		go func() {
			defer b.async.Done()
			if err := b.run(detached, s, event); err != nil {
				b.logger.ErrorContext(detached, "event handler failed", slog.String("event", fmt.Sprintf("%T", event)), slog.Any("error", err))
			}
		}()
	}
	return errors.Join(errs...)
}

func (b *EventBus) run(ctx context.Context, s *subscription, event any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return s.handle(ctx, event)
}

// subscribers returns the subscriptions an event of this type goes to, in order.
func (b *EventBus) subscribers(typ reflect.Type) []*subscription {
	b.mu.RLock()
	subs, ok := b.cache[typ]
	b.mu.RUnlock()
	if ok {
		return subs
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
		if typ.AssignableTo(s.typ) {
			subs = append(subs, s)
		}
	}
	b.cache[typ] = subs
	return subs
}

// OnStop waits for the handlers that are still running, until ctx ends.
func (b *EventBus) OnStop(ctx context.Context) error {
	done := make(chan struct{})
	go func() { b.async.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.New("gonestly: event handlers were still running when the application stopped")
	}
}
