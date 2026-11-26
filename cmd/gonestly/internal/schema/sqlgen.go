package schema

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Dialect is the database that SQL is written for.
type Dialect string

// The databases.
const (
	Postgres Dialect = "postgres"
	MySQL    Dialect = "mysql"
	SQLite   Dialect = "sqlite"
)

// Plan is what a change to the schema comes to: the SQL that makes it, the SQL that undoes it, and
// what the author should know before running it.
type Plan struct {
	Ops      []Op
	Up, Down string
	// DataLoss lists what the migration deletes, and Risks what can make it fail.
	DataLoss, Risks []string
}

// Empty reports that there is nothing to change.
func (p Plan) Empty() bool { return len(p.Ops) == 0 }

// NewPlan diffs two schemas and writes the SQL both ways for one database.
func NewPlan(d Dialect, from, to Schema, renames []Rename) (Plan, error) {
	ops, err := Diff(from, to, DiffOptions{Renames: renames})
	if err != nil {
		return Plan{}, err
	}
	if len(ops) == 0 {
		return Plan{}, nil
	}
	up, err := SQL(d, applyRenames(from, renames), to, ops)
	if err != nil {
		return Plan{}, err
	}
	inverse := make([]Rename, len(renames))
	for i, r := range renames {
		inverse[i] = Rename{Table: renamedTable(renames, r.Table), From: r.To, To: r.From}
	}
	downOps, err := Diff(to, from, DiffOptions{Renames: inverse, Relaxed: true})
	if err != nil {
		return Plan{}, err
	}
	down, err := SQL(d, applyRenames(to, inverse), relaxed(from, downOps), downOps)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Ops: ops, Up: up, Down: down, DataLoss: DataLoss(ops), Risks: Risks(ops)}, nil
}

// renamedTable returns the table name that a table rename gave, for the column renames that are
// listed under the old name of a renamed table.
func renamedTable(renames []Rename, table string) string {
	for _, r := range renames {
		if r.Table == "" && r.From == table {
			return r.To
		}
	}
	return table
}

var plainName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

var reserved = map[string]bool{
	"all": true, "alter": true, "and": true, "as": true, "asc": true, "between": true, "by": true, "case": true,
	"check": true, "column": true, "constraint": true, "create": true, "cross": true, "current_date": true,
	"current_time": true, "current_timestamp": true, "current_user": true, "default": true, "delete": true,
	"desc": true, "distinct": true, "drop": true, "else": true, "end": true, "exists": true, "false": true,
	"for": true, "foreign": true, "from": true, "full": true, "group": true, "having": true, "in": true,
	"index": true, "inner": true, "insert": true, "into": true, "is": true, "join": true, "key": true,
	"left": true, "like": true, "limit": true, "match": true, "natural": true, "not": true, "null": true,
	"offset": true, "on": true, "or": true, "order": true, "outer": true, "primary": true, "references": true,
	"right": true, "select": true, "set": true, "table": true, "then": true, "to": true, "true": true,
	"union": true, "unique": true, "update": true, "user": true, "using": true, "values": true, "when": true,
	"where": true, "with": true, "rank": true, "row": true, "rows": true, "range": true, "read": true,
	"write": true, "usage": true, "trigger": true, "type": false,
}

// quote writes an identifier, in quotes only when it has to be.
func (g *gen) quote(name string) string {
	if plainName.MatchString(name) && !reserved[name] {
		return name
	}
	if g.d == MySQL {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

type gen struct {
	d       Dialect
	base    Schema
	to      Schema
	script  []string
	rebuilt bool
}

// SQL writes the statements for the ops, which turn the schema base into the schema to.
func SQL(d Dialect, base, to Schema, ops []Op) (string, error) {
	if d != Postgres && d != MySQL && d != SQLite {
		return "", fmt.Errorf("schema: unknown database %q", d)
	}
	g := &gen{d: d, base: base, to: to}
	if err := g.run(ops); err != nil {
		return "", err
	}
	script := strings.Join(g.script, "\n\n") + "\n"
	if g.rebuilt {
		script = "-- gonestly:foreign-keys-off\n\n" + script
	}
	return script, nil
}

func (g *gen) add(format string, args ...any) {
	g.script = append(g.script, fmt.Sprintf(format, args...)+";")
}

func (g *gen) run(ops []Op) error {
	rebuild := g.rebuildTables(ops)
	rebuilt := map[string]bool{}
	created := map[string]bool{}
	for _, t := range g.base.Tables {
		created[t.Name] = true
	}
	var deferred []ForeignKey
	var deferredTables []string
	for _, op := range ops {
		switch o := op.(type) {
		case RenameTable:
			if g.d == MySQL {
				g.add("RENAME TABLE %s TO %s", g.quote(o.From), g.quote(o.To))
			} else {
				g.add("ALTER TABLE %s RENAME TO %s", g.quote(o.From), g.quote(o.To))
			}
		case RenameColumn:
			g.add("ALTER TABLE %s RENAME COLUMN %s TO %s", g.quote(o.Table), g.quote(o.From), g.quote(o.To))
		case CreateTable:
			t := o.Table
			var inline, later []ForeignKey
			for _, fk := range t.ForeignKeys {
				if g.d == SQLite || created[fk.RefTable] || fk.RefTable == t.Name {
					inline = append(inline, fk)
				} else {
					later = append(later, fk)
				}
			}
			created[t.Name] = true
			g.createTable(t, inline)
			for _, ix := range t.Indexes {
				g.createIndex(t.Name, ix)
			}
			deferred = append(deferred, later...)
			for range later {
				deferredTables = append(deferredTables, t.Name)
			}
		case DropTable:
			g.add("DROP TABLE %s", g.quote(o.Table.Name))
		case AddIndex:
			if !rebuild[o.Table] {
				g.createIndex(o.Table, o.Index)
			}
		case DropIndex:
			if !rebuild[o.Table] {
				g.dropIndex(o.Table, o.Index)
			}
		case AddForeignKey:
			if !rebuild[o.Table] {
				g.addForeignKey(o.Table, o.Key)
			} else if err := g.rebuildOnce(o.Table, rebuilt); err != nil {
				return err
			}
		case DropForeignKey:
			if !rebuild[o.Table] {
				g.dropForeignKey(o.Table, o.Key)
			} else if err := g.rebuildOnce(o.Table, rebuilt); err != nil {
				return err
			}
		case AddColumn:
			if rebuild[o.Table] {
				if err := g.rebuildOnce(o.Table, rebuilt); err != nil {
					return err
				}
				continue
			}
			g.add("ALTER TABLE %s ADD COLUMN %s", g.quote(o.Table), g.columnDef(o.Column, false, 0))
		case DropColumn:
			if rebuild[o.Table] {
				if err := g.rebuildOnce(o.Table, rebuilt); err != nil {
					return err
				}
				continue
			}
			g.add("ALTER TABLE %s DROP COLUMN %s", g.quote(o.Table), g.quote(o.Column.Name))
		case AlterColumn:
			if rebuild[o.Table] {
				if err := g.rebuildOnce(o.Table, rebuilt); err != nil {
					return err
				}
				continue
			}
			g.alterColumn(o)
		}
	}
	for i, fk := range deferred {
		g.addForeignKey(deferredTables[i], fk)
	}
	return nil
}

func (g *gen) rebuildOnce(table string, done map[string]bool) error {
	if done[table] {
		return nil
	}
	done[table] = true
	return g.rebuild(table)
}

// rebuildTables finds the tables that SQLite can only change by making them again.
func (g *gen) rebuildTables(ops []Op) map[string]bool {
	rebuild := map[string]bool{}
	if g.d != SQLite {
		return rebuild
	}
	droppedIndexes := map[string]bool{}
	for _, op := range ops {
		if o, ok := op.(DropIndex); ok {
			droppedIndexes[o.Index.Name] = true
		}
	}
	for _, op := range ops {
		switch o := op.(type) {
		case DropForeignKey:
			if _, ok := g.to.table(o.Table); ok {
				rebuild[o.Table] = true
			}
		case AlterColumn, AddForeignKey:
			rebuild[tableOf(o)] = true
		case AddColumn:
			c := o.Column
			if c.PK || c.Auto || (!c.Nullable && c.Default == "") || c.Default == "now" || strings.HasPrefix(c.Default, "expr:") {
				rebuild[o.Table] = true
			}
		case DropColumn:
			if g.columnIsTied(o.Table, o.Column, droppedIndexes) {
				rebuild[o.Table] = true
			}
		}
	}
	return rebuild
}

func tableOf(op Op) string {
	switch o := op.(type) {
	case AlterColumn:
		return o.Table
	case AddForeignKey:
		return o.Table
	case DropForeignKey:
		return o.Table
	}
	return ""
}

// columnIsTied reports whether SQLite refuses to drop a column, because a key, an index or a
// foreign key uses it.
func (g *gen) columnIsTied(table string, c Column, droppedIndexes map[string]bool) bool {
	if c.PK {
		return true
	}
	t, ok := g.base.table(table)
	if !ok {
		return false
	}
	for _, ix := range t.Indexes {
		if slices.Contains(ix.Columns, c.Name) && !droppedIndexes[ix.Name] {
			return true
		}
	}
	for _, fk := range t.ForeignKeys {
		if slices.Contains(fk.Columns, c.Name) {
			return true
		}
	}
	for _, other := range g.base.Tables {
		for _, fk := range other.ForeignKeys {
			if fk.RefTable == table && slices.Contains(fk.RefColumns, c.Name) {
				return true
			}
		}
	}
	return false
}

// rebuild makes a table again, as SQLite needs for most changes: the new table is made and filled,
// the old one is dropped and the new one takes its name. The migration runs with foreign keys off
// (see the sql module), so that the keys that point at the table neither delete rows nor follow
// the temporary name.
func (g *gen) rebuild(table string) error {
	old, ok := g.base.table(table)
	if !ok {
		return fmt.Errorf("schema: cannot rebuild the unknown table %s", table)
	}
	next, ok := g.to.table(table)
	if !ok {
		return fmt.Errorf("schema: cannot rebuild %s, which the new schema does not have", table)
	}
	g.rebuilt = true
	temp := next
	temp.Name = table + "__new"
	g.createTable(temp, next.ForeignKeys)
	var cols []string
	var values []string
	for _, c := range next.Columns {
		if before, ok := old.column(c.Name); ok {
			cols = append(cols, g.quote(c.Name))
			value := g.quote(c.Name)
			if before.Nullable && !c.Nullable && c.Default != "" {
				value = fmt.Sprintf("COALESCE(%s, %s)", value, g.defaultSQL(c))
			}
			values = append(values, value)
		} else if !c.Nullable && c.Default == "" && !c.Auto {
			return fmt.Errorf("%w: the new required column %s.%s needs a default, so that the rows of the table can be copied", ErrUnsupported, table, c.Name)
		}
	}
	g.add("INSERT INTO %s (%s)\nSELECT %s\nFROM %s", g.quote(temp.Name), strings.Join(cols, ", "), strings.Join(values, ", "), g.quote(table))
	g.add("DROP TABLE %s", g.quote(table))
	g.add("ALTER TABLE %s RENAME TO %s", g.quote(temp.Name), g.quote(table))
	for _, ix := range next.Indexes {
		g.createIndex(table, ix)
	}
	return nil
}

func (g *gen) createTable(t Table, fks []ForeignKey) {
	pk := t.pk()
	width := 0
	for _, c := range t.Columns {
		width = max(width, len(g.quote(c.Name)))
	}
	var lines []string
	for _, c := range t.Columns {
		lines = append(lines, g.columnDef(c, len(pk) == 1, width))
	}
	if len(pk) > 1 {
		names := make([]string, len(pk))
		for i, c := range pk {
			names[i] = g.quote(c.Name)
		}
		lines = append(lines, "PRIMARY KEY ("+strings.Join(names, ", ")+")")
	}
	for _, fk := range fks {
		lines = append(lines, g.foreignKeyDef(fk))
	}
	g.add("CREATE TABLE %s (\n  %s\n)", g.quote(t.Name), strings.Join(lines, ",\n  "))
}

func (g *gen) foreignKeyDef(fk ForeignKey) string {
	def := fmt.Sprintf("CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)", g.quote(fk.Name), g.list(fk.Columns), g.quote(fk.RefTable), g.list(fk.RefColumns))
	switch fk.OnDelete {
	case "cascade":
		def += " ON DELETE CASCADE"
	case "restrict":
		def += " ON DELETE RESTRICT"
	case "setnull":
		def += " ON DELETE SET NULL"
	}
	return def
}

func (g *gen) list(names []string) string {
	return strings.Join(mapStrings(names, g.quote), ", ")
}

func (g *gen) createIndex(table string, ix Index) {
	unique := ""
	if ix.Unique {
		unique = "UNIQUE "
	}
	g.add("CREATE %sINDEX %s ON %s (%s)", unique, g.quote(ix.Name), g.quote(table), g.list(ix.Columns))
}

func (g *gen) dropIndex(table string, ix Index) {
	if g.d == MySQL {
		g.add("DROP INDEX %s ON %s", g.quote(ix.Name), g.quote(table))
		return
	}
	g.add("DROP INDEX %s", g.quote(ix.Name))
}

func (g *gen) addForeignKey(table string, fk ForeignKey) {
	g.add("ALTER TABLE %s ADD %s", g.quote(table), g.foreignKeyDef(fk))
}

func (g *gen) dropForeignKey(table string, fk ForeignKey) {
	if g.d == MySQL {
		g.add("ALTER TABLE %s DROP FOREIGN KEY %s", g.quote(table), g.quote(fk.Name))
		return
	}
	g.add("ALTER TABLE %s DROP CONSTRAINT %s", g.quote(table), g.quote(fk.Name))
}

// columnDef writes a column for CREATE TABLE and ADD COLUMN. inlinePK puts PRIMARY KEY on the column.
func (g *gen) columnDef(c Column, inlinePK bool, width int) string {
	name := g.quote(c.Name)
	if width > len(name) {
		name += strings.Repeat(" ", width-len(name))
	}
	parts := []string{name, g.typeSQL(c)}
	if c.Auto && g.d == MySQL {
		parts = append(parts, "AUTO_INCREMENT")
	}
	if c.PK && inlinePK {
		parts = append(parts, "PRIMARY KEY")
		if c.Auto && g.d == SQLite {
			parts = append(parts, "AUTOINCREMENT")
		}
	}
	if !c.PK {
		if c.Nullable {
			if g.d == MySQL && c.Type.Kind == Time && c.Default == "" {
				parts = append(parts, "NULL")
			}
		} else {
			parts = append(parts, "NOT NULL")
		}
	}
	if c.Default != "" {
		parts = append(parts, "DEFAULT "+g.defaultSQL(c))
	}
	return strings.Join(parts, " ")
}

func (g *gen) alterColumn(o AlterColumn) {
	table := g.quote(o.Table)
	if g.d == MySQL {
		g.add("ALTER TABLE %s MODIFY COLUMN %s", table, g.columnDef(o.To, false, 0))
		return
	}
	col := g.quote(o.To.Name)
	if g.typeSQL(o.From) != g.typeSQL(o.To) {
		g.add("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s", table, col, g.typeSQL(o.To), col, g.typeSQL(o.To))
	}
	if o.From.Default != o.To.Default {
		if o.To.Default == "" {
			g.add("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", table, col)
		} else {
			g.add("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s", table, col, g.defaultSQL(o.To))
		}
	}
	if o.From.Nullable != o.To.Nullable {
		if o.To.Nullable {
			g.add("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL", table, col)
		} else {
			g.add("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL", table, col)
		}
	}
}

func (g *gen) typeSQL(c Column) string {
	t := c.Type
	switch g.d {
	case Postgres:
		switch t.Kind {
		case Int16:
			return pick(c.Auto, "SMALLSERIAL", "SMALLINT")
		case Int32:
			return pick(c.Auto, "SERIAL", "INTEGER")
		case Int64:
			return pick(c.Auto, "BIGSERIAL", "BIGINT")
		case Float32:
			return "REAL"
		case Float64:
			return "DOUBLE PRECISION"
		case Bool:
			return "BOOLEAN"
		case String:
			return fmt.Sprintf("VARCHAR(%d)", t.size())
		case Text:
			return "TEXT"
		case Time:
			return "TIMESTAMPTZ"
		case Bytes:
			return "BYTEA"
		case JSON:
			return "JSONB"
		case UUID:
			return "UUID"
		case Decimal:
			return fmt.Sprintf("NUMERIC(%d,%d)", t.Precision, t.Scale)
		case Raw:
			return t.Raw
		}
	case MySQL:
		switch t.Kind {
		case Int16:
			return "SMALLINT"
		case Int32:
			return "INT"
		case Int64:
			return "BIGINT"
		case Float32:
			return "FLOAT"
		case Float64:
			return "DOUBLE"
		case Bool:
			return "BOOLEAN"
		case String:
			return fmt.Sprintf("VARCHAR(%d)", t.size())
		case Text:
			return "LONGTEXT"
		case Time:
			return "DATETIME(6)"
		case Bytes:
			return "LONGBLOB"
		case JSON:
			return "JSON"
		case UUID:
			return "CHAR(36)"
		case Decimal:
			return fmt.Sprintf("DECIMAL(%d,%d)", t.Precision, t.Scale)
		case Raw:
			return t.Raw
		}
	case SQLite:
		switch t.Kind {
		case Int16, Int32, Int64:
			return "INTEGER"
		case Float32, Float64:
			return "REAL"
		case Bool:
			return "BOOLEAN"
		case String, Text, JSON, UUID:
			return "TEXT"
		case Time:
			return "TIMESTAMP"
		case Bytes:
			return "BLOB"
		case Decimal:
			return "NUMERIC"
		case Raw:
			return t.Raw
		}
	}
	return t.Raw
}

func pick(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}

func (g *gen) defaultSQL(c Column) string {
	v := c.Default
	switch {
	case v == "now":
		switch g.d {
		case Postgres:
			return "now()"
		case MySQL:
			return "CURRENT_TIMESTAMP(6)"
		case SQLite:
			return "CURRENT_TIMESTAMP"
		}
		return "CURRENT_TIMESTAMP"
	case v == "true" || v == "false":
		if g.d == Postgres {
			return strings.ToUpper(v)
		}
		return pick(v == "true", "1", "0")
	case strings.HasPrefix(v, "expr:"):
		return "(" + strings.TrimPrefix(v, "expr:") + ")"
	}
	return v
}

// relaxed returns the schema with the columns that the ops add as optional, as the SQL that undoes
// a drop makes them.
func relaxed(s Schema, ops []Op) Schema {
	out := Schema{Version: s.Version, Tables: slices.Clone(s.Tables)}
	for _, op := range ops {
		add, ok := op.(AddColumn)
		if !ok {
			continue
		}
		for i, t := range out.Tables {
			if t.Name != add.Table {
				continue
			}
			columns := slices.Clone(t.Columns)
			for j, c := range columns {
				if c.Name == add.Column.Name {
					columns[j] = add.Column
				}
			}
			out.Tables[i].Columns = columns
		}
	}
	return out
}
