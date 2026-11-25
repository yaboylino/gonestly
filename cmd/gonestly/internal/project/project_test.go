package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFind(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/shop\n\ngo 1.26.0\n"), 0o600)
	nested := filepath.Join(root, "internal", "users")
	_ = os.MkdirAll(nested, 0o750)

	p, err := Find(nested)
	if err != nil || p.Module != "example.com/shop" {
		t.Fatalf("p = %+v, err = %v", p, err)
	}
	if path, err := p.ImportPath(nested); err != nil || path != "example.com/shop/internal/users" {
		t.Errorf("import path = %s, %v", path, err)
	}
	if path, _ := p.ImportPath(root); path != "example.com/shop" {
		t.Errorf("root import path = %s", path)
	}
	if _, err := p.ImportPath(t.TempDir()); err == nil {
		t.Error("a directory outside the module was accepted")
	}
	if rel := p.Rel(filepath.Join(nested, "module.go")); rel != "internal/users/module.go" {
		t.Errorf("rel = %s", rel)
	}

	broken := t.TempDir()
	_ = os.WriteFile(filepath.Join(broken, "go.mod"), []byte("go 1.26.0\n"), 0o600)
	if _, err := Find(broken); err == nil {
		t.Error("a go.mod without a module line was accepted")
	}
}
