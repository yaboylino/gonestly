// Package astedit makes small, surgical changes to Go files, such as registering a provider in
// a module, without reformatting or losing comments elsewhere in the file.
package astedit

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

const gonestlyPath = "github.com/yaboylino/gonestly"

// helpers maps the fields of gonestly.Module to the helper that builds their value.
var helpers = map[string]string{
	"Imports":     "Imports",
	"Providers":   "Providers",
	"Controllers": "Controllers",
	"Exports":     "Exports",
}

// AddToModule adds expr, such as "users.Module" or "NewService", to a field of the
// gonestly.Module literal in the file, and imports importPath when it is not empty. It reports
// whether the file changed; an expression that is already there is left alone.
func AddToModule(path, field, expr, importPath string) (bool, error) {
	if _, ok := helpers[field]; !ok {
		return false, fmt.Errorf("unknown module field %q", field)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	out, changed, err := addToModule(path, src, field, expr, importPath)
	if err != nil || !changed {
		return false, err
	}
	return true, os.WriteFile(path, out, 0o644) //nolint:gosec // source files are meant to be readable
}

func addToModule(path string, src []byte, field, expr, importPath string) ([]byte, bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, false, err
	}
	pkgName := importName(file, gonestlyPath, "gonestly")
	lit, err := findModule(file, pkgName)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	offset := func(p token.Pos) int { return fset.Position(p).Offset }

	var edited []byte
	if kv := findField(lit, field); kv != nil {
		var open, close token.Pos
		var args []ast.Expr
		switch v := kv.Value.(type) {
		case *ast.CallExpr:
			open, close, args = v.Lparen, v.Rparen, v.Args
		case *ast.CompositeLit:
			open, close, args = v.Lbrace, v.Rbrace, v.Elts
		default:
			return nil, false, fmt.Errorf("%s: cannot add to %s, which is not a call or a list", path, field)
		}
		for _, arg := range args {
			if string(src[offset(arg.Pos()):offset(arg.End())]) == expr {
				return src, false, nil
			}
		}
		edited = insertArg(src, offset(open), offset(close), args, offset, expr)
	} else {
		value := fmt.Sprintf("%s.%s(%s)", pkgName, helpers[field], expr)
		at := offset(lit.Rbrace)
		insert := field + ": " + value + ",\n"
		if fset.Position(lit.Lbrace).Line == fset.Position(lit.Rbrace).Line {
			if len(lit.Elts) > 0 {
				insert = ", " + field + ": " + value
			} else {
				insert = field + ": " + value
			}
		}
		edited = slices.Concat(src[:at], []byte(insert), src[at:])
	}

	if importPath != "" {
		edited, err = addImport(path, edited, importPath)
		if err != nil {
			return nil, false, err
		}
	}
	formatted, err := format.Source(edited)
	if err != nil {
		return nil, false, fmt.Errorf("%s: the edited file does not compile: %w", path, err)
	}
	return formatted, true, nil
}

// insertArg adds expr as the last element between open and close, matching the layout.
func insertArg(src []byte, open, close int, args []ast.Expr, offset func(token.Pos) int, expr string) []byte {
	inner := src[open+1 : close]
	multiline := bytes.Contains(inner, []byte("\n"))
	var insert string
	switch {
	case len(args) == 0:
		insert = expr
	case multiline && bytes.HasSuffix(bytes.TrimRight(src[offset(args[len(args)-1].End()):close], " \t\n"), []byte(",")):
		insert = expr + ",\n"
	default:
		at := offset(args[len(args)-1].End())
		return slices.Concat(src[:at], []byte(", "+expr), src[at:])
	}
	return slices.Concat(src[:close], []byte(insert), src[close:])
}

// findModule returns the gonestly.Module literal, preferring the variable named Module.
func findModule(file *ast.File, pkgName string) (*ast.CompositeLit, error) {
	var found []*ast.CompositeLit
	var named *ast.CompositeLit
	ast.Inspect(file, func(n ast.Node) bool {
		if spec, ok := n.(*ast.ValueSpec); ok {
			for i, value := range spec.Values {
				if lit := moduleLiteral(value, pkgName); lit != nil && i < len(spec.Names) && spec.Names[i].Name == "Module" {
					named = lit
				}
			}
		}
		if lit := moduleLiteral(n, pkgName); lit != nil {
			found = append(found, lit)
		}
		return true
	})
	switch {
	case named != nil:
		return named, nil
	case len(found) == 1:
		return found[0], nil
	case len(found) == 0:
		return nil, errors.New("no gonestly.Module literal found")
	default:
		return nil, errors.New("more than one gonestly.Module literal; name the one to change Module")
	}
}

func moduleLiteral(n ast.Node, pkgName string) *ast.CompositeLit {
	if unary, ok := n.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		n = unary.X
	}
	lit, ok := n.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Module" {
		return nil
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != pkgName {
		return nil
	}
	return lit
}

func findField(lit *ast.CompositeLit, field string) *ast.KeyValueExpr {
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == field {
				return kv
			}
		}
	}
	return nil
}

func importName(file *ast.File, path, fallback string) string {
	for _, spec := range file.Imports {
		if p, _ := strconv.Unquote(spec.Path.Value); p == path {
			if spec.Name != nil {
				return spec.Name.Name
			}
			return fallback
		}
	}
	return fallback
}

func addImport(path string, src []byte, importPath string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%s: the edited file does not compile: %w", path, err)
	}
	if !astutil.AddImport(fset, file, importPath) {
		return src, nil
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Name returns the last element of an import path, the default package name.
func Name(importPath string) string {
	return importPath[strings.LastIndexByte(importPath, '/')+1:]
}

// AddImport adds an import to the file, blank when name is "_". It reports whether the file
// changed.
func AddImport(path, importPath, name string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if !astutil.AddNamedImport(fset, file, name, importPath) {
		return false, nil
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, buf.Bytes(), 0o644) //nolint:gosec // source files are meant to be readable
}

// AddStructField adds a field declaration, such as `DatabaseURL string `env:"DATABASE_URL"“, at
// the end of a struct type. It reports whether the file changed; a field with the same name is
// left alone.
func AddStructField(path, structName, declaration string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	var st *ast.StructType
	ast.Inspect(file, func(n ast.Node) bool {
		if spec, ok := n.(*ast.TypeSpec); ok && spec.Name.Name == structName {
			st, _ = spec.Type.(*ast.StructType)
		}
		return st == nil
	})
	if st == nil {
		return false, fmt.Errorf("%s has no struct type %s", path, structName)
	}
	name, _, _ := strings.Cut(strings.TrimSpace(declaration), " ")
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == name {
				return false, nil
			}
		}
	}
	at := fset.Position(st.Fields.Closing).Offset
	edited := slices.Concat(src[:at], []byte("\t"+declaration+"\n"), src[at:])
	formatted, err := format.Source(edited)
	if err != nil {
		return false, fmt.Errorf("%s: the edited file does not compile: %w", path, err)
	}
	return true, os.WriteFile(path, formatted, 0o644) //nolint:gosec // source files are meant to be readable
}
