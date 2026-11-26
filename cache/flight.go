package cache

import (
	"context"
	"fmt"
	"sync"
)

// flight makes calls with the same key share one execution.
type flight struct {
	mu    sync.Mutex
	calls map[string]*call
}

type call struct {
	done  chan struct{}
	value any
	err   error
}

// do runs fn once for concurrent callers with the same key; the others wait for its result. A
// caller whose context ends stops waiting, and fn goes on for the others.
func (f *flight) do(ctx context.Context, key string, fn func() (any, error)) (value any, err error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]*call{}
	}
	c, running := f.calls[key]
	if !running {
		c = &call{done: make(chan struct{})}
		f.calls[key] = c
		go func() {
			defer func() {
				if r := recover(); r != nil {
					c.err = panicError{r}
				}
				f.mu.Lock()
				delete(f.calls, key)
				f.mu.Unlock()
				close(c.done)
			}()
			c.value, c.err = fn()
		}()
	}
	f.mu.Unlock()

	select {
	case <-c.done:
		return c.value, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type panicError struct{ value any }

func (p panicError) Error() string { return "cache: the loader panicked: " + fmt.Sprint(p.value) }
