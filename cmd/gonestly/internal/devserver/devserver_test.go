package devserver

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
)

// syncBuffer collects output from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const program = `package main

import (
	"fmt"
	"os"
	"os/signal"
)

func main() {
	// The handler is set before the program says it runs, so a stop right after is graceful.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	fmt.Println("running %s")
	<-stop
	fmt.Println("stopped %s")
}
`

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, out *syncBuffer, text string, count int) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(out.String(), text) >= count {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q x%d in:\n%s", text, count, out.String())
}

func TestDevServer(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go programs")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/devtest\n\ngo 1.26.0\n")
	write(t, filepath.Join(root, "main.go"), strings.ReplaceAll(program, "%s", "v1"))
	write(t, filepath.Join(root, "assets", "embed.go"), "package assets\n\nimport \"embed\"\n\n//go:embed *.txt\nvar Files embed.FS\n")
	write(t, filepath.Join(root, "assets", "a.txt"), "a")

	var status, app syncBuffer
	server := New(Options{Root: root, Printer: term.New(&status), Stdout: &app, Stderr: &app, Debounce: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()

	waitFor(t, &app, "running v1", 1)
	waitFor(t, &status, "started in", 1)

	write(t, filepath.Join(root, "main.go"), strings.ReplaceAll(program, "%s", "v2"))
	waitFor(t, &app, "stopped v1", 1)
	waitFor(t, &app, "running v2", 1)
	waitFor(t, &status, "restarted in", 1)

	write(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() { undefined() }\n")
	waitFor(t, &status, "the build failed, so the application was stopped", 1)
	waitFor(t, &status, "main.go:3:15: undefined: undefined", 1)
	waitFor(t, &app, "stopped v2", 1)

	write(t, filepath.Join(root, "main.go"), strings.ReplaceAll(program, "%s", "v3"))
	waitFor(t, &app, "running v3", 1)

	// Files the program does not use, such as logs, never restart it.
	write(t, filepath.Join(root, "app.log"), "noise")
	write(t, filepath.Join(root, "notes.md"), "noise")
	write(t, filepath.Join(root, ".git", "HEAD"), "noise")
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(status.String(), "app.log") || strings.Contains(status.String(), "notes.md") {
		t.Errorf("an unrelated file caused a restart:\n%s", status.String())
	}

	// An embedded file does.
	write(t, filepath.Join(root, "assets", "b.txt"), "b")
	waitFor(t, &status, "assets/b.txt changed", 1)
	waitFor(t, &status, "restarted in", 2)
	waitFor(t, &app, "running v3", 2)

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitFor(t, &app, "stopped v3", 2)
}

func TestRelevantAndIgnored(t *testing.T) {
	root := t.TempDir()
	s := New(Options{Root: root, Printer: term.New(&bytes.Buffer{})})
	s.embeds = []embedPatterns{
		{dir: filepath.Join(root, "migrations"), patterns: []string{"*.sql"}},
		{dir: filepath.Join(root, "web"), patterns: []string{"all:static", "templates/*.html"}},
	}
	for path, want := range map[string]bool{
		"main.go":                       true,
		"go.mod":                        true,
		".env":                          true,
		".env.local":                    true,
		"migrations/0001_x.up.sql":      true,
		"migrations/readme.md":          false,
		"web/static/css/site.css":       true,
		"web/templates/home.html":       true,
		"web/templates/partials/a.html": false,
		"app.log":                       false,
		"shop.db":                       false,
	} {
		if got := s.relevant(filepath.Join(root, path)); got != want {
			t.Errorf("relevant(%s) = %v, want %v", path, got, want)
		}
	}
	for path, want := range map[string]bool{
		".git/HEAD":           true,
		".gonestly/app":       true,
		"node_modules/x/y.js": true,
		"vendor/x/y.go":       true,
		"internal/x.go~":      true,
		"internal/.#x.go":     true,
		"internal/x.go":       false,
		".env":                false,
	} {
		if got := s.ignored(filepath.Join(root, path)); got != want {
			t.Errorf("ignored(%s) = %v, want %v", path, got, want)
		}
	}
}

func TestTestPackages(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a", "a.go"), "package a")
	s := New(Options{Root: root, Printer: term.New(&bytes.Buffer{})})
	if got := s.testPackages([]string{filepath.Join(root, "a", "a.go"), filepath.Join(root, "a", "b.go")}); strings.Join(got, " ") != "./a" {
		t.Errorf("packages = %v", got)
	}
	if got := s.testPackages([]string{filepath.Join(root, "go.mod")}); got[0] != "./..." {
		t.Errorf("packages = %v", got)
	}
	if got := s.testPackages(nil); got[0] != "./..." {
		t.Errorf("packages = %v", got)
	}
}
