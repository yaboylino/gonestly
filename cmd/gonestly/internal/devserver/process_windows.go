package devserver

import (
	"errors"
	"os/exec"
)

func isolate(*exec.Cmd) {}

// Windows cannot interrupt another process, so the application is killed instead.
func interrupt(*exec.Cmd) error {
	return errors.New("interrupting a process is not supported on Windows")
}

func kill(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
