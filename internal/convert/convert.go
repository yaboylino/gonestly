// Package convert parses text into Go values for configuration and request binding.
package convert

import (
	"encoding"
	"fmt"
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
		return t.Elem().Kind() != reflect.Slice && Supported(t.Elem())
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
	if IsTextUnmarshaler(t) {
		if err := v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(raw)); err != nil { //nolint:forcetypeassert // checked by IsTextUnmarshaler
			return fmt.Errorf("%q is not a valid %s: %w", raw, t, err)
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
	default:
		return fmt.Errorf("unsupported type %s", t)
	}
	return nil
}
