//go:build unix

package devserver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/term"
)

const groupProgram = `package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	fmt.Println("own group:", syscall.Getpgrp() == os.Getpid())
	<-stop
	fmt.Println("stopped")
}
`

// The application runs in a process group of its own, so that Ctrl+C in the terminal does not
// interrupt it a second time and cut its graceful shutdown short.
func TestApplicationRunsInOwnProcessGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go programs")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/grouptest\n\ngo 1.26.0\n")
	write(t, filepath.Join(root, "main.go"), groupProgram)

	var status, app syncBuffer
	server := New(Options{Root: root, Printer: term.New(&status), Stdout: &app, Stderr: &app, Debounce: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()

	waitFor(t, &app, "own group: true", 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitFor(t, &app, "stopped", 1)
}
