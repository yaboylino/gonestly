package projects

import (
	"context"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/sql"
)

// Service holds the logic for projects.
type Service struct {
	db   *sql.DB
	repo *sql.Repository[Project, int64]
}

// NewService returns the service for projects.
func NewService(db *sql.DB, repo *sql.Repository[Project, int64]) *Service {
	return &Service{db: db, repo: repo}
}

// List returns the projects that are not archived, newest first.
func (s *Service) List(ctx context.Context) ([]Project, error) {
	items, err := s.repo.Query().Where(ProjectColumns.ArchivedAt.IsNull()).OrderBy(ProjectColumns.ID.Desc()).All(ctx)
	if items == nil {
		items = []Project{}
	}
	return items, err
}

// Find returns one project, or sql.ErrNotFound.
func (s *Service) Find(ctx context.Context, id int64) (Project, error) {
	return s.repo.FindByID(ctx, id)
}

// Create stores a new project; a slug that is taken answers 409 through sql.ErrConflict.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Project, error) {
	project := Project{Slug: req.Slug, Name: req.Name}
	err := s.repo.Create(ctx, &project)
	return project, err
}

// Archive hides a project once all its tasks are done.
func (s *Service) Archive(ctx context.Context, id int64) (Project, error) {
	var project Project
	err := s.db.Transaction(ctx, func(ctx context.Context) error {
		var err error
		if project, err = s.repo.FindByID(ctx, id); err != nil {
			return err
		}
		if project.OpenTasks > 0 {
			return gonestly.Conflict("the project still has %d open tasks", project.OpenTasks)
		}
		now := time.Now().UTC()
		project.ArchivedAt = &now
		return s.repo.Update(ctx, &project)
	})
	return project, err
}

// AdjustOpenTasks changes the number of open tasks of a project that accepts tasks. Called in
// a transaction of the tasks module, it joins that transaction through the context.
func (s *Service) AdjustOpenTasks(ctx context.Context, id int64, delta int) error {
	result, err := s.db.Exec(ctx, "UPDATE projects SET open_tasks = open_tasks + ? WHERE id = ? AND archived_at IS NULL", delta, id)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed == 1 {
		return err
	}
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return err
	}
	return gonestly.Conflict("the project is archived")
}
