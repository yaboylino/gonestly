package gonestly

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
)

const unexpectedErrorDetail = "An unexpected error occurred."

// Problem is the JSON body of an error response, as described by RFC 9457.
type Problem struct {
	Type     string       `json:"type"`
	Title    string       `json:"title"`
	Status   int          `json:"status"`
	Detail   string       `json:"detail,omitempty"`
	Instance string       `json:"instance,omitempty"`
	Errors   []FieldError `json:"errors,omitempty"`
}

// writeError renders err as problem details; errors that are not an *HTTPError become a 500
// whose cause is logged but never sent to the client.
func writeError(w http.ResponseWriter, r *http.Request, err error, logger *slog.Logger) {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		httpErr = &HTTPError{Status: http.StatusInternalServerError, Err: err}
	}
	status := httpErr.Status
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
	}
	// A client error is only worth a line when it carries a cause the client never sees.
	level := slog.LevelError
	if status < 500 {
		level = slog.LevelDebug
	}
	if status >= 500 || httpErr.Err != nil {
		logger.Log(r.Context(), level, "request failed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Any("error", err),
		)
	}

	detail := httpErr.Detail
	if detail == "" && status >= 500 {
		detail = unexpectedErrorDetail
	}
	title := httpErr.title()
	if httpErr.Status != status {
		title = http.StatusText(status)
	}
	writeProblem(w, Problem{
		Type:     "about:blank",
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: r.URL.Path,
		Errors:   httpErr.Errors,
	})
}

func writeProblem(w http.ResponseWriter, p Problem) {
	body, _ := json.Marshal(p) //nolint:errchkjson // Problem only holds strings and numbers
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(p.Status)
	_, _ = w.Write(body)
}

// writeJSON encodes v before writing anything, so an encoding error can still become a 500.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err := w.Write(buf.Bytes())
	return err
}
