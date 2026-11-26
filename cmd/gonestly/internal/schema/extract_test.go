package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, source string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}

func extract(t *testing.T, files map[string]string) (Schema, error) {
	t.Helper()
	dir := t.TempDir()
	for name, source := range files {
		write(t, dir, name, source)
	}
	return Extract(dir, "example.com/shop")
}

func TestExtractTypesAndTags(t *testing.T) {
	s, err := extract(t, map[string]string{"model.go": `package shop

import (
	"database/sql"
	"encoding/json"
	"time"
)

type Status string

type Item struct {
	ID        int64           ` + "`db:\"id\"`" + `
	Name      string          ` + "`db:\"name\" schema:\"size=120,unique\"`" + `
	Body      string          ` + "`db:\"body\" schema:\"type=text\"`" + `
	Note      *string         ` + "`db:\"note\"`" + `
	Count     int32           ` + "`db:\"count\" schema:\"default=0\"`" + `
	Small     int8            ` + "`db:\"small\"`" + `
	Big       uint64          ` + "`db:\"big\"`" + `
	Ratio     float32
	Price     float64         ` + "`db:\"price\"`" + `
	Active    bool            ` + "`db:\"active\" schema:\"default=true\"`" + `
	Created   time.Time       ` + "`db:\"created_at,default\"`" + `
	Seen      sql.NullTime    ` + "`db:\"seen\"`" + `
	Label     sql.NullString  ` + "`db:\"label\"`" + `
	Data      []byte          ` + "`db:\"data\"`" + `
	Meta      json.RawMessage ` + "`db:\"meta\"`" + `
	Tags      []string        ` + "`db:\"tags\"`" + `
	State     Status          ` + "`db:\"state\" schema:\"index\"`" + `
	Amount    string          ` + "`db:\"amount\" schema:\"type=decimal(10,2)\"`" + `
	Ref       string          ` + "`db:\"ref\" schema:\"type=uuid\"`" + `
	Weird     string          ` + "`db:\"weird\" schema:\"type=CITEXT\"`" + `
	Skipped   string          ` + "`db:\"-\"`" + `
	hidden    string
}
`})
	if err != nil {
		t.Fatal(err)
	}
	items, ok := s.table("items")
	if !ok || len(s.Tables) != 1 {
		t.Fatalf("tables = %+v", s.Tables)
	}
	want := map[string]struct {
		typ      Type
		nullable bool
		def      string
	}{
		"id":         {Type{Kind: Int64}, false, ""},
		"name":       {Type{Kind: String, Size: 120}, false, ""},
		"body":       {Type{Kind: String}, false, ""},
		"note":       {Type{Kind: String}, true, ""},
		"count":      {Type{Kind: Int32}, false, "0"},
		"small":      {Type{Kind: Int16}, false, ""},
		"big":        {Type{Kind: Int64}, false, ""},
		"ratio":      {Type{Kind: Float32}, false, ""},
		"price":      {Type{Kind: Float64}, false, ""},
		"active":     {Type{Kind: Bool}, false, "true"},
		"created_at": {Type{Kind: Time}, false, "now"},
		"seen":       {Type{Kind: Time}, true, ""},
		"label":      {Type{Kind: String}, true, ""},
		"data":       {Type{Kind: Bytes}, true, ""},
		"meta":       {Type{Kind: JSON}, true, ""},
		"tags":       {Type{Kind: JSON}, true, ""},
		"state":      {Type{Kind: String}, false, ""},
		"amount":     {Type{Kind: Decimal, Precision: 10, Scale: 2}, false, ""},
		"ref":        {Type{Kind: UUID}, false, ""},
		"weird":      {Type{Kind: Raw, Raw: "CITEXT"}, false, ""},
	}
	want["body"] = struct {
		typ      Type
		nullable bool
		def      string
	}{Type{Kind: Text}, false, ""}
	if len(items.Columns) != len(want) {
		t.Errorf("%d columns, want %d: %+v", len(items.Columns), len(want), items.Columns)
	}
	for _, c := range items.Columns {
		w, ok := want[c.Name]
		if !ok {
			t.Errorf("unexpected column %s", c.Name)
			continue
		}
		if c.Type != w.typ || c.Nullable != w.nullable || c.Default != w.def {
			t.Errorf("%s = %+v, want %+v nullable=%v default=%q", c.Name, c.Type, w.typ, w.nullable, w.def)
		}
	}
	if id, _ := items.column("id"); !id.PK || !id.Auto {
		t.Errorf("id = %+v", id)
	}
	names := []string{}
	for _, ix := range items.Indexes {
		names = append(names, ix.Name)
	}
	if strings.Join(names, ",") != "items_name_key,items_state_idx" {
		t.Errorf("indexes = %v", names)
	}
}

func TestExtractEmbeddedTablesAndKeys(t *testing.T) {
	s, err := extract(t, map[string]string{
		"model.go": `package shop

import "time"

type Base struct {
	ID        int64     ` + "`db:\"id\"`" + `
	CreatedAt time.Time ` + "`db:\"created_at,default\"`" + `
}

type Person struct {
	Base
	Name string ` + "`db:\"name\"`" + `
}

func (Person) TableName() string { return "people" }

type Slug struct {
	Code string ` + "`db:\"code,pk\"`" + `
	Hits int64  ` + "`db:\"hits\"`" + `
}

type Membership struct {
	TeamID   int64 ` + "`db:\"team_id,pk\"`" + `
	PersonID int64 ` + "`db:\"person_id,pk\"`" + `
}
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	people, ok := s.table("people")
	if !ok {
		t.Fatalf("tables = %v", s.Tables)
	}
	if len(people.Columns) != 3 || people.Columns[0].Name != "id" || people.Columns[2].Name != "name" {
		t.Errorf("an embedded struct was not expanded in place: %+v", people.Columns)
	}
	if _, ok := s.table("bases"); ok {
		t.Error("an embedded struct became a table")
	}
	slugs, _ := s.table("slugs")
	if code, _ := slugs.column("code"); !code.PK || code.Auto {
		t.Errorf("a string key = %+v", code)
	}
	members, _ := s.table("memberships")
	if len(members.pk()) != 2 || members.pk()[0].Auto {
		t.Errorf("a composite key = %+v", members.pk())
	}
}

func TestExtractRelations(t *testing.T) {
	s, err := extract(t, map[string]string{
		"people/model.go": `package people

type Person struct {
	ID    int64  ` + "`db:\"id\"`" + `
	Name  string ` + "`db:\"name\"`" + `
	Boss  *Person ` + "`rel:\"boss_id,belongs\"`" + `
	BossID *int64 ` + "`db:\"boss_id\"`" + `
}
`,
		"blog/model.go": `package blog

import "example.com/shop/people"

type Post struct {
	ID       int64          ` + "`db:\"id\"`" + `
	AuthorID int64          ` + "`db:\"author_id\"`" + `
	EditorID *int64         ` + "`db:\"editor_id\"`" + `
	Author   *people.Person ` + "`rel:\"author_id\" schema:\"onDelete=cascade\"`" + `
	Editor   *people.Person ` + "`rel:\"editor_id\" schema:\"onDelete=setnull\"`" + `
	Remarks  []Remark       ` + "`rel:\"post_id\"`" + `
}

type Remark struct {
	ID     int64 ` + "`db:\"id\"`" + `
	PostID int64 ` + "`db:\"post_id\"`" + `
}
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	posts, _ := s.table("posts")
	got := map[string]ForeignKey{}
	for _, fk := range posts.ForeignKeys {
		got[fk.Columns[0]] = fk
	}
	if fk := got["author_id"]; fk.RefTable != "persons" || fk.OnDelete != "cascade" || fk.RefColumns[0] != "id" || fk.Name != "posts_author_id_fkey" {
		t.Errorf("author key = %+v", fk)
	}
	if fk := got["editor_id"]; fk.OnDelete != "setnull" {
		t.Errorf("editor key = %+v", fk)
	}
	remarks, _ := s.table("remarks")
	if len(remarks.ForeignKeys) != 1 || remarks.ForeignKeys[0].RefTable != "posts" {
		t.Errorf("a has many relation did not give the key to the table with the column: %+v", remarks.ForeignKeys)
	}
	people, _ := s.table("persons")
	if len(people.ForeignKeys) != 1 || people.ForeignKeys[0].RefTable != "persons" {
		t.Errorf("a self reference = %+v", people.ForeignKeys)
	}
}

func TestExtractErrors(t *testing.T) {
	model := func(body string) map[string]string {
		return map[string]string{"model.go": "package shop\n\nimport \"time\"\n\nvar _ = time.Now\n\n" + body}
	}
	for name, tt := range map[string]struct {
		files map[string]string
		want  string
	}{
		"an unknown type":               {model("type A struct {\n\tID int64 `db:\"id\"`\n\tX  chan int `db:\"x\"`\n}\n"), "unsupported type"},
		"a struct field":                {model("type B struct{ N int }\ntype A struct {\n\tID int64 `db:\"id\"`\n\tX  B `db:\"x\"`\n}\n"), "give the column a type"},
		"an unknown option":             {model("type A struct {\n\tID int64 `db:\"id\"`\n\tX  string `db:\"x\" schema:\"huge\"`\n}\n"), `unknown schema option "huge"`},
		"a bad size":                    {model("type A struct {\n\tID int64 `db:\"id\"`\n\tX  string `db:\"x\" schema:\"size=-3\"`\n}\n"), "not a positive number"},
		"a size on a number":            {model("type A struct {\n\tID int64 `db:\"id\"`\n\tX  int `db:\"x\" schema:\"size=3\"`\n}\n"), "only a string column takes"},
		"a bad default":                 {model("type A struct {\n\tID int64 `db:\"id\"`\n\tX  string `db:\"x\" schema:\"default=oops\"`\n}\n"), "has the default"},
		"two fields one column":         {model("type A struct {\n\tID int64 `db:\"id\"`\n\tX  string `db:\"x\"`\n\tY  string `db:\"x\"`\n}\n"), "two fields map to the column"},
		"two models one table":          {model("type A struct {\n\tID int64 `db:\"id\"`\n}\nfunc (A) TableName() string { return \"things\" }\ntype B struct {\n\tID int64 `db:\"id\"`\n}\nfunc (B) TableName() string { return \"things\" }\n"), "both use the table"},
		"a computed table name":         {model("type A struct {\n\tID int64 `db:\"id\"`\n}\nfunc (A) TableName() string { return prefix + \"a\" }\nvar prefix = \"x\"\n"), "must return a string literal"},
		"a missing key column":          {model("type A struct {\n\tID int64 `db:\"id\"`\n\tBs []B `rel:\"a_id\"`\n}\ntype B struct {\n\tID int64 `db:\"id\"`\n}\n"), "has no such column"},
		"an ambiguous relation":         {model("type A struct {\n\tID int64 `db:\"id\"`\n\tB *B `rel:\"id\"`\n}\ntype B struct {\n\tID int64 `db:\"id\"`\n}\n"), "ambiguous"},
		"set null on a required column": {model("type A struct {\n\tID int64 `db:\"id\"`\n\tBID int64 `db:\"b_id\"`\n\tB *B `rel:\"b_id\" schema:\"onDelete=setnull\"`\n}\ntype B struct {\n\tID int64 `db:\"id\"`\n}\n"), "make the field a pointer"},
		"a bad onDelete":                {model("type A struct {\n\tID int64 `db:\"id\"`\n\tBID int64 `db:\"b_id\"`\n\tB *B `rel:\"b_id\" schema:\"onDelete=explode\"`\n}\ntype B struct {\n\tID int64 `db:\"id\"`\n}\n"), "has the onDelete"},
		"onDelete on a column":          {model("type A struct {\n\tID int64 `db:\"id\"`\n\tX string `db:\"x\" schema:\"onDelete=cascade\"`\n}\n"), "belongs on a relation field"},
	} {
		_, err := extract(t, tt.files)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.want)
		}
	}
}

func TestExtractIgnoresWhatIsNotAModel(t *testing.T) {
	s, err := extract(t, map[string]string{
		"model.go":              "package shop\n\ntype Plain struct{ Name string }\n\ntype Model struct {\n\tID int64 `db:\"id\"`\n}\n",
		"model_test.go":         "package shop\n\ntype Fixture struct {\n\tID int64 `db:\"id\"`\n}\n",
		"internal/.hidden/x.go": "package hidden\n\ntype H struct {\n\tID int64 `db:\"id\"`\n}\n",
		"vendor/x/x.go":         "package x\n\ntype V struct {\n\tID int64 `db:\"id\"`\n}\n",
		"columns_gen.go":        "package shop\n\nvar ModelColumns = struct{ ID int }{}\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tables) != 1 || s.Tables[0].Name != "models" {
		t.Errorf("tables = %+v", s.Tables)
	}
}

func TestCompositeIndexesAndUniques(t *testing.T) {
	s, err := extract(t, map[string]string{"model.go": "package shop\n\ntype A struct {\n\tID int64 `db:\"id\"`\n\tTenant int64 `db:\"tenant\" schema:\"unique=a_tenant_slug\"`\n\tSlug string `db:\"slug\" schema:\"unique=a_tenant_slug\"`\n\tCity string `db:\"city\" schema:\"index=a_place\"`\n\tZip string `db:\"zip\" schema:\"index=a_place\"`\n}\n"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.table("as")
	if len(a.Indexes) != 2 {
		t.Fatalf("indexes = %+v", a.Indexes)
	}
	if ix, _ := index(a, "a_tenant_slug"); !ix.Unique || strings.Join(ix.Columns, ",") != "tenant,slug" {
		t.Errorf("unique = %+v", ix)
	}
	if ix, _ := index(a, "a_place"); ix.Unique || strings.Join(ix.Columns, ",") != "city,zip" {
		t.Errorf("index = %+v", ix)
	}
}
