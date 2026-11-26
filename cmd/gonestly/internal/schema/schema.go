// Package schema reads the models of a project, compares them with the schema that the
// migrations made, and writes the SQL that closes the difference for PostgreSQL, MySQL or SQLite.
package schema

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Kind is the kind of a column, independent of the database.
type Kind string

// The kinds of columns.
const (
	Int16   Kind = "int16"
	Int32   Kind = "int32"
	Int64   Kind = "int64"
	Float32 Kind = "float32"
	Float64 Kind = "float64"
	Bool    Kind = "bool"
	String  Kind = "string"
	Text    Kind = "text"
	Time    Kind = "time"
	Bytes   Kind = "bytes"
	JSON    Kind = "json"
	UUID    Kind = "uuid"
	Decimal Kind = "decimal"
	// Raw is a type written in SQL by the author of the model, passed on as it is.
	Raw Kind = "raw"
)

// Type is the type of a column.
type Type struct {
	Kind      Kind   `json:"kind"`
	Size      int    `json:"size,omitempty"`
	Precision int    `json:"precision,omitempty"`
	Scale     int    `json:"scale,omitempty"`
	Raw       string `json:"raw,omitempty"`
}

// DefaultStringSize is the length of a string column that has no size.
const DefaultStringSize = 255

func (t Type) String() string {
	switch t.Kind {
	case String:
		return fmt.Sprintf("string(%d)", t.size())
	case Decimal:
		return fmt.Sprintf("decimal(%d,%d)", t.Precision, t.Scale)
	case Raw:
		return t.Raw
	default:
		return string(t.Kind)
	}
}

func (t Type) size() int {
	if t.Size == 0 {
		return DefaultStringSize
	}
	return t.Size
}

// Column is one column of a table.
type Column struct {
	Name     string `json:"name"`
	Type     Type   `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`
	// Default is the value of the column when an insert leaves it out: "now", "true", "false", a
	// number, a string in single quotes, or "expr:" followed by SQL.
	Default string `json:"default,omitempty"`
	PK      bool   `json:"pk,omitempty"`
	// Auto makes the database generate the number of an integer key.
	Auto bool `json:"auto,omitempty"`
}

// Index is an index on columns, unique or not.
type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
}

// ForeignKey makes the columns of a table refer to the key of another.
type ForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	// OnDelete is "", "cascade", "restrict" or "setnull"; empty leaves it to the database.
	OnDelete string `json:"onDelete,omitempty"`
}

// Table is a table with its columns, indexes and foreign keys.
type Table struct {
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	Indexes     []Index      `json:"indexes,omitempty"`
	ForeignKeys []ForeignKey `json:"foreignKeys,omitempty"`
}

// Schema is the tables of a database.
type Schema struct {
	Version int     `json:"version"`
	Tables  []Table `json:"tables"`
}

// snapshotVersion is the format of the snapshot file.
const snapshotVersion = 1

func (s Schema) table(name string) (Table, bool) {
	for _, t := range s.Tables {
		if t.Name == name {
			return t, true
		}
	}
	return Table{}, false
}

func (t Table) column(name string) (Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

func (t Table) pk() []Column {
	var pk []Column
	for _, c := range t.Columns {
		if c.PK {
			pk = append(pk, c)
		}
	}
	return pk
}

// normalize sorts what has no meaningful order, so that equal schemas encode equally.
func (s *Schema) normalize() {
	s.Version = snapshotVersion
	slices.SortFunc(s.Tables, func(a, b Table) int { return cmp.Compare(a.Name, b.Name) })
	for i := range s.Tables {
		t := &s.Tables[i]
		slices.SortFunc(t.Indexes, func(a, b Index) int { return cmp.Compare(a.Name, b.Name) })
		slices.SortFunc(t.ForeignKeys, func(a, b ForeignKey) int { return cmp.Compare(a.Name, b.Name) })
	}
}

// Marshal encodes a schema as the text of the snapshot file.
func Marshal(s Schema) ([]byte, error) {
	s.normalize()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Unmarshal reads the text of a snapshot file.
func Unmarshal(data []byte) (Schema, error) {
	var s Schema
	if err := json.Unmarshal(data, &s); err != nil {
		return Schema{}, fmt.Errorf("the schema snapshot is not valid: %w", err)
	}
	if s.Version != snapshotVersion {
		return Schema{}, fmt.Errorf("the schema snapshot has version %d, and this gonestly reads version %d", s.Version, snapshotVersion)
	}
	return s, nil
}

// constraintName builds a name for an index or a key that fits the 63 characters of PostgreSQL.
func constraintName(table string, columns []string, suffix string) string {
	name := table + "_" + strings.Join(columns, "_") + "_" + suffix
	if len(name) <= 63 {
		return name
	}
	sum := 0
	for _, r := range name {
		sum = (sum*31 + int(r)) % 1_000_003
	}
	return fmt.Sprintf("%s_%06d_%s", name[:63-len(suffix)-8], sum, suffix)
}
