package filesystem

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"grin/internal/errs"
	"grin/internal/redaction"
)

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
	if sensitiveReadPath(input.Path) || sensitiveReadPath(root) {
		return SearchOutput{Matches: []Match{}}, nil
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
		if !info.Mode().IsRegular() || sensitiveReadPath(path) {
			return nil
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
		if redaction.ContainsPrivateKey(string(data)) {
			return nil
		}
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
				if err := addMatch(Match{Path: s.relative(path), Line: lineNumber + 1, Text: redaction.SanitizeText(line)}); err != nil {
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
