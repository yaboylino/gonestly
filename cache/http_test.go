package cache_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/cache"
	"github.com/yaboylino/gonestly/gonestlytest"
)

type product struct {
	Name string `json:"name"`
	Seen int32  `json:"seen"`
}

type productQuery struct {
	Q string `query:"q"`
}

type routes func(r *gonestly.Router)

func (f routes) Routes(r *gonestly.Router) { f(r) }

type fixture struct {
	app   *gonestlytest.App
	cache *cache.Cache
	calls *atomic.Int32
}

func startCached(t *testing.T, options ...cache.ResponseOption) fixture {
	t.Helper()
	var calls atomic.Int32
	var c *cache.Cache
	list := func(_ context.Context, q productQuery) (product, error) {
		if q.Q == "boom" {
			return product{}, gonestly.NotFound("no such query")
		}
		return product{Name: "list " + q.Q, Seen: calls.Add(1)}, nil
	}
	app := gonestlytest.Start(t, &gonestly.Module{
		Name:    "app",
		Imports: gonestly.Imports(cache.Module(cache.Options{})),
		Providers: gonestly.Providers(func(cc *cache.Cache) *struct{} {
			c = cc
			return &struct{}{}
		}),
		Controllers: gonestly.Controllers(func(cc *cache.Cache) routes {
			return func(r *gonestly.Router) {
				cached := gonestly.WithInterceptors(cache.Responses(cc, time.Minute, options...))
				gonestly.Get(r, "/products", list, cached)
				gonestly.Get(r, "/products/missing", func(context.Context, gonestly.Empty) (product, error) {
					calls.Add(1)
					return product{}, gonestly.NotFound("no such product")
				}, cached)
				gonestly.Get(r, "/products/empty", func(context.Context, gonestly.Empty) (gonestly.Empty, error) {
					calls.Add(1)
					return gonestly.Empty{}, nil
				}, cached, gonestly.Status(http.StatusNoContent))
				gonestly.Post(r, "/products", func(context.Context, gonestly.Empty) (product, error) {
					return product{Name: "made", Seen: calls.Add(1)}, nil
				}, cached)
			}
		}),
	})
	return fixture{app: app, cache: c, calls: &calls}
}

func seen(t *testing.T, res *gonestlytest.Response) int32 {
	t.Helper()
	var p product
	res.JSON(&p)
	return p.Seen
}

func TestResponsesAreCached(t *testing.T) {
	f := startCached(t)
	first := f.app.Get("/products?q=a").ExpectStatus(http.StatusOK)
	second := f.app.Get("/products?q=a").ExpectStatus(http.StatusOK)
	if first.Header.Get("X-Cache") != "MISS" || second.Header.Get("X-Cache") != "HIT" {
		t.Errorf("X-Cache: %q then %q", first.Header.Get("X-Cache"), second.Header.Get("X-Cache"))
	}
	if seen(t, first) != 1 || seen(t, second) != 1 || f.calls.Load() != 1 {
		t.Errorf("the handler ran %d times", f.calls.Load())
	}
	if string(first.Body) != string(second.Body) {
		t.Errorf("bodies differ: %s vs %s", first.Body, second.Body)
	}
	// Another query is another response, and the order of parameters does not matter.
	f.app.Get("/products?q=b").ExpectStatus(http.StatusOK)
	if f.calls.Load() != 2 {
		t.Errorf("the handler ran %d times for two queries", f.calls.Load())
	}
}

func TestEachCredentialHasItsOwnResponse(t *testing.T) {
	f := startCached(t)
	alice := f.app.Get("/products", "Authorization", "Bearer alice")
	bob := f.app.Get("/products", "Authorization", "Bearer bob")
	again := f.app.Get("/products", "Authorization", "Bearer alice")
	if seen(t, alice) != 1 || seen(t, bob) != 2 || seen(t, again) != 1 {
		t.Errorf("responses: %s %s %s", alice.Body, bob.Body, again.Body)
	}
	if again.Header.Get("X-Cache") != "HIT" || bob.Header.Get("X-Cache") != "MISS" {
		t.Errorf("X-Cache: %q %q", again.Header.Get("X-Cache"), bob.Header.Get("X-Cache"))
	}
	cookie := f.app.Get("/products", "Cookie", "session=1")
	if cookie.Header.Get("X-Cache") != "MISS" {
		t.Error("a request with a cookie shared the response of one without")
	}
}

func TestSharedAcrossUsers(t *testing.T) {
	f := startCached(t, cache.SharedAcrossUsers())
	f.app.Get("/products", "Authorization", "Bearer alice")
	res := f.app.Get("/products", "Authorization", "Bearer bob")
	if res.Header.Get("X-Cache") != "HIT" || f.calls.Load() != 1 {
		t.Errorf("X-Cache %q, %d calls", res.Header.Get("X-Cache"), f.calls.Load())
	}
}

func TestVaryAndKeyFunc(t *testing.T) {
	f := startCached(t, cache.SharedAcrossUsers(), cache.Vary("Accept-Language"))
	f.app.Get("/products", "Accept-Language", "nl")
	f.app.Get("/products", "Accept-Language", "en")
	hit := f.app.Get("/products", "Accept-Language", "nl")
	if f.calls.Load() != 2 || hit.Header.Get("X-Cache") != "HIT" {
		t.Errorf("%d calls, X-Cache %q", f.calls.Load(), hit.Header.Get("X-Cache"))
	}

	byTenant := startCached(t, cache.KeyFunc(func(_ context.Context, r *http.Request) string {
		return r.Header.Get("X-Tenant")
	}))
	byTenant.app.Get("/products", "X-Tenant", "acme")
	byTenant.app.Get("/products", "X-Tenant", "globex")
	byTenant.app.Get("/products", "X-Tenant", "acme")
	byTenant.app.Get("/products") // no tenant: not cached
	byTenant.app.Get("/products")
	if byTenant.calls.Load() != 4 {
		t.Errorf("%d calls, want 4 (two tenants, two uncached)", byTenant.calls.Load())
	}
}

func TestWhatIsNotCached(t *testing.T) {
	f := startCached(t)
	for range 2 {
		f.app.Get("/products/missing").ExpectStatus(http.StatusNotFound)
	}
	if f.calls.Load() != 2 {
		t.Errorf("an error was cached: %d calls", f.calls.Load())
	}
	for range 2 {
		f.app.Get("/products/empty").ExpectStatus(http.StatusNoContent)
	}
	if f.calls.Load() != 4 {
		t.Errorf("a response without body was cached: %d calls", f.calls.Load())
	}
	for range 2 {
		f.app.Post("/products", nil).ExpectStatus(http.StatusCreated)
	}
	if f.calls.Load() != 6 {
		t.Errorf("a POST was cached: %d calls", f.calls.Load())
	}
}

func TestInvalidateByPrefix(t *testing.T) {
	f := startCached(t, cache.SharedAcrossUsers())
	f.app.Get("/products?q=a")
	f.app.Get("/products?q=b")
	if err := f.cache.DeletePrefix(t.Context(), cache.ResponsePrefix("/products")); err != nil {
		t.Fatal(err)
	}
	res := f.app.Get("/products?q=a")
	if res.Header.Get("X-Cache") != "MISS" || f.calls.Load() != 3 {
		t.Errorf("X-Cache %q, %d calls", res.Header.Get("X-Cache"), f.calls.Load())
	}
}

func TestSimultaneousMissesRunTheHandlerOnce(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	app := gonestlytest.Start(t, &gonestly.Module{
		Name:    "app",
		Imports: gonestly.Imports(cache.Module(cache.Options{})),
		Controllers: gonestly.Controllers(func(cc *cache.Cache) routes {
			return func(r *gonestly.Router) {
				gonestly.Get(r, "/slow", func(context.Context, gonestly.Empty) (product, error) {
					calls.Add(1)
					<-release
					return product{Name: "slow"}, nil
				}, gonestly.WithInterceptors(cache.Responses(cc, time.Minute)))
			}
		}),
	})
	results := make(chan int, 10)
	for range 10 {
		go func() { results <- app.Get("/slow").Status }()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	for range 10 {
		if status := <-results; status != http.StatusOK {
			t.Errorf("status %d", status)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("the handler ran %d times for 10 simultaneous requests", calls.Load())
	}
}

func TestETag(t *testing.T) {
	app := gonestlytest.Start(t, &gonestly.Module{
		Name: "app",
		Providers: gonestly.Providers(func() gonestly.ServerConfig {
			return gonestly.ServerConfig{Middleware: []gonestly.Middleware{cache.ETag()}}
		}),
		Controllers: gonestly.Controllers(func() routes {
			return func(r *gonestly.Router) {
				gonestly.Get(r, "/thing", func(context.Context, gonestly.Empty) (product, error) { return product{Name: "thing"}, nil })
				gonestly.Get(r, "/own", func(ctx context.Context, _ gonestly.Empty) (product, error) {
					gonestly.ResponseHeader(ctx).Set("ETag", `"mine"`)
					return product{Name: "own"}, nil
				})
				gonestly.Get(r, "/fail", func(context.Context, gonestly.Empty) (product, error) { return product{}, gonestly.NotFound("x") })
				gonestly.Post(r, "/thing", func(context.Context, gonestly.Empty) (product, error) { return product{}, nil })
				gonestly.Handle(r, http.MethodGet, "/stream", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte("one"))
					w.(http.Flusher).Flush()
					_, _ = w.Write([]byte("two"))
				}))
				gonestly.Handle(r, http.MethodGet, "/big", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(strings.Repeat("x", 2<<20)))
				}))
			}
		}),
	})
	first := app.Get("/thing").ExpectStatus(http.StatusOK)
	tag := first.Header.Get("ETag")
	if !strings.HasPrefix(tag, `"`) || len(tag) < 10 {
		t.Fatalf("ETag = %q", tag)
	}
	for name, header := range map[string]string{"the tag": tag, "weak": "W/" + tag, "a list": `"other", ` + tag, "any": "*"} {
		res := app.Get("/thing", "If-None-Match", header)
		if res.Status != http.StatusNotModified || len(res.Body) != 0 || res.Header.Get("ETag") != tag {
			t.Errorf("%s: status %d, body %q, ETag %q", name, res.Status, res.Body, res.Header.Get("ETag"))
		}
	}
	if res := app.Get("/thing", "If-None-Match", `"stale"`); res.Status != http.StatusOK || res.Header.Get("ETag") != tag || len(res.Body) == 0 {
		t.Errorf("a stale tag: status %d", res.Status)
	}
	if again := app.Get("/thing").Header.Get("ETag"); again != tag {
		t.Errorf("the ETag of the same body changed: %q vs %q", again, tag)
	}
	if res := app.Get("/own", "If-None-Match", `"mine"`); res.Status != http.StatusNotModified || res.Header.Get("ETag") != `"mine"` {
		t.Errorf("an own ETag: status %d, %q", res.Status, res.Header.Get("ETag"))
	}
	if res := app.Get("/fail", "If-None-Match", "*"); res.Status != http.StatusNotFound || res.Header.Get("ETag") != "" {
		t.Errorf("an error: status %d, ETag %q", res.Status, res.Header.Get("ETag"))
	}
	if res := app.Post("/thing", nil, "If-None-Match", "*"); res.Header.Get("ETag") != "" {
		t.Errorf("a POST got an ETag")
	}
	if res := app.Get("/stream"); string(res.Body) != "onetwo" || res.Header.Get("ETag") != "" {
		t.Errorf("a stream: %q, ETag %q", res.Body, res.Header.Get("ETag"))
	}
	if res := app.Get("/big"); len(res.Body) != 2<<20 || res.Header.Get("ETag") != "" {
		t.Errorf("a big response: %d bytes, ETag %q", len(res.Body), res.Header.Get("ETag"))
	}
}
