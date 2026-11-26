package tasks

// ListRequest filters and pages the tasks; a filter that is left out does not apply.
type ListRequest struct {
	Project *int64 `query:"project" doc:"Only the tasks of this project."`
	Done    *bool  `query:"done" doc:"Only the tasks that are done, or only the ones that are not."`
	Page    int    `query:"page" validate:"omitempty,gte=1" doc:"The page, starting at 1."`
	Limit   int    `query:"limit" validate:"omitempty,gte=1,lte=100" doc:"The number of tasks per page, at most 100."`
}

// FindRequest identifies one task.
type FindRequest struct {
	ID int64 `path:"id" doc:"The ID of the task."`
}

// CreateRequest holds the fields of a new task.
type CreateRequest struct {
	ProjectID int64  `json:"projectId" validate:"required" doc:"The project the task belongs to."`
	Title     string `json:"title" validate:"required,max=200" doc:"What has to be done." example:"Write the home page"`
}

// ListResponse is one page of tasks and the total number that match.
type ListResponse struct {
	Items []Task `json:"items"`
	Total int64  `json:"total"`
}
