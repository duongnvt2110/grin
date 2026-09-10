package git

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"grin/internal/errs"
	grinruntime "grin/internal/runtime"
)

type Service struct {
	executor *grinruntime.Service
	root     string
}

type StatusOutput struct {
	Output string `json:"output"`
}

type DiffInput struct {
	Staged bool   `json:"staged,omitempty"`
	Path   string `json:"path,omitempty"`
}

type DiffOutput struct {
	Output string `json:"output"`
}

type LogInput struct {
	MaxEntries int `json:"max_entries,omitempty"`
}

type LogOutput struct {
	Output string `json:"output"`
}

type ShowInput struct {
	Revision string `json:"revision,omitempty"`
	Path     string `json:"path,omitempty"`
}

type ShowOutput struct {
	Output string `json:"output"`
}

func New(executor *grinruntime.Service, root string) *Service {
	return &Service{executor: executor, root: root}
}

func ValidateDiffInput(input DiffInput) error {
	return validatePath(input.Path)
}

func NormalizeLogInput(input LogInput) (int, error) {
	if input.MaxEntries < 0 || input.MaxEntries > 100 {
		return 0, errs.New(errs.ErrInvalidInput, "max_entries must be between 0 and 100", false)
	}
	if input.MaxEntries == 0 {
		return 20, nil
	}
	return input.MaxEntries, nil
}

func NormalizeShowInput(input ShowInput) (ShowInput, error) {
	if input.Revision == "" {
		input.Revision = "HEAD"
	}
	if strings.HasPrefix(input.Revision, "-") || strings.IndexByte(input.Revision, 0) >= 0 {
		return ShowInput{}, errs.New(errs.ErrInvalidInput, "invalid revision", false)
	}
	if err := validatePath(input.Path); err != nil {
		return ShowInput{}, err
	}
	return input, nil
}

func (s *Service) Status(ctx context.Context) (StatusOutput, error) {
	result, err := s.run(ctx, "git.status", statusArgs())
	return StatusOutput{Output: result.Stdout}, err
}

func (s *Service) Diff(ctx context.Context, input DiffInput) (DiffOutput, error) {
	if err := ValidateDiffInput(input); err != nil {
		return DiffOutput{}, err
	}
	result, err := s.run(ctx, "git.diff", diffArgs(input))
	return DiffOutput{Output: result.Stdout}, err
}

func (s *Service) Log(ctx context.Context, input LogInput) (LogOutput, error) {
	maxEntries, err := NormalizeLogInput(input)
	if err != nil {
		return LogOutput{}, err
	}
	result, err := s.run(ctx, "git.log", logArgs(maxEntries))
	return LogOutput{Output: result.Stdout}, err
}

func (s *Service) Show(ctx context.Context, input ShowInput) (ShowOutput, error) {
	normalized, err := NormalizeShowInput(input)
	if err != nil {
		return ShowOutput{}, err
	}
	result, err := s.run(ctx, "git.show", showArgs(normalized))
	return ShowOutput{Output: result.Stdout}, err
}

func commonArgs() []string {
	return []string{"--no-pager", "--no-optional-locks", "--no-lazy-fetch", "--literal-pathspecs", "-c", "core.fsmonitor=false"}
}

func statusArgs() []string {
	args := append(commonArgs(), "status", "--porcelain=v1", "--branch", "--ignore-submodules=all")
	return args
}

func diffArgs(input DiffInput) []string {
	args := append(commonArgs(), "diff")
	if input.Staged {
		args = append(args, "--cached")
	}
	args = append(args, "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=all")
	if input.Path != "" {
		args = append(args, "--", input.Path)
	}
	return args
}

func logArgs(maxEntries int) []string {
	args := append(commonArgs(), "log", "-n", strconv.Itoa(maxEntries), "--no-show-signature", "--no-color", "--format=oneline")
	return args
}

func showArgs(input ShowInput) []string {
	args := append(commonArgs(), "show", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=all", "--no-show-signature", input.Revision)
	if input.Path != "" {
		args = append(args, "--", input.Path)
	}
	return args
}

func (s *Service) run(ctx context.Context, tool string, args []string) (grinruntime.RunCommandResult, error) {
	if s == nil || s.executor == nil {
		return grinruntime.RunCommandResult{}, errs.New(errs.ErrUnsupported, "Git runtime is unavailable", false)
	}
	if err := s.validateRepository(ctx); err != nil {
		return grinruntime.RunCommandResult{}, err
	}
	result, err := s.executor.RunCommand(ctx, grinruntime.RunCommandRequest{
		Executable:      "git",
		Args:            args,
		Cwd:             s.root,
		SafeEnvironment: true,
	})
	if err != nil {
		return result, err
	}
	if result.ExitCode == 0 {
		return result, nil
	}
	if tool == "git.log" || tool == "git.show" {
		return result, errs.New(errs.ErrNotFound, "Git history or revision was not found", false)
	}
	return result, errs.New(errs.ErrInternal, "Git command failed", false)
}

func (s *Service) validateRepository(ctx context.Context) error {
	if s == nil || s.executor == nil || strings.TrimSpace(s.root) == "" {
		return errs.New(errs.ErrUnsupported, "Git runtime is unavailable", false)
	}
	gitDir := filepath.Join(s.root, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errs.New(errs.ErrUnsupported, "workspace is not a supported Git repository", false)
	}
	result, err := s.executor.RunCommand(ctx, grinruntime.RunCommandRequest{
		Executable:      "git",
		Args:            append(commonArgs(), "rev-parse", "--show-toplevel"),
		Cwd:             s.root,
		SafeEnvironment: true,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return errs.New(errs.ErrUnsupported, "workspace is not a supported Git repository", false)
	}
	topLevel, err := filepath.EvalSymlinks(strings.TrimSpace(result.Stdout))
	if err != nil || topLevel != s.root {
		return errs.New(errs.ErrUnsupported, "workspace is not the Git repository root", false)
	}
	return nil
}

func validatePath(path string) error {
	if path == "" {
		return nil
	}
	if filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 || strings.HasPrefix(path, ":(") {
		return errs.New(errs.ErrInvalidInput, "invalid Git path", false)
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errs.New(errs.ErrInvalidInput, "Git path escapes the repository", false)
	}
	return nil
}
