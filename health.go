package gonestly

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
)

const (
	healthOK          = "ok"
	healthFailed      = "failed"
	healthUnavailable = "unavailable"
)

// HealthChecker is implemented by a provider that can tell whether what it depends on works,
// such as a database connection. Health asks every provider that implements it.
type HealthChecker interface {
	CheckHealth(ctx context.Context) error
}

// Health reports whether the application can handle requests. Every module can inject it, for
// example in a controller that a load balancer or an orchestrator calls before it sends traffic.
type Health interface {
	// Report asks every HealthChecker, at the same time, and gives up when ctx ends.
	Report(ctx context.Context) HealthReport
}

// HealthReport is the state of the application and of every provider that checks its health.
type HealthReport struct {
	// Status is "ok", or "unavailable" when a check fails or the application is stopping.
	Status string `json:"status" example:"ok"`
	// Checks has "ok" or "failed" for every provider that checks its health, such as "sql.DB".
	// Why a check failed is logged, not reported, because the cause may hold internal details.
	Checks map[string]string `json:"checks,omitempty"`
}

// OK reports whether the application can handle requests.
func (r HealthReport) OK() bool { return r.Status == healthOK }

// Err returns nil for a healthy application and a 503 Service Unavailable error otherwise, so
// a handler can return it and the caller sees which checks failed.
func (r HealthReport) Err() error {
	if r.OK() {
		return nil
	}
	var failed []string
	for name, status := range r.Checks {
		if status != healthOK {
			failed = append(failed, name)
		}
	}
	if len(failed) == 0 {
		return StatusError(http.StatusServiceUnavailable, "the application is stopping")
	}
	slices.Sort(failed)
	return StatusError(http.StatusServiceUnavailable, "these checks failed: %s", strings.Join(failed, ", "))
}

type appHealth struct{ app *App }

func (h appHealth) Report(ctx context.Context) HealthReport {
	a := h.app
	a.mu.RLock()
	started, live := a.state == stateStarted, a.live
	a.mu.RUnlock()
	if !started || a.stopping.Load() {
		return HealthReport{Status: healthUnavailable}
	}

	report := HealthReport{Status: healthOK}
	checkers := map[string]HealthChecker{}
	for _, pn := range live {
		checker, ok := pn.instance.Interface().(HealthChecker)
		if !ok || pn.provider.alias {
			continue
		}
		name := strings.TrimPrefix(pn.instance.Type().String(), "*")
		if _, taken := checkers[name]; taken {
			name = pn.String()
		}
		checkers[name] = checker
	}
	if len(checkers) > 0 {
		report.Checks = make(map[string]string, len(checkers))
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, checker := range checkers {
		wg.Go(func() {
			err := callHook(func() error { return checker.CheckHealth(ctx) })
			if err != nil {
				a.logger.WarnContext(ctx, "health check failed", slog.String("check", name), slog.Any("error", err))
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				report.Checks[name], report.Status = healthFailed, healthUnavailable
				return
			}
			report.Checks[name] = healthOK
		})
	}
	wg.Wait()
	return report
}
