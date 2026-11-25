package gonestly

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"strings"
)

var (
	contextType = reflect.TypeFor[context.Context]()
	errorType   = reflect.TypeFor[error]()
)

// Provider describes how the application constructs one dependency; New reports invalid ones.
type Provider struct {
	typ        reflect.Type
	fn         reflect.Value
	fnPtr      uintptr
	params     []reflect.Type
	returnsErr bool
	value      reflect.Value
	name       string
	alias      bool
	err        error
}

// Provide registers a constructor that returns a value, or a value and an error.
// Its parameters are injected; a context.Context parameter receives the context of App.Start.
func Provide(constructor any) Provider {
	return newConstructorProvider(constructor, nil)
}

// As registers a constructor whose result is provided as T, typically an interface it implements.
func As[T any](constructor any) Provider {
	return newConstructorProvider(constructor, reflect.TypeFor[T]())
}

// Value provides an existing value as T. Its OnStart and OnStop hooks run like any other provider's.
func Value[T any](v T) Provider {
	typ := reflect.TypeFor[T]()
	p := Provider{typ: typ, value: reflect.ValueOf(&v).Elem(), name: "value " + typ.String()}
	if isNil(p.value) {
		p.err = fmt.Errorf("value %s is nil", typ)
	}
	return p
}

// Alias provides the instance of Existing as T as well, so both types share one instance.
// Lifecycle hooks run once, for Existing.
func Alias[T, Existing any]() Provider {
	typ, existing := reflect.TypeFor[T](), reflect.TypeFor[Existing]()
	p := newConstructorProvider(func(e Existing) T { return any(e).(T) }, nil) //nolint:forcetypeassert // checked below
	p.fnPtr = 0
	p.alias = true
	p.name = fmt.Sprintf("alias of %s as %s", existing, typ)
	if !existing.AssignableTo(typ) {
		p.err = fmt.Errorf("%s cannot be used as %s", existing, typ)
	}
	return p
}

// Providers turns constructors and providers into a slice of providers.
func Providers(items ...any) []Provider {
	providers := make([]Provider, 0, len(items))
	for _, item := range items {
		if p, ok := item.(Provider); ok {
			providers = append(providers, p)
			continue
		}
		providers = append(providers, Provide(item))
	}
	return providers
}

func newConstructorProvider(constructor any, as reflect.Type) Provider {
	fn := reflect.ValueOf(constructor)
	if fn.Kind() != reflect.Func {
		return Provider{name: fmt.Sprintf("%T", constructor), err: fmt.Errorf("%T is not a constructor function", constructor)}
	}
	if fn.IsNil() {
		return Provider{name: fn.Type().String(), err: fmt.Errorf("constructor %s is nil", fn.Type())}
	}

	p := Provider{fn: fn, fnPtr: fn.Pointer(), name: funcName(fn)}
	ft := fn.Type()
	switch {
	case ft.IsVariadic():
		p.err = fmt.Errorf("constructor %s must not be variadic", p.name)
		return p
	case ft.NumOut() == 1 && ft.Out(0) != errorType:
	case ft.NumOut() == 2 && ft.Out(0) != errorType && ft.Out(1) == errorType:
		p.returnsErr = true
	default:
		p.err = fmt.Errorf("constructor %s must return a value, or a value and an error", p.name)
		return p
	}

	p.typ = ft.Out(0)
	if as != nil {
		if !p.typ.AssignableTo(as) {
			p.err = fmt.Errorf("constructor %s returns %s, which cannot be provided as %s", p.name, p.typ, as)
			return p
		}
		p.typ = as
	}
	for i := range ft.NumIn() {
		p.params = append(p.params, ft.In(i))
	}
	return p
}

// call constructs the provided value from the resolved arguments.
func (p *Provider) call(args []reflect.Value) (v reflect.Value, err error) {
	if !p.fn.IsValid() {
		return p.value, nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	out := p.fn.Call(args)
	if p.returnsErr && !out[1].IsNil() {
		return reflect.Value{}, out[1].Interface().(error) //nolint:forcetypeassert // the signature guarantees an error
	}
	v = out[0]
	if isNil(v) {
		return reflect.Value{}, fmt.Errorf("returned a nil %s", v.Type())
	}
	if v.Type() != p.typ {
		converted := reflect.New(p.typ).Elem()
		converted.Set(v)
		v = converted
	}
	return v, nil
}

// funcName returns a function's name without its import path, e.g. "users.NewService".
func funcName(fn reflect.Value) string {
	f := runtime.FuncForPC(fn.Pointer())
	if f == nil {
		return fn.Type().String()
	}
	name := f.Name()
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return strings.ReplaceAll(name, "[...]", "")
}

func isNil(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
