package runtime

import (
	"os"
	"strings"

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
