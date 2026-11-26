package app_test

import (
	"net/http"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/yaboylino/gonestly/gonestlytest"

	"github.com/yaboylino/gonestly/examples/tasks/internal/app"
	"github.com/yaboylino/gonestly/examples/tasks/internal/projects"
	"github.com/yaboylino/gonestly/examples/tasks/internal/tasks"
)

// token is the header that the routes that change data ask for.
var token = []string{"Authorization", "Bearer secret"}

// start runs the whole application against a database of its own.
func start(t *testing.T) *gonestlytest.App {
	t.Helper()
	t.Setenv("DATABASE_URL", "file:"+filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)")
	t.Setenv("API_TOKEN", "secret")
	return gonestlytest.Start(t, app.Module)
}

func TestProjectWithTasks(t *testing.T) {
	api := start(t)

	var project projects.Project
	api.Post("/projects", map[string]string{"slug": "website", "name": "New website"}, token...).ExpectStatus(http.StatusCreated).JSON(&project)

	var first, second tasks.Task
	api.Post("/tasks", map[string]any{"projectId": project.ID, "title": "Write the home page"}, token...).ExpectStatus(http.StatusCreated).JSON(&first)
	api.Post("/tasks", map[string]any{"projectId": project.ID, "title": "Choose a font"}, token...).ExpectStatus(http.StatusCreated).JSON(&second)
	api.Get("/projects/1").ExpectStatus(http.StatusOK).JSON(&project)
	if project.OpenTasks != 2 {
		t.Fatalf("open tasks = %d, want 2", project.OpenTasks)
	}

	// A project with open tasks cannot be archived.
	if problem := api.Post("/projects/1/archive", nil, token...).Problem(); problem.Status != http.StatusConflict {
		t.Errorf("archive with open tasks = %+v", problem)
	}

	// Completing twice counts once, and deleting an open task counts too.
	api.Post("/tasks/1/complete", nil, token...).ExpectStatus(http.StatusOK).JSON(&first)
	api.Post("/tasks/1/complete", nil, token...).ExpectStatus(http.StatusOK)
	if !first.Done {
		t.Errorf("task = %+v", first)
	}
	var open tasks.ListResponse
	api.Get("/tasks?project=1&done=false").ExpectStatus(http.StatusOK).JSON(&open)
	if open.Total != 1 || open.Items[0].Title != "Choose a font" {
		t.Errorf("open tasks = %+v", open)
	}
	api.Delete("/tasks/2", token...).ExpectStatus(http.StatusNoContent)

	api.Post("/projects/1/archive", nil, token...).ExpectStatus(http.StatusOK).JSON(&project)
	if project.ArchivedAt == nil || project.OpenTasks != 0 {
		t.Errorf("archived = %+v", project)
	}
	var active []projects.Project
	api.Get("/projects").ExpectStatus(http.StatusOK).JSON(&active)
	if len(active) != 0 {
		t.Errorf("active projects = %+v", active)
	}

	// An archived project takes no new tasks, and the refused task is not stored.
	if problem := api.Post("/tasks", map[string]any{"projectId": 1, "title": "Too late"}, token...).Problem(); problem.Status != http.StatusConflict {
		t.Errorf("task in an archived project = %+v", problem)
	}
	var all tasks.ListResponse
	api.Get("/tasks").ExpectStatus(http.StatusOK).JSON(&all)
	if all.Total != 1 {
		t.Errorf("tasks = %+v", all)
	}
}

func TestRefusals(t *testing.T) {
	api := start(t)
	body := map[string]string{"slug": "website", "name": "New website"}

	for name, headers := range map[string][]string{"no token": nil, "a wrong token": {"Authorization", "Bearer nope"}} {
		if problem := api.Post("/projects", body, headers...).Problem(); problem.Status != http.StatusUnauthorized {
			t.Errorf("%s = %+v", name, problem)
		}
	}

	api.Post("/projects", body, token...).ExpectStatus(http.StatusCreated)
	if problem := api.Post("/projects", body, token...).Problem(); problem.Status != http.StatusConflict {
		t.Errorf("a slug that is taken = %+v", problem)
	}
	problem := api.Post("/projects", map[string]string{"slug": "Not Valid", "name": ""}, token...).Problem()
	if problem.Status != http.StatusBadRequest || len(problem.Errors) != 2 {
		t.Errorf("an invalid project = %+v", problem)
	}
	if problem := api.Post("/tasks", map[string]any{"projectId": 99, "title": "Nowhere"}, token...).Problem(); problem.Status != http.StatusNotFound {
		t.Errorf("a task in a missing project = %+v", problem)
	}
}
