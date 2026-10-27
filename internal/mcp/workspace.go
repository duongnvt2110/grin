package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"grin/internal/config"
	"grin/internal/errs"
	"grin/internal/filesystem"
	gringit "grin/internal/git"
	"grin/internal/policy"
	grinruntime "grin/internal/runtime"
)

func selectCodexWorkspace(base Dependencies, raw json.RawMessage) (string, json.RawMessage, Dependencies, error) {
	workspace, inner, err := splitWorkspace(raw, true)
	if err != nil {
		return "", nil, base, err
	}
	if !filepath.IsAbs(workspace) {
		return "", nil, base, errs.New(errs.ErrInvalidInput, "workspace must be an absolute path", false)
	}
	requested := filepath.Clean(workspace)
	var canonical string
	if !base.Config.Yolo {
		selected, root, err := resolveRegisteredWorkspace(requested, base.Config)
		if err != nil {
			return "", nil, base, err
		}
		canonical = root
		base.Config = selected
	} else {
		var err error
		canonical, err = filepath.EvalSymlinks(requested)
		if err != nil || filepath.Clean(canonical) != requested {
			return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
		}
	}
	base.Config.Workspace.Root = canonical
	return canonical, inner, base, nil
}

func resolveRegisteredWorkspace(requested string, serverConfig config.Config) (config.Config, string, error) {
	registered, err := config.LoadRegistry()
	if err != nil {
		return config.Config{}, "", errs.New(errs.ErrWorkspaceUnavailable, "workspace registry is unavailable", true)
	}
	matched := false
	for _, root := range registered {
		if root == requested {
			matched = true
			break
		}
	}
	if !matched {
		return config.Config{}, "", errs.New(errs.ErrWorkspaceNotFound, "workspace is not registered", false)
	}
	canonical, err := filepath.EvalSymlinks(requested)
	if err != nil || filepath.Clean(canonical) != requested {
		return config.Config{}, "", errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return config.Config{}, "", errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
	}
	selected, _, err := config.LoadWorkspace(canonical)
	if err != nil {
		return config.Config{}, "", errs.New(errs.ErrWorkspaceUnavailable, "workspace configuration is unavailable", true)
	}
	selected.Server = serverConfig.Server
	selected.Yolo = false
	if err := config.Validate(selected); err != nil {
		return config.Config{}, "", errs.New(errs.ErrWorkspaceUnavailable, "workspace configuration is invalid", false)
	}
	return selected, canonical, nil
}

func selectYOLOWorkspace(base Dependencies, raw json.RawMessage) (string, json.RawMessage, Dependencies, error) {
	workspace, inner, err := splitWorkspace(raw, false)
	if err != nil {
		return "", nil, base, err
	}
	if workspace == "" {
		root := base.Filesystem.Root()
		base.Config.Workspace.Root = root
		return root, inner, base, nil
	}
	if !filepath.IsAbs(workspace) {
		return "", nil, base, errs.New(errs.ErrInvalidInput, "workspace must be an absolute path", false)
	}
	requested := filepath.Clean(workspace)
	canonical, err := filepath.EvalSymlinks(requested)
	if err != nil || filepath.Clean(canonical) != requested {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
	}

	selected := base.Config
	selected.Yolo = true
	selected.Workspace.Root = canonical
	files, err := filesystem.New(canonical, selected.Limits, true)
	if err != nil {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace filesystem is unavailable", true)
	}
	runtimeService, err := grinruntime.New(selected, files.Root())
	if err != nil {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace runtime is unavailable", true)
	}
	selected.Workspace.Root = files.Root()
	base.Config = selected
	base.Filesystem = files
	base.Runtime = runtimeService
	base.Git = gringit.New(runtimeService, files.Root())
	return files.Root(), inner, base, nil
}

func workspaceSchema(schema any, requiredWorkspace bool) map[string]any {
	result, ok := schema.(map[string]any)
	if !ok {
		result = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	}
	copy := make(map[string]any, len(result)+1)
	for key, value := range result {
		copy[key] = value
	}
	properties := map[string]any{"workspace": map[string]any{"type": "string", "minLength": 1}}
	if existing, ok := copy["properties"].(map[string]any); ok {
		for key, value := range existing {
			properties[key] = value
		}
	}
	copy["properties"] = properties
	required := stringSlice(copy["required"])
	filtered := make([]string, 0, len(required)+1)
	for _, field := range required {
		if field != "workspace" {
			filtered = append(filtered, field)
		}
	}
	if requiredWorkspace {
		filtered = append([]string{"workspace"}, filtered...)
	}
	if len(filtered) == 0 {
		delete(copy, "required")
	} else {
		copy["required"] = filtered
	}
	copy["additionalProperties"] = false
	return copy
}

func stringSlice(value any) []string {
	values, _ := value.([]string)
	return values
}

func selectWorkspace(base Dependencies, raw json.RawMessage) (string, json.RawMessage, Dependencies, error) {
	workspace, inner, err := splitWorkspace(raw, true)
	if err != nil {
		return "", nil, base, err
	}
	if !filepath.IsAbs(workspace) {
		return "", nil, base, errs.New(errs.ErrInvalidInput, "workspace must be an absolute path", false)
	}
	requested := filepath.Clean(workspace)
	selected, _, err := resolveRegisteredWorkspace(requested, base.Config)
	if err != nil {
		return "", nil, base, err
	}
	files, err := filesystem.New(requested, selected.Limits)
	if err != nil {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace filesystem is unavailable", true)
	}
	runtimeService, err := grinruntime.New(selected, files.Root())
	if err != nil {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace runtime is unavailable", true)
	}
	selected.Workspace.Root = files.Root()
	base.Config = selected
	base.Filesystem = files
	base.Runtime = runtimeService
	base.Git = gringit.New(runtimeService, files.Root())
	base.Policy = policy.New(false)
	return files.Root(), inner, base, nil
}

func splitWorkspace(raw json.RawMessage, required bool) (string, json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := decode(raw, &fields); err != nil {
		return "", nil, errs.New(errs.ErrInvalidInput, "invalid tool arguments", false)
	}
	value, ok := fields["workspace"]
	if !ok {
		if required {
			return "", nil, errs.New(errs.ErrWorkspaceRequired, "workspace is required", false)
		}
		inner, err := json.Marshal(fields)
		if err != nil {
			return "", nil, errs.New(errs.ErrInvalidInput, "invalid tool arguments", false)
		}
		return "", inner, nil
	}
	var workspace string
	if err := json.Unmarshal(value, &workspace); err != nil || strings.TrimSpace(workspace) == "" {
		return "", nil, errs.New(errs.ErrInvalidInput, "workspace must be a non-empty string", false)
	}
	delete(fields, "workspace")
	inner, err := json.Marshal(fields)
	if err != nil {
		return "", nil, errs.New(errs.ErrInvalidInput, "invalid tool arguments", false)
	}
	return workspace, inner, nil
}
