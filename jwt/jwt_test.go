package jwt_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	golangjwt "github.com/golang-jwt/jwt/v5"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
	"github.com/yaboylino/gonestly/jwt"
)

const secret = "a-secret-of-at-least-32-bytes-long!"

func tokens(t *testing.T, opts jwt.Options) *jwt.Tokens {
	t.Helper()
	tk, err := jwt.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tk.OnStop(context.Background()) })
	return tk
}

func pemBlock(t *testing.T, kind string, key any) string {
	t.Helper()
	var der []byte
	var err error
	if kind == "PRIVATE KEY" {
		der, err = x509.MarshalPKCS8PrivateKey(key)
	} else {
		der, err = x509.MarshalPKIXPublicKey(key)
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
}

func TestSignAndVerify(t *testing.T) {
	tk := tokens(t, jwt.Options{Secret: secret, Issuer: "shop", Audience: "api"})
	token, err := tk.Sign(jwt.Claims{Subject: "42", Roles: []string{"admin", "editor"}, Extra: map[string]any{"email": "ada@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := tk.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "42" || !claims.HasRole("editor") || claims.HasRole("owner") || claims.Extra["email"] != "ada@example.com" {
		t.Errorf("claims = %+v", claims)
	}
	if ttl := claims.ExpiresAt.Sub(claims.IssuedAt); ttl != time.Hour {
		t.Errorf("ttl = %s, want the default of an hour", ttl)
	}
	if _, standard := claims.Extra["iss"]; standard {
		t.Errorf("a standard claim is in Extra: %v", claims.Extra)
	}

	for name, other := range map[string]jwt.Options{
		"another secret":   {Secret: strings.Repeat("x", 32), Issuer: "shop", Audience: "api"},
		"another issuer":   {Secret: secret, Issuer: "other", Audience: "api"},
		"another audience": {Secret: secret, Issuer: "shop", Audience: "other"},
	} {
		if _, err := tokens(t, other).Verify(token); !errors.Is(err, jwt.ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
	for name, bad := range map[string]string{"garbage": "not.a.token", "empty": "", "tampered": token[:len(token)-2] + "xx"} {
		if _, err := tk.Verify(bad); !errors.Is(err, jwt.ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}

	expired, _ := tk.Sign(jwt.Claims{Subject: "42", ExpiresAt: time.Now().Add(-time.Minute)})
	if _, err := tk.Verify(expired); !errors.Is(err, jwt.ErrExpiredToken) {
		t.Errorf("expired: err = %v", err)
	}
	if _, err := tokens(t, jwt.Options{Secret: secret, Issuer: "shop", Audience: "api", Leeway: 2 * time.Minute}).Verify(expired); err != nil {
		t.Errorf("expired within the leeway: %v", err)
	}

	// A token without an expiry, or without a signature, is never accepted.
	forever, _ := golangjwt.NewWithClaims(golangjwt.SigningMethodHS256, golangjwt.MapClaims{"sub": "42", "iss": "shop", "aud": "api"}).SignedString([]byte(secret))
	unsigned, _ := golangjwt.NewWithClaims(golangjwt.SigningMethodNone, golangjwt.MapClaims{"sub": "42", "exp": time.Now().Add(time.Hour).Unix()}).SignedString(golangjwt.UnsafeAllowNoneSignatureType)
	for name, bad := range map[string]string{"without an expiry": forever, "unsigned": unsigned} {
		if _, err := tk.Verify(bad); !errors.Is(err, jwt.ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
}

func TestKeyPairs(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	edPublic, edKey, _ := ed25519.GenerateKey(rand.Reader)
	for name, pair := range map[string][2]any{
		"RSA":     {rsaKey, &rsaKey.PublicKey},
		"ECDSA":   {ecKey, &ecKey.PublicKey},
		"Ed25519": {edKey, edPublic},
	} {
		signer := tokens(t, jwt.Options{PrivateKey: pemBlock(t, "PRIVATE KEY", pair[0]), TTL: time.Minute})
		verifier := tokens(t, jwt.Options{PublicKey: pemBlock(t, "PUBLIC KEY", pair[1])})
		token, err := signer.Sign(jwt.Claims{Subject: name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, tk := range []*jwt.Tokens{signer, verifier} {
			if claims, err := tk.Verify(token); err != nil || claims.Subject != name {
				t.Errorf("%s: %+v, %v", name, claims, err)
			}
		}
		if _, err := verifier.Sign(jwt.Claims{}); err == nil || !strings.Contains(err.Error(), "these options only verify tokens") {
			t.Errorf("%s: signing with a public key = %v", name, err)
		}
		// A token signed with HMAC, with the public key as the secret, must not pass.
		forged, _ := golangjwt.NewWithClaims(golangjwt.SigningMethodHS256, golangjwt.MapClaims{"sub": "x", "exp": time.Now().Add(time.Hour).Unix()}).
			SignedString([]byte(pemBlock(t, "PUBLIC KEY", pair[1])))
		if _, err := verifier.Verify(forged); !errors.Is(err, jwt.ErrInvalidToken) {
			t.Errorf("%s: a forged token = %v", name, err)
		}
	}
}

func TestIdentityProvider(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","use":"sig","kid":"key-1","x":"` + base64.RawURLEncoding.EncodeToString(public) + `"}]}`))
	}))
	defer provider.Close()

	tk := tokens(t, jwt.Options{JWKSURL: provider.URL, Issuer: "https://id.example.com", RolesClaim: "realm_access.roles"})
	sign := func(kid string, key ed25519.PrivateKey) string {
		token := golangjwt.NewWithClaims(golangjwt.SigningMethodEdDSA, golangjwt.MapClaims{
			"sub": "user-1", "iss": "https://id.example.com", "exp": time.Now().Add(time.Hour).Unix(),
			"realm_access": map[string]any{"roles": []string{"admin"}},
		})
		token.Header["kid"] = kid
		signed, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	claims, err := tk.Verify(sign("key-1", private))
	if err != nil || claims.Subject != "user-1" || !claims.HasRole("admin") {
		t.Errorf("claims = %+v, %v", claims, err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := tk.Verify(sign("key-1", other)); !errors.Is(err, jwt.ErrInvalidToken) {
		t.Errorf("a token signed with another key = %v", err)
	}
	if _, err := tk.Sign(jwt.Claims{}); err == nil {
		t.Error("signing without a key succeeded")
	}
}

func TestWeakRSAKey(t *testing.T) {
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	for name, opts := range map[string]jwt.Options{
		"private": {PrivateKey: pemBlock(t, "PRIVATE KEY", weak)},
		"public":  {PublicKey: pemBlock(t, "PUBLIC KEY", &weak.PublicKey)},
	} {
		if _, err := jwt.New(opts); err == nil || !strings.Contains(err.Error(), "the RSA key has 1024 bits and needs at least 2048") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOptionMistakes(t *testing.T) {
	for want, opts := range map[string]jwt.Options{
		"set exactly one of Secret, PrivateKey, PublicKey and JWKSURL; 0 are set": {},
		"set exactly one of Secret, PrivateKey, PublicKey and JWKSURL; 2 are set": {Secret: secret, JWKSURL: "https://id.example.com/keys"},
		"the secret has 5 bytes and needs at least 32":                            {Secret: "short"},
		"the private key is not an RSA, ECDSA or Ed25519 key in PEM format":       {PrivateKey: "nonsense"},
		"the public key is not an RSA, ECDSA or Ed25519 key in PEM format":        {PublicKey: "nonsense"},
	} {
		if _, err := jwt.New(opts); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("New(%+v) = %v, want %q", opts, err, want)
		}
	}
}

type controller struct{ tokens *jwt.Tokens }

func (c *controller) Routes(r *gonestly.Router) {
	r.Prefix("/orders").Guard(c.tokens.Guard())
	gonestly.Get(r, "/", func(ctx context.Context, _ gonestly.Empty) (map[string]any, error) {
		claims, ok := jwt.ClaimsFrom(ctx)
		return map[string]any{"subject": claims.Subject, "found": ok}, nil
	})
	gonestly.Delete(r, "/{id}", func(context.Context, gonestly.Empty) (gonestly.Empty, error) { return gonestly.Empty{}, nil },
		gonestly.WithGuards(c.tokens.Guard("admin", "owner")))
}

type config struct{ Secret string }

func TestGuard(t *testing.T) {
	app := gonestlytest.Start(t, &gonestly.Module{
		Name: "app",
		Imports: gonestly.Imports(
			&gonestly.Module{Name: "config", Global: true, Providers: gonestly.Providers(gonestly.Value(config{Secret: secret})), Exports: gonestly.Exports(gonestly.Type[config]())},
			jwt.ModuleFunc(func(c config) jwt.Options { return jwt.Options{Secret: c.Secret} }),
		),
		Controllers: gonestly.Controllers(func(tokens *jwt.Tokens) *controller { return &controller{tokens: tokens} }),
	})
	tk := gonestlytest.Resolve[*jwt.Tokens](app)
	bearer := func(claims jwt.Claims) []string {
		token, err := tk.Sign(claims)
		if err != nil {
			t.Fatal(err)
		}
		return []string{"Authorization", "Bearer " + token}
	}

	var body map[string]any
	app.Get("/orders", bearer(jwt.Claims{Subject: "42"})...).ExpectStatus(http.StatusOK).JSON(&body)
	if body["subject"] != "42" || body["found"] != true {
		t.Errorf("body = %v", body)
	}
	app.Delete("/orders/1", bearer(jwt.Claims{Subject: "42", Roles: []string{"owner"}})...).ExpectStatus(http.StatusNoContent)
	if problem := app.Delete("/orders/1", bearer(jwt.Claims{Subject: "42", Roles: []string{"viewer"}})...).Problem(); problem.Status != http.StatusForbidden || problem.Detail != "this needs the role admin or owner" {
		t.Errorf("a missing role = %+v", problem)
	}

	for detail, headers := range map[string][]string{
		"a bearer token is required": nil,
		"the token is invalid":       {"Authorization", "Bearer nonsense"},
		"the token has expired":      bearer(jwt.Claims{ExpiresAt: time.Now().Add(-time.Hour)}),
	} {
		res := app.Get("/orders", headers...)
		if problem := res.Problem(); problem.Status != http.StatusUnauthorized || problem.Detail != detail || res.Header.Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: %+v %v", detail, problem, res.Header)
		}
	}
	if problem := app.Get("/orders", "Authorization", "Basic abc").Problem(); problem.Status != http.StatusUnauthorized {
		t.Errorf("another scheme = %+v", problem)
	}

	document := string(app.Get("/openapi.json").ExpectStatus(http.StatusOK).Body)
	if !strings.Contains(document, `"securitySchemes":{"bearer":{"type":"http","scheme":"bearer","bearerFormat":"JWT"}}`) {
		t.Error("the document does not describe the bearer token")
	}

	broken, err := gonestly.New(&gonestly.Module{Name: "app", Imports: gonestly.Imports(jwt.Module(jwt.Options{Secret: "short"}))}, gonestly.WithLogger(nil))
	if err == nil {
		err = broken.Start(t.Context())
	}
	if err == nil || !strings.Contains(err.Error(), "the secret has 5 bytes") {
		t.Errorf("a short secret = %v", err)
	}
}
