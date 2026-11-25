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
	// ErrTooManyRows reports that QueryOne found more than one row.
	ErrTooManyRows = errors.New("sql: the query returned more than one row")
	// ErrUnsafeQuery reports an update or delete without conditions, which would affect every row.
	ErrUnsafeQuery = errors.New("sql: an update or delete needs a condition; use Where(sql.Always()) to affect every row")
	// ErrInvalidModel reports a struct that cannot be mapped to a table.
	ErrInvalidModel = errors.New("sql: invalid model")
)
