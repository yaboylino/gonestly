package tasks

import "time"

//go:generate gonestly generate columns

// Task is one thing to do in a project.
type Task struct {
	ID        int64     `db:"id" json:"id"`
	ProjectID int64     `db:"project_id" json:"projectId"`
	Title     string    `db:"title" json:"title"`
	Done      bool      `db:"done" json:"done"`
	CreatedAt time.Time `db:"created_at,default" json:"createdAt"`
}
