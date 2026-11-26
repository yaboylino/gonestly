package schema

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/yaboylino/gonestly/sql"
)

type structKey struct{ pkg, name string }

type source struct {
	key     structKey
	spec    *ast.StructType
	imports map[string]string
	pkg     *pkgInfo
}

type pkgInfo struct {
	path    string
	types   map[string]ast.Expr
	tables  map[string]string
	nonLit  map[string]bool
	structs map[string]*source
	order   []string
}

type draftRelation struct {
	field   string
	expr    ast.Expr
	tag     string
	options tagOptions
	imports map[string]string
	pkg     *pkgInfo
	owner   *draftModel
}

type draftModel struct {
	key       structKey
	table     string
	columns   []Column
	nullable  map[string]bool
	groups    map[string][]string
	unique    map[string]bool
	groupList []string
	relations []draftRelation
}

type tagOptions map[string]string

var schemaOptionNames = []string{"size", "type", "unique", "index", "default", "noauto", "onDelete"}

// Extract reads the models of the Go packages below root and returns the schema they describe.
// A model is an exported struct with at least one db tag, like the structs that
// gonestly generate columns reads.
func Extract(root, module string) (Schema, error) {
	packages, err := parseProject(root, module)
	if err != nil {
		return Schema{}, err
	}
	all := map[structKey]*source{}
	for _, p := range packages {
		for _, name := range p.order {
			all[structKey{p.path, name}] = p.structs[name]
		}
	}

	var drafts []*draftModel
	var problems []error
	for _, p := range packages {
		embedded := map[string]bool{}
		for _, name := range p.order {
			for _, f := range p.structs[name].spec.Fields.List {
				if id, ok := f.Type.(*ast.Ident); ok && len(f.Names) == 0 {
					embedded[id.Name] = true
				}
			}
		}
		for _, name := range p.order {
			src := p.structs[name]
			if !ast.IsExported(name) || embedded[name] || !hasDBTag(src.spec) {
				continue
			}
			draft, err := buildDraft(src, all)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			drafts = append(drafts, draft)
		}
	}
	byKey := map[structKey]*draftModel{}
	byTable := map[string]*draftModel{}
	for _, d := range drafts {
		byKey[d.key] = d
		if other, ok := byTable[d.table]; ok {
			problems = append(problems, fmt.Errorf("%s and %s both use the table %q", other.key.name, d.key.name, d.table))
		}
		byTable[d.table] = d
	}

	foreignKeys := map[string][]ForeignKey{}
	for _, d := range drafts {
		for _, rel := range d.relations {
			fk, holder, err := resolveRelation(rel, byKey)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			foreignKeys[holder] = mergeForeignKey(foreignKeys[holder], fk)
		}
	}
	if len(problems) > 0 {
		return Schema{}, errors.Join(problems...)
	}

	var result Schema
	for _, d := range drafts {
		t := Table{Name: d.table, Columns: d.columns, ForeignKeys: foreignKeys[d.table]}
		for _, name := range d.groupList {
			t.Indexes = append(t.Indexes, Index{Name: name, Columns: d.groups[name], Unique: d.unique[name]})
		}
		result.Tables = append(result.Tables, t)
	}
	result.normalize()
	return result, nil
}

func mergeForeignKey(list []ForeignKey, fk ForeignKey) []ForeignKey {
	for i, existing := range list {
		if existing.Name == fk.Name {
			if existing.OnDelete == "" {
				list[i].OnDelete = fk.OnDelete
			}
			return list
		}
	}
	return append(list, fk)
}

func parseProject(root, module string) ([]*pkgInfo, error) {
	var packages []*pkgInfo
	err := filepath.WalkDir(root, func(dir string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if dir != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata") {
			return filepath.SkipDir
		}
		p, err := parseDir(dir, importPath(root, module, dir))
		if err != nil {
			return err
		}
		if p != nil {
			packages = append(packages, p)
		}
		return nil
	})
	return packages, err
}

func importPath(root, module, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return module
	}
	return path.Join(module, filepath.ToSlash(rel))
}

func parseDir(dir, importPath string) (*pkgInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	p := &pkgInfo{path: importPath, types: map[string]ast.Expr{}, tables: map[string]string{}, nonLit: map[string]bool{}, structs: map[string]*source{}}
	fset := token.NewFileSet()
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "columns_gen.go" {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		found = true
		imports := map[string]string{}
		for _, spec := range file.Imports {
			ip, _ := strconv.Unquote(spec.Path.Value)
			local := path.Base(ip)
			if spec.Name != nil {
				local = spec.Name.Name
			}
			imports[local] = ip
		}
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.GenDecl:
				if decl.Tok != token.TYPE {
					continue
				}
				for _, spec := range decl.Specs {
					ts := spec.(*ast.TypeSpec) //nolint:forcetypeassert // type declarations hold type specs
					if ts.TypeParams != nil {
						continue
					}
					if st, ok := ts.Type.(*ast.StructType); ok {
						p.structs[ts.Name.Name] = &source{key: structKey{importPath, ts.Name.Name}, spec: st, imports: imports, pkg: p}
						p.order = append(p.order, ts.Name.Name)
					} else {
						p.types[ts.Name.Name] = ts.Type
					}
				}
			case *ast.FuncDecl:
				recordTableName(p, decl)
			}
		}
	}
	if !found {
		return nil, nil
	}
	return p, nil
}

func recordTableName(p *pkgInfo, fn *ast.FuncDecl) {
	if fn.Name.Name != "TableName" || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Body == nil {
		return
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	id, ok := recv.(*ast.Ident)
	if !ok {
		return
	}
	if len(fn.Body.List) == 1 {
		if ret, ok := fn.Body.List[0].(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
			if lit, ok := ret.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if name, err := strconv.Unquote(lit.Value); err == nil {
					p.tables[id.Name] = name
					return
				}
			}
		}
	}
	p.nonLit[id.Name] = true
}

func hasDBTag(st *ast.StructType) bool {
	for _, f := range st.Fields.List {
		if _, ok := lookupTag(f, "db"); ok {
			return true
		}
	}
	return false
}

func lookupTag(f *ast.Field, key string) (string, bool) {
	if f.Tag == nil {
		return "", false
	}
	raw, err := strconv.Unquote(f.Tag.Value)
	if err != nil {
		return "", false
	}
	return structTag(raw).lookup(key)
}

type structTag string

// lookup follows reflect.StructTag.Lookup.
func (tag structTag) lookup(key string) (string, bool) {
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

// splitOptions splits "size=200,default='a,b',type=decimal(10,2)" at the commas that are not in
// quotes or parentheses.
func splitOptions(s string) []string {
	var parts []string
	depth, quoted, start := 0, false, 0
	for i, r := range s {
		switch {
		case r == '\'':
			quoted = !quoted
		case quoted:
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

func parseSchemaTag(where, tag string) (tagOptions, error) {
	options := tagOptions{}
	for _, part := range splitOptions(tag) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !slices.Contains(schemaOptionNames, name) {
			return nil, fmt.Errorf("%s has the unknown schema option %q; use %s", where, name, strings.Join(schemaOptionNames, ", "))
		}
		options[name] = strings.TrimSpace(value)
	}
	return options, nil
}

func snake(s string) string {
	runes := []rune(s)
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

func buildDraft(src *source, all map[structKey]*source) (*draftModel, error) {
	name := src.key.name
	table := sql.DefaultTableName(name)
	if literal, ok := src.pkg.tables[name]; ok {
		table = literal
	} else if src.pkg.nonLit[name] {
		return nil, fmt.Errorf("%s: the TableName method must return a string literal, so that the schema can read it", name)
	}
	d := &draftModel{key: src.key, table: table, nullable: map[string]bool{}, groups: map[string][]string{}, unique: map[string]bool{}}
	if err := d.collect(src, all, map[structKey]bool{}); err != nil {
		return nil, err
	}
	var pks []int
	for i, c := range d.columns {
		if c.PK {
			pks = append(pks, i)
		}
	}
	if len(pks) == 0 {
		for i, c := range d.columns {
			if c.Name == "id" {
				d.columns[i].PK = true
				pks = append(pks, i)
			}
		}
	}
	for _, i := range pks {
		c := &d.columns[i]
		c.Nullable = false
		if len(pks) > 1 {
			c.Auto = false
		}
	}
	return d, nil
}

var defaultPattern = regexp.MustCompile(`^(now|true|false|-?[0-9]+(\.[0-9]+)?|'([^']|'')*'|expr:.+)$`)

func (d *draftModel) collect(src *source, all map[structKey]*source, seen map[structKey]bool) error {
	if seen[src.key] {
		return fmt.Errorf("%s embeds itself", src.key.name)
	}
	seen[src.key] = true
	defer delete(seen, src.key)
	model := d.key.name
	for _, f := range src.spec.Fields.List {
		relTag, isRel := lookupTag(f, "rel")
		dbTag, hasDB := lookupTag(f, "db")
		colName, dbOptions, _ := strings.Cut(dbTag, ",")
		if colName == "-" && !isRel {
			continue
		}
		schemaTag, _ := lookupTag(f, "schema")

		if len(f.Names) == 0 {
			if hasDB || isRel {
				if isRel {
					return fmt.Errorf("%s: an embedded field cannot be a relation", model)
				}
			} else if embedded := resolveStruct(f.Type, src, all); embedded != nil {
				if err := d.collect(embedded, all, seen); err != nil {
					return err
				}
				continue
			} else {
				continue
			}
		}

		names := f.Names
		if len(names) == 0 {
			names = []*ast.Ident{ast.NewIdent(embeddedName(f.Type))}
		}
		for _, n := range names {
			if !n.IsExported() {
				continue
			}
			where := model + "." + n.Name
			options, err := parseSchemaTag(where, schemaTag)
			if err != nil {
				return err
			}
			if isRel {
				rel, err := parseRelTag(where, relTag)
				if err != nil {
					return err
				}
				d.relations = append(d.relations, draftRelation{field: n.Name, expr: f.Type, tag: relTag, options: mergeOptions(options, rel), imports: src.imports, pkg: src.pkg, owner: d})
				continue
			}
			column, err := buildColumn(where, n.Name, colName, dbOptions, options, f.Type, src)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(d.columns, func(c Column) bool { return c.Name == column.Name }) {
				return fmt.Errorf("%s: two fields map to the column %q", model, column.Name)
			}
			d.columns = append(d.columns, column)
			if _, ok := options["unique"]; ok {
				d.addIndex(column.Name, options["unique"], true, "key")
			}
			if _, ok := options["index"]; ok {
				d.addIndex(column.Name, options["index"], false, "idx")
			}
		}
	}
	return nil
}

func mergeOptions(options tagOptions, rel tagOptions) tagOptions {
	merged := tagOptions{}
	for k, v := range options {
		merged[k] = v
	}
	for k, v := range rel {
		merged["rel."+k] = v
	}
	return merged
}

func (d *draftModel) addIndex(column, group string, unique bool, suffix string) {
	name := group
	if name == "" {
		name = constraintName(d.table, []string{column}, suffix)
	}
	if _, ok := d.groups[name]; !ok {
		d.groupList = append(d.groupList, name)
		d.unique[name] = unique
	}
	d.groups[name] = append(d.groups[name], column)
}

func embeddedName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

func resolveStruct(expr ast.Expr, src *source, all map[structKey]*source) *source {
	switch t := expr.(type) {
	case *ast.Ident:
		return all[structKey{src.pkg.path, t.Name}]
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			if ip, ok := src.imports[x.Name]; ok {
				return all[structKey{ip, t.Sel.Name}]
			}
		}
	}
	return nil
}

func buildColumn(where, fieldName, tagName, dbOptions string, options tagOptions, expr ast.Expr, src *source) (Column, error) {
	c := Column{Name: tagName}
	if c.Name == "" {
		c.Name = snake(fieldName)
	}
	typ, nullable, err := resolveType(expr, src, 0)
	if override, ok := options["type"]; ok {
		if override == "" {
			return Column{}, fmt.Errorf("%s has an empty schema type", where)
		}
		typ, err = parseTypeOverride(where, override)
		if err != nil {
			return Column{}, err
		}
	} else if err != nil {
		return Column{}, fmt.Errorf("%s: %w; give the column a type with schema:\"type=...\"", where, err)
	}
	if size, ok := options["size"]; ok {
		n, convErr := strconv.Atoi(size)
		if convErr != nil || n < 1 {
			return Column{}, fmt.Errorf("%s has the size %q, which is not a positive number", where, size)
		}
		if typ.Kind != String {
			return Column{}, fmt.Errorf("%s has a size, which only a string column takes", where)
		}
		typ.Size = n
	}
	c.Type, c.Nullable = typ, nullable
	opts := strings.Split(dbOptions, ",")
	c.PK = slices.Contains(opts, "pk")
	if !c.PK && fieldName == "ID" {
		c.PK = true
	}
	_, noAuto := options["noauto"]
	c.Auto = c.PK && !noAuto && slices.Contains([]Kind{Int16, Int32, Int64}, typ.Kind)
	if value, ok := options["default"]; ok {
		if !defaultPattern.MatchString(value) {
			return Column{}, fmt.Errorf("%s has the default %q; use now, true, false, a number, a string in single quotes or expr:SQL", where, value)
		}
		c.Default = value
	} else if slices.Contains(opts, "default") && typ.Kind == Time {
		c.Default = "now"
	}
	if onDelete, ok := options["onDelete"]; ok && onDelete != "" {
		return Column{}, fmt.Errorf("%s: onDelete belongs on a relation field, not on a column", where)
	}
	return c, nil
}

var typeCall = regexp.MustCompile(`^([a-zA-Z]+)\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\)$`)

func parseTypeOverride(where, s string) (Type, error) {
	lower := strings.ToLower(strings.TrimSpace(s))
	switch lower {
	case "text":
		return Type{Kind: Text}, nil
	case "json", "jsonb":
		return Type{Kind: JSON}, nil
	case "uuid":
		return Type{Kind: UUID}, nil
	case "bytes", "blob", "bytea":
		return Type{Kind: Bytes}, nil
	case "string":
		return Type{Kind: String}, nil
	case "bool", "boolean":
		return Type{Kind: Bool}, nil
	case "time", "timestamp", "datetime":
		return Type{Kind: Time}, nil
	case "int16", "smallint":
		return Type{Kind: Int16}, nil
	case "int32", "int", "integer":
		return Type{Kind: Int32}, nil
	case "int64", "bigint":
		return Type{Kind: Int64}, nil
	case "float32", "real":
		return Type{Kind: Float32}, nil
	case "float64", "double":
		return Type{Kind: Float64}, nil
	}
	if m := typeCall.FindStringSubmatch(lower); m != nil {
		a, _ := strconv.Atoi(m[2])
		switch m[1] {
		case "varchar", "string":
			return Type{Kind: String, Size: a}, nil
		case "decimal", "numeric":
			scale := 0
			if m[3] != "" {
				scale, _ = strconv.Atoi(m[3])
			}
			if scale > a {
				return Type{}, fmt.Errorf("%s has the type %s, whose scale is bigger than its precision", where, s)
			}
			return Type{Kind: Decimal, Precision: a, Scale: scale}, nil
		}
	}
	return Type{Kind: Raw, Raw: strings.TrimSpace(s)}, nil
}

var builtinTypes = map[string]Type{
	"string": {Kind: String}, "bool": {Kind: Bool},
	"int": {Kind: Int64}, "int64": {Kind: Int64}, "int32": {Kind: Int32}, "int16": {Kind: Int16}, "int8": {Kind: Int16},
	"uint": {Kind: Int64}, "uint64": {Kind: Int64}, "uint32": {Kind: Int64}, "uint16": {Kind: Int32}, "uint8": {Kind: Int16},
	"float64": {Kind: Float64}, "float32": {Kind: Float32}, "byte": {Kind: Int16}, "rune": {Kind: Int32},
}

var nullTypes = map[string]Type{
	"NullString": {Kind: String}, "NullInt64": {Kind: Int64}, "NullInt32": {Kind: Int32}, "NullInt16": {Kind: Int16},
	"NullBool": {Kind: Bool}, "NullFloat64": {Kind: Float64}, "NullTime": {Kind: Time}, "NullByte": {Kind: Int16},
}

// resolveType maps the Go type of a field to a column type, and tells whether it can be NULL.
func resolveType(expr ast.Expr, src *source, depth int) (Type, bool, error) {
	if depth > 8 {
		return Type{}, false, errors.New("a type that refers to itself")
	}
	switch t := expr.(type) {
	case *ast.StarExpr:
		typ, _, err := resolveType(t.X, src, depth+1)
		return typ, true, err
	case *ast.Ident:
		if typ, ok := builtinTypes[t.Name]; ok {
			return typ, false, nil
		}
		if under, ok := src.pkg.types[t.Name]; ok {
			return resolveType(under, src, depth+1)
		}
		return Type{}, false, fmt.Errorf("the type %s is not one that a column can hold", t.Name)
	case *ast.ArrayType:
		if elt, ok := t.Elt.(*ast.Ident); ok && t.Len == nil && (elt.Name == "byte" || elt.Name == "uint8") {
			return Type{Kind: Bytes}, true, nil
		}
		return Type{Kind: JSON}, true, nil
	case *ast.MapType:
		return Type{Kind: JSON}, true, nil
	case *ast.SelectorExpr:
		x, ok := t.X.(*ast.Ident)
		if !ok {
			return Type{}, false, errors.New("an unsupported type")
		}
		switch src.imports[x.Name] {
		case "time":
			if t.Sel.Name == "Time" {
				return Type{Kind: Time}, false, nil
			}
		case "database/sql":
			if typ, ok := nullTypes[t.Sel.Name]; ok {
				return typ, true, nil
			}
		case "encoding/json":
			if t.Sel.Name == "RawMessage" {
				return Type{Kind: JSON}, true, nil
			}
		}
		return Type{}, false, fmt.Errorf("the type %s.%s is not one that a column can hold", x.Name, t.Sel.Name)
	}
	return Type{}, false, errors.New("an unsupported type")
}

func parseRelTag(where, tag string) (tagOptions, error) {
	key, rest, _ := strings.Cut(tag, ",")
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%s has an empty rel tag", where)
	}
	opts := tagOptions{"key": key}
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		name, value, _ := strings.Cut(part, "=")
		switch {
		case part == "":
		case name == "references" && value != "":
			opts["references"] = value
		case part == "has" || part == "belongs":
			opts["direction"] = part
		default:
			return nil, fmt.Errorf("%s has the unknown rel option %q", where, part)
		}
	}
	return opts, nil
}

func normalizeOnDelete(where, v string) (string, error) {
	switch strings.ToLower(strings.ReplaceAll(v, " ", "")) {
	case "":
		return "", nil
	case "cascade":
		return "cascade", nil
	case "restrict":
		return "restrict", nil
	case "setnull":
		return "setnull", nil
	case "noaction":
		return "", nil
	}
	return "", fmt.Errorf("%s has the onDelete %q; use cascade, restrict, setnull or noaction", where, v)
}

// resolveRelation finds the foreign key that a relation field declares, and the table it is in.
func resolveRelation(rel draftRelation, models map[structKey]*draftModel) (ForeignKey, string, error) {
	where := rel.owner.key.name + "." + rel.field
	var targetExpr ast.Expr
	hasMany := false
	switch t := rel.expr.(type) {
	case *ast.ArrayType:
		targetExpr, hasMany = t.Elt, true
	case *ast.StarExpr:
		targetExpr = t.X
	default:
		return ForeignKey{}, "", fmt.Errorf("%s: a relation is a slice of structs or a pointer to a struct", where)
	}
	var target *draftModel
	switch t := targetExpr.(type) {
	case *ast.Ident:
		target = models[structKey{rel.pkg.path, t.Name}]
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			target = models[structKey{rel.imports[x.Name], t.Sel.Name}]
		}
	}
	if target == nil {
		return ForeignKey{}, "", fmt.Errorf("%s points at a type that is not a model", where)
	}
	key := rel.options["rel.key"]
	owner := rel.owner
	has := func(m *draftModel) bool {
		return slices.ContainsFunc(m.columns, func(c Column) bool { return c.Name == key })
	}
	var holder, pointed *draftModel
	switch {
	case hasMany:
		holder, pointed = target, owner
	case rel.options["rel.direction"] == "belongs":
		holder, pointed = owner, target
	case rel.options["rel.direction"] == "has":
		holder, pointed = target, owner
	case has(owner) && has(target):
		return ForeignKey{}, "", fmt.Errorf("%s is ambiguous: both models have the column %q; add ,belongs or ,has", where, key)
	case has(owner):
		holder, pointed = owner, target
	default:
		holder, pointed = target, owner
	}
	if !has(holder) {
		return ForeignKey{}, "", fmt.Errorf("%s uses the key column %q, but %s has no such column", where, key, holder.key.name)
	}
	refColumn := rel.options["rel.references"]
	if refColumn == "" {
		var pk []string
		for _, c := range pointed.columns {
			if c.PK || (c.Name == "id" && !slices.ContainsFunc(pointed.columns, func(o Column) bool { return o.PK })) {
				pk = append(pk, c.Name)
			}
		}
		if len(pk) != 1 {
			return ForeignKey{}, "", fmt.Errorf("%s needs a single primary key in %s to point at; or name a column with references=", where, pointed.key.name)
		}
		refColumn = pk[0]
	} else if !slices.ContainsFunc(pointed.columns, func(c Column) bool { return c.Name == refColumn }) {
		return ForeignKey{}, "", fmt.Errorf("%s references the column %q, but %s has no such column", where, refColumn, pointed.key.name)
	}
	onDelete, err := normalizeOnDelete(where, rel.options["onDelete"])
	if err != nil {
		return ForeignKey{}, "", err
	}
	if onDelete == "setnull" {
		for _, c := range holder.columns {
			if c.Name == key && !c.Nullable {
				return ForeignKey{}, "", fmt.Errorf("%s sets %s.%s to null on delete, but the column is not nullable; make the field a pointer", where, holder.key.name, key)
			}
		}
	}
	return ForeignKey{
		Name:       constraintName(holder.table, []string{key}, "fkey"),
		Columns:    []string{key},
		RefTable:   pointed.table,
		RefColumns: []string{refColumn},
		OnDelete:   onDelete,
	}, holder.table, nil
}
