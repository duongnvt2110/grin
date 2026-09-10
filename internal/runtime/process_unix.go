//go:build darwin || linux

package runtime

import (
	"os/exec"
	"syscall"
)

func ensureProcessGroupsSupported() error { return nil }

func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
