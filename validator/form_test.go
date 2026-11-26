package validator_test

import (
	"errors"
	"testing"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/validator"
)

type uploadRequest struct {
	Email string         `form:"email" validate:"required,email"`
	Photo gonestly.File  `form:"photo"`
	Scan  *gonestly.File `form:"scan" validate:"required"`
}

func TestFormFields(t *testing.T) {
	v, err := validator.New()
	if err != nil {
		t.Fatal(err)
	}
	var httpErr *gonestly.HTTPError
	if err := v.ValidateStruct(uploadRequest{Email: "nope"}); !errors.As(err, &httpErr) || len(httpErr.Errors) != 2 {
		t.Fatalf("err = %v", err)
	}
	for i, want := range []gonestly.FieldError{
		{Field: "email", In: "form", Message: httpErr.Errors[0].Message},
		{Field: "scan", In: "form", Message: "is required"},
	} {
		if httpErr.Errors[i] != want {
			t.Errorf("error %d = %+v, want %+v", i, httpErr.Errors[i], want)
		}
	}
}
