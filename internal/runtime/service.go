package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"grin/internal/config"
	"grin/internal/errs"
)

type Service struct {
	root   string
	limits config.Limits
	shell  config.Shell
	yolo   bool
}

func New(cfg config.Config, canonicalWorkspaceRoot string) (*Service, error) {
	if strings.TrimSpace(canonicalWorkspaceRoot) == "" {
		return nil, errs.New(errs.ErrInvalidInput, "canonical workspace root is required", false)
	}
	info, err := os.Stat(canonicalWorkspaceRoot)
	if err != nil {
		return nil, errs.New(errs.ErrNotFound, "workspace root was not found", false)
	}
	if !info.IsDir() {
		return nil, errs.New(errs.ErrInvalidInput, "workspace root must be a directory", false)
	}
	if err := ensureProcessGroupsSupported(); err != nil {
		return nil, err
	}
	return &Service{root: canonicalWorkspaceRoot, limits: cfg.Limits, shell: cfg.Shell, yolo: cfg.Yolo}, nil
}

func (s *Service) ClassifyCwd(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.root, candidate)
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return false, errs.New(errs.ErrInvalidInput, "command cwd could not be resolved", false)
	}
	prefix, err := deepestExistingPrefix(candidate)
	if err != nil {
		return false, err
	}
	canonical, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return false, errs.New(errs.ErrNotFound, "command cwd was not found", false)
	}
	return !withinRoot(s.root, canonical), nil
}

func (s *Service) RunCommand(ctx context.Context, request RunCommandRequest) (RunCommandResult, error) {
	if strings.TrimSpace(request.Executable) == "" {
		return RunCommandResult{}, errs.New(errs.ErrInvalidInput, "executable is required", false)
	}
	if err := ctx.Err(); err != nil {
		return RunCommandResult{}, errs.New(errs.ErrCancelled, "command was cancelled", true)
	}
	workingDir, err := s.resolveCwd(request.Cwd, request.AllowOutside)
	if err != nil {
		return RunCommandResult{}, err
	}
	timeout := request.Timeout
	if timeout < 0 {
		return RunCommandResult{}, errs.New(errs.ErrInvalidInput, "timeout must not be negative", false)
	}
	if timeout == 0 {
		timeout = s.limits.DefaultShellTimeout
	}
	if timeout > s.limits.MaxShellTimeout {
		return RunCommandResult{}, errs.New(errs.ErrRequestLimit, "command timeout exceeds configured maximum", false)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.Command(request.Executable, request.Args...)
	command.Dir = workingDir
	command.Env = s.environment(request.SafeEnvironment)
	configureProcess(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return RunCommandResult{}, errs.New(errs.ErrInternal, "create stdout pipe failed", false)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return RunCommandResult{}, errs.New(errs.ErrInternal, "create stderr pipe failed", false)
	}
	if err := command.Start(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RunCommandResult{}, errs.New(errs.ErrNotFound, "executable was not found", false)
		}
		return RunCommandResult{}, errs.New(errs.ErrInternal, "start command failed", false)
	}

	var killOnce sync.Once
	kill := func() { killOnce.Do(func() { _ = killProcessGroup(command.Process.Pid) }) }
	stopWatcher := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			kill()
		case <-stopWatcher:
		}
	}()

	var stdoutBuffer, stderrBuffer cappedBuffer
	stdoutBuffer.limit = s.limits.MaxShellStdoutBytes
	stdoutBuffer.onLimit = kill
	stderrBuffer.limit = s.limits.MaxShellStderrBytes
	stderrBuffer.onLimit = kill
	stdoutDone := make(chan error, 1)
	stderrDone := make(chan error, 1)
	go copyOutput(stdoutDone, stdout, &stdoutBuffer)
	go copyOutput(stderrDone, stderr, &stderrBuffer)
	waitErr := command.Wait()
	close(stopWatcher)
	stdoutErr := <-stdoutDone
	stderrErr := <-stderrDone
	if runCtx.Err() != nil {
		kill()
	}

	result := RunCommandResult{ExitCode: exitCode(waitErr), Stdout: stdoutBuffer.String(), Stderr: stderrBuffer.String()}
	if stdoutBuffer.limited || stderrBuffer.limited || errors.Is(stdoutErr, errOutputLimit) || errors.Is(stderrErr, errOutputLimit) {
		return result, errs.New(errs.ErrOutputLimit, "command output exceeded configured limit", false)
	}
	if ctx.Err() != nil {
		return result, errs.New(errs.ErrCancelled, "command was cancelled", true)
	}
	if runCtx.Err() == context.DeadlineExceeded {
		return result, errs.New(errs.ErrExecutionTimeout, "command timed out", true)
	}
	if waitErr != nil {
		if _, ok := waitErr.(*exec.ExitError); ok {
			return result, nil
		}
		return result, errs.New(errs.ErrInternal, "wait for command failed", false)
	}
	return result, nil
}

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

func (s *Service) resolveCwd(path string, allowOutside bool) (string, error) {
	if path == "" {
		return s.root, nil
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.root, candidate)
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil || (!s.yolo && !allowOutside && !withinRoot(s.root, candidate)) {
		return "", errs.New(errs.ErrOutsideWorkspace, "command cwd is outside the workspace", false)
	}
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", errs.New(errs.ErrNotFound, "command cwd was not found", false)
	}
	if !s.yolo && !allowOutside && !withinRoot(s.root, canonical) {
		return "", errs.New(errs.ErrOutsideWorkspace, "command cwd is outside the workspace", false)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", errs.New(errs.ErrNotFound, "command cwd was not found", false)
	}
	if !info.IsDir() {
		return "", errs.New(errs.ErrInvalidInput, "command cwd is not a directory", false)
	}
	return canonical, nil
}

func deepestExistingPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			return current, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", errs.New(errs.ErrInternal, "path could not be classified", false)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errs.New(errs.ErrNotFound, "path could not be classified", false)
		}
		current = parent
	}
}

func (s *Service) environment(safe bool) []string {
	if safe {
		result := make([]string, 0, 5)
		for _, name := range []string{"PATH", "LANG", "LC_ALL"} {
			if value, ok := os.LookupEnv(name); ok {
				result = append(result, name+"="+value)
			}
		}
		result = append(result, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		return result
	}
	if s.shell.InheritEnvironment {
		result := make([]string, 0, len(os.Environ()))
		for _, entry := range os.Environ() {
			name, _, ok := strings.Cut(entry, "=")
			if ok && !sensitiveEnvironmentName(name) {
				result = append(result, entry)
			}
		}
		return result
	}
	result := make([]string, 0, len(s.shell.AllowedEnvironment))
	for _, name := range s.shell.AllowedEnvironment {
		if sensitiveEnvironmentName(name) {
			continue
		}
		if value, ok := os.LookupEnv(name); ok {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func sensitiveEnvironmentName(name string) bool {
	name = strings.ToUpper(name)
	for _, marker := range []string{"SECRET", "TOKEN", "PASSWORD", "PASSWD", "API_KEY", "PRIVATE_KEY", "CREDENTIAL", "KUBECONFIG", "DATABASE_URL", "AUTH", "ACCESS_KEY"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

var errOutputLimit = errors.New("output limit reached")

type cappedBuffer struct {
	mu      sync.Mutex
	data    bytes.Buffer
	limit   int64
	limited bool
	onLimit func()
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - int64(b.data.Len())
	if remaining <= 0 {
		b.markLimited()
		return 0, errOutputLimit
	}
	if int64(len(data)) > remaining {
		_, _ = b.data.Write(data[:remaining])
		b.markLimited()
		return int(remaining), errOutputLimit
	}
	_, _ = b.data.Write(data)
	return len(data), nil
}

func (b *cappedBuffer) markLimited() {
	if b.limited {
		return
	}
	b.limited = true
	if b.onLimit != nil {
		b.onLimit()
	}
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func copyOutput(done chan<- error, source io.Reader, target io.Writer) {
	_, err := io.Copy(target, source)
	done <- err
}
