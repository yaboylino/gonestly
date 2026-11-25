package validator_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	playground "github.com/go-playground/validator/v10"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
	"github.com/yaboylino/gonestly/validator"
)

type address struct {
	City string `json:"city" validate:"required"`
}

type createRequest struct {
	Tenant  string   `header:"X-Tenant" validate:"required"`
	Page    int      `query:"page" validate:"gte=1"`
	Name    string   `json:"name" validate:"required,max=5"`
	Email   string   `json:"email" validate:"omitempty,email"`
	Role    string   `json:"role" validate:"oneof=admin member"`
	Tags    []string `json:"tags" validate:"max=2"`
	Address address  `json:"address"`
	Slug    string   `json:"slug" validate:"omitempty,slug"`
	Secret  string   `json:"-" validate:"omitempty,len=3"`
}

type controller struct{}

func (controller) Routes(r *gonestly.Router) {
	gonestly.Post(r, "/items", func(context.Context, createRequest) (string, error) { return "ok", nil })
	gonestly.Post(r, "/list", func(_ context.Context, items []string) (int, error) { return len(items), nil })
}

func slug(fl playground.FieldLevel) bool {
	for _, r := range fl.Field().String() {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return true
}

func start(t *testing.T) *gonestlytest.App {
	t.Helper()
	return gonestlytest.Start(t, &gonestly.Module{
		Name: "app",
		Imports: gonestly.Imports(validator.Module(func(v *playground.Validate) error {
			return v.RegisterValidation("slug", slug)
		})),
		Controllers: gonestly.Controllers(func() controller { return controller{} }),
	})
}

func TestValidRequest(t *testing.T) {
	body := map[string]any{"name": "Ada", "role": "admin", "address": map[string]string{"city": "London"}, "slug": "ada-l"}
	start(t).Request(http.MethodPost, "/items?page=1", jsonBody(body), "Content-Type", "application/json", "X-Tenant", "acme").ExpectStatus(http.StatusCreated)
}

func TestInvalidRequest(t *testing.T) {
	body := map[string]any{"name": "Grace Hopper", "email": "nope", "role": "owner", "tags": []string{"a", "b", "c"}, "slug": "Not A Slug"}
	p := start(t).Request(http.MethodPost, "/items?page=0", jsonBody(body), "Content-Type", "application/json").Problem()

	want := []gonestly.FieldError{
		{Field: "X-Tenant", In: "header", Message: "is required"},
		{Field: "page", In: "query", Message: "must be at least 1"},
		{Field: "name", In: "body", Message: "must be at most 5 characters long"},
		{Field: "email", In: "body", Message: "must be a valid email address"},
		{Field: "role", In: "body", Message: "must be one of: admin, member"},
		{Field: "tags", In: "body", Message: "must contain at most 2 items"},
		{Field: "address.city", In: "body", Message: "is required"},
		{Field: "slug", In: "body", Message: "must satisfy slug"},
	}
	if p.Status != http.StatusBadRequest || !slices.Equal(p.Errors, want) {
		t.Errorf("status %d, errors:\n%+v\nwant:\n%+v", p.Status, p.Errors, want)
	}
}

func TestNonStructRequestsAreSkipped(t *testing.T) {
	start(t).Post("/list", []string{"a"}).ExpectStatus(http.StatusCreated)
}

func TestConfigureError(t *testing.T) {
	_, err := validator.New(func(*playground.Validate) error { return errors.New("bad rule") })
	if err == nil || err.Error() != "validator: bad rule" {
		t.Errorf("err = %v", err)
	}
}

func TestMessages(t *testing.T) {
	v, err := validator.New()
	if err != nil {
		t.Fatal(err)
	}
	type sample struct {
		A string            `validate:"len=2"`
		B int               `validate:"gt=5"`
		C int               `validate:"lt=1"`
		D string            `validate:"url"`
		E string            `validate:"uuid"`
		F string            `validate:"alphanum"`
		G string            `validate:"numeric"`
		H string            `validate:"eqfield=A"`
		I string            `validate:"nefield=A"`
		J map[string]string `validate:"min=1"`
		K string            `validate:"contains=x"`
		L string            `validate:"lowercase"`
	}
	err = v.ValidateStruct(sample{A: "abc", B: 1, C: 2, D: "x", E: "x", F: "-", G: "x", H: "z", I: "abc", K: "y", L: "Up"})
	var httpErr *gonestly.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v", err)
	}
	var got []string
	for _, f := range httpErr.Errors {
		got = append(got, f.Field+" "+f.Message)
	}
	want := []string{
		"A must be exactly 2 characters long",
		"B must be more than 5",
		"C must be less than 1",
		"D must be a valid URL",
		"E must be a valid UUID",
		"F must contain only letters and digits",
		"G must be a number",
		"H must be equal to A",
		"I must differ from A",
		"J must contain at least 1 item",
		"K must satisfy contains=x",
		"L must satisfy lowercase",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
