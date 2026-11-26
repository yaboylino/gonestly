// Package gonestlytest starts Gonestly applications in tests and sends requests to them
// without opening a port.
package gonestlytest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yaboylino/gonestly"
)

// App is a started application under test.
type App struct {
	*gonestly.App
	t testing.TB
}

// Start builds and starts the module with a silent logger and without listening, and stops it
// when the test ends. Options such as gonestly.Override are applied after the defaults.
func Start(t testing.TB, root *gonestly.Module, opts ...gonestly.Option) *App {
	t.Helper()
	defaults := []gonestly.Option{gonestly.WithLogger(slog.New(slog.DiscardHandler)), gonestly.WithoutListener()}
	app, err := gonestly.New(root, append(defaults, opts...)...)
	if err != nil {
		t.Fatalf("gonestlytest: %v", err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("gonestlytest: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Errorf("gonestlytest: %v", err)
		}
	})
	return &App{App: app, t: t}
}

// Resolve returns the provider of type T from the root module, failing the test if it is missing.
func Resolve[T any](app *App) T {
	app.t.Helper()
	v, err := gonestly.Resolve[T](app.App)
	if err != nil {
		app.t.Fatalf("gonestlytest: %v", err)
	}
	return v
}

// Get sends a GET request.
func (a *App) Get(path string, headers ...string) *Response {
	a.t.Helper()
	return a.Request(http.MethodGet, path, nil, headers...)
}

// Post sends a POST request with body encoded as JSON.
func (a *App) Post(path string, body any, headers ...string) *Response {
	a.t.Helper()
	return a.Request(http.MethodPost, path, body, headers...)
}

// Put sends a PUT request with body encoded as JSON.
func (a *App) Put(path string, body any, headers ...string) *Response {
	a.t.Helper()
	return a.Request(http.MethodPut, path, body, headers...)
}

// Patch sends a PATCH request with body encoded as JSON.
func (a *App) Patch(path string, body any, headers ...string) *Response {
	a.t.Helper()
	return a.Request(http.MethodPatch, path, body, headers...)
}

// Delete sends a DELETE request.
func (a *App) Delete(path string, headers ...string) *Response {
	a.t.Helper()
	return a.Request(http.MethodDelete, path, nil, headers...)
}

// Request sends a request. A string, []byte or io.Reader body is sent as is, url.Values as a
// URL encoded form and a Form as a multipart form; any other non-nil body is encoded as JSON. Headers are given as name and value pairs.
func (a *App) Request(method, path string, body any, headers ...string) *Response {
	a.t.Helper()
	if len(headers)%2 != 0 {
		a.t.Fatalf("gonestlytest: headers must be name and value pairs, got %d values", len(headers))
	}
	reader, contentType := a.encode(body)
	req := httptest.NewRequestWithContext(a.t.Context(), method, path, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return a.Do(req)
}

// Do sends a prepared request.
func (a *App) Do(req *http.Request) *Response {
	a.t.Helper()
	handler := a.Handler()
	if handler == nil {
		a.t.Fatal("gonestlytest: the application has no controllers")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	result := recorder.Result()
	defer result.Body.Close()
	data, _ := io.ReadAll(result.Body)
	return &Response{Status: result.StatusCode, Header: result.Header, Body: data, t: a.t}
}

// Form is a multipart form to send as the body of a request, with its fields and files.
type Form struct {
	Values url.Values
	Files  []FormFile
}

// FormFile is one file in a Form: the form field, the file name and the content.
type FormFile struct {
	Field   string
	Name    string
	Content []byte
}

// encode returns the body and the content type that it needs, if any.
func (a *App) encode(body any) (io.Reader, string) {
	switch b := body.(type) {
	case nil:
		return nil, ""
	case string:
		return strings.NewReader(b), ""
	case []byte:
		return bytes.NewReader(b), ""
	case io.Reader:
		return b, ""
	case url.Values:
		return strings.NewReader(b.Encode()), "application/x-www-form-urlencoded"
	case Form:
		return a.multipart(b)
	}
	data, err := json.Marshal(body)
	if err != nil {
		a.t.Fatalf("gonestlytest: encoding the request body: %v", err)
	}
	return bytes.NewReader(data), "application/json"
}

func (a *App) multipart(form Form) (io.Reader, string) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for name, values := range form.Values {
		for _, value := range values {
			if err := writer.WriteField(name, value); err != nil {
				a.t.Fatalf("gonestlytest: encoding the form: %v", err)
			}
		}
	}
	for _, file := range form.Files {
		part, err := writer.CreateFormFile(file.Field, file.Name)
		if err == nil {
			_, err = part.Write(file.Content)
		}
		if err != nil {
			a.t.Fatalf("gonestlytest: encoding the form: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		a.t.Fatalf("gonestlytest: encoding the form: %v", err)
	}
	return &buffer, writer.FormDataContentType()
}

// Response is the recorded response to a request.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	t      testing.TB
}

// JSON decodes the body into v, failing the test if it is not valid JSON.
func (r *Response) JSON(v any) {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.t.Fatalf("gonestlytest: decoding the response body %q: %v", r.Body, err)
	}
}

// Problem decodes an error response, failing the test if the body is not problem details.
func (r *Response) Problem() gonestly.Problem {
	r.t.Helper()
	if ct := r.Header.Get("Content-Type"); ct != "application/problem+json" {
		r.t.Fatalf("gonestlytest: expected problem details, got %d with Content-Type %q: %s", r.Status, ct, r.Body)
	}
	var p gonestly.Problem
	r.JSON(&p)
	return p
}

// ExpectStatus fails the test if the status differs, and shows the body to explain why.
func (r *Response) ExpectStatus(status int) *Response {
	r.t.Helper()
	if r.Status != status {
		r.t.Fatalf("gonestlytest: status %d, want %d: %s", r.Status, status, r.Body)
	}
	return r
}
