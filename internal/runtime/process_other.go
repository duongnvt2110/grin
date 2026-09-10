//go:build !darwin && !linux

package runtime

import (
	"os/exec"

	"grin/internal/errs"
)

func ensureProcessGroupsSupported() error {
	return errs.New(errs.ErrUnsupported, "process groups are unsupported on this platform", false)
}

func configureProcess(*exec.Cmd) {}

func killProcessGroup(int) error { return nil }
