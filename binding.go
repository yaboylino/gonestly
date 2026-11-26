package gonestly

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"

	"github.com/yaboylino/gonestly/internal/convert"
)

var bindingSources = []string{"path", "query", "header", "form"}

// multipartMemory is how much of a multipart form is kept in memory; the rest goes to disk.
const multipartMemory = 8 << 20

type boundField struct {
	index []int
	name  string
	in    string
	field string
	slice bool
}

// binder fills a request value from the URL, the headers and the body: JSON or a form.
type binder struct {
	typ    reflect.Type
	fields []boundField
	body   bool
	form   bool
	files  bool
}

// newBinder plans how to bind requests of type t and reports declaration problems.
func newBinder(t reflect.Type) (*binder, []string) {
	b := &binder{typ: t}
	switch {
	case t == emptyType:
		return b, nil
	case t.Kind() == reflect.Interface || t.Kind() == reflect.Pointer || t.Kind() == reflect.Func || t.Kind() == reflect.Chan:
		return b, []string{fmt.Sprintf("the request type %s must be a struct, a slice or a map", t)}
	case t.Kind() != reflect.Struct:
		b.body = true
		return b, nil
	}

	var problems []string
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || (f.Anonymous && f.Type.Kind() == reflect.Struct) {
			continue
		}
		var sources []string
		for _, source := range bindingSources {
			if name, ok := f.Tag.Lookup(source); ok {
				sources = append(sources, source)
				if embeddedThroughPointer(t, f.Index) {
					problems = append(problems, fmt.Sprintf("%s.%s is embedded through a pointer; embed the struct itself", t.Name(), f.Name))
					continue
				}
				b.fields = append(b.fields, boundField{
					index: f.Index,
					name:  name,
					in:    source,
					field: f.Name,
					slice: f.Type.Kind() == reflect.Slice && !convert.IsTextUnmarshaler(f.Type),
				})
				if name == "" {
					problems = append(problems, fmt.Sprintf("%s.%s has an empty %s tag", t.Name(), f.Name, source))
				}
				if source == "form" {
					b.form = true
					if isFileType(f.Type) {
						b.files = true
						continue
					}
				}
				if !convert.Supported(f.Type) {
					problems = append(problems, fmt.Sprintf("%s.%s has type %s, which cannot be read from the %s", t.Name(), f.Name, f.Type, source))
				}
			}
		}
		switch {
		case len(sources) > 1:
			problems = append(problems, fmt.Sprintf("%s.%s has more than one of the tags %s", t.Name(), f.Name, strings.Join(sources, ", ")))
		case len(sources) == 0 && f.Tag.Get("json") != "-":
			b.body = true
		}
	}
	if b.form && b.body {
		problems = append(problems, t.Name()+" reads a form, so its body cannot be JSON too; give every other field a path, query, header or form tag, or json:\"-\"")
		b.body = false
	}
	return b, problems
}

func isFileType(t reflect.Type) bool {
	return t == fileType || t == filePtrType || t == fileSliceType
}

// embeddedThroughPointer reports whether a promoted field lies behind an embedded pointer,
// which is nil in a new request value.
func embeddedThroughPointer(t reflect.Type, index []int) bool {
	for _, i := range index[:len(index)-1] {
		f := t.Field(i)
		if f.Type.Kind() == reflect.Pointer {
			return true
		}
		t = f.Type
	}
	return false
}

// pathProblems reports path fields that the route's pattern does not provide.
func (b *binder) pathProblems(path string) []string {
	wildcards := map[string]bool{}
	for _, segment := range strings.Split(path, "/") {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			wildcards[strings.TrimSuffix(strings.Trim(segment, "{}"), "...")] = true
		}
	}
	var problems []string
	for _, f := range b.fields {
		if f.in == "path" && f.name != "" && !wildcards[f.name] {
			problems = append(problems, fmt.Sprintf("%s.%s reads the path value %q, but the path %s has no {%s} wildcard", b.typ.Name(), f.field, f.name, path, f.name))
		}
	}
	return problems
}

// bind returns an addressable request value filled from r.
func (b *binder) bind(w http.ResponseWriter, r *http.Request, maxBodyBytes int64) (reflect.Value, error) {
	v := reflect.New(b.typ).Elem()
	if b.body && hasBody(r) {
		if err := decodeBody(w, r, v, maxBodyBytes); err != nil {
			return v, err
		}
	}

	if b.form && hasBody(r) {
		if err := parseForm(w, r, b.files, maxBodyBytes); err != nil {
			return v, err
		}
	}

	var invalid []FieldError
	for _, f := range b.fields {
		field := v.FieldByIndex(f.index)
		if f.in == "form" && isFileType(field.Type()) {
			if !setFiles(field, r, f.name) {
				invalid = append(invalid, FieldError{Field: f.name, In: f.in, Message: "is required"})
			}
			continue
		}
		// Values from the body must never stand in for path, query or header values.
		field.SetZero()
		values := sourceValues(r, f)
		if len(values) == 0 {
			continue
		}
		var err error
		if f.slice {
			err = convert.SetAll(field, values)
		} else {
			err = convert.Set(field, values[0])
		}
		if err != nil {
			invalid = append(invalid, FieldError{Field: f.name, In: f.in, Message: err.Error()})
		}
	}
	if len(invalid) > 0 {
		return v, Invalid(invalid...)
	}
	return v, nil
}

func sourceValues(r *http.Request, f boundField) []string {
	switch f.in {
	case "path":
		if value := r.PathValue(f.name); value != "" {
			return []string{value}
		}
		return nil
	case "query":
		return r.URL.Query()[f.name]
	case "form":
		// PostForm holds only the body, so a query parameter never stands in for a form value.
		return r.PostForm[f.name]
	default:
		return r.Header.Values(f.name)
	}
}

// setFiles fills a File, *File or []File field and reports whether a required file is there.
func setFiles(field reflect.Value, r *http.Request, name string) bool {
	var headers []*multipart.FileHeader
	if r.MultipartForm != nil {
		headers = r.MultipartForm.File[name]
	}
	switch field.Type() {
	case fileSliceType:
		files := make([]File, len(headers))
		for i, header := range headers {
			files[i] = newFile(header)
		}
		if len(files) > 0 {
			field.Set(reflect.ValueOf(files))
		}
	case filePtrType:
		if len(headers) > 0 {
			file := newFile(headers[0])
			field.Set(reflect.ValueOf(&file))
		}
	default:
		if len(headers) == 0 {
			return false
		}
		field.Set(reflect.ValueOf(newFile(headers[0])))
	}
	return true
}

// parseForm reads a URL encoded or multipart form; files need the latter.
func parseForm(w http.ResponseWriter, r *http.Request, files bool, maxBodyBytes int64) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	multipartForm := mediaType == "multipart/form-data"
	switch {
	case err == nil && multipartForm, err == nil && !files && mediaType == "application/x-www-form-urlencoded":
	case files:
		return StatusError(http.StatusUnsupportedMediaType, "send the request body as multipart/form-data")
	default:
		return StatusError(http.StatusUnsupportedMediaType, "send the request body as application/x-www-form-urlencoded or multipart/form-data")
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if multipartForm {
		err = r.ParseMultipartForm(min(maxBodyBytes, multipartMemory)) //nolint:gosec // the body is limited above
	} else {
		err = r.ParseForm()
	}
	var maxBytesErr *http.MaxBytesError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &maxBytesErr):
		return StatusError(http.StatusRequestEntityTooLarge, "the request body is larger than %d bytes", maxBytesErr.Limit)
	default:
		return BadRequest("the request body is not a valid form").WithCause(err)
	}
}

func hasBody(r *http.Request) bool {
	return r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
}

func decodeBody(w http.ResponseWriter, r *http.Request, v reflect.Value, maxBodyBytes int64) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return StatusError(http.StatusUnsupportedMediaType, "send the request body as application/json")
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	err = decoder.Decode(v.Addr().Interface())
	if err == nil && decoder.More() {
		return BadRequest("the request body must contain a single JSON value")
	}

	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return nil
	case errors.As(err, &maxBytesErr):
		return StatusError(http.StatusRequestEntityTooLarge, "the request body is larger than %d bytes", maxBytesErr.Limit)
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return BadRequest("the request body is not valid JSON").WithCause(err)
	case errors.As(err, &typeErr):
		field := typeErr.Field
		if field == "" {
			return BadRequest("the request body must be a JSON %s", jsonKind(v.Type()))
		}
		return Invalid(FieldError{Field: field, In: "body", Message: "must be a " + jsonKind(typeErr.Type)})
	default:
		return BadRequest("the request body could not be read").WithCause(err)
	}
}

// jsonKind names the JSON type that a Go type is decoded from.
func jsonKind(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.String:
		return "string"
	case reflect.Slice, reflect.Array:
		return "array"
	default:
		return "object"
	}
}
