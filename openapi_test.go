package gonestly_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

type bearerGuard struct{}

func (bearerGuard) Check(ctx context.Context, _ *http.Request) (context.Context, error) {
	return ctx, nil
}

func (bearerGuard) SecurityScheme() (string, gonestly.SecurityScheme) {
	return "bearer", gonestly.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"}
}

type apiKeyGuard struct{}

func (apiKeyGuard) Check(ctx context.Context, _ *http.Request) (context.Context, error) {
	return ctx, nil
}

func (apiKeyGuard) SecurityScheme() (string, gonestly.SecurityScheme) {
	return "apiKey", gonestly.SecurityScheme{Type: "apiKey", In: "header", Name: "X-API-Key"}
}

type Money struct{ Cents int64 }

func (Money) JSONSchema() *gonestly.Schema {
	return &gonestly.Schema{Type: "string", Description: "An amount such as 12.50."}
}

type Base struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}

type Tree struct {
	Name     string `json:"name"`
	Children []Tree `json:"children,omitempty"`
}

type Product struct {
	Base
	Name     string            `json:"name" validate:"required,min=2,max=50" doc:"The product name." example:"Tea"`
	Price    Money             `json:"price"`
	Stock    int               `json:"stock" validate:"gte=0,lt=1000" example:"12"`
	Rating   *float64          `json:"rating"`
	Note     string            `json:"note,omitempty"`
	Kind     string            `json:"kind" validate:"oneof=food drink"`
	Size     int               `json:"size" validate:"oneof=1 2 3"`
	Website  string            `json:"website,omitempty" validate:"omitempty,url"`
	Code     string            `json:"code" validate:"len=4,alphanum"`
	Tags     []string          `json:"tags" validate:"min=1,dive,max=10"`
	Meta     map[string]string `json:"meta"`
	Raw      []byte            `json:"raw"`
	Any      any               `json:"any"`
	Addr     netip.Addr        `json:"addr"`
	TTL      time.Duration     `json:"ttl"`
	Count    int64             `json:"count,string"`
	Category *Tree             `json:"category"`
	Secret   string            `json:"-"`
	internal string            //nolint:unused // shows that unexported fields are not documented
}

type Page[T any] struct {
	Items []T `json:"items"`
}

type CreateProductRequest struct {
	Name  string `json:"name" validate:"required"`
	Price Money  `json:"price"`
}

type UpdateProductRequest struct {
	ID      int64  `path:"id" doc:"The product ID."`
	Version int    `header:"If-Match" validate:"required"`
	Name    string `json:"name"`
}

type SearchRequest struct {
	Query string   `query:"q" validate:"required,min=2" doc:"What to search for."`
	Tags  []string `query:"tag"`
	Limit int      `query:"limit" validate:"omitempty,lte=100"`
}

type productController struct{}

func (productController) Routes(r *gonestly.Router) {
	r.Prefix("/products")
	gonestly.Get(r, "/", productController{}.Search)
	gonestly.Get(r, "/{id}", productController{}.FindOne)
	gonestly.Post(r, "/", productController{}.Create, gonestly.WithGuards(bearerGuard{}, apiKeyGuard{}))
	gonestly.Put(r, "/{id}", productController{}.Update, gonestly.Summary("Replace a product"), gonestly.Description("Replaces **every** field."), gonestly.Tags("catalog"), gonestly.OperationID("replaceProduct"))
	gonestly.Delete(r, "/{id}", productController{}.Remove, gonestly.Deprecated())
	gonestly.Post(r, "/import", func(_ context.Context, items []CreateProductRequest) (int, error) { return len(items), nil }, gonestly.Status(http.StatusAccepted))
	gonestly.Handle(r, http.MethodGet, "/files/{path...}", http.NotFoundHandler())
	gonestly.Get(r, "/tree", func(context.Context, gonestly.Empty) (Tree, error) { return Tree{}, nil })
}

func (productController) Search(context.Context, SearchRequest) (Page[Product], error) {
	return Page[Product]{}, nil
}

func (productController) FindOne(context.Context, struct {
	ID int64 `path:"id"`
},
) (*Product, error) {
	return nil, nil
}

func (productController) Create(context.Context, CreateProductRequest) (Product, error) {
	return Product{}, nil
}

func (productController) Update(context.Context, UpdateProductRequest) (Product, error) {
	return Product{}, nil
}

func (productController) Remove(context.Context, struct {
	ID int64 `path:"id"`
},
) (gonestly.Empty, error) {
	return gonestly.Empty{}, nil
}

type otherController struct{}

func (otherController) Routes(r *gonestly.Router) {
	gonestly.Get(r, "/other/search", productController{}.Search)
}

func openAPIApp(t *testing.T, config *gonestly.OpenAPIConfig) *gonestlytest.App {
	t.Helper()
	providers := gonestly.Providers()
	if config != nil {
		providers = gonestly.Providers(gonestly.Value(*config))
	}
	return gonestlytest.Start(t, &gonestly.Module{
		Name:      "app",
		Providers: providers,
		Imports: gonestly.Imports(
			&gonestly.Module{Name: "products", Controllers: gonestly.Controllers(func() productController { return productController{} })},
			&gonestly.Module{Name: "other", Controllers: gonestly.Controllers(func() otherController { return otherController{} })},
		),
	})
}

// doc decodes the document into generic maps; get walks it with a path of keys.
type doc map[string]any

func (d doc) get(t *testing.T, path ...string) any {
	t.Helper()
	var node any = map[string]any(d)
	for _, key := range path {
		switch n := node.(type) {
		case map[string]any:
			next, ok := n[key]
			if !ok {
				t.Fatalf("no %q at %s", key, strings.Join(path, "."))
			}
			node = next
		case []any:
			var i int
			for j := range key {
				i = i*10 + int(key[j]-'0')
			}
			node = n[i]
		default:
			t.Fatalf("cannot walk into %T at %s", node, key)
		}
	}
	return node
}

func (d doc) json(t *testing.T, path ...string) string {
	t.Helper()
	data, err := json.Marshal(d.get(t, path...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func loadDoc(t *testing.T, app *gonestlytest.App) doc {
	t.Helper()
	var d doc
	if err := json.Unmarshal(app.OpenAPI(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func expectJSON(t *testing.T, d doc, want string, path ...string) {
	t.Helper()
	if got := d.json(t, path...); got != want {
		t.Errorf("%s\n  got  %s\n  want %s", strings.Join(path, "."), got, want)
	}
}

func TestOpenAPIOperations(t *testing.T) {
	d := loadDoc(t, openAPIApp(t, &gonestly.OpenAPIConfig{Title: "Shop", Version: "1.2.0", Description: "The shop API.", Servers: []string{"https://api.example.com"}, Tags: map[string]string{"products": "Everything about products."}}))

	expectJSON(t, d, `"3.1.0"`, "openapi")
	expectJSON(t, d, `{"description":"The shop API.","title":"Shop","version":"1.2.0"}`, "info")
	expectJSON(t, d, `[{"url":"https://api.example.com"}]`, "servers")
	expectJSON(t, d, `[{"description":"Everything about products.","name":"products"},{"name":"catalog"},{"name":"other"}]`, "tags")

	expectJSON(t, d, `"products.search"`, "paths", "/products", "get", "operationId")
	expectJSON(t, d, `"other.search"`, "paths", "/other/search", "get", "operationId")
	expectJSON(t, d, `"Search"`, "paths", "/products", "get", "summary")
	expectJSON(t, d, `[{"description":"What to search for.","in":"query","name":"q","required":true,"schema":{"minLength":2,"type":"string"}},{"in":"query","name":"tag","schema":{"items":{"type":"string"},"type":"array"}},{"in":"query","name":"limit","schema":{"format":"int64","maximum":100,"type":"integer"}}]`, "paths", "/products", "get", "parameters")
	expectJSON(t, d, `{"$ref":"#/components/schemas/PageOfProduct"}`, "paths", "/products", "get", "responses", "200", "content", "application/json", "schema")

	expectJSON(t, d, `[{"in":"path","name":"id","required":true,"schema":{"format":"int64","type":"integer"}}]`, "paths", "/products/{id}", "get", "parameters")
	expectJSON(t, d, `{"anyOf":[{"$ref":"#/components/schemas/Product"},{"type":"null"}]}`, "paths", "/products/{id}", "get", "responses", "200", "content", "application/json", "schema")

	post := []string{"paths", "/products", "post"}
	expectJSON(t, d, `{"$ref":"#/components/schemas/CreateProductRequest"}`, append(post, "requestBody", "content", "application/json", "schema")...)
	expectJSON(t, d, `true`, append(post, "requestBody", "required")...)
	expectJSON(t, d, `"Created"`, append(post, "responses", "201", "description")...)
	expectJSON(t, d, `[{"apiKey":[],"bearer":[]}]`, append(post, "security")...)
	expectJSON(t, d, `{"$ref":"#/components/schemas/Problem"}`, append(post, "responses", "401", "content", "application/problem+json", "schema")...)
	expectJSON(t, d, `[]`, "paths", "/products", "get", "security")

	put := []string{"paths", "/products/{id}", "put"}
	expectJSON(t, d, `"replaceProduct"`, append(put, "operationId")...)
	expectJSON(t, d, `"Replace a product"`, append(put, "summary")...)
	expectJSON(t, d, `"Replaces **every** field."`, append(put, "description")...)
	expectJSON(t, d, `["catalog"]`, append(put, "tags")...)
	expectJSON(t, d, `[{"description":"The product ID.","in":"path","name":"id","required":true,"schema":{"format":"int64","type":"integer"}},{"in":"header","name":"If-Match","required":true,"schema":{"format":"int64","type":"integer"}}]`, append(put, "parameters")...)
	expectJSON(t, d, `{"properties":{"name":{"type":"string"}},"required":["name"],"type":"object"}`, append(put, "requestBody", "content", "application/json", "schema")...)

	expectJSON(t, d, `true`, "paths", "/products/{id}", "delete", "deprecated")
	expectJSON(t, d, `{"description":"No Content"}`, "paths", "/products/{id}", "delete", "responses", "204")
	expectJSON(t, d, `{"items":{"$ref":"#/components/schemas/CreateProductRequest"},"type":"array"}`, "paths", "/products/import", "post", "requestBody", "content", "application/json", "schema")
	expectJSON(t, d, `"Accepted"`, "paths", "/products/import", "post", "responses", "202", "description")
	expectJSON(t, d, `"postProductsImport"`, "paths", "/products/import", "post", "operationId")
	expectJSON(t, d, `{"description":"OK"}`, "paths", "/products/files/{path}", "get", "responses", "200")
	expectJSON(t, d, `"getProductsFilesByPath"`, "paths", "/products/files/{path}", "get", "operationId")
	if _, ok := d.get(t, "paths", "/products/tree", "get", "responses").(map[string]any)["400"]; ok {
		t.Error("a route without input documents a 400 response")
	}
	expectJSON(t, d, `{"bearerFormat":"JWT","scheme":"bearer","type":"http"}`, "components", "securitySchemes", "bearer")
	expectJSON(t, d, `{"in":"header","name":"X-API-Key","type":"apiKey"}`, "components", "securitySchemes", "apiKey")
}

func TestOpenAPISchemas(t *testing.T) {
	d := loadDoc(t, openAPIApp(t, nil))
	product := []string{"components", "schemas", "Product", "properties"}

	for field, want := range map[string]string{
		"id":        `{"format":"int64","type":"integer"}`,
		"createdAt": `{"format":"date-time","type":"string"}`,
		"name":      `{"description":"The product name.","examples":["Tea"],"maxLength":50,"minLength":2,"type":"string"}`,
		"price":     `{"description":"An amount such as 12.50.","type":"string"}`,
		"stock":     `{"examples":[12],"exclusiveMaximum":1000,"format":"int64","minimum":0,"type":"integer"}`,
		"rating":    `{"format":"double","type":["number","null"]}`,
		"kind":      `{"enum":["food","drink"],"type":"string"}`,
		"size":      `{"enum":[1,2,3],"format":"int64","type":"integer"}`,
		"website":   `{"format":"uri","type":"string"}`,
		"code":      `{"maxLength":4,"minLength":4,"pattern":"^[a-zA-Z0-9]*$","type":"string"}`,
		"tags":      `{"items":{"type":"string"},"minItems":1,"type":"array"}`,
		"meta":      `{"additionalProperties":{"type":"string"},"type":"object"}`,
		"raw":       `{"format":"byte","type":"string"}`,
		"any":       `{}`,
		"addr":      `{"type":"string"}`,
		"ttl":       `{"description":"A duration in nanoseconds.","format":"int64","type":"integer"}`,
		"count":     `{"type":"string"}`,
		"category":  `{"anyOf":[{"$ref":"#/components/schemas/Tree"},{"type":"null"}]}`,
	} {
		expectJSON(t, d, want, append(product, field)...)
	}
	properties := d.get(t, product...).(map[string]any)
	for _, hidden := range []string{"Secret", "internal", "Base"} {
		if _, ok := properties[hidden]; ok {
			t.Errorf("the property %q should not be documented", hidden)
		}
	}
	expectJSON(t, d, `["id","createdAt","name","price","stock","kind","size","code","tags","meta","raw","any","addr","ttl","count"]`, "components", "schemas", "Product", "required")

	raw := string(gonestlytest.Start(t, &gonestly.Module{Name: "app", Controllers: gonestly.Controllers(func() productController { return productController{} })}).OpenAPI())
	if !strings.Contains(raw, `"properties":{"id":{`) || strings.Index(raw, `"createdAt"`) > strings.Index(raw, `"Tea"`) {
		t.Error("properties are not in the order of the struct fields")
	}
	expectJSON(t, d, `{"properties":{"children":{"items":{"$ref":"#/components/schemas/Tree"},"type":"array"},"name":{"type":"string"}},"required":["name"],"type":"object"}`, "components", "schemas", "Tree")
	expectJSON(t, d, `{"properties":{"items":{"items":{"$ref":"#/components/schemas/Product"},"type":"array"}},"required":["items"],"type":"object"}`, "components", "schemas", "PageOfProduct")
	expectJSON(t, d, `["type","title","status"]`, "components", "schemas", "Problem", "required")
}

func TestOpenAPIServing(t *testing.T) {
	app := openAPIApp(t, nil)
	res := app.Get("/openapi.json").ExpectStatus(http.StatusOK)
	if string(res.Body) != string(app.OpenAPI()) || res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Error("the served document differs from App.OpenAPI")
	}
	html := string(app.Get("/docs").ExpectStatus(http.StatusOK).Body)
	for _, part := range []string{`swagger-ui-dist@5.34.1/swagger-ui-bundle.js`, `integrity="sha384-`, `url: "openapi.json"`, `<title>API</title>`} {
		if !strings.Contains(html, part) {
			t.Errorf("Swagger UI does not contain %q", part)
		}
	}

	custom := openAPIApp(t, &gonestly.OpenAPIConfig{Title: "<Shop>", JSONPath: "/api/spec.json", DocsPath: "/reference/docs"})
	custom.Get("/api/spec.json").ExpectStatus(http.StatusOK)
	html = string(custom.Get("/reference/docs").ExpectStatus(http.StatusOK).Body)
	if !strings.Contains(html, `url: "../api/spec.json"`) || !strings.Contains(html, "<title>&lt;Shop&gt;</title>") {
		t.Errorf("custom docs page: %s", html)
	}
	custom.Get("/openapi.json").ExpectStatus(http.StatusNotFound)

	disabled := openAPIApp(t, &gonestly.OpenAPIConfig{Disabled: true})
	disabled.Get("/openapi.json").ExpectStatus(http.StatusNotFound)
	disabled.Get("/docs").ExpectStatus(http.StatusNotFound)
	if len(disabled.OpenAPI()) == 0 {
		t.Error("App.OpenAPI is empty when serving is disabled")
	}
}

func TestOpenAPIRouteConflict(t *testing.T) {
	err := startError(t, func(r *gonestly.Router) {
		gonestly.Get(r, "/docs", func(context.Context, gonestly.Empty) (string, error) { return "", nil })
	})
	assertContains(t, err, "GET /docs in", "the path is used for the OpenAPI documentation")
}
