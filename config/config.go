// Package config loads typed configuration from environment variables and dotenv files.
//
// Each exported field of the configuration struct is read from a variable named after the field
// in upper snake case, so DatabaseURL reads DATABASE_URL. Tags change that behavior:
//
//	type Config struct {
//		Port        int           `default:"3000"`
//		DatabaseURL string        `env:"DATABASE_URL,required"`
//		Timeout     time.Duration `default:"5s"`
//		Origins     []string      // a comma separated list
//		Redis       RedisConfig   // nested fields read REDIS_HOST, REDIS_PORT, ...
//		Internal    string        `env:"-"` // never read
//	}
//
// A variable that is set but empty is used as is: it does not fall back to the default.
//
// Supported field types are strings, booleans, integers, floats, time.Duration, slices of those,
// nested structs and any type implementing encoding.TextUnmarshaler.
package config

import (
	"encoding"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	durationType        = reflect.TypeFor[time.Duration]()
)

// Options controls where Load reads configuration from.
type Options struct {
	// Files are dotenv files, read in order: later files override earlier ones and missing files
	// are skipped. A nil slice reads ".env"; an empty slice reads no files.
	Files []string
	// Prefix is prepended to every variable name, for example "APP_".
	Prefix string
	// LookupEnv reads a variable from the environment. It defaults to os.LookupEnv.
	LookupEnv func(key string) (string, bool)
}

// Load reads a T from the environment and the dotenv files. Environment variables take
// precedence over the files. Every invalid or missing value is reported at once.
func Load[T any](opts Options) (T, error) {
	var cfg T
	v := reflect.ValueOf(&cfg).Elem()
	if v.Kind() != reflect.Struct {
		return cfg, fmt.Errorf("config: %s is not a struct", v.Type())
	}

	files := opts.Files
	if files == nil {
		files = []string{".env"}
	}
	fileVars, err := readFiles(files)
	if err != nil {
		return cfg, err
	}

	lookupEnv := opts.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	l := loader{
		lookup: func(key string) (string, bool) {
			if value, ok := lookupEnv(key); ok {
				return value, true
			}
			value, ok := fileVars[key]
			return value, ok
		},
	}
	l.loadStruct(v, opts.Prefix)
	if len(l.problems) > 0 {
		return cfg, errors.Join(l.problems...)
	}
	return cfg, nil
}

type loader struct {
	lookup   func(key string) (string, bool)
	problems []error
}

func (l *loader) problem(format string, args ...any) {
	l.problems = append(l.problems, fmt.Errorf("config: "+format, args...))
}

func (l *loader) loadStruct(v reflect.Value, prefix string) {
	t := v.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("env")
		if tag == "-" {
			continue
		}
		name, required := parseTag(tag)

		if isNestedStruct(field.Type) {
			nested := prefix
			switch {
			case name != "":
				nested += name + "_"
			case !field.Anonymous:
				nested += snakeCase(field.Name) + "_"
			}
			l.loadStruct(v.Field(i), nested)
			continue
		}

		key := prefix + name
		if name == "" {
			key = prefix + snakeCase(field.Name)
		}
		if !supported(field.Type) {
			l.problem("%s: field %s has unsupported type %s", key, field.Name, field.Type)
			continue
		}

		raw, ok := l.lookup(key)
		if !ok {
			raw, ok = field.Tag.Lookup("default")
		}
		if !ok || (required && raw == "") {
			if required {
				l.problem("%s is required", key)
			}
			continue
		}
		if err := setValue(v.Field(i), raw); err != nil {
			l.problem("%s: %v", key, err)
		}
	}
}

func parseTag(tag string) (name string, required bool) {
	name, rest, _ := strings.Cut(tag, ",")
	for opt := range strings.SplitSeq(rest, ",") {
		if strings.TrimSpace(opt) == "required" {
			required = true
		}
	}
	return strings.TrimSpace(name), required
}

func isTextUnmarshaler(t reflect.Type) bool {
	return reflect.PointerTo(t).Implements(textUnmarshalerType)
}

func isNestedStruct(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && !isTextUnmarshaler(t)
}

func supported(t reflect.Type) bool {
	if isTextUnmarshaler(t) || t == durationType {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice:
		return t.Elem().Kind() != reflect.Slice && supported(t.Elem())
	default:
		return false
	}
}

func setValue(v reflect.Value, raw string) error {
	t := v.Type()
	if isTextUnmarshaler(t) {
		return v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(raw)) //nolint:forcetypeassert // checked by isTextUnmarshaler
	}
	if t == durationType {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("%q is not a valid duration, such as 30s or 5m", raw)
		}
		v.SetInt(int64(d))
		return nil
	}

	switch t.Kind() {
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("%q is not a valid boolean, such as true or false", raw)
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, t.Bits())
		if err != nil {
			return fmt.Errorf("%q is not a valid %s", raw, t)
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, t.Bits())
		if err != nil {
			return fmt.Errorf("%q is not a valid %s", raw, t)
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, t.Bits())
		if err != nil {
			return fmt.Errorf("%q is not a valid %s", raw, t)
		}
		v.SetFloat(f)
	case reflect.Slice:
		return setSlice(v, raw)
	default:
		return fmt.Errorf("unsupported type %s", t)
	}
	return nil
}

func setSlice(v reflect.Value, raw string) error {
	if strings.TrimSpace(raw) == "" {
		v.Set(reflect.MakeSlice(v.Type(), 0, 0))
		return nil
	}
	parts := strings.Split(raw, ",")
	slice := reflect.MakeSlice(v.Type(), len(parts), len(parts))
	for i, part := range parts {
		if err := setValue(slice.Index(i), strings.TrimSpace(part)); err != nil {
			return fmt.Errorf("item %d: %w", i+1, err)
		}
	}
	v.Set(slice)
	return nil
}

// snakeCase converts a Go field name to an environment variable name, e.g. APIKey to API_KEY.
func snakeCase(name string) string {
	runes := []rune(name)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}
