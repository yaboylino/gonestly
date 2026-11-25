// Package validator validates requests with struct tags, using go-playground/validator.
//
// Import Module in the root module, and every route checks the validate tags of its request:
//
//	type CreateUserRequest struct {
//		Name  string `json:"name" validate:"required,max=100"`
//		Email string `json:"email" validate:"required,email"`
//		Page  int    `query:"page" validate:"gte=1"`
//	}
//
// Invalid requests get a 400 response that lists every invalid field by the name the client
// used, such as "email" in the body or "page" in the query.
package validator

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	playground "github.com/go-playground/validator/v10"

	"github.com/yaboylino/gonestly"
)

var sources = []string{"path", "query", "header"}

// Validator checks struct tags and turns failures into a gonestly validation error.
type Validator struct {
	validate *playground.Validate
}

// New returns a Validator. The configure functions can register custom validations, for example
// with RegisterValidation; an error from one of them is returned.
func New(configure ...func(v *playground.Validate) error) (*Validator, error) {
	v := playground.New(playground.WithRequiredStructEnabled())
	v.RegisterTagNameFunc(fieldName)
	for _, fn := range configure {
		if err := fn(v); err != nil {
			return nil, fmt.Errorf("validator: %w", err)
		}
	}
	return &Validator{validate: v}, nil
}

// Module returns a global module that provides a Validator to every route. Import it once, in
// the root module.
func Module(configure ...func(v *playground.Validate) error) *gonestly.Module {
	return &gonestly.Module{
		Name:   "validator",
		Global: true,
		Providers: gonestly.Providers(gonestly.As[gonestly.StructValidator](func() (*Validator, error) {
			return New(configure...)
		})),
		Exports: gonestly.Exports(gonestly.Type[gonestly.StructValidator]()),
	}
}

// ValidateStruct checks the validate tags of v. Requests that are not structs are not checked.
func (v *Validator) ValidateStruct(value any) error {
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Struct {
		return nil
	}
	err := v.validate.Struct(value)
	var failures playground.ValidationErrors
	if !errors.As(err, &failures) {
		return err
	}

	fields := make([]gonestly.FieldError, 0, len(failures))
	for _, failure := range failures {
		field, in := locate(rv.Type(), failure.StructNamespace(), failure.Namespace())
		fields = append(fields, gonestly.FieldError{Field: field, In: in, Message: message(failure)})
	}
	return gonestly.Invalid(fields...)
}

// fieldName names a field the way the client sends it: its path, query, header or JSON name.
func fieldName(f reflect.StructField) string {
	for _, tag := range append(sources, "json") {
		name, _, _ := strings.Cut(f.Tag.Get(tag), ",")
		if name != "" && name != "-" {
			return name
		}
	}
	return f.Name
}

// fieldPath drops the name of the request type from a namespace such as "Request.address.city".
func fieldPath(namespace string) string {
	_, path, found := strings.Cut(namespace, ".")
	if !found {
		return namespace
	}
	return path
}

// locate returns the name the client knows a failed field by, and where it is read from. An
// embedded struct adds no level of its own, as in the JSON encoding and in binding.
func locate(t reflect.Type, structNamespace, namespace string) (field, in string) {
	goNames := strings.Split(fieldPath(structNamespace), ".")
	names := strings.Split(fieldPath(namespace), ".")
	if len(goNames) != len(names) {
		return fieldPath(namespace), "body"
	}
	for i, goName := range goNames {
		goName, _, _ = strings.Cut(goName, "[")
		f, ok := t.FieldByName(goName)
		if !ok {
			break
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if _, named := f.Tag.Lookup("json"); f.Anonymous && ft.Kind() == reflect.Struct && !named && i < len(goNames)-1 {
			t = ft
			continue
		}
		in = "body"
		for _, s := range sources {
			if _, ok := f.Tag.Lookup(s); ok {
				in = s
			}
		}
		return strings.Join(names[i:], "."), in
	}
	return fieldPath(namespace), "body"
}

func message(f playground.FieldError) string {
	param := f.Param()
	kind := f.Kind()
	switch f.Tag() {
	case "required", "required_if", "required_unless", "required_with", "required_without":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "url", "http_url":
		return "must be a valid URL"
	case "uuid", "uuid4", "uuid7":
		return "must be a valid UUID"
	case "oneof":
		return "must be one of: " + strings.Join(strings.Fields(param), ", ")
	case "min", "gte":
		return bound("at least", param, kind)
	case "max", "lte":
		return bound("at most", param, kind)
	case "gt":
		return bound("more than", param, kind)
	case "lt":
		return bound("less than", param, kind)
	case "len":
		return bound("exactly", param, kind)
	case "alphanum":
		return "must contain only letters and digits"
	case "numeric":
		return "must be a number"
	case "eqfield":
		return "must be equal to " + param
	case "nefield":
		return "must differ from " + param
	default:
		if param != "" {
			return fmt.Sprintf("must satisfy %s=%s", f.Tag(), param)
		}
		return "must satisfy " + f.Tag()
	}
}

func bound(comparison, param string, kind reflect.Kind) string {
	switch kind {
	case reflect.String:
		return fmt.Sprintf("must be %s %s characters long", comparison, param)
	case reflect.Slice, reflect.Array, reflect.Map:
		noun := "items"
		if param == "1" {
			noun = "item"
		}
		return fmt.Sprintf("must contain %s %s %s", comparison, param, noun)
	default:
		return fmt.Sprintf("must be %s %s", comparison, param)
	}
}
