//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package process

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Give each invocation its own process group. On timeout, cancellation or output
// overflow, kill compiler/runner descendants as well as the direct process.
// A child that deliberately leaves the group still needs backend enforcement;
// this operational cleanup helper is not a native isolation proof.
func configureCancellation(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
