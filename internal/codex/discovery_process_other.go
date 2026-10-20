//go:build !darwin && !linux

package codex

import "os/exec"

func configureDiscoveryProcess(*exec.Cmd) {}

func killDiscoveryProcess(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
