package sql_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/yaboylino/gonestly/sql"
)

type Blogger struct {
	ID    int64      `db:"id"`
	Name  string     `db:"name"`
	Posts []BlogPost `rel:"blogger_id"`
	Bio   *Biography `rel:"blogger_id"`
}

type BlogPost struct {
	ID        int64    `db:"id"`
	BloggerID int64    `db:"blogger_id"`
	Title     string   `db:"title"`
	Author    *Blogger `rel:"blogger_id"`
	Remarks   []Remark `rel:"post_id"`
	Editor    *Blogger `rel:"editor_id"`
	EditorID  *int64   `db:"editor_id"`
}

type Remark struct {
	ID     int64     `db:"id"`
	PostID int64     `db:"post_id"`
	Text   string    `db:"text"`
	Post   *BlogPost `rel:"post_id"`
}

type Biography struct {
	ID        int64    `db:"id"`
	BloggerID int64    `db:"blogger_id"`
	About     string   `db:"about"`
	Blogger   *Blogger `rel:"blogger_id"`
}

var (
	bloggerPosts = sql.Relation[Blogger, BlogPost]("Posts")
	bloggerBio   = sql.Relation[Blogger, Biography]("Bio")
	postAuthor   = sql.Relation[BlogPost, Blogger]("Author")
	postEditor   = sql.Relation[BlogPost, Blogger]("Editor")
	postRemarks  = sql.Relation[BlogPost, Remark]("Remarks")
	remarkPost   = sql.Relation[Remark, BlogPost]("Post")
	bioBlogger   = sql.Relation[Biography, Blogger]("Blogger")
)

func blogSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	exec(t, db,
		"CREATE TABLE bloggers (id "+autoID(db)+", name VARCHAR(100) NOT NULL)",
		"CREATE TABLE blog_posts (id "+autoID(db)+", blogger_id BIGINT NOT NULL, editor_id BIGINT, title VARCHAR(100) NOT NULL)",
		"CREATE TABLE remarks (id "+autoID(db)+", post_id BIGINT NOT NULL, text VARCHAR(100) NOT NULL)",
		"CREATE TABLE biographies (id "+autoID(db)+", blogger_id BIGINT NOT NULL, about VARCHAR(100) NOT NULL)",
		"INSERT INTO bloggers (name) VALUES ('Ada'), ('Grace'), ('Linus')",
		"INSERT INTO blog_posts (blogger_id, editor_id, title) VALUES (1, 2, 'Engines'), (1, NULL, 'Notes'), (2, 1, 'Compilers')",
		"INSERT INTO remarks (post_id, text) VALUES (1, 'great'), (1, 'more'), (3, 'nice')",
		"INSERT INTO biographies (blogger_id, about) VALUES (1, 'Mathematician'), (2, 'Admiral')",
	)
}

// queryCounter counts the queries a database runs through a logger-free wrapper: the log of the
// DB reports every statement at debug level.
func countQueries(t *testing.T, db *sql.DB, fn func()) int {
	t.Helper()
	before := queryCount(t, db)
	fn()
	return queryCount(t, db) - before
}

func TestIncludeLoadsRelations(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		blogSchema(t, db)
		ctx := t.Context()

		bloggers, err := sql.From[Blogger](db).Include(bloggerPosts, bloggerBio).OrderBy(sql.Col[int64]("id").Asc()).All(ctx)
		if err != nil || len(bloggers) != 3 {
			t.Fatalf("bloggers = %+v, %v", bloggers, err)
		}
		titles := func(b Blogger) string {
			var out []string
			for _, p := range b.Posts {
				out = append(out, p.Title)
			}
			return strings.Join(out, ",")
		}
		if titles(bloggers[0]) != "Engines,Notes" || titles(bloggers[1]) != "Compilers" {
			t.Errorf("posts = %q, %q", titles(bloggers[0]), titles(bloggers[1]))
		}
		// A blogger without posts has an empty slice, so that it encodes as [].
		if bloggers[2].Posts == nil || len(bloggers[2].Posts) != 0 {
			t.Errorf("posts of a blogger without any = %#v", bloggers[2].Posts)
		}
		if bloggers[0].Bio == nil || bloggers[0].Bio.About != "Mathematician" || bloggers[2].Bio != nil {
			t.Errorf("bio = %+v, %+v", bloggers[0].Bio, bloggers[2].Bio)
		}
		// A relation that was not asked for stays empty.
		plain, err := sql.From[Blogger](db).Where(sql.Col[int64]("id").Eq(1)).First(ctx)
		if err != nil || plain.Posts != nil || plain.Bio != nil {
			t.Errorf("without Include = %+v, %v", plain, err)
		}

		// Belongs to, with a nullable key.
		posts, err := sql.From[BlogPost](db).Include(postAuthor, postEditor).OrderBy(sql.Col[int64]("id").Asc()).All(ctx)
		if err != nil || len(posts) != 3 {
			t.Fatalf("posts = %+v, %v", posts, err)
		}
		if posts[0].Author == nil || posts[0].Author.Name != "Ada" || posts[2].Author.Name != "Grace" {
			t.Errorf("authors = %+v, %+v", posts[0].Author, posts[2].Author)
		}
		if posts[0].Editor == nil || posts[0].Editor.Name != "Grace" || posts[1].Editor != nil {
			t.Errorf("editors = %+v, %+v", posts[0].Editor, posts[1].Editor)
		}
		// The rows that share a parent get a copy each.
		if posts[0].Author == posts[1].Author {
			t.Error("two posts share one author value")
		}
	})
}

func TestIncludeNestedUsesOneQueryPerLevel(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		blogSchema(t, db)
		ctx := t.Context()

		var bloggers []Blogger
		queries := countQueries(t, db, func() {
			var err error
			bloggers, err = sql.From[Blogger](db).
				Include(bloggerPosts.With(postRemarks.With(remarkPost), postAuthor), bloggerBio.With(bioBlogger)).
				OrderBy(sql.Col[int64]("id").Asc()).All(ctx)
			if err != nil {
				t.Fatal(err)
			}
		})
		// Bloggers, posts, remarks, the posts of the remarks, the authors of the posts, bios and
		// their bloggers: a query each, however many rows there are.
		if queries != 7 {
			t.Errorf("queries = %d, want 7", queries)
		}
		ada := bloggers[0]
		if len(ada.Posts) != 2 || len(ada.Posts[0].Remarks) != 2 || ada.Posts[0].Remarks[0].Text != "great" {
			t.Fatalf("ada = %+v", ada)
		}
		if ada.Posts[0].Remarks[0].Post == nil || ada.Posts[0].Remarks[0].Post.Title != "Engines" {
			t.Errorf("the post of a remark = %+v", ada.Posts[0].Remarks[0].Post)
		}
		if ada.Posts[1].Remarks == nil || len(ada.Posts[1].Remarks) != 0 || ada.Posts[1].Author == nil || ada.Posts[1].Author.Name != "Ada" {
			t.Errorf("second post = %+v", ada.Posts[1])
		}
		if ada.Bio.Blogger == nil || ada.Bio.Blogger.Name != "Ada" {
			t.Errorf("bio = %+v", ada.Bio)
		}
	})
}

func TestLoadAndLoadAll(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		blogSchema(t, db)
		ctx := t.Context()
		bloggers, _ := sql.NewRepository[Blogger, int64](db)

		one, err := bloggers.FindByID(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := bloggers.Load(ctx, &one, bloggerPosts.With(postRemarks)); err != nil || len(one.Posts) != 2 || len(one.Posts[0].Remarks) != 2 {
			t.Errorf("Load = %+v, %v", one, err)
		}
		all, _ := bloggers.FindAll(ctx)
		if err := bloggers.LoadAll(ctx, all, bloggerBio); err != nil {
			t.Fatal(err)
		}
		withBio := 0
		for _, b := range all {
			if b.Bio != nil {
				withBio++
			}
		}
		if withBio != 2 {
			t.Errorf("bloggers with a bio = %d, want 2", withBio)
		}
		if err := bloggers.LoadAll(ctx, nil, bloggerBio); err != nil {
			t.Errorf("LoadAll without rows = %v", err)
		}
	})
}

func TestIncludeWithManyKeys(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		exec(t, db,
			"CREATE TABLE bloggers (id "+autoID(db)+", name VARCHAR(100) NOT NULL)",
			"CREATE TABLE blog_posts (id "+autoID(db)+", blogger_id BIGINT NOT NULL, editor_id BIGINT, title VARCHAR(100) NOT NULL)",
		)
		bloggers, _ := sql.NewRepository[Blogger, int64](db)
		posts, _ := sql.NewRepository[BlogPost, int64](db)
		var batch []Blogger
		for i := range 12000 {
			batch = append(batch, Blogger{Name: "b"})
			_ = i
		}
		if err := bloggers.CreateAll(ctx, batch); err != nil {
			t.Fatal(err)
		}
		if err := posts.CreateAll(ctx, []BlogPost{{BloggerID: 1, Title: "first"}, {BloggerID: 12000, Title: "last"}}); err != nil {
			t.Fatal(err)
		}
		// More keys than fit in one statement are loaded in several.
		loaded, err := sql.From[Blogger](db).Include(bloggerPosts).OrderBy(sql.Col[int64]("id").Asc()).All(ctx)
		if err != nil || len(loaded) != 12000 || len(loaded[0].Posts) != 1 || len(loaded[11999].Posts) != 1 || loaded[11999].Posts[0].Title != "last" {
			t.Errorf("loaded %d bloggers, %v", len(loaded), err)
		}
	})
}

type Topic struct {
	ID       int64   `db:"id"`
	Name     string  `db:"name"`
	ParentID *int64  `db:"parent_id"`
	Parent   *Topic  `rel:"parent_id"`
	Children []Topic `rel:"parent_id"`
}

func TestSelfReferencingRelations(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		ctx := t.Context()
		exec(t, db,
			"CREATE TABLE topics (id "+autoID(db)+", name VARCHAR(100) NOT NULL, parent_id BIGINT)",
			"INSERT INTO topics (name, parent_id) VALUES ('root', NULL), ('child', 1), ('grandchild', 2)",
		)
		parent, children := sql.Relation[Topic, Topic]("Parent"), sql.Relation[Topic, Topic]("Children")
		all, err := sql.From[Topic](db).Include(parent.With(parent), children).OrderBy(sql.Col[int64]("id").Asc()).All(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if all[2].Parent == nil || all[2].Parent.Name != "child" || all[2].Parent.Parent == nil || all[2].Parent.Parent.Name != "root" || all[0].Parent != nil {
			t.Errorf("parents = %+v", all)
		}
		if len(all[0].Children) != 1 || all[0].Children[0].Name != "child" || len(all[2].Children) != 0 {
			t.Errorf("children = %+v", all)
		}
	})
}

type Unlinked struct {
	ID    int64      `db:"id"`
	Posts []BlogPost `rel:"nobody_id"`
	Wrong []BlogPost `rel:"title"`
	Other string
}

func TestRelationMistakes(t *testing.T) {
	db := sql.New(nil, sql.SQLite)
	for name, tt := range map[string]struct {
		call func() error
		want string
	}{
		"a key column that is not there": {func() error { _, err := sql.NewRepository[Unlinked, int64](db); return err }, `Unlinked.Posts uses the key column "nobody_id", but BlogPost has no such column; it has id, blogger_id, title, editor_id`},
		"keys of another kind":           {func() error { _, err := sql.NewRepository[Unlinked, int64](db); return err }, `Unlinked.Wrong joins BlogPost.Title (string) with Unlinked.ID (int64), which are different kinds of keys`},
		"an unknown relation": {func() error {
			_, err := sql.From[Blogger](db).Include(sql.Relation[Blogger, BlogPost]("Postz")).All(t.Context())
			return err
		}, `Blogger has no relation "Postz"; it has Posts, Bio`},
		"no relations at all": {func() error {
			_, err := sql.From[Remark](db).Include(sql.Relation[Remark, BlogPost]("Nothing")).All(t.Context())
			return err
		}, `Remark has no relation "Nothing"`},
	} {
		if err := tt.call(); !errors.Is(err, sql.ErrInvalidModel) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.want)
		}
	}
}

type badRelations struct {
	ID    int64      `db:"id"`
	Empty []BlogPost `rel:""`
	Value Blogger    `rel:"x"`
	Text  string     `rel:"x"`
	Odd   []BlogPost `rel:"blogger_id,through=x"`
}

func TestRelationDeclarations(t *testing.T) {
	_, err := sql.NewRepository[badRelations, int64](sql.New(nil, sql.SQLite))
	if err == nil {
		t.Fatal("the declarations were accepted")
	}
	for _, want := range []string{
		`badRelations.Empty has an empty rel tag`,
		`badRelations.Value is a sql_test.Blogger, which cannot tell a missing row from an empty one; use *sql_test.Blogger`,
		`badRelations.Text has the type string; a relation is a slice of structs`,
		`badRelations.Odd has the unknown rel option "through=x"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error misses %q:\n%v", want, err)
		}
	}
}

func TestCreateNested(t *testing.T) {
	eachDatabase(t, func(t *testing.T, db *sql.DB) {
		exec(t, db,
			"CREATE TABLE bloggers (id "+autoID(db)+", name VARCHAR(100) NOT NULL)",
			"CREATE TABLE blog_posts (id "+autoID(db)+", blogger_id BIGINT NOT NULL, editor_id BIGINT, title VARCHAR(100) NOT NULL UNIQUE)",
			"CREATE TABLE remarks (id "+autoID(db)+", post_id BIGINT NOT NULL, text VARCHAR(100) NOT NULL)",
			"CREATE TABLE biographies (id "+autoID(db)+", blogger_id BIGINT NOT NULL, about VARCHAR(100) NOT NULL)",
		)
		ctx := t.Context()
		bloggers, _ := sql.NewRepository[Blogger, int64](db)
		posts, _ := sql.NewRepository[BlogPost, int64](db)

		ada := Blogger{
			Name: "Ada",
			Bio:  &Biography{About: "Mathematician"},
			Posts: []BlogPost{
				{Title: "Engines", Remarks: []Remark{{Text: "great"}, {Text: "more"}}},
				{Title: "Notes"},
			},
		}
		if err := bloggers.CreateNested(ctx, &ada); err != nil {
			t.Fatal(err)
		}
		// Every row has its generated key, and the rows below carry the key of the row above.
		if ada.ID == 0 || ada.Bio.ID == 0 || ada.Bio.BloggerID != ada.ID || len(ada.Posts) != 2 ||
			ada.Posts[0].ID == 0 || ada.Posts[0].BloggerID != ada.ID || ada.Posts[0].Remarks[1].PostID != ada.Posts[0].ID {
			t.Errorf("ada = %+v", ada)
		}
		loaded, err := sql.From[Blogger](db).Include(bloggerPosts.With(postRemarks), bloggerBio).Where(sql.Col[int64]("id").Eq(ada.ID)).First(ctx)
		if err != nil || len(loaded.Posts) != 2 || len(loaded.Posts[0].Remarks) != 2 || loaded.Bio == nil {
			t.Errorf("stored = %+v, %v", loaded, err)
		}

		// A row that belongs to another one creates it first when it is new, and uses it when it exists.
		fresh := BlogPost{Title: "Compilers", Author: &Blogger{Name: "Grace"}}
		if err := posts.CreateNested(ctx, &fresh); err != nil || fresh.Author.ID == 0 || fresh.BloggerID != fresh.Author.ID {
			t.Errorf("a post with a new author = %+v, %v", fresh, err)
		}
		existing := BlogPost{Title: "Kernels", Author: &ada, Editor: &Blogger{Name: "Linus"}}
		before, _ := bloggers.Count(ctx)
		if err := posts.CreateNested(ctx, &existing); err != nil || existing.BloggerID != ada.ID || existing.EditorID == nil || *existing.EditorID != existing.Editor.ID {
			t.Errorf("a post with an existing author = %+v, %v", existing, err)
		}
		if after, _ := bloggers.Count(ctx); after != before+1 {
			t.Errorf("bloggers went from %d to %d, want one more (the editor)", before, after)
		}

		// One failing row undoes everything, also the rows that were stored before it.
		before, _ = bloggers.Count(ctx)
		failing := Blogger{Name: "Failing", Posts: []BlogPost{{Title: "Unique"}, {Title: "Unique"}}}
		if err := bloggers.CreateNested(ctx, &failing); !errors.Is(err, sql.ErrConflict) || !strings.Contains(err.Error(), "creating Blogger.Posts") {
			t.Errorf("a duplicate = %v", err)
		}
		if after, _ := bloggers.Count(ctx); after != before {
			t.Errorf("the blogger was kept: %d -> %d", before, after)
		}
		if n, _ := posts.Count(ctx, sql.Col[string]("title").Eq("Unique")); n != 0 {
			t.Errorf("a post was kept: %d", n)
		}

		// Rows that point at each other are inserted once.
		cycle := Blogger{Name: "Cycle"}
		post := BlogPost{Title: "Loop", Author: &cycle}
		cycle.Posts = []BlogPost{post}
		cycle.Posts[0].Author = &cycle
		if err := bloggers.CreateNested(ctx, &cycle); err != nil || cycle.Posts[0].BloggerID != cycle.ID {
			t.Errorf("a cycle = %+v, %v", cycle, err)
		}
		if n, _ := posts.Count(ctx, sql.Col[string]("title").Eq("Loop")); n != 1 {
			t.Errorf("the post of a cycle was stored %d times", n)
		}
	})
}
