package filesystem

import (
	"context"
	"os"
	"path/filepath"

	"grin/internal/errs"
)

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
