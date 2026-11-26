package projects

import "time"

//go:generate gonestly generate columns

// Project groups tasks. OpenTasks is kept up to date by the tasks module.
type Project struct {
	ID         int64      `db:"id" json:"id"`
	Slug       string     `db:"slug" json:"slug"`
	Name       string     `db:"name" json:"name"`
	OpenTasks  int64      `db:"open_tasks" json:"openTasks"`
	ArchivedAt *time.Time `db:"archived_at" json:"archivedAt"`
	CreatedAt  time.Time  `db:"created_at,default" json:"createdAt"`
}
