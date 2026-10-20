package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"

	"grin/internal/errs"
)

func (s *Service) ListProcesses(ctx context.Context, _ ListProcessesRequest) (ListProcessesResult, error) {
	output, err := s.ps(ctx, "-axo", "pid=,user=,comm=,args=")
	if err != nil {
		return ListProcessesResult{}, err
	}
	processes := parseProcesses(output)
	if len(processes) > s.limits.MaxProcesses {
		processes = processes[:s.limits.MaxProcesses]
	}
	return ListProcessesResult{Processes: processes}, nil
}

func (s *Service) ProcessInfo(ctx context.Context, request ProcessInfoRequest) (ProcessInfoResult, error) {
	if request.PID <= 0 {
		return ProcessInfoResult{}, errs.New(errs.ErrInvalidInput, "pid must be positive", false)
	}
	output, err := s.ps(ctx, "-p", fmt.Sprint(request.PID), "-o", "pid=,user=,comm=,args=")
	if err != nil {
		return ProcessInfoResult{}, err
	}
	processes := parseProcesses(output)
	for _, process := range processes {
		if process.PID == request.PID {
			return ProcessInfoResult{Process: process}, nil
		}
	}
	return ProcessInfoResult{}, errs.New(errs.ErrNotFound, "process was not found", false)
}

func (s *Service) SystemInfo(context.Context) (SystemInfoResult, error) {
	return SystemInfoResult{OS: runtime.GOOS, Arch: runtime.GOARCH}, nil
}

func (s *Service) ps(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "ps", args...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errs.New(errs.ErrCancelled, "process inspection was cancelled", true)
		}
		return "", errs.New(errs.ErrInternal, "process inspection failed", false)
	}
	return output.String(), nil
}
