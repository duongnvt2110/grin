package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Options struct {
	Config  Config
	Help    bool
	Version bool
}

const WorkspaceConfigPath = ".grin/config.yaml"

type Config struct {
	Version   int       `yaml:"version"`
	Server    Server    `yaml:"server"`
	Workspace Workspace `yaml:"workspace"`
	Limits    Limits    `yaml:"limits"`
	Shell     Shell     `yaml:"shell"`
	TUI       TUI       `yaml:"tui"`
	Yolo      bool      `yaml:"-"`
}

type Server struct {
	Bind string `yaml:"bind"`
	Port int    `yaml:"port"`
}

type Workspace struct {
	Root string `yaml:"-"`
}

type Limits struct {
	MaxFileReadBytes    int64         `yaml:"max_file_read_bytes"`
	MaxFileWriteBytes   int64         `yaml:"max_file_write_bytes"`
	MaxSearchResults    int           `yaml:"max_search_results"`
	MaxSearchFiles      int           `yaml:"max_search_files"`
	MaxSearchBytes      int64         `yaml:"max_search_bytes"`
	MaxSearchDuration   time.Duration `yaml:"max_search_duration"`
	MaxListEntries      int           `yaml:"max_list_entries"`
	MaxShellStdoutBytes int64         `yaml:"max_shell_stdout_bytes"`
	MaxShellStderrBytes int64         `yaml:"max_shell_stderr_bytes"`
	DefaultShellTimeout time.Duration `yaml:"default_shell_timeout"`
	MaxShellTimeout     time.Duration `yaml:"max_shell_timeout"`
	MaxProcesses        int           `yaml:"max_processes"`
	MaxConcurrent       int           `yaml:"max_concurrent_requests"`
	ApprovalTimeout     time.Duration `yaml:"approval_timeout"`
	EventBufferSize     int           `yaml:"event_buffer_size"`
}

type Shell struct {
	InheritEnvironment bool     `yaml:"inherit_environment"`
	AllowedEnvironment []string `yaml:"allowed_environment"`
}

type TUI struct {
	Mode      string `yaml:"mode"`
	MaxEvents int    `yaml:"max_events"`
}

func Defaults() Config {
	return Config{
		Version:   1,
		Server:    Server{Bind: "127.0.0.1", Port: 8765},
		Workspace: Workspace{Root: "."},
		Limits: Limits{
			MaxFileReadBytes: 262144, MaxFileWriteBytes: 1048576,
			MaxSearchResults: 100, MaxSearchFiles: 10000, MaxSearchBytes: 1073741824,
			MaxSearchDuration: 30 * time.Second, MaxListEntries: 500,
			MaxShellStdoutBytes: 1048576, MaxShellStderrBytes: 1048576,
			DefaultShellTimeout: 30 * time.Second, MaxShellTimeout: 5 * time.Minute,
			MaxProcesses:  256,
			MaxConcurrent: 8, ApprovalTimeout: 5 * time.Minute, EventBufferSize: 1000,
		},
		Shell: Shell{AllowedEnvironment: []string{"PATH", "LANG", "LC_ALL"}},
		TUI:   TUI{Mode: "inline", MaxEvents: 1000},
	}
}

func Parse(args []string) (Options, error) {
	cfg := Defaults()
	set := flag.NewFlagSet("grin", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	workspace := set.String("workspace", cfg.Workspace.Root, "workspace root")
	port := set.Int("port", cfg.Server.Port, "MCP port")
	yolo := set.Bool("yolo", false, "bypass Grin workspace and approval restrictions")
	help := set.Bool("help", false, "show help")
	version := set.Bool("version", false, "show version")
	if err := set.Parse(args); err != nil {
		return Options{}, err
	}
	cfg.Workspace.Root = *workspace
	cfg.Server.Port = *port
	cfg.Yolo = *yolo
	if set.NArg() != 0 {
		return Options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(set.Args(), " "))
	}
	if *help || *version {
		return Options{Config: cfg, Help: *help, Version: *version}, nil
	}
	explicit := map[string]bool{}
	set.Visit(func(f *flag.Flag) {
		explicit[f.Name] = true
	})
	cwd, err := os.Getwd()
	if err != nil {
		return Options{}, fmt.Errorf("get current directory: %w", err)
	}
	root, err := resolveWorkspaceRoot(cwd, *workspace, explicit["workspace"])
	if err != nil {
		return Options{}, err
	}
	cfg, _, err = LoadWorkspace(root)
	if err != nil {
		return Options{}, err
	}
	if explicit["port"] {
		cfg.Server.Port = *port
	}
	cfg.Yolo = *yolo
	cfg.Workspace.Root = root
	if err := Validate(cfg); err != nil {
		return Options{}, err
	}
	return Options{Config: cfg}, nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg, err := decodeConfig(data)
	if err != nil {
		return Config{}, err
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func FindWorkspaceRoot(start string) (string, bool, error) {
	dir, err := canonicalDirectory(start)
	if err != nil {
		return "", false, err
	}
	for {
		path := filepath.Join(dir, WorkspaceConfigPath)
		info, statErr := os.Stat(path)
		switch {
		case statErr == nil && info.Mode().IsRegular():
			return dir, true, nil
		case statErr != nil && !os.IsNotExist(statErr):
			return "", false, fmt.Errorf("inspect workspace config %s: %w", path, statErr)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

func LoadWorkspace(root string) (Config, bool, error) {
	root, err := canonicalDirectory(root)
	if err != nil {
		return Config{}, false, err
	}
	data, err := os.ReadFile(filepath.Join(root, WorkspaceConfigPath))
	if os.IsNotExist(err) {
		cfg := Defaults()
		cfg.Workspace.Root = root
		return cfg, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read workspace config: %w", err)
	}
	cfg, err := decodeConfig(data)
	if err != nil {
		return Config{}, false, err
	}
	cfg.Workspace.Root = root
	return cfg, true, nil
}

func decodeConfig(data []byte) (Config, error) {
	var legacy struct {
		Policy *struct {
			Profile *string `yaml:"profile"`
		} `yaml:"policy"`
	}
	if err := yaml.Unmarshal(data, &legacy); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if legacy.Policy != nil && legacy.Policy.Profile != nil {
		return Config{}, errors.New("parse config: policy.profile is no longer supported; Grin now has only normal and --yolo modes; remove the policy section from .grin/config.yaml")
	}

	cfg := Defaults()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func resolveWorkspaceRoot(start, explicitRoot string, hasExplicitRoot bool) (string, error) {
	if hasExplicitRoot {
		return canonicalDirectory(explicitRoot)
	}
	root, found, err := FindWorkspaceRoot(start)
	if err != nil {
		return "", err
	}
	if found {
		return root, nil
	}
	return canonicalDirectory(start)
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("workspace root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace root is not a directory: %s", absolute)
	}
	return absolute, nil
}

func Validate(cfg Config) error {
	if getEffectiveUID() == 0 && !cfg.Yolo {
		return errors.New("grin must not run as root")
	}
	if cfg.Version != 1 {
		return fmt.Errorf("unsupported config version: %d", cfg.Version)
	}
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", cfg.Server.Port)
	}
	if !isLoopbackAddress(cfg.Server.Bind) {
		return fmt.Errorf("server bind must be loopback: %s", cfg.Server.Bind)
	}
	if cfg.Limits.MaxProcesses < 0 {
		return errors.New("max_processes must not be negative")
	}
	if strings.TrimSpace(cfg.Workspace.Root) == "" {
		return errors.New("workspace root is required")
	}
	root, err := filepath.Abs(cfg.Workspace.Root)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("workspace root: %w", err)
	}
	return nil
}

var getEffectiveUID = os.Geteuid

func isLoopbackAddress(bind string) bool {
	if net.ParseIP(bind) == nil {
		return false
	}
	return net.ParseIP(bind).IsLoopback()
}

func Usage() string {
	return "Grin - supervised local MCP runtime\n\nUsage:\n  grin [--workspace PATH] [--port PORT] [--yolo]\n  grin init [--workspace PATH]\n  grin doctor [--workspace PATH] [--port PORT] [--check-ready]\n  grin upgrade\n  grin --help\n  grin --version\n\nIf --workspace is omitted, Grin searches upward for the nearest .grin/config.yaml.\ninit creates the local workspace config when needed and registers its canonical path for normal-mode routing.\nupgrade installs the latest stable GitHub release when a newer version is available.\n--yolo bypasses Grin workspace and approval restrictions for supported tools; OS permissions and runtime limits still apply.\nV1 is interactive; --headless is not supported.\n"
}

func Init(args []string) (string, bool, error) {
	set := flag.NewFlagSet("grin init", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	workspace := set.String("workspace", "", "workspace root")
	help := set.Bool("help", false, "show init help")
	if err := set.Parse(args); err != nil {
		return "", false, err
	}
	if *help {
		return "", true, nil
	}
	if set.NArg() != 0 {
		return "", false, fmt.Errorf("unexpected arguments: %s", strings.Join(set.Args(), " "))
	}
	root := *workspace
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", false, fmt.Errorf("get current directory: %w", err)
		}
		if found, ok, err := FindWorkspaceRoot(cwd); err != nil {
			return "", false, err
		} else if ok {
			root = found
		} else {
			root = cwd
		}
	}
	registered, err := InitializeWorkspace(root)
	return registered, false, err
}

func InitUsage() string {
	return "Usage: grin init [--workspace PATH]\n\nCreates the local workspace config when needed and registers its canonical path for normal-mode routing.\n"
}

func (s Server) Address() string { return s.Bind + ":" + strconv.Itoa(s.Port) }
