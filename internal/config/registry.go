package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type workspaceRegistry struct {
	Workspaces []string `yaml:"workspaces"`
}

// RegistryPath returns the user-level registry used by normal-mode routing.
func RegistryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".grin", "config.yaml"), nil
}

// LoadRegistry reads canonical workspace paths. A missing registry is empty.
func LoadRegistry() ([]string, error) {
	path, err := RegistryPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read workspace registry: %w", err)
	}
	var registry workspaceRegistry
	if err := yaml.Unmarshal(data, &registry); err != nil {
		return nil, fmt.Errorf("parse workspace registry: %w", err)
	}
	seen := make(map[string]struct{}, len(registry.Workspaces))
	for _, root := range registry.Workspaces {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return nil, fmt.Errorf("workspace registry contains a non-canonical path")
		}
		if _, ok := seen[root]; ok {
			return nil, fmt.Errorf("workspace registry contains duplicate paths")
		}
		seen[root] = struct{}{}
	}
	return registry.Workspaces, nil
}

// RegisterWorkspace adds one canonical directory to the registry atomically.
func RegisterWorkspace(root string) (string, error) {
	canonical, err := canonicalWorkspace(root)
	if err != nil {
		return "", err
	}
	workspaces, err := LoadRegistry()
	if err != nil {
		return "", err
	}
	for _, registered := range workspaces {
		if registered == canonical {
			return canonical, nil
		}
	}
	workspaces = append(workspaces, canonical)
	if err := writeRegistry(workspaces); err != nil {
		return "", err
	}
	return canonical, nil
}

// InitializeWorkspace creates the local config when absent and registers it.
func InitializeWorkspace(root string) (string, error) {
	canonical, err := canonicalWorkspace(root)
	if err != nil {
		return "", err
	}
	defaults := Defaults()
	initial := struct {
		Version int `yaml:"version"`
		Shell   struct {
			AllowedEnvironment []string `yaml:"allowed_environment"`
		} `yaml:"shell"`
	}{Version: defaults.Version}
	initial.Shell.AllowedEnvironment = defaults.Shell.AllowedEnvironment
	data, err := yaml.Marshal(initial)
	if err != nil {
		return "", fmt.Errorf("encode workspace config: %w", err)
	}
	legacy := fmt.Sprintf("version: %d\n", defaults.Version)
	localPath := filepath.Join(canonical, WorkspaceConfigPath)
	_, err = os.Stat(localPath)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
			return "", fmt.Errorf("create workspace config directory: %w", err)
		}
		if err := os.WriteFile(localPath, data, 0o600); err != nil {
			return "", fmt.Errorf("write workspace config: %w", err)
		}
	case err != nil:
		return "", fmt.Errorf("inspect workspace config: %w", err)
	default:
		existing, err := os.ReadFile(localPath)
		if err != nil {
			return "", fmt.Errorf("read workspace config: %w", err)
		}
		if string(existing) == legacy {
			if err := os.WriteFile(localPath, data, 0o600); err != nil {
				return "", fmt.Errorf("write workspace config: %w", err)
			}
		} else if _, _, err := LoadWorkspace(canonical); err != nil {
			return "", err
		}
	}
	return RegisterWorkspace(canonical)
}

func canonicalWorkspace(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect workspace path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory: %s", canonical)
	}
	return filepath.Clean(canonical), nil
}

func writeRegistry(workspaces []string) error {
	path, err := RegistryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create registry directory: %w", err)
	}
	data, err := yaml.Marshal(workspaceRegistry{Workspaces: workspaces})
	if err != nil {
		return fmt.Errorf("encode workspace registry: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config.yaml.tmp-*")
	if err != nil {
		return fmt.Errorf("create workspace registry temp file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect workspace registry: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write workspace registry: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close workspace registry: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace workspace registry: %w", err)
	}
	return nil
}
