package validator_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/validator"
)

type pagination struct {
	Page int `query:"page" validate:"gte=1"`
}

type audit struct {
	Reason string `json:"reason" validate:"required"`
}

type line struct {
	Quantity int `json:"quantity" validate:"gte=1"`
}

type embeddedRequest struct {
	pagination
	audit
	Lines []line `json:"lines" validate:"dive"`
}

// Fields of an embedded struct are reported the way the client sent them, not by the name of
// the Go type they happen to be declared in.
func TestEmbeddedFields(t *testing.T) {
	v, err := validator.New()
	if err != nil {
		t.Fatal(err)
	}
	err = v.ValidateStruct(embeddedRequest{Lines: []line{{Quantity: 1}, {}}})
	var httpErr *gonestly.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v", err)
	}
	want := []gonestly.FieldError{
		{Field: "page", In: "query", Message: "must be at least 1"},
		{Field: "reason", In: "body", Message: "is required"},
		{Field: "lines[1].quantity", In: "body", Message: "must be at least 1"},
	}
	if !slices.Equal(httpErr.Errors, want) {
		t.Errorf("errors:\n%+v\nwant:\n%+v", httpErr.Errors, want)
	}
}
