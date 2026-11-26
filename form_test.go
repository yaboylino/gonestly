package gonestly_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/gonestlytest"
)

type signupRequest struct {
	Source string   `query:"source"`
	Name   string   `form:"name"`
	Age    *int     `form:"age"`
	Tags   []string `form:"tag"`
}

type uploadRequest struct {
	ID      int64           `path:"id"`
	Title   string          `form:"title" doc:"The title of the photo."`
	Photo   gonestly.File   `form:"photo" doc:"The photo itself."`
	Thumb   *gonestly.File  `form:"thumb"`
	Extras  []gonestly.File `form:"extra"`
	Ignored string          `json:"-"`
}

type uploadResponse struct {
	Title   string   `json:"title"`
	Photo   string   `json:"photo"`
	Content string   `json:"content"`
	Type    string   `json:"type"`
	Size    int64    `json:"size"`
	Thumb   bool     `json:"thumb"`
	Extras  []string `json:"extras"`
}

type formController struct{}

func (formController) Routes(r *gonestly.Router) {
	gonestly.Post(r, "/signup", func(_ context.Context, req signupRequest) (signupRequest, error) { return req, nil })
	gonestly.Post(r, "/albums/{id}/photos", func(_ context.Context, req uploadRequest) (uploadResponse, error) {
		file, err := req.Photo.Open()
		if err != nil {
			return uploadResponse{}, err
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		res := uploadResponse{Title: req.Title, Photo: req.Photo.Name, Content: string(content), Type: req.Photo.ContentType, Size: req.Photo.Size, Thumb: req.Thumb != nil}
		for _, extra := range req.Extras {
			res.Extras = append(res.Extras, extra.Name)
		}
		return res, err
	}, gonestly.MaxBodyBytes(4096))
}

func formModule() *gonestly.Module {
	return &gonestly.Module{
		Name:        "forms",
		Providers:   gonestly.Providers(gonestly.Value(gonestly.ServerConfig{MaxBodyBytes: 512})),
		Controllers: gonestly.Controllers(func() formController { return formController{} }),
	}
}

func TestFormBinding(t *testing.T) {
	app := gonestlytest.Start(t, formModule())

	var got signupRequest
	values := url.Values{"name": {"Ada"}, "age": {"36"}, "tag": {"a", "b"}, "source": {"body"}}
	app.Post("/signup?source=query&name=Query", values).ExpectStatus(http.StatusCreated).JSON(&got)
	if got.Name != "Ada" || got.Age == nil || *got.Age != 36 || strings.Join(got.Tags, ",") != "a,b" || got.Source != "query" {
		t.Errorf("urlencoded = %+v", got)
	}
	// A multipart form without files is a form too, and an absent value stays empty.
	got = signupRequest{}
	app.Post("/signup", gonestlytest.Form{Values: url.Values{"name": {"Grace"}}}).ExpectStatus(http.StatusCreated).JSON(&got)
	if got.Name != "Grace" || got.Age != nil || got.Tags != nil {
		t.Errorf("multipart = %+v", got)
	}
	// A value in the query never stands in for a form value.
	got = signupRequest{}
	app.Post("/signup?name=Query", nil).ExpectStatus(http.StatusCreated).JSON(&got)
	if got.Name != "" {
		t.Errorf("without a body = %+v", got)
	}

	problem := app.Post("/signup", url.Values{"age": {"old"}}).Problem()
	if problem.Status != http.StatusBadRequest || len(problem.Errors) != 1 || problem.Errors[0].Field != "age" || problem.Errors[0].In != "form" {
		t.Errorf("an invalid value = %+v", problem)
	}
	if problem := app.Post("/signup", map[string]string{"name": "Ada"}).Problem(); problem.Status != http.StatusUnsupportedMediaType || !strings.Contains(problem.Detail, "application/x-www-form-urlencoded or multipart/form-data") {
		t.Errorf("JSON for a form = %+v", problem)
	}
	if problem := app.Post("/signup", "%zz", "Content-Type", "application/x-www-form-urlencoded").Problem(); problem.Status != http.StatusBadRequest || problem.Detail != "the request body is not a valid form" {
		t.Errorf("a broken form = %+v", problem)
	}
	if problem := app.Post("/signup", url.Values{"name": {strings.Repeat("x", 600)}}).Problem(); problem.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("a form over the limit of the server = %+v", problem)
	}
}

func TestFileUpload(t *testing.T) {
	app := gonestlytest.Start(t, formModule())

	// The route allows more than the 512 bytes of the server.
	content := strings.Repeat("pixel", 200)
	var got uploadResponse
	app.Post("/albums/7/photos", gonestlytest.Form{
		Values: url.Values{"title": {"Sunset"}},
		Files: []gonestlytest.FormFile{
			{Field: "photo", Name: "../../sunset.jpg", Content: []byte(content)},
			{Field: "extra", Name: "a.txt", Content: []byte("a")},
			{Field: "extra", Name: "b.txt", Content: []byte("b")},
		},
	}).ExpectStatus(http.StatusCreated).JSON(&got)
	if got.Title != "Sunset" || got.Photo != "sunset.jpg" || got.Content != content || got.Size != int64(len(content)) ||
		got.Type != "application/octet-stream" || got.Thumb || strings.Join(got.Extras, ",") != "a.txt,b.txt" {
		t.Errorf("upload = %+v", got)
	}

	problem := app.Post("/albums/7/photos", gonestlytest.Form{Values: url.Values{"title": {"No photo"}}}).Problem()
	if problem.Status != http.StatusBadRequest || len(problem.Errors) != 1 || problem.Errors[0].Field != "photo" || problem.Errors[0].Message != "is required" {
		t.Errorf("a missing file = %+v", problem)
	}
	if problem := app.Post("/albums/7/photos", nil).Problem(); problem.Status != http.StatusBadRequest || problem.Errors[0].Field != "photo" {
		t.Errorf("no body = %+v", problem)
	}
	if problem := app.Post("/albums/7/photos", url.Values{"title": {"x"}}).Problem(); problem.Status != http.StatusUnsupportedMediaType || !strings.HasSuffix(problem.Detail, "as multipart/form-data") {
		t.Errorf("a URL encoded form for a file = %+v", problem)
	}
	large := gonestlytest.Form{Files: []gonestlytest.FormFile{{Field: "photo", Name: "big.bin", Content: make([]byte, 5000)}}}
	if problem := app.Post("/albums/7/photos", large).Problem(); problem.Status != http.StatusRequestEntityTooLarge || !strings.Contains(problem.Detail, "4096") {
		t.Errorf("a file over the limit of the route = %+v", problem)
	}

	if _, err := (gonestly.File{}).Open(); err == nil {
		t.Error("a File that was not uploaded opened")
	}
}

type mixedFormRequest struct {
	Name  string   `form:"name"`
	Email string   `json:"email"`
	Both  string   `form:"both" query:"both"`
	Bad   struct{} `form:"bad"`
}

func TestFormMistakes(t *testing.T) {
	root := &gonestly.Module{Name: "app", Controllers: gonestly.Controllers(func() mixedController { return mixedController{} })}
	app, err := gonestly.New(root, quietLogger())
	if err == nil {
		err = app.Start(t.Context())
		_ = app.Stop(t.Context())
	}
	if err == nil {
		t.Fatal("the mistakes were accepted")
	}
	for _, want := range []string{
		`mixedFormRequest reads a form, so its body cannot be JSON too`,
		`mixedFormRequest.Both has more than one of the tags query, form`,
		`mixedFormRequest.Bad has type struct {}, which cannot be read from the form`,
		`the body limit -1 is negative`,
	} {
		assertContains(t, err, want)
	}
}

type mixedController struct{}

func (mixedController) Routes(r *gonestly.Router) {
	gonestly.Post(r, "/mixed", func(context.Context, mixedFormRequest) (gonestly.Empty, error) { return gonestly.Empty{}, nil }, gonestly.MaxBodyBytes(-1))
}

func TestFormOpenAPI(t *testing.T) {
	app := gonestlytest.Start(t, formModule())
	document := string(app.Get("/openapi.json").ExpectStatus(http.StatusOK).Body)
	for _, want := range []string{
		`"multipart/form-data":{"schema":{"type":"object","properties":{"title":{"type":"string","description":"The title of the photo."},"photo":{"type":"string","format":"binary","description":"The photo itself."},"thumb":{"type":"string","format":"binary"},"extra":{"type":"array","items":{"type":"string","format":"binary"}}},"required":["photo"]}}`,
		`"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"`,
		`"parameters":[{"name":"id","in":"path"`,
		`"parameters":[{"name":"source","in":"query"`,
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the document misses %s", want)
		}
	}
	if strings.Contains(document, `"in":"form"`) {
		t.Error("a form field is listed as a parameter")
	}
}
