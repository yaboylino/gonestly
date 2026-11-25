//go:build unix

package devserver

import (
	"os"
	"os/exec"
	"syscall"
)

// isolate gives the application a process group of its own. Ctrl+C in the terminal then
// reaches only the dev server, which stops the application with a single interrupt; two
// interrupts would cut its graceful shutdown short.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func interrupt(cmd *exec.Cmd) error {
	return cmd.Process.Signal(os.Interrupt)
}

// kill ends the application and everything it started.
func kill(cmd *exec.Cmd) {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
