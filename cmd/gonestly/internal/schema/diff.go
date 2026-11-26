package schema

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Op is one change to a schema.
type Op interface{ describe() string }

// The changes that a diff is made of.
type (
	// CreateTable adds a table.
	CreateTable struct{ Table Table }
	// DropTable removes a table and its data.
	DropTable struct{ Table Table }
	// RenameTable changes the name of a table and keeps its data.
	RenameTable struct{ From, To string }
	// AddColumn adds a column to a table.
	AddColumn struct {
		Table  string
		Column Column
	}
	// DropColumn removes a column and its data.
	DropColumn struct {
		Table  string
		Column Column
	}
	// AlterColumn changes the type, nullability or default of a column.
	AlterColumn struct {
		Table    string
		From, To Column
	}
	// RenameColumn changes the name of a column and keeps its data.
	RenameColumn struct{ Table, From, To string }
	// AddIndex adds an index to a table.
	AddIndex struct {
		Table string
		Index Index
	}
	// DropIndex removes an index.
	DropIndex struct {
		Table string
		Index Index
	}
	// AddForeignKey adds a foreign key to a table.
	AddForeignKey struct {
		Table string
		Key   ForeignKey
	}
	// DropForeignKey removes a foreign key.
	DropForeignKey struct {
		Table string
		Key   ForeignKey
	}
)

func (o CreateTable) describe() string {
	return "create the table " + o.Table.Name
}

func (o DropTable) describe() string {
	return "drop the table " + o.Table.Name
}

func (o RenameTable) describe() string {
	return "rename the table " + o.From + " to " + o.To
}

func (o AddColumn) describe() string {
	return fmt.Sprintf("add the column %s.%s", o.Table, o.Column.Name)
}

func (o DropColumn) describe() string {
	return fmt.Sprintf("drop the column %s.%s", o.Table, o.Column.Name)
}

func (o AlterColumn) describe() string {
	return fmt.Sprintf("change the column %s.%s", o.Table, o.To.Name)
}

func (o RenameColumn) describe() string {
	return fmt.Sprintf("rename the column %s.%s to %s", o.Table, o.From, o.To)
}

func (o AddIndex) describe() string {
	return "add the index " + o.Index.Name
}

func (o DropIndex) describe() string {
	return "drop the index " + o.Index.Name
}

func (o AddForeignKey) describe() string {
	return "add the foreign key " + o.Key.Name
}

func (o DropForeignKey) describe() string {
	return "drop the foreign key " + o.Key.Name
}

// Rename says that a column or a table in the old schema is the one with a new name in the new
// schema. Table is empty for the rename of a table.
type Rename struct{ Table, From, To string }

// Candidate is a rename that a diff could be, which only the author can confirm.
type Candidate struct{ Rename Rename }

func (c Candidate) String() string {
	if c.Rename.Table == "" {
		return fmt.Sprintf("table %s to %s", c.Rename.From, c.Rename.To)
	}
	return fmt.Sprintf("column %s.%s to %s", c.Rename.Table, c.Rename.From, c.Rename.To)
}

// ErrUnsupported is returned for a change that cannot be made with generated SQL.
var ErrUnsupported = errors.New("schema: unsupported change")

// TableCandidates lists the tables that disappeared while one of the same shape appeared, which is
// what a rename looks like. A diff without the rename drops the old one and creates the new one,
// and loses the data.
func TableCandidates(from, to Schema) []Candidate {
	var gone, added []Table
	for _, t := range from.Tables {
		if _, ok := to.table(t.Name); !ok {
			gone = append(gone, t)
		}
	}
	for _, t := range to.Tables {
		if _, ok := from.table(t.Name); !ok {
			added = append(added, t)
		}
	}
	var candidates []Candidate
	used := map[string]bool{}
	for _, g := range gone {
		for _, a := range added {
			if !used[a.Name] && similarTables(g, a) {
				used[a.Name] = true
				candidates = append(candidates, Candidate{Rename{From: g.Name, To: a.Name}})
				break
			}
		}
	}
	return candidates
}

// ColumnCandidates lists the columns that disappeared from a table while one of the same type
// appeared. The tables that were renamed, as confirmed in tableRenames, count as the same table.
func ColumnCandidates(from, to Schema, tableRenames []Rename) []Candidate {
	var candidates []Candidate
	for _, oldTable := range from.Tables {
		newName := renamedTable(tableRenames, oldTable.Name)
		newTable, ok := to.table(newName)
		if !ok {
			continue
		}
		var gone, added []Column
		for _, c := range oldTable.Columns {
			if _, ok := newTable.column(c.Name); !ok {
				gone = append(gone, c)
			}
		}
		for _, c := range newTable.Columns {
			if _, ok := oldTable.column(c.Name); !ok {
				added = append(added, c)
			}
		}
		used := map[string]bool{}
		for _, g := range gone {
			for _, a := range added {
				if !used[a.Name] && g.Type == a.Type {
					used[a.Name] = true
					candidates = append(candidates, Candidate{Rename{Table: oldTable.Name, From: g.Name, To: a.Name}})
					break
				}
			}
		}
	}
	return candidates
}

// similarTables reports whether two tables look like one table that was renamed: they have the same
// column types, or most of their columns are the same.
func similarTables(a, b Table) bool {
	types := func(t Table) []string {
		var list []string
		for _, c := range t.Columns {
			list = append(list, c.Type.String())
		}
		slices.Sort(list)
		return list
	}
	if slices.Equal(types(a), types(b)) {
		return true
	}
	same := 0
	for _, c := range a.Columns {
		if other, ok := b.column(c.Name); ok && other.Type == c.Type {
			same++
		}
	}
	return same*2 >= max(len(a.Columns), len(b.Columns))
}

// applyRenames returns the old schema as if the renames had been made, so that a diff against the
// new schema only sees the other changes.
func applyRenames(s Schema, renames []Rename) Schema {
	out := Schema{Version: s.Version}
	tableNames := map[string]string{}
	for _, r := range renames {
		if r.Table == "" {
			tableNames[r.From] = r.To
		}
	}
	columnNames := map[string]map[string]string{}
	for _, r := range renames {
		if r.Table != "" {
			if columnNames[r.Table] == nil {
				columnNames[r.Table] = map[string]string{}
			}
			columnNames[r.Table][r.From] = r.To
		}
	}
	renameTable := func(n string) string {
		if to, ok := tableNames[n]; ok {
			return to
		}
		return n
	}
	for _, t := range s.Tables {
		oldName := t.Name
		cols := columnNames[oldName]
		rename := func(n string) string {
			if to, ok := cols[n]; ok {
				return to
			}
			return n
		}
		nt := Table{Name: renameTable(oldName)}
		for _, c := range t.Columns {
			c.Name = rename(c.Name)
			nt.Columns = append(nt.Columns, c)
		}
		for _, ix := range t.Indexes {
			ix.Columns = mapStrings(ix.Columns, rename)
			nt.Indexes = append(nt.Indexes, ix)
		}
		for _, fk := range t.ForeignKeys {
			fk.Columns = mapStrings(fk.Columns, rename)
			fk.RefTable = renameTable(fk.RefTable)
			fk.RefColumns = mapStrings(fk.RefColumns, func(n string) string { return renamedColumn(columnNames, fk.RefTable, n, tableNames) })
			nt.ForeignKeys = append(nt.ForeignKeys, fk)
		}
		out.Tables = append(out.Tables, nt)
	}
	return out
}

// renamedColumn finds the new name of a column in a table that may itself be renamed.
func renamedColumn(columnNames map[string]map[string]string, newTable, column string, tableNames map[string]string) string {
	oldTable := newTable
	for from, to := range tableNames {
		if to == newTable {
			oldTable = from
		}
	}
	if to, ok := columnNames[oldTable][column]; ok {
		return to
	}
	return column
}

func mapStrings(in []string, fn func(string) string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = fn(s)
	}
	return out
}

// DiffOptions change how a diff is made.
type DiffOptions struct {
	// Renames are the renames that the author confirmed.
	Renames []Rename
	// Relaxed makes a new column that has no default nullable, for the SQL that undoes a drop,
	// where the data of the column cannot come back.
	Relaxed bool
}

// Diff lists the changes that turn the schema from into the schema to, in the order they must be
// made: renames, then what has to go before a column or table changes, then the changes, then what
// has to come after.
func Diff(from, to Schema, opts DiffOptions) ([]Op, error) {
	var ops []Op
	for _, r := range opts.Renames {
		if r.Table == "" {
			ops = append(ops, RenameTable{From: r.From, To: r.To})
		}
	}
	for _, r := range opts.Renames {
		if r.Table != "" {
			table := r.Table
			for _, tr := range opts.Renames {
				if tr.Table == "" && tr.From == table {
					table = tr.To
				}
			}
			ops = append(ops, RenameColumn{Table: table, From: r.From, To: r.To})
		}
	}
	base := applyRenames(from, opts.Renames)

	var before, columns, creates, after, drops []Op
	dropped := map[string]Table{}
	for _, t := range base.Tables {
		if _, ok := to.table(t.Name); !ok {
			dropped[t.Name] = t
		}
	}
	for _, t := range base.Tables {
		newTable, ok := to.table(t.Name)
		if !ok {
			for _, fk := range t.ForeignKeys {
				if inCycle(dropped, t.Name, fk) {
					before = append(before, DropForeignKey{Table: t.Name, Key: fk})
				}
			}
			continue
		}
		b, c, a, err := diffTable(t, newTable, opts)
		if err != nil {
			return nil, err
		}
		before, columns, after = append(before, b...), append(columns, c...), append(after, a...)
	}
	for _, t := range to.Tables {
		if _, ok := base.table(t.Name); !ok {
			creates = append(creates, CreateTable{Table: t})
		}
	}
	creates = orderCreates(creates)
	var dropList []Table
	for _, t := range base.Tables {
		if _, ok := dropped[t.Name]; ok {
			dropList = append(dropList, t)
		}
	}
	for _, t := range orderDrops(dropList) {
		drops = append(drops, DropTable{Table: t})
	}
	ops = slices.Concat(ops, before, columns, creates, after, drops)
	return ops, nil
}

func diffTable(from, to Table, opts DiffOptions) (before, columns, after []Op, err error) {
	for _, fk := range from.ForeignKeys {
		if other, ok := foreignKey(to, fk.Name); !ok || !sameForeignKey(fk, other) {
			before = append(before, DropForeignKey{Table: from.Name, Key: fk})
		}
	}
	for _, ix := range from.Indexes {
		if other, ok := index(to, ix.Name); !ok || !sameIndex(ix, other) {
			before = append(before, DropIndex{Table: from.Name, Index: ix})
		}
	}
	oldPK, newPK := pkNames(from), pkNames(to)
	if !slices.Equal(oldPK, newPK) {
		return nil, nil, nil, fmt.Errorf("%w: the primary key of %s changes from (%s) to (%s); write that migration by hand", ErrUnsupported, from.Name, strings.Join(oldPK, ", "), strings.Join(newPK, ", "))
	}
	for _, c := range from.Columns {
		if _, ok := to.column(c.Name); !ok {
			columns = append(columns, DropColumn{Table: from.Name, Column: c})
		}
	}
	for _, c := range to.Columns {
		old, ok := from.column(c.Name)
		switch {
		case !ok:
			if opts.Relaxed && !c.Nullable && c.Default == "" && !c.Auto {
				c.Nullable = true
			}
			columns = append(columns, AddColumn{Table: from.Name, Column: c})
		case !sameColumn(old, c):
			if old.Auto != c.Auto {
				return nil, nil, nil, fmt.Errorf("%w: the column %s.%s changes between generated and not generated; write that migration by hand", ErrUnsupported, from.Name, c.Name)
			}
			columns = append(columns, AlterColumn{Table: from.Name, From: old, To: c})
		}
	}
	for _, ix := range to.Indexes {
		if other, ok := index(from, ix.Name); !ok || !sameIndex(ix, other) {
			after = append(after, AddIndex{Table: from.Name, Index: ix})
		}
	}
	for _, fk := range to.ForeignKeys {
		if other, ok := foreignKey(from, fk.Name); !ok || !sameForeignKey(fk, other) {
			after = append(after, AddForeignKey{Table: from.Name, Key: fk})
		}
	}
	return before, columns, after, nil
}

func pkNames(t Table) []string {
	var names []string
	for _, c := range t.pk() {
		names = append(names, c.Name)
	}
	return names
}

func sameColumn(a, b Column) bool {
	return a.Type == b.Type && a.Nullable == b.Nullable && a.Default == b.Default && a.PK == b.PK && a.Auto == b.Auto
}

func sameIndex(a, b Index) bool {
	return a.Unique == b.Unique && slices.Equal(a.Columns, b.Columns)
}

func sameForeignKey(a, b ForeignKey) bool {
	return a.RefTable == b.RefTable && a.OnDelete == b.OnDelete && slices.Equal(a.Columns, b.Columns) && slices.Equal(a.RefColumns, b.RefColumns)
}

func foreignKey(t Table, name string) (ForeignKey, bool) {
	for _, fk := range t.ForeignKeys {
		if fk.Name == name {
			return fk, true
		}
	}
	return ForeignKey{}, false
}

func index(t Table, name string) (Index, bool) {
	for _, ix := range t.Indexes {
		if ix.Name == name {
			return ix, true
		}
	}
	return Index{}, false
}

// orderCreates puts a table after the tables that its foreign keys point at; tables that point at
// each other keep their order, and their keys are added after the tables exist.
func orderCreates(creates []Op) []Op {
	byName := map[string]CreateTable{}
	var names []string
	for _, op := range creates {
		ct := op.(CreateTable) //nolint:forcetypeassert // only CreateTable is passed
		byName[ct.Table.Name] = ct
		names = append(names, ct.Table.Name)
	}
	slices.Sort(names)
	var ordered []Op
	state := map[string]int{}
	var visit func(string)
	visit = func(name string) {
		if state[name] != 0 {
			return
		}
		state[name] = 1
		for _, fk := range byName[name].Table.ForeignKeys {
			if _, ok := byName[fk.RefTable]; ok && fk.RefTable != name {
				visit(fk.RefTable)
			}
		}
		state[name] = 2
		ordered = append(ordered, byName[name])
	}
	for _, name := range names {
		visit(name)
	}
	return ordered
}

// orderDrops puts a table before the tables that its foreign keys point at.
func orderDrops(tables []Table) []Table {
	var ops []Op
	for _, t := range tables {
		ops = append(ops, CreateTable{Table: t})
	}
	ordered := orderCreates(ops)
	out := make([]Table, len(ordered))
	for i, op := range ordered {
		out[len(ordered)-1-i] = op.(CreateTable).Table //nolint:forcetypeassert // only CreateTable is passed
	}
	return out
}

// DataLoss describes the changes that lose data: dropping a table or a column, or changing a
// column to a type that cannot hold what it holds.
func DataLoss(ops []Op) []string {
	var warnings []string
	for _, op := range ops {
		switch o := op.(type) {
		case DropTable:
			warnings = append(warnings, "the table "+o.Table.Name+" and all its rows are dropped")
		case DropColumn:
			warnings = append(warnings, fmt.Sprintf("the column %s.%s and its data are dropped", o.Table, o.Column.Name))
		case AlterColumn:
			if lossy(o.From, o.To) {
				warnings = append(warnings, fmt.Sprintf("the column %s.%s changes from %s to %s, which can lose data or fail", o.Table, o.To.Name, o.From.Type, o.To.Type))
			}
		}
	}
	return warnings
}

// Risks describes the changes that fail on a table whose rows do not fit them. The migration then
// rolls back and nothing is lost.
func Risks(ops []Op) []string {
	var warnings []string
	for _, op := range ops {
		switch o := op.(type) {
		case AlterColumn:
			if !lossy(o.From, o.To) && o.From.Nullable && !o.To.Nullable {
				warnings = append(warnings, fmt.Sprintf("the column %s.%s becomes required, which fails when a row has no value", o.Table, o.To.Name))
			}
		case AddColumn:
			if !o.Column.Nullable && o.Column.Default == "" && !o.Column.Auto {
				warnings = append(warnings, fmt.Sprintf("the required column %s.%s has no default, which fails when the table has rows; give it a default or make the field a pointer", o.Table, o.Column.Name))
			}
		case AddIndex:
			if o.Index.Unique {
				warnings = append(warnings, fmt.Sprintf("the unique index %s fails when rows hold the same value twice", o.Index.Name))
			}
		}
	}
	return warnings
}

// lossy reports whether turning one type into another can lose data.
func lossy(from, to Column) bool {
	if from.Type == to.Type {
		return false
	}
	if from.Type.Kind == to.Type.Kind {
		switch from.Type.Kind {
		case String:
			return to.Type.size() < from.Type.size()
		case Decimal:
			return to.Type.Precision < from.Type.Precision || to.Type.Scale < from.Type.Scale
		default:
			return from.Type.Kind == Raw
		}
	}
	widening := map[[2]Kind]bool{
		{Int16, Int32}: true, {Int16, Int64}: true, {Int32, Int64}: true, {Float32, Float64}: true,
		{String, Text}: true, {Int16, Float64}: true, {Int32, Float64}: true,
	}
	return !widening[[2]Kind{from.Type.Kind, to.Type.Kind}]
}

// SuggestName gives a name for a migration, such as create_tracks or add_tracks_slug.
func SuggestName(ops []Op) string {
	var parts []string
	add := func(s string) {
		if !slices.Contains(parts, s) {
			parts = append(parts, s)
		}
	}
	for _, op := range ops {
		switch o := op.(type) {
		case CreateTable:
			add("create_" + o.Table.Name)
		case DropTable:
			add("drop_" + o.Table.Name)
		case RenameTable:
			add("rename_" + o.From + "_to_" + o.To)
		case AddColumn:
			add("add_" + o.Table + "_" + o.Column.Name)
		case DropColumn:
			add("drop_" + o.Table + "_" + o.Column.Name)
		case AlterColumn:
			add("change_" + o.Table + "_" + o.To.Name)
		case RenameColumn:
			add("rename_" + o.Table + "_" + o.From + "_to_" + o.To)
		case AddIndex:
			add("add_index_" + o.Index.Name)
		case AddForeignKey:
			add("add_" + o.Key.Name)
		}
	}
	if len(parts) == 0 {
		return "update_schema"
	}
	name := strings.Join(parts[:min(len(parts), 3)], "_and_")
	if len(parts) > 3 {
		name += "_and_more"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	return strings.Trim(name, "_")
}

// inCycle reports whether a foreign key of a dropped table is part of a loop of dropped tables
// that point at each other, which can only be dropped after one key of the loop is gone.
func inCycle(dropped map[string]Table, table string, fk ForeignKey) bool {
	if fk.RefTable == table {
		return false
	}
	if _, ok := dropped[fk.RefTable]; !ok {
		return false
	}
	seen := map[string]bool{fk.RefTable: true}
	queue := []string{fk.RefTable}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range dropped[current].ForeignKeys {
			if next.RefTable == table {
				return true
			}
			if _, ok := dropped[next.RefTable]; ok && !seen[next.RefTable] {
				seen[next.RefTable] = true
				queue = append(queue, next.RefTable)
			}
		}
	}
	return false
}

// Describe lists in words what the ops do, one per line.
func Describe(ops []Op) []string {
	lines := make([]string, len(ops))
	for i, op := range ops {
		lines[i] = op.describe()
	}
	return lines
}
