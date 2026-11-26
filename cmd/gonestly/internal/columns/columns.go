// Package columns generates typed columns for the models of a package, for the query builder
// of the gonestly sql module.
package columns

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// FileName is the file that Generate writes in the package directory.
const FileName = "columns_gen.go"

const sqlPath = "github.com/yaboylino/gonestly/sql"

type model struct {
	name      string
	columns   []column
	relations []relation
}

// relation is a field tagged rel, and the type of the rows it holds.
type relation struct {
	field  string
	target string
}

type column struct {
	field  string
	name   string
	goType string
}

type structInfo struct {
	spec    *ast.StructType
	file    *ast.File
	imports map[string]string
}

// Generate writes FileName with a <Model>Columns variable for every struct in dir that has a
// db tag. It returns the names of the models, and removes the file when there are none.
func Generate(dir string) ([]string, error) {
	pkgName, files, err := parsePackage(dir)
	if err != nil {
		return nil, err
	}

	structs := map[string]structInfo{}
	var order []string
	for _, file := range files {
		imports := fileImports(file)
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts := spec.(*ast.TypeSpec) //nolint:forcetypeassert // type declarations hold type specs
				st, ok := ts.Type.(*ast.StructType)
				if !ok || ts.TypeParams != nil {
					continue
				}
				structs[ts.Name.Name] = structInfo{spec: st, file: file, imports: imports}
				order = append(order, ts.Name.Name)
			}
		}
	}

	// A struct embedded in another one, such as a shared Base, gets its columns through it.
	embedded := map[string]bool{}
	for _, info := range structs {
		for _, f := range info.spec.Fields.List {
			if ident, ok := f.Type.(*ast.Ident); ok && len(f.Names) == 0 {
				embedded[ident.Name] = true
			}
		}
	}

	var models []model
	used := map[string]string{}
	for _, name := range order {
		info := structs[name]
		if !hasDBTag(info.spec) || !ast.IsExported(name) || embedded[name] {
			continue
		}
		m := model{name: name}
		if err := collect(&m, info, structs, used); err != nil {
			return nil, err
		}
		models = append(models, m)
	}

	target := filepath.Join(dir, FileName)
	if len(models) == 0 {
		_ = os.Remove(target)
		return nil, nil
	}
	src, err := render(pkgName, models, used)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(target, src, 0o644); err != nil { //nolint:gosec // source files are meant to be readable
		return nil, err
	}
	names := make([]string, len(models))
	for i, m := range models {
		names[i] = m.name
	}
	return names, nil
}

// parsePackage parses the non-test Go files of dir, in name order, except the generated file.
func parsePackage(dir string) (string, []*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	fset := token.NewFileSet()
	var pkgName string
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == FileName {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return "", nil, err
		}
		if pkgName != "" && file.Name.Name != pkgName {
			return "", nil, fmt.Errorf("%s has more than one package: %s and %s", dir, pkgName, file.Name.Name)
		}
		pkgName = file.Name.Name
		files = append(files, file)
	}
	if len(files) == 0 {
		return "", nil, fmt.Errorf("%s has no Go files", dir)
	}
	return pkgName, files, nil
}

func collect(m *model, info structInfo, structs map[string]structInfo, used map[string]string) error {
	for _, f := range info.spec.Fields.List {
		if _, ok := lookupTag(f, "rel"); ok {
			target, err := relationTarget(f, info.imports, used)
			if err != nil {
				return fmt.Errorf("%s: %w", m.name, err)
			}
			for _, n := range f.Names {
				if n.IsExported() {
					m.relations = append(m.relations, relation{field: n.Name, target: target})
				}
			}
			continue
		}
		tag := fieldTag(f, "db")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if len(f.Names) == 0 {
			if tag == "" {
				if ident, ok := f.Type.(*ast.Ident); ok {
					if embedded, ok := structs[ident.Name]; ok {
						if err := collect(m, embedded, structs, used); err != nil {
							return err
						}
						continue
					}
				}
				continue
			}
		}
		goType, err := typeString(f.Type, info.imports, used)
		if err != nil {
			return fmt.Errorf("%s: %w", m.name, err)
		}
		names := f.Names
		if len(names) == 0 {
			names = []*ast.Ident{ast.NewIdent(embeddedName(f.Type))}
		}
		for _, n := range names {
			if !n.IsExported() {
				continue
			}
			columnName := name
			if columnName == "" {
				columnName = snakeCase(n.Name)
			}
			m.columns = append(m.columns, column{field: n.Name, name: columnName, goType: goType})
		}
	}
	return nil
}

// relationTarget returns the type of the rows of a relation field: the element of a slice, or
// what a pointer points at.
func relationTarget(f *ast.Field, imports, used map[string]string) (string, error) {
	expr := f.Type
	switch t := expr.(type) {
	case *ast.ArrayType:
		if t.Len != nil {
			return "", errors.New("a relation is a slice of structs, not an array")
		}
		if _, ok := t.Elt.(*ast.StarExpr); ok {
			return "", errors.New("a relation is a slice of structs, not of pointers")
		}
		expr = t.Elt
	case *ast.StarExpr:
		if _, ok := t.X.(*ast.StarExpr); ok {
			return "", errors.New("a relation is a pointer to a struct, not to a pointer")
		}
		expr = t.X
	default:
		return "", errors.New("a relation is a slice of structs or a pointer to a struct")
	}
	return typeString(expr, imports, used)
}

func render(pkgName string, models []model, used map[string]string) ([]byte, error) {
	sqlName := "sql"
	if path, ok := used["sql"]; ok && path != sqlPath {
		sqlName = "gonestlysql"
	}
	var b bytes.Buffer
	b.WriteString("// Code generated by gonestly generate columns; DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\nimport (\n", pkgName)
	// The standard library comes first, in its own group, as goimports writes it.
	var std, others []string
	for _, name := range sortedKeys(used) {
		path := used[name]
		spec := fmt.Sprintf("%q", path)
		if name != pathName(path) {
			spec = name + " " + spec
		}
		if strings.Contains(strings.Split(path, "/")[0], ".") {
			others = append(others, spec)
		} else {
			std = append(std, spec)
		}
	}
	if sqlName == "sql" {
		others = append(others, fmt.Sprintf("%q", sqlPath))
	} else {
		others = append(others, fmt.Sprintf("%s %q", sqlName, sqlPath))
	}
	slices.Sort(others)
	for _, spec := range std {
		fmt.Fprintf(&b, "\t%s\n", spec)
	}
	if len(std) > 0 {
		b.WriteString("\n")
	}
	for _, spec := range others {
		fmt.Fprintf(&b, "\t%s\n", spec)
	}
	b.WriteString(")\n")
	for _, m := range models {
		fmt.Fprintf(&b, "\nvar %sColumns = struct {\n", m.name)
		for _, c := range m.columns {
			fmt.Fprintf(&b, "\t%s %s.Column[%s]\n", c.field, sqlName, c.goType)
		}
		b.WriteString("}{\n")
		for _, c := range m.columns {
			fmt.Fprintf(&b, "\t%s: %s.Col[%s](%q),\n", c.field, sqlName, c.goType, c.name)
		}
		b.WriteString("}\n")
		if len(m.relations) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\nvar %sRelations = struct {\n", m.name)
		for _, r := range m.relations {
			fmt.Fprintf(&b, "\t%s %s.Rel[%s, %s]\n", r.field, sqlName, m.name, r.target)
		}
		b.WriteString("}{\n")
		for _, r := range m.relations {
			fmt.Fprintf(&b, "\t%s: %s.Relation[%s, %s](%q),\n", r.field, sqlName, m.name, r.target, r.field)
		}
		b.WriteString("}\n")
	}
	return format.Source(b.Bytes())
}

// typeString writes a field type as source and records the packages it refers to.
func typeString(expr ast.Expr, imports, used map[string]string) (string, error) {
	var b strings.Builder
	var walk func(ast.Expr) error
	walk = func(e ast.Expr) error {
		switch t := e.(type) {
		case *ast.Ident:
			b.WriteString(t.Name)
		case *ast.StarExpr:
			b.WriteString("*")
			return walk(t.X)
		case *ast.SelectorExpr:
			pkg, ok := t.X.(*ast.Ident)
			if !ok {
				return fmt.Errorf("unsupported type %T", t.X)
			}
			path, ok := imports[pkg.Name]
			if !ok {
				return fmt.Errorf("the package %s is not imported", pkg.Name)
			}
			used[pkg.Name] = path
			b.WriteString(pkg.Name + "." + t.Sel.Name)
		case *ast.ArrayType:
			b.WriteString("[")
			if t.Len != nil {
				lit, ok := t.Len.(*ast.BasicLit)
				if !ok {
					return errors.New("unsupported array length")
				}
				b.WriteString(lit.Value)
			}
			b.WriteString("]")
			return walk(t.Elt)
		case *ast.MapType:
			b.WriteString("map[")
			if err := walk(t.Key); err != nil {
				return err
			}
			b.WriteString("]")
			return walk(t.Value)
		case *ast.IndexExpr:
			if err := walk(t.X); err != nil {
				return err
			}
			b.WriteString("[")
			if err := walk(t.Index); err != nil {
				return err
			}
			b.WriteString("]")
		default:
			return fmt.Errorf("unsupported field type %T", e)
		}
		return nil
	}
	if err := walk(expr); err != nil {
		return "", err
	}
	return b.String(), nil
}

func hasDBTag(st *ast.StructType) bool {
	for _, f := range st.Fields.List {
		if _, ok := lookupTag(f, "db"); ok {
			return true
		}
	}
	return false
}

func fieldTag(f *ast.Field, key string) string {
	v, _ := lookupTag(f, key)
	return v
}

func lookupTag(f *ast.Field, key string) (string, bool) {
	if f.Tag == nil {
		return "", false
	}
	raw, err := strconv.Unquote(f.Tag.Value)
	if err != nil {
		return "", false
	}
	return reflectTag(raw).lookup(key)
}

type reflectTag string

// lookup follows the struct tag convention of reflect.StructTag.Lookup.
func (tag reflectTag) lookup(key string) (string, bool) {
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			break
		}
		name := string(tag[:i])
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		quoted := string(tag[:i+1])
		tag = tag[i+1:]
		if key == name {
			value, err := strconv.Unquote(quoted)
			return value, err == nil
		}
	}
	return "", false
}

func fileImports(file *ast.File) map[string]string {
	imports := map[string]string{}
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		name := pathName(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = path
	}
	return imports
}

func pathName(path string) string {
	name := path[strings.LastIndexByte(path, '/')+1:]
	if strings.HasPrefix(name, "v") && strings.Trim(name[1:], "0123456789") == "" && strings.Contains(path, "/") {
		trimmed := strings.TrimSuffix(path, "/"+name)
		name = trimmed[strings.LastIndexByte(trimmed, '/')+1:]
	}
	return name
}

func embeddedName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return ""
	}
}

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
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
