package filesystem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"grin/internal/errs"
)

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
