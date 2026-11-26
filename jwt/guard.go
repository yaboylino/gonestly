package jwt

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/yaboylino/gonestly"
)

type claimsKey struct{}

// ClaimsFrom returns the claims of the token that a Guard accepted for the request of ctx.
func ClaimsFrom(ctx context.Context) (Claims, bool) {
	claims, ok := ctx.Value(claimsKey{}).(Claims)
	return claims, ok
}

// Guard returns a guard that lets a request through when it carries a valid token in the
// Authorization header, as "Bearer <token>". With roles, the subject needs at least one of
// them. A request without a valid token gets a 401, and one without the role a 403.
func (t *Tokens) Guard(roles ...string) gonestly.SecuredGuard {
	return &guard{tokens: t, roles: roles}
}

type guard struct {
	tokens *Tokens
	roles  []string
}

func (g *guard) Check(ctx context.Context, r *http.Request) (context.Context, error) {
	// A request that an outer guard already authenticated is not verified twice.
	claims, ok := ClaimsFrom(ctx)
	if !ok {
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || token == "" {
			return nil, g.unauthorized(ctx, "a bearer token is required", nil)
		}
		var err error
		if claims, err = g.tokens.Verify(token); err != nil {
			if errors.Is(err, ErrExpiredToken) {
				return nil, g.unauthorized(ctx, "the token has expired", err)
			}
			return nil, g.unauthorized(ctx, "the token is invalid", err)
		}
		ctx = context.WithValue(ctx, claimsKey{}, claims)
	}
	if len(g.roles) > 0 && !claims.HasRole(g.roles...) {
		return nil, gonestly.Forbidden("this needs the role %s", strings.Join(g.roles, " or "))
	}
	return ctx, nil
}

// unauthorized answers 401 and tells the client how to authenticate; the cause is only logged.
func (g *guard) unauthorized(ctx context.Context, detail string, cause error) error {
	if header := gonestly.ResponseHeader(ctx); header != nil {
		header.Set("WWW-Authenticate", "Bearer")
	}
	err := gonestly.Unauthorized("%s", detail)
	if cause != nil {
		return err.WithCause(cause)
	}
	return err
}

func (g *guard) SecurityScheme() (string, gonestly.SecurityScheme) {
	return "bearer", gonestly.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"}
}
