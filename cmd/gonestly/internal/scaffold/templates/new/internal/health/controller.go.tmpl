package health

import (
	"context"
	"time"

	"github.com/yaboylino/gonestly"
)

// checkTimeout is how long the checks of a readiness request may take together.
const checkTimeout = 5 * time.Second

// Status reports that the application is running.
type Status struct {
	Status string `json:"status" example:"ok"`
}

// Controller serves the health checks.
type Controller struct {
	health gonestly.Health
}

// NewController returns the health controller.
func NewController(health gonestly.Health) *Controller {
	return &Controller{health: health}
}

// Routes registers the health checks.
func (c *Controller) Routes(r *gonestly.Router) {
	r.Prefix("/health")
	gonestly.Get(r, "/live", c.Live, gonestly.Summary("Check that the application is running"))
	gonestly.Get(r, "/ready", c.Ready, gonestly.Summary("Check that the application can handle requests"))
}

// Live answers as long as the application runs; restart it when this fails.
func (c *Controller) Live(context.Context, gonestly.Empty) (Status, error) {
	return Status{Status: "ok"}, nil
}

// Ready answers 503 when something the application needs, such as the database, cannot be
// reached, or when the application is stopping; send it no traffic until this succeeds.
func (c *Controller) Ready(ctx context.Context, _ gonestly.Empty) (gonestly.HealthReport, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	report := c.health.Report(ctx)
	return report, report.Err()
}
