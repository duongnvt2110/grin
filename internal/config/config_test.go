package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseDefaults(t *testing.T) {
	options, err := Parse([]string{"--help"})
	if err != nil || !options.Help {
		t.Fatalf("parse help: options=%+v err=%v", options, err)
	}
	if got := options.Config.Server.Address(); got != "127.0.0.1:8765" {
		t.Fatalf("default address = %q", got)
	}
	if options.Config.Yolo {
		t.Fatal("YOLO enabled by default")
	}
}

func TestInitializeWorkspaceRegistersCanonicalPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "workspace")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	registered, err := InitializeWorkspace(link)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if registered != canonical {
		t.Fatalf("registered root = %q, want %q", registered, canonical)
	}
	if _, err := os.Stat(filepath.Join(root, WorkspaceConfigPath)); err != nil {
		t.Fatalf("local workspace config missing: %v", err)
	}
	workspaces, err := LoadRegistry()
	if err != nil || len(workspaces) != 1 || workspaces[0] != canonical {
		t.Fatalf("registry = %v, err=%v", workspaces, err)
	}
	if _, err := RegisterWorkspace(root); err != nil {
		t.Fatal(err)
	}
	workspaces, err = LoadRegistry()
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("duplicate registration = %v, err=%v", workspaces, err)
	}
}

func TestInitializeWorkspaceWritesDefaultConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	_, err := InitializeWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, WorkspaceConfigPath)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertGeneratedWorkspaceConfig(t, data)

	loaded, _, err := LoadWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defaults := Defaults()
	if loaded.Version != defaults.Version {
		t.Fatalf("version = %d, want %d", loaded.Version, defaults.Version)
	}
	if !reflect.DeepEqual(loaded.Shell.AllowedEnvironment, defaults.Shell.AllowedEnvironment) {
		t.Fatalf("allowed environment = %v, want %v", loaded.Shell.AllowedEnvironment, defaults.Shell.AllowedEnvironment)
	}
}

func TestInitializeWorkspaceMigratesLegacyConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	configDir := filepath.Join(root, ".grin")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}

	defaults := Defaults()
	legacy := []byte(fmt.Sprintf("version: %d\n", defaults.Version))
	path := filepath.Join(root, WorkspaceConfigPath)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	registered, err := InitializeWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == string(legacy) {
		t.Fatal("legacy config was not migrated")
	}
	assertGeneratedWorkspaceConfig(t, data)

	loaded, _, err := LoadWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != defaults.Version {
		t.Fatalf("migrated version = %d, want %d", loaded.Version, defaults.Version)
	}
	if !reflect.DeepEqual(loaded.Shell.AllowedEnvironment, defaults.Shell.AllowedEnvironment) {
		t.Fatalf("migrated allowed environment = %v, want %v", loaded.Shell.AllowedEnvironment, defaults.Shell.AllowedEnvironment)
	}

	if _, err := InitializeWorkspace(root); err != nil {
		t.Fatal(err)
	}
	workspaces, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 1 || workspaces[0] != registered {
		t.Fatalf("registry after migration = %v, want [%q]", workspaces, registered)
	}
}

func TestInitializeWorkspacePreservesCustomizedConfigDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	configDir := filepath.Join(root, ".grin")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, WorkspaceConfigPath)
	want := []byte("version: 1\nshell:\n  inherit_environment: true\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := InitializeWorkspace(root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("custom config changed: got %q, want %q", got, want)
	}

	loaded, _, err := LoadWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defaults := Defaults()
	if !reflect.DeepEqual(loaded.Shell.AllowedEnvironment, defaults.Shell.AllowedEnvironment) {
		t.Fatalf("allowed environment = %v, want %v", loaded.Shell.AllowedEnvironment, defaults.Shell.AllowedEnvironment)
	}
	if !loaded.Shell.InheritEnvironment {
		t.Fatal("inherit_environment was not preserved")
	}
}

func assertGeneratedWorkspaceConfig(t *testing.T, data []byte) {
	t.Helper()

	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("generated config is invalid YAML: %v", err)
	}
	if len(document) != 2 {
		t.Fatalf("generated config top-level keys = %v, want only version and shell", document)
	}
	if _, ok := document["version"]; !ok {
		t.Fatal("generated config is missing version")
	}
	if _, ok := document["shell"]; !ok {
		t.Fatal("generated config is missing shell")
	}

	var generated struct {
		Version int   `yaml:"version"`
		Shell   Shell `yaml:"shell"`
	}
	if err := yaml.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode generated config: %v", err)
	}
	defaults := Defaults()
	if generated.Version != defaults.Version {
		t.Fatalf("generated version = %d, want %d", generated.Version, defaults.Version)
	}
	if !reflect.DeepEqual(generated.Shell.AllowedEnvironment, defaults.Shell.AllowedEnvironment) {
		t.Fatalf("generated allowed environment = %v, want %v", generated.Shell.AllowedEnvironment, defaults.Shell.AllowedEnvironment)
	}
}

func TestInitializeWorkspaceRejectsRemovedPolicyProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceConfigPath), []byte("version: 1\npolicy:\n  profile: restricted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := InitializeWorkspace(root); err == nil || !strings.Contains(err.Error(), "policy.profile is no longer supported") {
		t.Fatalf("legacy policy.profile init error = %v", err)
	}
	workspaces, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 0 {
		t.Fatalf("legacy workspace was registered: %v", workspaces)
	}
}

func TestInitHelpAndExistingConfigPreservation(t *testing.T) {
	if _, help, err := Init([]string{"--help"}); err != nil || !help {
		t.Fatalf("init help = help:%v err:%v", help, err)
	}
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, WorkspaceConfigPath)
	want := []byte("version: 1\nserver:\n  port: 9001\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, help, err := Init([]string{"--workspace", root}); err != nil || help {
		t.Fatalf("init existing config = help:%v err:%v", help, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("existing config = %q, err=%v", got, err)
	}
}

func TestParseYoloIsCLIOnly(t *testing.T) {
	options, err := Parse([]string{"--help", "--yolo"})
	if err != nil || !options.Help || !options.Config.Yolo {
		t.Fatalf("parse YOLO help: options=%+v err=%v", options, err)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceConfigPath), []byte("version: 1\nyolo: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	options, err = Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.Config.Yolo {
		t.Fatal("workspace YAML enabled YOLO")
	}
}

func TestParseRejectsHeadless(t *testing.T) {
	if _, err := Parse([]string{"--headless"}); err == nil {
		t.Fatal("--headless was accepted")
	}
}

func TestValidateRejectsNonLoopback(t *testing.T) {
	cfg := Defaults()
	cfg.Server.Bind = "0.0.0.0"
	if err := Validate(cfg); err == nil {
		t.Fatal("non-loopback bind was accepted")
	}
}

func TestValidateRejectsNegativeMaxProcesses(t *testing.T) {
	previous := getEffectiveUID
	getEffectiveUID = func() int { return 501 }
	t.Cleanup(func() { getEffectiveUID = previous })

	cfg := Defaults()
	cfg.Limits.MaxProcesses = -1
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "max_processes must not be negative") {
		t.Fatalf("negative max_processes error = %v", err)
	}

	if err := Validate(Defaults()); err != nil {
		t.Fatalf("default max_processes rejected: %v", err)
	}
}

func TestValidateRejectsRoot(t *testing.T) {
	previous := getEffectiveUID
	getEffectiveUID = func() int { return 0 }
	t.Cleanup(func() { getEffectiveUID = previous })
	if err := Validate(Defaults()); err == nil {
		t.Fatal("root execution was accepted")
	}
}

func TestValidateAllowsRootOnlyInYolo(t *testing.T) {
	previous := getEffectiveUID
	getEffectiveUID = func() int { return 0 }
	t.Cleanup(func() { getEffectiveUID = previous })
	cfg := Defaults()
	cfg.Yolo = true
	if err := Validate(cfg); err != nil {
		t.Fatalf("YOLO root execution was rejected: %v", err)
	}
}

func TestFindWorkspaceRootFromNestedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceConfigPath), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "internal", "mcp")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	foundRoot, found, err := FindWorkspaceRoot(nested)
	if err != nil || !found || foundRoot != root {
		t.Fatalf("workspace root = %q, found=%v, err=%v; want %q", foundRoot, found, err, root)
	}
}

func TestFindWorkspaceRootSelectsNearestConfig(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, WorkspaceConfigPath), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nestedRoot := filepath.Join(parent, "sub")
	if err := os.MkdirAll(filepath.Join(nestedRoot, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedRoot, WorkspaceConfigPath), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(nestedRoot, "src")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	foundRoot, found, err := FindWorkspaceRoot(nested)
	if err != nil || !found || foundRoot != nestedRoot {
		t.Fatalf("nearest workspace root = %q, found=%v, err=%v; want %q", foundRoot, found, err, nestedRoot)
	}
}

func TestParseLoadsWorkspaceConfigAndAppliesOnlyExplicitCLIOverrides(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grin", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, WorkspaceConfigPath)
	configData := []byte("version: 1\nworkspace:\n  root: /tmp/not-the-workspace\nserver:\n  port: 9001\n")
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(root, ".grin", "nested"))

	options, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.Config.Workspace.Root != root || options.Config.Server.Port != 9001 {
		t.Fatalf("loaded config = %+v", options.Config)
	}

	options, err = Parse([]string{"--port", "9002"})
	if err != nil {
		t.Fatal(err)
	}
	if options.Config.Server.Port != 9002 {
		t.Fatalf("CLI overrides = %+v", options.Config)
	}
}

func TestParseRejectsRemovedProfileFlag(t *testing.T) {
	if _, err := Parse([]string{"--profile", "restricted"}); err == nil {
		t.Fatal("removed --profile flag was accepted")
	}
}

func TestLoadWorkspaceRejectsRemovedPolicyProfile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceConfigPath), []byte("version: 1\npolicy:\n  profile: restricted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadWorkspace(root)
	if err == nil || !strings.Contains(err.Error(), "policy.profile is no longer supported") {
		t.Fatalf("legacy policy.profile error = %v", err)
	}
}

func TestParseLoadsWorkspaceConfigFromCurrentDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceConfigPath), []byte("version: 1\nserver:\n  port: 9003\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	options, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.Config.Workspace.Root != root || options.Config.Server.Port != 9003 {
		t.Fatalf("current-directory config = %+v", options.Config)
	}
}

func TestParseExplicitWorkspaceIsAuthoritative(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, WorkspaceConfigPath), []byte("version: 1\nserver:\n  port: 9001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	explicitRoot := filepath.Join(parent, "other")
	if err := os.MkdirAll(filepath.Join(explicitRoot, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(explicitRoot, WorkspaceConfigPath), []byte("version: 1\nserver:\n  port: 9002\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(parent, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(parent, "nested"))

	options, err := Parse([]string{"--workspace", explicitRoot})
	if err != nil {
		t.Fatal(err)
	}
	if options.Config.Workspace.Root != explicitRoot || options.Config.Server.Port != 9002 {
		t.Fatalf("explicit workspace config = %+v", options.Config)
	}
}

func TestParseExplicitWorkspaceWithoutConfigIgnoresParentConfig(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, WorkspaceConfigPath), []byte("version: 1\nserver:\n  port: 9001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	explicitRoot := filepath.Join(parent, "explicit")
	if err := os.MkdirAll(explicitRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(parent, ".grin"))

	options, err := Parse([]string{"--workspace", explicitRoot})
	if err != nil {
		t.Fatal(err)
	}
	if options.Config.Workspace.Root != explicitRoot || options.Config.Server.Port != Defaults().Server.Port {
		t.Fatalf("explicit workspace without config = %+v", options.Config)
	}
}

func TestParseWithoutWorkspaceConfigUsesCurrentDirectory(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	options, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.Config.Workspace.Root != root || options.Config.Server.Port != Defaults().Server.Port {
		t.Fatalf("fallback config = %+v", options.Config)
	}
}

func TestParseRejectsMalformedWorkspaceConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceConfigPath), []byte("server: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	_, err := Parse(nil)
	if err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("malformed config error = %v", err)
	}
}

func TestLoadWorkspaceRejectsUnreadableConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission test requires a non-root user")
	}
	root := t.TempDir()
	configDir := filepath.Join(root, ".grin")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, WorkspaceConfigPath)
	if err := os.WriteFile(configPath, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configPath, 0o600) })

	_, _, err := LoadWorkspace(root)
	if err == nil || !strings.Contains(err.Error(), "read workspace config") {
		t.Fatalf("unreadable config error = %v", err)
	}
}
