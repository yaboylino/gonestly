package gonestly_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

type healthDatabase struct{ down atomic.Bool }

func (d *healthDatabase) CheckHealth(context.Context) error {
	if d.down.Load() {
		return errors.New("connection refused by 10.0.0.5")
	}
	return nil
}

type cache struct{}

func (*cache) CheckHealth(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

type panicking struct{}

func (*panicking) CheckHealth(context.Context) error { panic("boom") }

type healthController struct{ health gonestly.Health }

func (c *healthController) Routes(r *gonestly.Router) {
	gonestly.Get(r, "/health/ready", func(ctx context.Context, _ gonestly.Empty) (gonestly.HealthReport, error) {
		ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		report := c.health.Report(ctx)
		return report, report.Err()
	})
}

func healthModule(providers ...any) *gonestly.Module {
	return &gonestly.Module{
		Name:        "app",
		Providers:   gonestly.Providers(providers...),
		Controllers: gonestly.Controllers(func(health gonestly.Health) *healthController { return &healthController{health: health} }),
	}
}

func TestHealth(t *testing.T) {
	app := gonestlytest.Start(t, healthModule(func() *healthDatabase { return &healthDatabase{} }))
	var report gonestly.HealthReport
	app.Get("/health/ready").ExpectStatus(http.StatusOK).JSON(&report)
	if !report.OK() || report.Checks["gonestly_test.healthDatabase"] != "ok" || len(report.Checks) != 1 {
		t.Errorf("report = %+v", report)
	}

	// A failing check makes the application unavailable, without telling the caller why.
	gonestlytest.Resolve[*healthDatabase](app).down.Store(true)
	problem := app.Get("/health/ready").Problem()
	if problem.Status != http.StatusServiceUnavailable || problem.Detail != "these checks failed: gonestly_test.healthDatabase" {
		t.Errorf("problem = %+v", problem)
	}

	// An application without checkers is healthy, until it stops.
	plain := gonestlytest.Start(t, healthModule())
	report = gonestly.HealthReport{}
	plain.Get("/health/ready").ExpectStatus(http.StatusOK).JSON(&report)
	if !report.OK() || report.Checks != nil {
		t.Errorf("report without checkers = %+v", report)
	}
	health := gonestlytest.Resolve[gonestly.Health](plain)
	if err := plain.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	stopped := health.Report(t.Context())
	if stopped.OK() || stopped.Err() == nil || stopped.Err().Error() != "503 Service Unavailable: the application is stopping" {
		t.Errorf("after Stop = %+v, %v", stopped, stopped.Err())
	}
}

func TestHealthChecksThatHangOrPanic(t *testing.T) {
	app := gonestlytest.Start(t, healthModule(func() *healthDatabase { return &healthDatabase{} }, func() *cache { return &cache{} }, func() *panicking { return &panicking{} }))
	start := time.Now()
	problem := app.Get("/health/ready").Problem()
	if problem.Status != http.StatusServiceUnavailable || problem.Detail != "these checks failed: gonestly_test.cache, gonestly_test.panicking" {
		t.Errorf("problem = %+v", problem)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the report took %s; a check that hangs must end with the context", took)
	}
}
