package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"grin/internal/config"
	"grin/internal/errs"
)

type Service struct {
	root   string
	limits config.Limits
	yolo   bool
}

type Entry struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Type    string    `json:"type"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"mod_time,omitempty"`
}

type ListInput struct {
	Path       string `json:"path,omitempty"`
	Depth      int    `json:"depth,omitempty"`
	MaxEntries int    `json:"max_entries,omitempty"`
}

type ListOutput struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

type StatInput struct {
	Path string `json:"path"`
}

type StatOutput struct {
	Path    string    `json:"path"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

type ReadInput struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

type ReadOutput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Bytes   int64  `json:"bytes"`
}

type WriteInput struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

type WriteOutput struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type EditTextInput struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

type EditTextOutput struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type SearchInput struct {
	Path       string `json:"path,omitempty"`
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type Match struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text,omitempty"`
}

type SearchOutput struct {
	Matches []Match `json:"matches"`
}

var errSearchComplete = errors.New("search result limit reached")

var sensitiveReadBasenames = map[string]struct{}{
	".env":             {},
	".netrc":           {},
	".git-credentials": {},
	"credentials":      {},
	"credentials.json": {},
	"id_rsa":           {},
	"id_dsa":           {},
	"id_ecdsa":         {},
	"id_ed25519":       {},
}

var sensitiveReadExtensions = map[string]struct{}{
	".p12": {},
	".pfx": {},
}

func New(root string, limits config.Limits, yoloFlag ...bool) (*Service, error) {
	yolo := len(yoloFlag) > 0 && yoloFlag[0]
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, errs.New(errs.ErrInvalidInput, "workspace root could not be resolved", false)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, errs.New(errs.ErrNotFound, "workspace root was not found", false)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return nil, errs.New(errs.ErrInvalidInput, "workspace root must be a directory", false)
	}
	return &Service{root: canonical, limits: limits, yolo: yolo}, nil
}

func (s *Service) Root() string { return s.root }

func (s *Service) Info() map[string]any {
	mode := "normal"
	if s.yolo {
		mode = "yolo"
	}
	return map[string]any{"root": s.root, "mode": mode, "yolo": s.yolo}
}

func (s *Service) ClassifyExisting(input string) (bool, error) {
	if strings.TrimSpace(input) == "" {
		input = "."
	}
	candidate, err := filepath.Abs(s.workspacePath(input))
	if err != nil {
		return false, errs.New(errs.ErrInvalidInput, "path could not be resolved", false)
	}
	prefix, err := deepestExistingPrefix(candidate)
	if err != nil {
		return false, err
	}
	canonical, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return false, fileError(err)
	}
	return !within(s.root, canonical), nil
}

func (s *Service) ClassifyReadText(input string) (bool, error) {
	if sensitiveReadPath(input) {
		return false, errs.New(errs.ErrSensitiveFileBlocked, "sensitive file read is blocked", false)
	}
	outside, err := s.ClassifyExisting(input)
	if err != nil {
		return false, err
	}
	candidate := s.workspacePath(input)
	if path, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil && sensitiveReadPath(path) {
		return false, errs.New(errs.ErrSensitiveFileBlocked, "sensitive file read is blocked", false)
	}
	return outside, nil
}

func (s *Service) ClassifyCreate(input string) (bool, error) {
	candidate, err := filepath.Abs(s.workspacePath(input))
	if err != nil {
		return false, errs.New(errs.ErrInvalidInput, "path could not be resolved", false)
	}
	prefix, err := deepestExistingPrefix(candidate)
	if err != nil {
		return false, err
	}
	canonical, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return false, fileError(err)
	}
	return !within(s.root, canonical), nil
}

func (s *Service) resolveExisting(input string, allowOutside bool) (string, error) {
	if strings.TrimSpace(input) == "" {
		input = "."
	}
	candidate := s.workspacePath(input)
	canonical, err := filepath.EvalSymlinks(candidate)
	if errors.Is(err, os.ErrNotExist) {
		return "", errs.New(errs.ErrNotFound, "path was not found", false)
	}
	if err != nil {
		return "", fileError(err)
	}
	if !s.yolo && !allowOutside && !within(s.root, canonical) {
		return "", errs.New(errs.ErrOutsideWorkspace, "path is outside the configured workspace", false)
	}
	return canonical, nil
}

func (s *Service) resolveCreate(input string, allowOutside bool) (string, bool, error) {
	candidate := s.workspacePath(input)
	if !s.yolo && !allowOutside && !s.candidateWithinWorkspace(candidate) {
		return "", false, errs.New(errs.ErrOutsideWorkspace, "path is outside the configured workspace", false)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, errs.New(errs.ErrNotFound, "parent directory was not found", false)
	}
	if err != nil {
		return "", false, fileError(err)
	}
	if !s.yolo && !allowOutside && !within(s.root, parent) {
		return "", false, errs.New(errs.ErrOutsideWorkspace, "path is outside the configured workspace", false)
	}
	target := filepath.Join(parent, filepath.Base(candidate))
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return target, false, nil
	}
	if err != nil {
		return "", false, fileError(err)
	}
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", false, fileError(err)
	}
	if !s.yolo && !allowOutside && !within(s.root, canonical) {
		return "", false, errs.New(errs.ErrOutsideWorkspace, "path is outside the configured workspace", false)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target = canonical
	}
	return target, true, nil
}

func deepestExistingPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			return current, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fileError(err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errs.New(errs.ErrNotFound, "path could not be classified", false)
		}
		current = parent
	}
}

func (s *Service) workspacePath(input string) string {
	clean := filepath.Clean(input)
	if filepath.IsAbs(clean) {
		return clean
	}
	return filepath.Join(s.root, clean)
}

func (s *Service) candidateWithinWorkspace(candidate string) bool {
	if within(s.root, candidate) {
		return true
	}
	for parent := filepath.Dir(candidate); ; parent = filepath.Dir(parent) {
		canonical, err := filepath.EvalSymlinks(parent)
		if err == nil {
			return within(s.root, canonical)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		next := filepath.Dir(parent)
		if next == parent {
			return false
		}
	}
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Service) relative(path string) string {
	rel, err := filepath.Rel(s.root, path)
	if err != nil || rel == "." {
		return "."
	}
	return filepath.ToSlash(rel)
}

func boundedInt(value, limit int, name string) (int, error) {
	if value < 0 {
		return 0, errs.New(errs.ErrInvalidInput, name+" must not be negative", false)
	}
	if value == 0 {
		return limit, nil
	}
	if limit > 0 && value > limit {
		return 0, errs.New(errs.ErrRequestLimit, name+" exceeds the configured limit", false)
	}
	return value, nil
}

func boundedInt64(value, limit int64, name string) (int64, error) {
	if value < 0 {
		return 0, errs.New(errs.ErrInvalidInput, name+" must not be negative", false)
	}
	if value == 0 {
		return limit, nil
	}
	if limit > 0 && value > limit {
		return 0, errs.New(errs.ErrRequestLimit, name+" exceeds the configured limit", false)
	}
	return value, nil
}

func fileError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return errs.New(errs.ErrNotFound, "path was not found", false)
	}
	if errors.Is(err, os.ErrPermission) {
		return errs.New(errs.ErrPermissionDenied, "permission denied", false)
	}
	return errs.New(errs.ErrInternal, "filesystem operation failed", false)
}
