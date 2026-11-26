package projects

// FindRequest identifies one project.
type FindRequest struct {
	ID int64 `path:"id" doc:"The ID of the project."`
}

// CreateRequest holds the fields of a new project.
type CreateRequest struct {
	Slug string `json:"slug" validate:"required,max=60,lowercase,alphanum" doc:"A short unique name for links." example:"website"`
	Name string `json:"name" validate:"required,max=200" doc:"The name of the project." example:"New website"`
}
