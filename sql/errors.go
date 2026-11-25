package sql

import (
	"errors"
	"net/http"

	"github.com/yaboylino/gonestly"
)

var (
	// ErrNotFound reports that a query found no row. It is an HTTP error, so a handler that
	// returns it answers with 404 Not Found; check for it with errors.Is.
	ErrNotFound error = &gonestly.HTTPError{Status: http.StatusNotFound, Detail: "The requested record does not exist."}
	// ErrConflict reports that a row with the same unique or primary key value already exists.
	// It is an HTTP error, so a handler that returns it answers with 409 Conflict; the error of
	// the driver stays available to errors.As.
	ErrConflict error = &gonestly.HTTPError{Status: http.StatusConflict, Detail: "A record with these values already exists."}
	// ErrTooManyRows reports that QueryOne found more than one row.
	ErrTooManyRows = errors.New("sql: the query returned more than one row")
	// ErrUnsafeQuery reports an update or delete without conditions, which would affect every row.
	ErrUnsafeQuery = errors.New("sql: an update or delete needs a condition; use Where(sql.Always()) to affect every row")
	// ErrInvalidModel reports a struct that cannot be mapped to a table.
	ErrInvalidModel = errors.New("sql: invalid model")
)

// conflictError is ErrConflict together with the driver error that caused it.
type conflictError struct{ cause error }

func (e *conflictError) Error() string   { return "sql: unique violation: " + e.cause.Error() }
func (e *conflictError) Unwrap() []error { return []error{ErrConflict, e.cause} }

// wrap turns a unique violation into an error that is ErrConflict.
func (db *DB) wrap(err error) error {
	if err == nil || !db.dialect.isUniqueViolation(err) {
		return err
	}
	return &conflictError{cause: err}
}
