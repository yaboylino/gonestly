package projects

import (
	"context"

	"github.com/yaboylino/gonestly"

	"github.com/yaboylino/gonestly/examples/tasks/internal/auth"
)

// Controller serves the routes for projects.
type Controller struct {
	service *Service
	guard   *auth.Guard
}

// NewController returns the controller for projects.
func NewController(service *Service, guard *auth.Guard) *Controller {
	return &Controller{service: service, guard: guard}
}

// Routes registers the routes; reading is open, changing needs the token.
func (c *Controller) Routes(r *gonestly.Router) {
	r.Prefix("/projects")
	gonestly.Get(r, "/", c.List, gonestly.Summary("List the active projects"))
	gonestly.Get(r, "/{id}", c.Find, gonestly.Summary("Find a project"))
	r.Group("", func(r *gonestly.Router) {
		r.Guard(c.guard)
		gonestly.Post(r, "/", c.Create, gonestly.Summary("Create a project"))
		gonestly.Post(r, "/{id}/archive", c.Archive, gonestly.Summary("Archive a project without open tasks"), gonestly.Status(200))
	})
}

// List returns the active projects.
func (c *Controller) List(ctx context.Context, _ gonestly.Empty) ([]Project, error) {
	return c.service.List(ctx)
}

// Find returns one project.
func (c *Controller) Find(ctx context.Context, req FindRequest) (Project, error) {
	return c.service.Find(ctx, req.ID)
}

// Create stores a new project.
func (c *Controller) Create(ctx context.Context, req CreateRequest) (Project, error) {
	return c.service.Create(ctx, req)
}

// Archive hides a project.
func (c *Controller) Archive(ctx context.Context, req FindRequest) (Project, error) {
	return c.service.Archive(ctx, req.ID)
}
