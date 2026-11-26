package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/yaboylino/gonestly"
)

// Guard lets requests through that carry the API token as a bearer token.
type Guard struct {
	token []byte
}

// NewGuard returns the guard for the configured token.
func NewGuard(cfg Config) *Guard {
	return &Guard{token: []byte(cfg.APIToken)}
}

// Check rejects requests without the token; the comparison takes the same time for every token.
func (g *Guard) Check(ctx context.Context, r *http.Request) (context.Context, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(token), g.token) != 1 {
		return nil, gonestly.Unauthorized("a valid bearer token is required")
	}
	return ctx, nil
}

// SecurityScheme describes the token in the OpenAPI document.
func (g *Guard) SecurityScheme() (string, gonestly.SecurityScheme) {
	return "bearer", gonestly.SecurityScheme{Type: "http", Scheme: "bearer"}
}
