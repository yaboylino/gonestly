package gonestly

import (
	"errors"
	"mime/multipart"
	"reflect"
)

var (
	fileType      = reflect.TypeFor[File]()
	filePtrType   = reflect.TypeFor[*File]()
	fileSliceType = reflect.TypeFor[[]File]()
)

// File is a file that a client uploaded in a multipart form. Give a request field with a form
// tag the type File for a required file, *File for an optional one or []File for any number:
//
//	type UploadRequest struct {
//		Title string        `form:"title"`
//		Photo gonestly.File `form:"photo"`
//	}
//
// The file is available while the request is handled; copy what must be kept.
type File struct {
	// Name is the file name the client sent, without a directory. Do not trust it as a path.
	Name string
	// Size is the size of the file in bytes.
	Size int64
	// ContentType is the type the client claims; detect it from the content when it matters.
	ContentType string

	header *multipart.FileHeader
}

// Open returns the content of the file. Close it when done.
func (f File) Open() (multipart.File, error) {
	if f.header == nil {
		return nil, errors.New("gonestly: the file was not uploaded with a request")
	}
	return f.header.Open()
}

// JSONSchema describes the file as binary content in the OpenAPI document.
func (File) JSONSchema() *Schema {
	return &Schema{Type: "string", Format: "binary"}
}

func newFile(header *multipart.FileHeader) File {
	return File{Name: header.Filename, Size: header.Size, ContentType: header.Header.Get("Content-Type"), header: header}
}
