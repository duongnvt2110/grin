package filesystem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

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

var readAssignmentPattern = regexp.MustCompile(`^(\s*["']?([A-Za-z_][A-Za-z0-9_.-]*)["']?\s*)([:=])(\s*)(.*)$`)
var readURLCredentialPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([A-Za-z0-9._~%+-]+):([A-Za-z0-9._~%:+-]+)@([A-Za-z0-9][A-Za-z0-9.-]*|\[[0-9A-Fa-f:.]+\])`)

var readSensitiveNames = []string{
	"password", "passwd", "pwd", "secret", "token",
	"credential", "credentials", "creds", "authorization",
}

var readSensitiveCompoundSuffixes = []string{
	"apikey", "authkey", "accesskey", "accesskeyid", "secretkey", "secretaccesskey",
	"privatekey", "servicekey", "accountkey", "clientkey", "dbkey", "databasekey",
	"clientsecret", "consumersecret", "jwtsecret",
	"accesstoken", "refreshtoken", "authtoken", "apitoken", "jwttoken",
	"dbpassword", "databasepassword", "dbpasswd", "databasepasswd",
	"dbpwd", "databasepwd", "dbpass", "databasepass",
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

func (s *Service) List(ctx context.Context, input ListInput, allowOutside bool) (ListOutput, error) {
	path, err := s.resolveExisting(input.Path, allowOutside)
	if err != nil {
		return ListOutput{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return ListOutput{}, fileError(err)
	}
	if !info.IsDir() {
		return ListOutput{}, errs.New(errs.ErrInvalidInput, "path is not a directory", false)
	}
	depth := input.Depth
	if depth < 0 {
		return ListOutput{}, errs.New(errs.ErrInvalidInput, "depth must not be negative", false)
	}
	maxEntries, err := boundedInt(input.MaxEntries, s.limits.MaxListEntries, "max_entries")
	if err != nil {
		return ListOutput{}, err
	}
	result := ListOutput{Path: s.relative(path), Entries: []Entry{}}
	if err := s.list(ctx, path, depth, maxEntries, &result.Entries); err != nil {
		return ListOutput{}, err
	}
	return result, nil
}

func (s *Service) list(ctx context.Context, path string, depth, max int, result *[]Entry) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return fileError(err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errs.New(errs.ErrCancelled, "filesystem listing was cancelled", true)
		}
		if len(*result) >= max {
			return errs.New(errs.ErrRequestLimit, "directory entry limit exceeded", false)
		}
		info, err := entry.Info()
		if err != nil {
			return fileError(err)
		}
		entryPath := filepath.Join(path, entry.Name())
		typeName := "file"
		if entry.IsDir() {
			typeName = "directory"
		} else if entry.Type()&os.ModeSymlink != 0 {
			typeName = "symlink"
		}
		*result = append(*result, Entry{Path: s.relative(entryPath), Name: entry.Name(), Type: typeName, Size: info.Size(), ModTime: info.ModTime()})
		if depth > 0 && entry.IsDir() {
			if err := s.list(ctx, entryPath, depth-1, max, result); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) Stat(_ context.Context, input StatInput, allowOutside bool) (StatOutput, error) {
	path, err := s.resolveExisting(input.Path, allowOutside)
	if err != nil {
		return StatOutput{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return StatOutput{}, fileError(err)
	}
	typeName := "file"
	if info.IsDir() {
		typeName = "directory"
	}
	return StatOutput{Path: s.relative(path), Type: typeName, Size: info.Size(), ModTime: info.ModTime()}, nil
}

func (s *Service) ReadText(_ context.Context, input ReadInput, allowOutside bool) (ReadOutput, error) {
	path, err := s.resolveExisting(input.Path, allowOutside)
	if err != nil {
		return ReadOutput{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return ReadOutput{}, fileError(err)
	}
	if info.IsDir() {
		return ReadOutput{}, errs.New(errs.ErrInvalidInput, "path is a directory", false)
	}
	maxBytes, err := boundedInt64(input.MaxBytes, s.limits.MaxFileReadBytes, "max_bytes")
	if err != nil {
		return ReadOutput{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return ReadOutput{}, fileError(err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return ReadOutput{}, fileError(err)
	}
	if int64(len(data)) > maxBytes {
		return ReadOutput{}, errs.New(errs.ErrOutputLimit, "file read limit exceeded", false)
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return ReadOutput{}, errs.New(errs.ErrInvalidInput, "file is not valid text", false)
	}
	content := redactSensitiveContent(string(data))
	return ReadOutput{Path: s.relative(path), Content: content, Bytes: int64(len(data))}, nil
}

func redactSensitiveContent(content string) string {
	var result strings.Builder
	result.Grow(len(content))
	for _, line := range strings.SplitAfter(content, "\n") {
		body := line
		terminator := ""
		if strings.HasSuffix(body, "\n") {
			body = strings.TrimSuffix(body, "\n")
			terminator = "\n"
			if strings.HasSuffix(body, "\r") {
				body = strings.TrimSuffix(body, "\r")
				terminator = "\r\n"
			}
		}

		replacedAssignment := false
		if match := readAssignmentPattern.FindStringSubmatch(body); match != nil {
			shortDeclaration := match[3] == ":" && match[4] == "" && strings.HasPrefix(match[5], "=")
			if !shortDeclaration && sensitiveReadName(match[2]) {
				body = match[1] + match[3] + match[4] + redactSensitiveValue(match[5])
				replacedAssignment = true
			}
		}
		if !replacedAssignment {
			if redacted, ok := redactURLCredentials(body); ok {
				body = redacted
			}
		}
		result.WriteString(body)
		result.WriteString(terminator)
	}
	return result.String()
}

func sensitiveReadName(name string) bool {
	lower := strings.ToLower(name)
	for _, candidate := range readSensitiveNames {
		if lower == candidate || strings.HasSuffix(lower, "_"+candidate) || strings.HasSuffix(lower, "-"+candidate) || strings.HasSuffix(lower, "."+candidate) {
			return true
		}
	}
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(lower)
	for _, suffix := range readSensitiveCompoundSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func redactSensitiveValue(raw string) string {
	trimmed := strings.TrimSpace(raw)
	comma := ""
	if strings.HasSuffix(trimmed, ",") {
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, ","))
		comma = ","
	}
	if len(trimmed) >= 2 && (trimmed[0] == '"' || trimmed[0] == '\'') && trimmed[len(trimmed)-1] == trimmed[0] {
		quote := string(trimmed[0])
		return quote + "<redacted>" + quote + comma
	}
	return "<redacted>"
}

func redactURLCredentials(value string) (string, bool) {
	if !readURLCredentialPattern.MatchString(value) {
		return value, false
	}
	return readURLCredentialPattern.ReplaceAllString(value, `${1}<redacted>@${4}`), true
}

func (s *Service) ValidateWrite(input WriteInput) error {
	if strings.TrimSpace(input.Path) == "" {
		return errs.New(errs.ErrInvalidInput, "path is required", false)
	}
	if int64(len(input.Content)) > s.limits.MaxFileWriteBytes {
		return errs.New(errs.ErrOutputLimit, "file write limit exceeded", false)
	}
	if !utf8.ValidString(input.Content) || strings.IndexByte(input.Content, 0) >= 0 {
		return errs.New(errs.ErrInvalidInput, "file content is not valid text", false)
	}
	return nil
}

func (s *Service) ValidateEdit(input EditTextInput) error {
	if strings.TrimSpace(input.Path) == "" {
		return errs.New(errs.ErrInvalidInput, "path is required", false)
	}
	if input.OldText == "" {
		return errs.New(errs.ErrInvalidInput, "old_text is required", false)
	}
	if int64(len(input.OldText)) > s.limits.MaxFileReadBytes {
		return errs.New(errs.ErrOutputLimit, "old_text exceeds the file read limit", false)
	}
	if int64(len(input.NewText)) > s.limits.MaxFileWriteBytes {
		return errs.New(errs.ErrOutputLimit, "new_text exceeds the file write limit", false)
	}
	if !utf8.ValidString(input.OldText) || strings.IndexByte(input.OldText, 0) >= 0 {
		return errs.New(errs.ErrInvalidInput, "old_text is not valid text", false)
	}
	if !utf8.ValidString(input.NewText) || strings.IndexByte(input.NewText, 0) >= 0 {
		return errs.New(errs.ErrInvalidInput, "new_text is not valid text", false)
	}
	return nil
}

func (s *Service) EditText(ctx context.Context, input EditTextInput, allowOutside bool) (EditTextOutput, error) {
	if err := ctx.Err(); err != nil {
		return EditTextOutput{}, errs.New(errs.ErrCancelled, "filesystem edit was cancelled", true)
	}
	if err := s.ValidateEdit(input); err != nil {
		return EditTextOutput{}, err
	}
	target, err := s.resolveExisting(input.Path, allowOutside)
	if err != nil {
		return EditTextOutput{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return EditTextOutput{}, fileError(err)
	}
	if info.IsDir() {
		return EditTextOutput{}, errs.New(errs.ErrInvalidInput, "path is a directory", false)
	}
	file, err := os.Open(target)
	if err != nil {
		return EditTextOutput{}, fileError(err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, s.limits.MaxFileReadBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return EditTextOutput{}, fileError(readErr)
	}
	if closeErr != nil {
		return EditTextOutput{}, fileError(closeErr)
	}
	if int64(len(data)) > s.limits.MaxFileReadBytes {
		return EditTextOutput{}, errs.New(errs.ErrOutputLimit, "file read limit exceeded", false)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return EditTextOutput{}, errs.New(errs.ErrInvalidInput, "file is not valid text", false)
	}
	oldText := []byte(input.OldText)
	matches := bytes.Count(data, oldText)
	if matches == 0 {
		return EditTextOutput{}, errs.New(errs.ErrNotFound, "old_text was not found", false)
	}
	if matches > 1 {
		return EditTextOutput{}, errs.New(errs.ErrInvalidInput, "old_text matched more than once", false)
	}
	final := bytes.Replace(data, oldText, []byte(input.NewText), 1)
	if int64(len(final)) > s.limits.MaxFileWriteBytes {
		return EditTextOutput{}, errs.New(errs.ErrOutputLimit, "edited file exceeds the write limit", false)
	}
	if !utf8.Valid(final) || bytes.IndexByte(final, 0) >= 0 {
		return EditTextOutput{}, errs.New(errs.ErrInvalidInput, "edited file is not valid text", false)
	}

	temporary, err := os.CreateTemp(filepath.Dir(target), ".grin-edit-*")
	if err != nil {
		return EditTextOutput{}, fileError(err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if _, err := temporary.Write(final); err != nil {
		_ = temporary.Close()
		return EditTextOutput{}, fileError(err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return EditTextOutput{}, fileError(err)
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		_ = temporary.Close()
		return EditTextOutput{}, fileError(err)
	}
	if err := temporary.Close(); err != nil {
		return EditTextOutput{}, fileError(err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return EditTextOutput{}, fileError(err)
	}
	removeTemporary = false
	return EditTextOutput{Path: s.relative(target), Bytes: int64(len(final))}, nil
}

func (s *Service) WriteText(ctx context.Context, input WriteInput, allowOutside bool) (WriteOutput, error) {
	if err := ctx.Err(); err != nil {
		return WriteOutput{}, errs.New(errs.ErrCancelled, "filesystem write was cancelled", true)
	}
	if err := s.ValidateWrite(input); err != nil {
		return WriteOutput{}, err
	}
	target, exists, err := s.resolveCreate(input.Path, allowOutside)
	if err != nil {
		return WriteOutput{}, err
	}
	if exists && !input.Overwrite {
		return WriteOutput{}, errs.New(errs.ErrFileExists, "file already exists", false)
	}
	if exists {
		info, statErr := os.Stat(target)
		if statErr != nil {
			return WriteOutput{}, fileError(statErr)
		}
		if info.IsDir() {
			return WriteOutput{}, errs.New(errs.ErrInvalidInput, "path is a directory", false)
		}
	}
	parent := filepath.Dir(target)
	temporary, err := os.CreateTemp(parent, ".grin-write-*")
	if err != nil {
		return WriteOutput{}, fileError(err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return WriteOutput{}, fileError(err)
	}
	if _, err := temporary.WriteString(input.Content); err != nil {
		_ = temporary.Close()
		return WriteOutput{}, fileError(err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return WriteOutput{}, fileError(err)
	}
	if err := temporary.Close(); err != nil {
		return WriteOutput{}, fileError(err)
	}
	if !input.Overwrite {
		if err := os.Link(temporaryName, target); err != nil {
			if errors.Is(err, os.ErrExist) {
				return WriteOutput{}, errs.New(errs.ErrFileExists, "file already exists", false)
			}
			return WriteOutput{}, fileError(err)
		}
		removeTemporary = true
	} else {
		if err := os.Rename(temporaryName, target); err != nil {
			return WriteOutput{}, fileError(err)
		}
		removeTemporary = false
	}
	return WriteOutput{Path: s.relative(target), Bytes: int64(len(input.Content))}, nil
}

func (s *Service) Search(ctx context.Context, input SearchInput, allowOutside bool) (SearchOutput, error) {
	if strings.TrimSpace(input.Query) == "" {
		return SearchOutput{}, errs.New(errs.ErrInvalidInput, "query is required", false)
	}
	root, err := s.resolveExisting(input.Path, allowOutside)
	if err != nil {
		return SearchOutput{}, err
	}
	maxResults, err := boundedInt(input.MaxResults, s.limits.MaxSearchResults, "max_results")
	if err != nil {
		return SearchOutput{}, err
	}
	started := time.Now()
	files, varBytes := 0, int64(0)
	result := SearchOutput{Matches: []Match{}}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fileError(walkErr)
		}
		if err := ctx.Err(); err != nil {
			return errs.New(errs.ErrCancelled, "filesystem search was cancelled", true)
		}
		if time.Since(started) > s.limits.MaxSearchDuration {
			return errs.New(errs.ErrRequestLimit, "search duration limit exceeded", false)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		files++
		if files > s.limits.MaxSearchFiles {
			return errs.New(errs.ErrRequestLimit, "search file limit exceeded", false)
		}
		info, err := entry.Info()
		if err != nil {
			return fileError(err)
		}
		remaining := s.limits.MaxSearchBytes - varBytes
		if info.Size() > remaining {
			return errs.New(errs.ErrRequestLimit, "search byte limit exceeded", false)
		}
		file, err := os.Open(path)
		if err != nil {
			return fileError(err)
		}
		data, err := io.ReadAll(io.LimitReader(file, remaining+1))
		_ = file.Close()
		if err != nil {
			return fileError(err)
		}
		if int64(len(data)) > remaining {
			return errs.New(errs.ErrRequestLimit, "search byte limit exceeded", false)
		}
		varBytes += int64(len(data))
		addMatch := func(match Match) error {
			if len(result.Matches) >= maxResults {
				return errSearchComplete
			}
			result.Matches = append(result.Matches, match)
			return nil
		}
		if strings.Contains(entry.Name(), input.Query) {
			if err := addMatch(Match{Path: s.relative(path)}); err != nil {
				return err
			}
		}
		for lineNumber, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, input.Query) {
				if err := addMatch(Match{Path: s.relative(path), Line: lineNumber + 1, Text: line}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if errors.Is(err, errSearchComplete) {
		return result, nil
	}
	if err != nil {
		return SearchOutput{}, err
	}
	return result, nil
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
