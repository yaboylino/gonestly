package convert_test

import (
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/yaboylino/gonestly/internal/convert"
)

func set[T any](t *testing.T, raw string) (T, error) {
	t.Helper()
	var v T
	err := convert.Set(reflect.ValueOf(&v).Elem(), raw)
	return v, err
}

func TestSet(t *testing.T) {
	if v, err := set[int16](t, "-42"); err != nil || v != -42 {
		t.Errorf("int16 = %v, %v", v, err)
	}
	if v, err := set[uint8](t, "255"); err != nil || v != 255 {
		t.Errorf("uint8 = %v, %v", v, err)
	}
	if v, err := set[float64](t, "0.5"); err != nil || v != 0.5 {
		t.Errorf("float64 = %v, %v", v, err)
	}
	if v, err := set[bool](t, "true"); err != nil || !v {
		t.Errorf("bool = %v, %v", v, err)
	}
	if v, err := set[time.Duration](t, "1m30s"); err != nil || v != 90*time.Second {
		t.Errorf("duration = %v, %v", v, err)
	}
	if v, err := set[netip.Addr](t, "::1"); err != nil || v.String() != "::1" {
		t.Errorf("addr = %v, %v", v, err)
	}
	if v, err := set[[]string](t, " a, b ,c"); err != nil || !reflect.DeepEqual(v, []string{"a", "b", "c"}) {
		t.Errorf("slice = %v, %v", v, err)
	}
	if v, err := set[[]int](t, " "); err != nil || v == nil || len(v) != 0 {
		t.Errorf("empty slice = %#v, %v", v, err)
	}
}

func TestSetErrors(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{second(set[int8](t, "300")), `"300" is not a valid int8`},
		{second(set[uint](t, "-1")), `"-1" is not a valid uint`},
		{second(set[float32](t, "x")), `"x" is not a valid float32`},
		{second(set[bool](t, "maybe")), `"maybe" is not a valid boolean, such as true or false`},
		{second(set[time.Duration](t, "5")), `"5" is not a valid duration, such as 30s or 5m`},
		{second(set[netip.Addr](t, "nope")), `"nope" is not a valid netip.Addr: ParseAddr("nope"): unable to parse IP`},
		{second(set[[]int](t, "1,x")), `item 2: "x" is not a valid int`},
		{second(set[complex64](t, "1")), "unsupported type complex64"},
	}
	for _, tt := range tests {
		if tt.err == nil || tt.err.Error() != tt.want {
			t.Errorf("err = %v, want %q", tt.err, tt.want)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func TestSupported(t *testing.T) {
	for _, v := range []any{"", 0, uint(0), 0.0, false, time.Duration(0), netip.Addr{}, []string{}, []netip.Addr{}} {
		if !convert.Supported(reflect.TypeOf(v)) {
			t.Errorf("%T is not supported", v)
		}
	}
	for _, v := range []any{map[string]string{}, [][]string{}, struct{}{}, complex64(0), &struct{}{}} {
		if convert.Supported(reflect.TypeOf(v)) {
			t.Errorf("%T is supported", v)
		}
	}
}
