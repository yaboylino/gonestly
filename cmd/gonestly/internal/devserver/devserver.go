// Package devserver rebuilds and restarts an application on every change, and stops it when a
// change breaks the build, the vet checks or the tests.
package devserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
)

// Options configure the dev server.
type Options struct {
	// Root is the module root to watch and build.
	Root string
	// Main is the package to run, relative to Root. It defaults to ".".
	Main string
	// Test also runs the tests of the changed packages, and of every package at the start.
	Test bool
	// Args are passed to the application.
	Args []string
	// Printer receives the status lines of the dev server.
	Printer *term.Printer
	// Stdout and Stderr receive the output of the application.
	Stdout, Stderr io.Writer
	// Debounce groups changes that arrive close together. It defaults to 150ms.
	Debounce time.Duration
	// StopTimeout is how long the application may take to stop before it is killed. It
	// defaults to 15 seconds.
	StopTimeout time.Duration
}

// Server watches, checks and runs one application.
type Server struct {
	opts   Options
	binary string

	mu      sync.Mutex
	process *exec.Cmd
	exited  chan struct{}
	embeds  []embedPatterns
}

// embedPatterns are the go:embed patterns of one package, relative to its directory.
type embedPatterns struct {
	dir      string
	patterns []string
}

// New prepares a dev server.
func New(opts Options) *Server {
	if opts.Main == "" {
		opts.Main = "."
	}
	if opts.Debounce == 0 {
		opts.Debounce = 150 * time.Millisecond
	}
	if opts.StopTimeout == 0 {
		opts.StopTimeout = 15 * time.Second
	}
	binary := filepath.Join(opts.Root, ".gonestly", "app")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	return &Server{opts: opts, binary: binary}
}

// Run watches until ctx is canceled, then stops the application.
func (s *Server) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("watching for changes: %w", err)
	}
	defer watcher.Close()
	if err := s.watchTree(watcher, s.opts.Root); err != nil {
		return err
	}
	defer s.stop()

	s.loadEmbeds(ctx)
	p := s.opts.Printer
	p.Info("watching %s for changes; press Ctrl+C to stop", s.opts.Root)
	changes := make(chan []string)
	go s.collect(ctx, watcher, changes)

	var cancelCycle context.CancelFunc = func() {}
	defer func() { cancelCycle() }()
	done := make(chan struct{})
	start := func(changed []string) {
		cancelCycle()
		<-done
		var cycleCtx context.Context
		cycleCtx, cancelCycle = context.WithCancel(ctx)
		done = make(chan struct{})
		go func(finished chan struct{}) {
			defer close(finished)
			s.cycle(cycleCtx, changed)
		}(done)
	}
	close(done)
	start(nil)

	for {
		select {
		case <-ctx.Done():
			cancelCycle()
			<-done
			return nil
		case changed := <-changes:
			start(changed)
		}
	}
}

// collect turns file events into batches of changed paths, debounced.
func (s *Server) collect(ctx context.Context, watcher *fsnotify.Watcher, changes chan<- []string) {
	var pending []string
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if s.ignored(event.Name) {
				continue
			}
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					_ = s.watchTree(watcher, event.Name)
					continue
				}
			}
			if !s.relevant(event.Name) {
				continue
			}
			if !slices.Contains(pending, event.Name) {
				pending = append(pending, event.Name)
			}
			timer.Reset(s.opts.Debounce)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			s.opts.Printer.Failure("watching: %v", err)
		case <-timer.C:
			batch := pending
			pending = nil
			select {
			case changes <- batch:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (s *Server) watchTree(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a directory that disappeared while walking is not an error
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && s.ignored(path) {
			return filepath.SkipDir
		}
		if err := watcher.Add(path); err != nil {
			return fmt.Errorf("watching %s: %w", path, err)
		}
		return nil
	})
}

// ignored skips version control, build output, dependencies and editor scratch files.
func (s *Server) ignored(path string) bool {
	rel, err := filepath.Rel(s.opts.Root, path)
	if err != nil || rel == "." {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") && part != ".env" && !strings.HasPrefix(part, ".env.") {
			return true
		}
		switch part {
		case "node_modules", "vendor", "tmp":
			return true
		}
	}
	base := filepath.Base(path)
	return strings.HasSuffix(base, "~") || strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, ".swx") ||
		strings.HasPrefix(base, ".#") || base == "4913" || strings.HasSuffix(base, ".db-journal") ||
		strings.HasSuffix(base, ".db") || strings.HasSuffix(base, ".db-wal") || strings.HasSuffix(base, ".db-shm")
}

// relevant reports whether a file affects the program: Go code, the module files, the .env
// file, or a file that a go:embed pattern puts into the binary. Other files, such as logs,
// uploads or a SQLite database the application writes, never cause a restart.
func (s *Server) relevant(file string) bool {
	base := filepath.Base(file)
	switch {
	case strings.HasSuffix(base, ".go"), base == "go.mod", base == "go.sum", base == ".env", strings.HasPrefix(base, ".env."):
		return true
	}
	s.mu.Lock()
	embeds := s.embeds
	s.mu.Unlock()
	for _, e := range embeds {
		rel, err := filepath.Rel(e.dir, file)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		// Embed patterns use slashes on every system, so they are matched with path, not filepath.
		for _, pattern := range e.patterns {
			pattern = strings.TrimPrefix(pattern, "all:")
			if rel == pattern || strings.HasPrefix(rel, pattern+"/") {
				return true
			}
			if ok, _ := path.Match(pattern, rel); ok {
				return true
			}
			if first, _, found := strings.Cut(rel, "/"); found {
				if ok, _ := path.Match(pattern, first); ok {
					return true
				}
			}
		}
	}
	return false
}

// loadEmbeds reads the go:embed patterns of every package with go list.
func (s *Server) loadEmbeds(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-json=Dir,EmbedPatterns", "./...")
	cmd.Dir = s.opts.Root
	out, err := cmd.Output()
	if err != nil {
		return
	}
	var embeds []embedPatterns
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg struct {
			Dir           string
			EmbedPatterns []string
		}
		if decoder.Decode(&pkg) != nil {
			break
		}
		if len(pkg.EmbedPatterns) > 0 {
			embeds = append(embeds, embedPatterns{dir: pkg.Dir, patterns: pkg.EmbedPatterns})
		}
	}
	s.mu.Lock()
	s.embeds = embeds
	s.mu.Unlock()
}

// cycle checks the code and restarts the application, or stops it and shows what is wrong.
func (s *Server) cycle(ctx context.Context, changed []string) {
	p := s.opts.Printer
	if len(changed) > 0 {
		p.Info("%s changed", s.describe(changed))
	}
	began := time.Now()

	steps := []struct {
		name string
		args []string
	}{
		{"build", []string{"build", "-buildvcs=false", "-o", s.binary, "./" + strings.TrimPrefix(s.opts.Main, "./")}},
		{"vet", []string{"vet", "./..."}},
	}
	if s.opts.Test {
		steps = append(steps, struct {
			name string
			args []string
		}{"tests", append([]string{"test"}, s.testPackages(changed)...)})
	}
	for _, step := range steps {
		output, err := s.goCommand(ctx, step.args...)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if s.stop() {
				p.Failure("the %s failed, so the application was stopped:", step.name)
			} else {
				p.Failure("the %s failed:", step.name)
			}
			s.printProblems(output)
			p.Info("fix the problem and save; the application starts again when everything passes")
			return
		}
	}

	s.loadEmbeds(ctx)
	restarted := s.stop()
	if err := s.start(); err != nil {
		p.Failure("starting the application: %v", err)
		return
	}
	verb := "started"
	if restarted {
		verb = "restarted"
	}
	p.Success("%s in %s", verb, time.Since(began).Round(10*time.Millisecond))
}

func (s *Server) describe(changed []string) string {
	rel := make([]string, 0, len(changed))
	for _, c := range changed {
		if r, err := filepath.Rel(s.opts.Root, c); err == nil {
			rel = append(rel, filepath.ToSlash(r))
		}
	}
	if len(rel) > 3 {
		return fmt.Sprintf("%s and %d more files", strings.Join(rel[:3], ", "), len(rel)-3)
	}
	return strings.Join(rel, ", ")
}

// testPackages returns the packages of the changed Go files, or every package.
func (s *Server) testPackages(changed []string) []string {
	var packages []string
	for _, c := range changed {
		if filepath.Ext(c) != ".go" {
			return []string{"./..."}
		}
		rel, err := filepath.Rel(s.opts.Root, filepath.Dir(c))
		if err != nil {
			continue
		}
		pkg := "./" + filepath.ToSlash(rel)
		if _, err := os.Stat(filepath.Dir(c)); err == nil && !slices.Contains(packages, pkg) {
			packages = append(packages, pkg)
		}
	}
	if len(packages) == 0 {
		return []string{"./..."}
	}
	return packages
}

func (s *Server) goCommand(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = s.opts.Root
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// printProblems shows the compiler, vet or test output, without the noise around it.
func (s *Server) printProblems(output string) {
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		switch {
		case line == "", strings.HasPrefix(line, "# "), strings.HasPrefix(line, "FAIL\t"), line == "FAIL":
			continue
		case strings.HasPrefix(line, "ok  \t"), strings.HasPrefix(line, "?   \t"):
			continue
		}
		s.opts.Printer.Detail("%s", strings.TrimPrefix(line, "./"))
	}
}

func (s *Server) start() error {
	// The application outlives the cycle that started it; stop ends it with a signal instead.
	cmd := exec.Command(s.binary, s.opts.Args...) //nolint:noctx // see above
	cmd.Dir = s.opts.Root
	cmd.Env = append(os.Environ(), gonestly.DevEnv+"=1")
	cmd.Stdout = s.opts.Stdout
	cmd.Stderr = s.opts.Stderr
	cmd.Stdin = nil
	isolate(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	s.mu.Lock()
	s.process, s.exited = cmd, exited
	s.mu.Unlock()

	go func() {
		err := cmd.Wait()
		close(exited)
		s.mu.Lock()
		stoppedByUs := s.process != cmd
		if !stoppedByUs {
			s.process = nil
		}
		s.mu.Unlock()
		if !stoppedByUs {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				s.opts.Printer.Failure("the application exited with code %d; save a change to start it again", exitErr.ExitCode())
			} else {
				s.opts.Printer.Info("the application stopped; save a change to start it again")
			}
		}
	}()
	return nil
}

// stop interrupts the application, kills it after the stop timeout, and reports whether it
// was running.
func (s *Server) stop() bool {
	s.mu.Lock()
	cmd, exited := s.process, s.exited
	s.process = nil
	s.mu.Unlock()
	if cmd == nil {
		return false
	}
	if err := interrupt(cmd); err != nil {
		kill(cmd)
	}
	select {
	case <-exited:
	case <-time.After(s.opts.StopTimeout):
		s.opts.Printer.Failure("the application did not stop within %s, so it was killed", s.opts.StopTimeout)
		kill(cmd)
		<-exited
	}
	return true
}
