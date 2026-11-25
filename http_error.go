package gonestly

import (
	"fmt"
	"net/http"
)

// HTTPError is an error with an HTTP status. Handlers, guards and interceptors return it to
// control the response, which is rendered as problem details (RFC 9457).
type HTTPError struct {
	// Status is the HTTP status code, such as 404.
	Status int
	// Title is a short summary; it defaults to the status text, such as "Not Found".
	Title string
	// Detail explains this occurrence to the client. It is also shown for 5xx errors.
	Detail string
	// Errors lists invalid fields, for validation errors.
	Errors []FieldError
	// Err is the underlying cause. It is logged, at debug level for a 4xx status, and never
	// sent to the client.
	Err error
}

// FieldError describes one invalid field of a request.
type FieldError struct {
	// Field is the name the client used: the JSON name, query parameter, path value or header.
	Field string `json:"field"`
	// In is where the field was read from: "body", "query", "path" or "header".
	In string `json:"in,omitempty"`
	// Message says what is wrong with the value.
	Message string `json:"message"`
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("%d %s", e.Status, e.title())
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause.
func (e *HTTPError) Unwrap() error {
	return e.Err
}

// WithCause returns a copy of the error with err as its cause, for logging.
func (e *HTTPError) WithCause(err error) *HTTPError {
	c := *e
	c.Err = err
	return &c
}

func (e *HTTPError) title() string {
	if e.Title != "" {
		return e.Title
	}
	if text := http.StatusText(e.Status); text != "" {
		return text
	}
	return "Error"
}

// StatusError returns an error with the given status and a formatted detail message.
func StatusError(status int, format string, args ...any) *HTTPError {
	return &HTTPError{Status: status, Detail: fmt.Sprintf(format, args...)}
}

// BadRequest returns a 400 Bad Request error.
func BadRequest(format string, args ...any) *HTTPError {
	return StatusError(http.StatusBadRequest, format, args...)
}

// Unauthorized returns a 401 Unauthorized error.
func Unauthorized(format string, args ...any) *HTTPError {
	return StatusError(http.StatusUnauthorized, format, args...)
}

// Forbidden returns a 403 Forbidden error.
func Forbidden(format string, args ...any) *HTTPError {
	return StatusError(http.StatusForbidden, format, args...)
}

// NotFound returns a 404 Not Found error.
func NotFound(format string, args ...any) *HTTPError {
	return StatusError(http.StatusNotFound, format, args...)
}

// Conflict returns a 409 Conflict error.
func Conflict(format string, args ...any) *HTTPError {
	return StatusError(http.StatusConflict, format, args...)
}

// UnprocessableEntity returns a 422 Unprocessable Entity error.
func UnprocessableEntity(format string, args ...any) *HTTPError {
	return StatusError(http.StatusUnprocessableEntity, format, args...)
}

// TooManyRequests returns a 429 Too Many Requests error.
func TooManyRequests(format string, args ...any) *HTTPError {
	return StatusError(http.StatusTooManyRequests, format, args...)
}

// Invalid returns a 400 validation error that lists the invalid fields.
func Invalid(errs ...FieldError) *HTTPError {
	return &HTTPError{
		Status: http.StatusBadRequest,
		Title:  "Validation failed",
		Detail: "One or more fields are invalid.",
		Errors: errs,
	}
}
