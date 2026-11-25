// Package project finds the Go module that the CLI works in.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var modulePattern = regexp.MustCompile(`(?m)^module\s+(\S+)`)

// Project is a Go module on disk.
type Project struct {
	// Root is the directory that holds go.mod.
	Root string
	// Module is the module path from go.mod.
	Module string
}

// Find walks up from dir to the nearest go.mod.
func Find(dir string) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			match := modulePattern.FindSubmatch(data)
			if match == nil {
				return nil, fmt.Errorf("%s has no module line", filepath.Join(dir, "go.mod"))
			}
			return &Project{Root: dir, Module: string(match[1])}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, errors.New("no go.mod found; run this inside a Go module, or create one with gonestly new")
		}
		dir = parent
	}
}

// ImportPath returns the import path of a directory inside the project.
func (p *Project) ImportPath(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(p.Root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s is outside the module in %s", dir, p.Root)
	}
	if rel == "." {
		return p.Module, nil
	}
	return p.Module + "/" + filepath.ToSlash(rel), nil
}

// Rel returns path relative to the project root, for messages.
func (p *Project) Rel(path string) string {
	if rel, err := filepath.Rel(p.Root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}
