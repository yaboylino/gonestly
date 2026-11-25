package gonestly

import (
	"context"
	"net/http"
	"reflect"
)

var emptyType = reflect.TypeFor[Empty]()

// Empty is the request type of a handler without input, and the response type of a handler
// without output. An Empty response is sent as 204 No Content.
type Empty struct{}

// Handler handles one route: it receives the bound and validated request and returns the
// response, which is sent as JSON, or an error.
type Handler[Req, Res any] func(ctx context.Context, req Req) (Res, error)

// Guard decides whether a request may reach its handler. It returns an error, typically an
// *HTTPError such as Unauthorized, to reject the request, and may return a context that
// carries extra values, such as the authenticated user.
type Guard interface {
	Check(ctx context.Context, r *http.Request) (context.Context, error)
}

// GuardFunc turns a function into a Guard.
type GuardFunc func(ctx context.Context, r *http.Request) (context.Context, error)

// Check calls f.
func (f GuardFunc) Check(ctx context.Context, r *http.Request) (context.Context, error) {
	return f(ctx, r)
}

// Next continues the request: it calls the next interceptor, or the handler.
type Next func(ctx context.Context) (any, error)

// Interceptor wraps the handler of a route. It can run code before and after it, change its
// result or error, or answer without calling it at all.
type Interceptor interface {
	Intercept(ctx context.Context, r *http.Request, next Next) (any, error)
}

// InterceptorFunc turns a function into an Interceptor.
type InterceptorFunc func(ctx context.Context, r *http.Request, next Next) (any, error)

// Intercept calls f.
func (f InterceptorFunc) Intercept(ctx context.Context, r *http.Request, next Next) (any, error) {
	return f(ctx, r, next)
}

// Validatable is implemented by request types that check their own fields after binding.
// Return Invalid with the invalid fields, or any other error for a 400 Bad Request.
type Validatable interface {
	Validate() error
}

// StructValidator validates every bound request, for example based on struct tags. Provide one
// in the root module, or import a module that exports one, to enable it for every route.
type StructValidator interface {
	ValidateStruct(v any) error
}

type requestKey struct{}

type requestState struct {
	request *http.Request
	header  http.Header
}

// Request returns the HTTP request that ctx belongs to, or nil outside a request.
func Request(ctx context.Context) *http.Request {
	if s, ok := ctx.Value(requestKey{}).(*requestState); ok {
		return s.request
	}
	return nil
}

// ResponseHeader returns the headers of the response to the request that ctx belongs to.
// Set headers before the handler returns. Outside a request it returns an unused header map.
func ResponseHeader(ctx context.Context) http.Header {
	if s, ok := ctx.Value(requestKey{}).(*requestState); ok {
		return s.header
	}
	return http.Header{}
}
