package gonestly_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
)

type userCreated struct{ ID int64 }

func (u userCreated) String() string { return fmt.Sprintf("user %d", u.ID) }

type eventCtxKey struct{}

func newBus(t *testing.T) (*gonestly.EventBus, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	var bus *gonestly.EventBus
	app, err := gonestly.New(&gonestly.Module{
		Name:      "app",
		Providers: gonestly.Providers(func(b *gonestly.EventBus) *struct{} { bus = b; return &struct{}{} }),
	}, gonestly.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))), gonestly.WithoutListener())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	return bus, &logs
}

func TestPublishRunsHandlersInOrder(t *testing.T) {
	bus, _ := newBus(t)
	var got []string
	gonestly.Subscribe(bus, func(_ context.Context, e userCreated) error { got = append(got, fmt.Sprint("a", e.ID)); return nil })
	gonestly.Subscribe(bus, func(_ context.Context, e userCreated) error { got = append(got, fmt.Sprint("b", e.ID)); return nil })
	if err := bus.Publish(t.Context(), userCreated{ID: 7}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a7,b7" {
		t.Errorf("handlers ran %v", got)
	}
	if err := bus.Publish(t.Context(), "nobody listens"); err != nil {
		t.Errorf("an event without subscribers: %v", err)
	}
}

func TestSubscribeToAnInterface(t *testing.T) {
	bus, _ := newBus(t)
	var got []string
	gonestly.Subscribe(bus, func(_ context.Context, s fmt.Stringer) error { got = append(got, s.String()); return nil })
	gonestly.Subscribe(bus, func(_ context.Context, _ *userCreated) error { got = append(got, "pointer"); return nil })
	_ = bus.Publish(t.Context(), userCreated{ID: 1})
	_ = bus.Publish(t.Context(), &userCreated{ID: 2})
	// A pointer is another type than the value, and *userCreated implements Stringer too.
	if strings.Join(got, ",") != "user 1,user 2,pointer" {
		t.Errorf("handlers ran %v", got)
	}
}

func TestEveryHandlerRunsAndErrorsAreJoined(t *testing.T) {
	bus, _ := newBus(t)
	first, second := errors.New("first failed"), errors.New("second failed")
	ran := 0
	gonestly.Subscribe(bus, func(context.Context, userCreated) error { ran++; return first })
	gonestly.Subscribe(bus, func(context.Context, userCreated) error { ran++; return nil })
	gonestly.Subscribe(bus, func(context.Context, userCreated) error { ran++; return second })
	err := bus.Publish(t.Context(), userCreated{})
	if ran != 3 || !errors.Is(err, first) || !errors.Is(err, second) || !strings.Contains(err.Error(), "handling gonestly_test.userCreated") {
		t.Errorf("ran %d handlers, err = %v", ran, err)
	}
}

func TestPanicInAHandlerIsAnError(t *testing.T) {
	bus, _ := newBus(t)
	after := false
	gonestly.Subscribe(bus, func(context.Context, userCreated) error { panic("boom") })
	gonestly.Subscribe(bus, func(context.Context, userCreated) error { after = true; return nil })
	err := bus.Publish(t.Context(), userCreated{})
	if err == nil || !strings.Contains(err.Error(), "panic: boom") || !after {
		t.Errorf("err = %v, the next handler ran: %v", err, after)
	}
}

func TestHandlerSharesTheContextOfThePublisher(t *testing.T) {
	bus, _ := newBus(t)
	var seen any
	gonestly.Subscribe(bus, func(ctx context.Context, _ userCreated) error { seen = ctx.Value(eventCtxKey{}); return ctx.Err() })
	ctx := context.WithValue(t.Context(), eventCtxKey{}, "request")
	if err := bus.Publish(ctx, userCreated{}); err != nil || seen != "request" {
		t.Errorf("seen = %v, err = %v", seen, err)
	}
}

func TestUnsubscribe(t *testing.T) {
	bus, _ := newBus(t)
	calls := 0
	unsubscribe := gonestly.Subscribe(bus, func(context.Context, userCreated) error { calls++; return nil })
	_ = bus.Publish(t.Context(), userCreated{})
	unsubscribe()
	unsubscribe()
	_ = bus.Publish(t.Context(), userCreated{})
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestPublishNilIsAnError(t *testing.T) {
	bus, _ := newBus(t)
	if err := bus.Publish(t.Context(), nil); err == nil {
		t.Error("a nil event was published")
	}
}

func TestAsyncHandlers(t *testing.T) {
	bus, logs := newBus(t)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan any)
	gonestly.Subscribe(bus, func(ctx context.Context, _ userCreated) error {
		close(started)
		<-release
		finished <- ctx.Value(eventCtxKey{})
		return errors.New("slow handler failed")
	}, gonestly.Async())

	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), eventCtxKey{}, "request"))
	if err := bus.Publish(ctx, userCreated{}); err != nil {
		t.Fatalf("an async handler made Publish fail: %v", err)
	}
	<-started
	// The request ends, but the handler goes on with the values of the request.
	cancel()
	close(release)
	select {
	case v := <-finished:
		if v != "request" {
			t.Errorf("the handler saw %v", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the async handler did not finish")
	}
	if err := bus.OnStop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "event handler failed") || !strings.Contains(logs.String(), "slow handler failed") {
		t.Errorf("the failure was not logged:\n%s", logs.String())
	}
}

func TestStopWaitsForAsyncHandlers(t *testing.T) {
	bus, _ := newBus(t)
	var mu sync.Mutex
	done := false
	gonestly.Subscribe(bus, func(context.Context, userCreated) error {
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		done = true
		mu.Unlock()
		return nil
	}, gonestly.Async())
	_ = bus.Publish(t.Context(), userCreated{})
	if err := bus.OnStop(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !done {
		t.Error("OnStop returned before the handler finished")
	}

	expired, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	gonestly.Subscribe(bus, func(context.Context, userCreated) error { <-release; return nil }, gonestly.Async())
	_ = bus.Publish(t.Context(), userCreated{})
	if err := bus.OnStop(expired); err == nil {
		t.Error("OnStop did not report handlers that were still running")
	}
	close(release)
}

func TestHandlersCanPublishAndSubscribeWhilePublishing(t *testing.T) {
	bus, _ := newBus(t)
	var got []string
	gonestly.Subscribe(bus, func(ctx context.Context, _ userCreated) error {
		got = append(got, "created")
		gonestly.Subscribe(bus, func(context.Context, string) error { got = append(got, "late"); return nil })
		return bus.Publish(ctx, "next")
	})
	if err := bus.Publish(t.Context(), userCreated{}); err != nil {
		t.Fatal(err)
	}
	_ = bus.Publish(t.Context(), "again")
	if strings.Join(got, ",") != "created,late,late" {
		t.Errorf("got %v", got)
	}
}
