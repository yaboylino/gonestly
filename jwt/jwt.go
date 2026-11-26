package jwt

import (
	"cmp"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	golangjwt "github.com/golang-jwt/jwt/v5"
)

const (
	defaultTTL        = time.Hour
	defaultRolesClaim = "roles"
	minSecretBytes    = 32
	minRSABits        = 2048
)

// ErrInvalidToken reports a token that is malformed, has a wrong signature or fails a check
// such as the issuer; ErrExpiredToken reports one that is no longer valid.
var (
	ErrInvalidToken = errors.New("jwt: the token is invalid")
	ErrExpiredToken = errors.New("jwt: the token has expired")
)

// Options configures how tokens are signed and verified. Set exactly one of Secret,
// PrivateKey, PublicKey and JWKSURL.
type Options struct {
	// Secret signs and verifies tokens with HMAC (HS256). It needs at least 32 bytes.
	Secret string
	// PrivateKey is an RSA, ECDSA or Ed25519 key in PEM format. It signs tokens, and its public
	// half verifies them.
	PrivateKey string
	// PublicKey is a key in PEM format that only verifies tokens, signed elsewhere.
	PublicKey string
	// JWKSURL is the address of the key set of an identity provider, whose tokens are verified
	// with it. The keys are fetched in the background and refreshed when they change.
	JWKSURL string
	// Issuer and Audience are written into signed tokens, and required in verified ones.
	Issuer   string
	Audience string
	// TTL is how long a signed token is valid. It defaults to one hour.
	TTL time.Duration
	// Leeway is how much the clocks of the signer and the verifier may differ.
	Leeway time.Duration
	// RolesClaim is the claim that holds the roles: a list of names or one name. A dot reaches
	// into an object, as in "realm_access.roles". It defaults to "roles".
	RolesClaim string
	// Logger receives the failures to refresh the keys of JWKSURL; Module uses the logger of
	// the application.
	Logger *slog.Logger
}

// Claims is what a token says about its subject.
type Claims struct {
	// Subject identifies who the token is about, usually a user ID.
	Subject string
	// Roles are the roles of the subject, which Guard can require.
	Roles []string
	// IssuedAt and ExpiresAt are filled in by Sign when they are zero.
	IssuedAt  time.Time
	ExpiresAt time.Time
	// Extra holds every other claim, such as "email".
	Extra map[string]any
}

// HasRole reports whether the subject has at least one of the roles.
func (c Claims) HasRole(roles ...string) bool {
	return slices.ContainsFunc(roles, func(role string) bool { return slices.Contains(c.Roles, role) })
}

// Tokens signs and verifies tokens, and makes guards that require them.
type Tokens struct {
	opts       Options
	method     golangjwt.SigningMethod
	signKey    any
	keyfunc    golangjwt.Keyfunc
	parser     *golangjwt.Parser
	stop       context.CancelFunc
	rolesClaim []string
}

// New returns Tokens for the options, or an error that says what is wrong with them.
func New(opts Options) (*Tokens, error) {
	t := &Tokens{opts: opts, stop: func() {}, rolesClaim: strings.Split(cmp.Or(opts.RolesClaim, defaultRolesClaim), ".")}
	if t.opts.TTL == 0 {
		t.opts.TTL = defaultTTL
	}
	set := 0
	for _, value := range []string{opts.Secret, opts.PrivateKey, opts.PublicKey, opts.JWKSURL} {
		if value != "" {
			set++
		}
	}
	var methods []string
	switch {
	case set != 1:
		return nil, fmt.Errorf("jwt: set exactly one of Secret, PrivateKey, PublicKey and JWKSURL; %d are set", set)
	case opts.Secret != "":
		if len(opts.Secret) < minSecretBytes {
			return nil, fmt.Errorf("jwt: the secret has %d bytes and needs at least %d; generate one with: openssl rand -base64 48", len(opts.Secret), minSecretBytes)
		}
		secret := []byte(opts.Secret)
		t.method, t.signKey = golangjwt.SigningMethodHS256, secret
		t.keyfunc = func(*golangjwt.Token) (any, error) { return secret, nil }
		methods = []string{"HS256"}
	case opts.PrivateKey != "":
		private, err := parsePrivateKey(opts.PrivateKey)
		if err != nil {
			return nil, err
		}
		method, err := methodFor(private.Public())
		if err != nil {
			return nil, err
		}
		t.method, t.signKey = method, private
		t.keyfunc = func(*golangjwt.Token) (any, error) { return private.Public(), nil }
		methods = []string{method.Alg()}
	case opts.PublicKey != "":
		public, err := parsePublicKey(opts.PublicKey)
		if err != nil {
			return nil, err
		}
		method, err := methodFor(public)
		if err != nil {
			return nil, err
		}
		t.keyfunc = func(*golangjwt.Token) (any, error) { return public, nil }
		methods = []string{method.Alg()}
	default:
		// The keys are refreshed until the application stops; a provider that is down at
		// startup is retried in the background, and tokens are refused until it answers.
		ctx, cancel := context.WithCancel(context.Background())
		logger := opts.Logger
		if logger == nil {
			logger = slog.Default()
		}
		keys, err := keyfunc.NewDefaultOverrideCtx(ctx, []string{opts.JWKSURL}, keyfunc.Override{
			RefreshErrorHandlerFunc: func(url string) func(context.Context, error) {
				return func(ctx context.Context, err error) {
					logger.ErrorContext(ctx, "jwt: refreshing the keys failed", slog.String("url", url), slog.Any("error", err))
				}
			},
		})
		if err != nil {
			cancel()
			return nil, fmt.Errorf("jwt: reading the keys from %s: %w", opts.JWKSURL, err)
		}
		t.keyfunc, t.stop = keys.Keyfunc, cancel
		methods = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"}
	}

	parserOptions := []golangjwt.ParserOption{
		golangjwt.WithValidMethods(methods),
		golangjwt.WithExpirationRequired(),
		golangjwt.WithLeeway(opts.Leeway),
	}
	if opts.Issuer != "" {
		parserOptions = append(parserOptions, golangjwt.WithIssuer(opts.Issuer))
	}
	if opts.Audience != "" {
		parserOptions = append(parserOptions, golangjwt.WithAudience(opts.Audience))
	}
	t.parser = golangjwt.NewParser(parserOptions...)
	return t, nil
}

// OnStop ends the refreshing of the keys of an identity provider.
func (t *Tokens) OnStop(context.Context) error {
	t.stop()
	return nil
}

// Sign returns a signed token for the claims. It fails when the options only verify tokens.
func (t *Tokens) Sign(claims Claims) (string, error) {
	if t.signKey == nil {
		return "", errors.New("jwt: signing needs a Secret or a PrivateKey; these options only verify tokens")
	}
	now := time.Now()
	if claims.IssuedAt.IsZero() {
		claims.IssuedAt = now
	}
	if claims.ExpiresAt.IsZero() {
		claims.ExpiresAt = now.Add(t.opts.TTL)
	}
	payload := golangjwt.MapClaims{}
	for name, value := range claims.Extra {
		payload[name] = value
	}
	payload["sub"] = claims.Subject
	payload["iat"] = claims.IssuedAt.Unix()
	payload["exp"] = claims.ExpiresAt.Unix()
	if t.opts.Issuer != "" {
		payload["iss"] = t.opts.Issuer
	}
	if t.opts.Audience != "" {
		payload["aud"] = t.opts.Audience
	}
	if len(claims.Roles) > 0 {
		setPath(payload, t.rolesClaim, claims.Roles)
	}
	token, err := golangjwt.NewWithClaims(t.method, payload).SignedString(t.signKey)
	if err != nil {
		return "", fmt.Errorf("jwt: signing the token: %w", err)
	}
	return token, nil
}

// Verify checks the signature, the expiry and the configured issuer and audience of a token,
// and returns its claims. The error is ErrExpiredToken or ErrInvalidToken.
func (t *Tokens) Verify(token string) (Claims, error) {
	payload := golangjwt.MapClaims{}
	if _, err := t.parser.ParseWithClaims(token, payload, t.keyfunc); err != nil {
		if errors.Is(err, golangjwt.ErrTokenExpired) {
			return Claims{}, fmt.Errorf("%w: %w", ErrExpiredToken, err)
		}
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	claims := Claims{Roles: roles(payload, t.rolesClaim), Extra: map[string]any{}}
	claims.Subject, _ = payload.GetSubject()
	if issued, _ := payload.GetIssuedAt(); issued != nil {
		claims.IssuedAt = issued.Time
	}
	if expires, _ := payload.GetExpirationTime(); expires != nil {
		claims.ExpiresAt = expires.Time
	}
	for name, value := range payload {
		switch name {
		case "sub", "iat", "exp", "iss", "aud", "nbf":
		default:
			claims.Extra[name] = value
		}
	}
	return claims, nil
}

// setPath stores value under a path of nested objects.
func setPath(payload map[string]any, path []string, value any) {
	for _, name := range path[:len(path)-1] {
		next, ok := payload[name].(map[string]any)
		if !ok {
			next = map[string]any{}
			payload[name] = next
		}
		payload = next
	}
	payload[path[len(path)-1]] = value
}

// roles reads the roles claim: a list of names, or one name.
func roles(payload map[string]any, path []string) []string {
	var value any = payload
	for _, name := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[name]
	}
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		names := make([]string, 0, len(v))
		for _, item := range v {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		return names
	default:
		return nil
	}
}

func parsePrivateKey(pem string) (crypto.Signer, error) {
	data := []byte(pem)
	if key, err := golangjwt.ParseRSAPrivateKeyFromPEM(data); err == nil {
		return key, nil
	}
	if key, err := golangjwt.ParseECPrivateKeyFromPEM(data); err == nil {
		return key, nil
	}
	if key, err := golangjwt.ParseEdPrivateKeyFromPEM(data); err == nil {
		if signer, ok := key.(ed25519.PrivateKey); ok {
			return signer, nil
		}
	}
	return nil, errors.New("jwt: the private key is not an RSA, ECDSA or Ed25519 key in PEM format")
}

func parsePublicKey(pem string) (crypto.PublicKey, error) {
	data := []byte(pem)
	if key, err := golangjwt.ParseRSAPublicKeyFromPEM(data); err == nil {
		return key, nil
	}
	if key, err := golangjwt.ParseECPublicKeyFromPEM(data); err == nil {
		return key, nil
	}
	if key, err := golangjwt.ParseEdPublicKeyFromPEM(data); err == nil {
		return key, nil
	}
	return nil, errors.New("jwt: the public key is not an RSA, ECDSA or Ed25519 key in PEM format")
}

// methodFor picks the signing method that belongs to a key.
func methodFor(public crypto.PublicKey) (golangjwt.SigningMethod, error) {
	switch key := public.(type) {
	case *rsa.PublicKey:
		if key.N.BitLen() < minRSABits {
			return nil, fmt.Errorf("jwt: the RSA key has %d bits and needs at least %d", key.N.BitLen(), minRSABits)
		}
		return golangjwt.SigningMethodRS256, nil
	case ed25519.PublicKey:
		return golangjwt.SigningMethodEdDSA, nil
	case *ecdsa.PublicKey:
		switch key.Curve.Params().BitSize {
		case 256:
			return golangjwt.SigningMethodES256, nil
		case 384:
			return golangjwt.SigningMethodES384, nil
		case 521:
			return golangjwt.SigningMethodES512, nil
		}
	}
	return nil, errors.New("jwt: the key is not an RSA key, an Ed25519 key or an ECDSA key on the curve P-256, P-384 or P-521")
}
