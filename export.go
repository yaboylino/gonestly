package gonestly

import (
	"errors"
	"fmt"
	"reflect"
)

// Export names a provider, or an imported module, that a module shares with the modules importing it.
type Export struct {
	fnPtr  uintptr
	typ    reflect.Type
	module *Module
	label  string
	err    error
}

// Exports turns its arguments into exports. Each argument is the constructor of one of the
// module's providers, an imported *Module whose exports are passed on, or an Export from Type.
func Exports(items ...any) []Export {
	exports := make([]Export, 0, len(items))
	for _, item := range items {
		exports = append(exports, newExport(item))
	}
	return exports
}

// Type exports the provider of type T, for providers registered with Value.
func Type[T any]() Export {
	typ := reflect.TypeFor[T]()
	return Export{typ: typ, label: typ.String()}
}

func newExport(item any) Export {
	switch v := item.(type) {
	case Export:
		return v
	case *Module:
		if v == nil {
			return Export{err: errors.New("cannot export a nil module")}
		}
		return Export{module: v, label: fmt.Sprintf("module %q", v.Name)}
	}
	fn := reflect.ValueOf(item)
	if fn.Kind() != reflect.Func || fn.IsNil() {
		return Export{err: fmt.Errorf("cannot export %T: pass a constructor, a module or gonestly.Type", item)}
	}
	return Export{fnPtr: fn.Pointer(), label: funcName(fn)}
}
