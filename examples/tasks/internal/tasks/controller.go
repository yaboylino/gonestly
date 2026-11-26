package tasks

import (
	"context"

	"github.com/yaboylino/gonestly"

	"github.com/yaboylino/gonestly/examples/tasks/internal/auth"
)

// Controller serves the routes for tasks.
type Controller struct {
	service *Service
	guard   *auth.Guard
}

// NewController returns the controller for tasks.
func NewController(service *Service, guard *auth.Guard) *Controller {
	return &Controller{service: service, guard: guard}
}

// Routes registers the routes; reading is open, changing needs the token.
func (c *Controller) Routes(r *gonestly.Router) {
	r.Prefix("/tasks")
	gonestly.Get(r, "/", c.List, gonestly.Summary("List tasks"))
	r.Group("", func(r *gonestly.Router) {
		r.Guard(c.guard)
		gonestly.Post(r, "/", c.Create, gonestly.Summary("Create a task"))
		gonestly.Post(r, "/{id}/complete", c.Complete, gonestly.Summary("Mark a task as done"), gonestly.Status(200))
		gonestly.Delete(r, "/{id}", c.Delete, gonestly.Summary("Delete a task"))
	})
}

// List returns a page of tasks.
func (c *Controller) List(ctx context.Context, req ListRequest) (ListResponse, error) {
	return c.service.List(ctx, req)
}

// Create stores a new task.
func (c *Controller) Create(ctx context.Context, req CreateRequest) (Task, error) {
	return c.service.Create(ctx, req)
}

// Complete marks a task as done.
func (c *Controller) Complete(ctx context.Context, req FindRequest) (Task, error) {
	return c.service.Complete(ctx, req.ID)
}

// Delete removes a task.
func (c *Controller) Delete(ctx context.Context, req FindRequest) (gonestly.Empty, error) {
	return gonestly.Empty{}, c.service.Delete(ctx, req.ID)
}
