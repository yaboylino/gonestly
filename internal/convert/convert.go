// Package convert parses text into Go values for configuration and request binding.
package convert

import (
	"encoding"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	durationType        = reflect.TypeFor[time.Duration]()
)

// IsTextUnmarshaler reports whether a pointer to t implements encoding.TextUnmarshaler.
func IsTextUnmarshaler(t reflect.Type) bool {
	return reflect.PointerTo(t).Implements(textUnmarshalerType)
}

// Supported reports whether Set can parse text into a value of type t.
func Supported(t reflect.Type) bool {
	if t.Kind() == reflect.Slice && !IsTextUnmarshaler(t) {
		return t.Elem().Kind() != reflect.Slice && t.Elem().Kind() != reflect.Pointer && Supported(t.Elem())
	}
	if t.Kind() == reflect.Pointer {
		return t.Elem().Kind() != reflect.Pointer && supportedScalar(t.Elem())
	}
	return supportedScalar(t)
}

func supportedScalar(t reflect.Type) bool {
	if IsTextUnmarshaler(t) || t == durationType {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

// Set parses raw into v. A slice is read as a comma separated list.
func Set(v reflect.Value, raw string) error {
	if v.Kind() == reflect.Slice && !IsTextUnmarshaler(v.Type()) {
		if strings.TrimSpace(raw) == "" {
			v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			return nil
		}
		parts := strings.Split(raw, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return SetAll(v, parts)
	}
	return setScalar(v, raw)
}

// SetAll parses each value into one element of the slice v.
func SetAll(v reflect.Value, values []string) error {
	slice := reflect.MakeSlice(v.Type(), len(values), len(values))
	for i, value := range values {
		if err := setScalar(slice.Index(i), value); err != nil {
			return fmt.Errorf("item %d: %w", i+1, err)
		}
	}
	v.Set(slice)
	return nil
}

func setScalar(v reflect.Value, raw string) error {
	t := v.Type()
	if t.Kind() == reflect.Pointer {
		// A pointer stays nil when the value is absent, and points at the parsed value otherwise.
		target := reflect.New(t.Elem())
		if err := setScalar(target.Elem(), raw); err != nil {
			return err
		}
		v.Set(target)
		return nil
	}
	if IsTextUnmarshaler(t) {
		if err := v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(raw)); err != nil { //nolint:forcetypeassert // checked by IsTextUnmarshaler
			return fmt.Errorf("%q is not valid: %w", raw, err)
		}
		return nil
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
			return numberError(raw, "a whole number", err)
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, t.Bits())
		if err != nil {
			return numberError(raw, "a whole number of 0 or more", err)
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, t.Bits())
		// NaN and infinity parse, but no JSON response can carry them.
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return numberError(raw, "a number", err)
		}
		v.SetFloat(f)
	default:
		return fmt.Errorf("unsupported type %s", t)
	}
	return nil
}

// numberError names the kind of number in words, since the message can reach an API client.
func numberError(raw, kind string, err error) error {
	if errors.Is(err, strconv.ErrRange) {
		return fmt.Errorf("%q is out of range", raw)
	}
	return fmt.Errorf("%q is not %s", raw, kind)
}
