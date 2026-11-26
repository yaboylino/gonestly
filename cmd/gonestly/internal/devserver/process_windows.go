package devserver

import (
	"os/exec"
	"strconv"
	"syscall"
)

var generateConsoleCtrlEvent = syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")

// isolate gives the application a process group of its own. Ctrl+C in the terminal then
// reaches only the dev server, and the group is what a break event can be sent to.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// interrupt sends Ctrl+Break, which a Go program receives as an interrupt.
func interrupt(cmd *exec.Cmd) error {
	if ok, _, err := generateConsoleCtrlEvent.Call(syscall.CTRL_BREAK_EVENT, uintptr(cmd.Process.Pid)); ok == 0 {
		return err
	}
	return nil
}

// kill ends the application and everything it started.
func kill(cmd *exec.Cmd) {
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := exec.Command("taskkill", "/T", "/F", "/PID", pid).Run(); err != nil { //nolint:noctx // must not be cancelled
		_ = cmd.Process.Kill()
	}
}
