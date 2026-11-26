package tasks

import (
	"context"

	"github.com/yaboylino/gonestly/sql"

	"github.com/yaboylino/gonestly/examples/tasks/internal/projects"
)

const defaultLimit = 20

// Service holds the logic for tasks.
type Service struct {
	db       *sql.DB
	repo     *sql.Repository[Task, int64]
	projects *projects.Service
}

// NewService returns the service for tasks.
func NewService(db *sql.DB, repo *sql.Repository[Task, int64], projects *projects.Service) *Service {
	return &Service{db: db, repo: repo, projects: projects}
}

// List returns a page of the tasks that match the filters, oldest first.
func (s *Service) List(ctx context.Context, req ListRequest) (ListResponse, error) {
	var conditions []sql.Condition
	if req.Project != nil {
		conditions = append(conditions, TaskColumns.ProjectID.Eq(*req.Project))
	}
	if req.Done != nil {
		conditions = append(conditions, TaskColumns.Done.Eq(*req.Done))
	}
	page, limit := max(req.Page, 1), req.Limit
	if limit < 1 {
		limit = defaultLimit
	}
	query := s.repo.Query().Where(conditions...)
	items, err := query.OrderBy(TaskColumns.ID.Asc()).Limit(limit).Offset((page - 1) * limit).All(ctx)
	if err != nil {
		return ListResponse{}, err
	}
	total, err := query.Count(ctx)
	if items == nil {
		items = []Task{}
	}
	return ListResponse{Items: items, Total: total}, err
}

// Create stores a task and counts it in its project, together or not at all.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Task, error) {
	task := Task{ProjectID: req.ProjectID, Title: req.Title}
	err := s.db.Transaction(ctx, func(ctx context.Context) error {
		if err := s.projects.AdjustOpenTasks(ctx, task.ProjectID, 1); err != nil {
			return err
		}
		return s.repo.Create(ctx, &task)
	})
	return task, err
}

// Complete marks a task as done; completing it twice changes nothing.
func (s *Service) Complete(ctx context.Context, id int64) (Task, error) {
	var task Task
	err := s.db.Transaction(ctx, func(ctx context.Context) error {
		var err error
		if task, err = s.repo.FindByID(ctx, id); err != nil || task.Done {
			return err
		}
		task.Done = true
		if err := s.repo.Update(ctx, &task); err != nil {
			return err
		}
		return s.projects.AdjustOpenTasks(ctx, task.ProjectID, -1)
	})
	return task, err
}

// Delete removes a task, and no longer counts it when it was open.
func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.db.Transaction(ctx, func(ctx context.Context) error {
		task, err := s.repo.FindByID(ctx, id)
		if err != nil {
			return err
		}
		if err := s.repo.Delete(ctx, id); err != nil || task.Done {
			return err
		}
		return s.projects.AdjustOpenTasks(ctx, task.ProjectID, -1)
	})
}
