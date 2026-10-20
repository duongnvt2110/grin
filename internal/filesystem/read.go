package filesystem

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"grin/internal/errs"
	"grin/internal/redaction"
)

func (s *Service) ReadText(_ context.Context, input ReadInput, allowOutside bool) (ReadOutput, error) {
	path, err := s.resolveExisting(input.Path, allowOutside)
	if err != nil {
		return ReadOutput{}, err
	}
	if sensitiveReadPath(input.Path) || sensitiveReadPath(path) {
		return ReadOutput{}, errs.New(errs.ErrSensitiveFileBlocked, "sensitive file read is blocked", false)
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
	if redaction.ContainsPrivateKey(string(data)) {
		return ReadOutput{}, errs.New(errs.ErrSensitiveFileBlocked, "sensitive file read is blocked", false)
	}
	content := redaction.SanitizeText(string(data))
	return ReadOutput{Path: s.relative(path), Content: content, Bytes: int64(len(data))}, nil
}

func sensitiveReadPath(value string) bool {
	clean := filepath.Clean(value)
	base := strings.ToLower(filepath.Base(clean))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	if _, ok := sensitiveReadBasenames[base]; ok {
		return true
	}
	if _, ok := sensitiveReadExtensions[strings.ToLower(filepath.Ext(base))]; ok {
		return true
	}
	return false
}
