package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"grin/internal/config"
	"grin/internal/errs"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("HELPER_MODE") {
	case "args":
		_, _ = fmt.Print(strings.Join(os.Args[3:], "\n"))
	case "cwd":
		cwd, _ := os.Getwd()
		_, _ = fmt.Print(cwd)
	case "env":
		_, _ = fmt.Print(os.Getenv("GRIN_TEST_SECRET"))
	case "sleep":
		time.Sleep(time.Minute)
	case "output":
		_, _ = fmt.Print(strings.Repeat("x", 1024))
	}
	os.Exit(0)
}

func TestIsDestructiveCommand(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		args       []string
		want       bool
	}{
		{name: "rm", executable: "rm", want: true},
		{name: "absolute rm", executable: "/bin/rm", want: true},
		{name: "rmdir", executable: "rmdir", want: true},
		{name: "unlink", executable: "unlink", want: true},
		{name: "git clean", executable: "git", args: []string{"clean", "-fd"}, want: true},
		{name: "find delete", executable: "find", args: []string{".", "-delete"}, want: true},
		{name: "go test", executable: "go", args: []string{"test", "./..."}},
		{name: "git status", executable: "git", args: []string{"status"}},
		{name: "shell wrapper", executable: "sh", args: []string{"-c", "rm -rf x"}},
		{name: "git option before clean", executable: "git", args: []string{"-C", "repo", "clean"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsDestructiveCommand(test.executable, test.args); got != test.want {
				t.Fatalf("IsDestructiveCommand(%q, %q) = %v, want %v", test.executable, test.args, got, test.want)
			}
		})
	}
}

func TestRedactCommandPreservesSensitiveArgumentRedaction(t *testing.T) {
	tests := []struct {
		name string
		flag string
	}{
		{name: "token", flag: "--token"},
		{name: "password", flag: "--password"},
		{name: "passwd", flag: "--passwd"},
		{name: "secret", flag: "--secret"},
		{name: "api key", flag: "--api-key"},
		{name: "private key", flag: "--private-key"},
		{name: "credential", flag: "--credential"},
		{name: "auth", flag: "--auth"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RedactCommand("tool", []string{test.flag, "secret-value"})
			want := "tool " + test.flag + " <redacted>"
			if got != want {
				t.Fatalf("RedactCommand() = %q, want %q", got, want)
			}
		})
	}
}

func TestRedactCommandRedactsAssignmentsWithoutChangingArguments(t *testing.T) {
	args := []string{
		"HOME=/Users/example",
		"DATABASE_URL=https://user:pass@example.com",
		"FOO=hello world",
		"go",
		"run",
		"review_tmp.go",
	}
	original := append([]string(nil), args...)
	got := RedactCommand("env", args)
	want := "env HOME=<redacted> DATABASE_URL=<redacted> FOO=<redacted> go run review_tmp.go"
	if got != want {
		t.Fatalf("RedactCommand() = %q, want %q", got, want)
	}
	if strings.Join(args, "\x00") != strings.Join(original, "\x00") {
		t.Fatalf("RedactCommand() modified arguments: got=%q want=%q", args, original)
	}
}

func TestRedactProcessTextUsesBestEffortFlattenedFields(t *testing.T) {
	got := redactProcessText("env FOO=hello world app")
	want := "env FOO=<redacted> world app"
	if got != want {
		t.Fatalf("redactProcessText() = %q, want %q", got, want)
	}
}

func testService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	cfg.Limits.DefaultShellTimeout = 5 * time.Second
	cfg.Limits.MaxShellTimeout = 10 * time.Second
	cfg.Limits.MaxShellStdoutBytes = 4096
	cfg.Limits.MaxShellStderrBytes = 4096
	cfg.Shell.AllowedEnvironment = []string{"PATH", "GO_WANT_HELPER_PROCESS", "HELPER_MODE", "GRIN_TEST_SECRET"}
	service, err := New(cfg, canonicalRoot(t, root))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	return service
}

func canonicalRoot(t *testing.T, root string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func helperRequest(mode string, args ...string) RunCommandRequest {
	return RunCommandRequest{Executable: os.Args[0], Args: append([]string{"-test.run=TestHelperProcess", "--"}, args...), Cwd: "", Timeout: 5 * time.Second}
}

func TestRunCommandUsesDirectArgumentsAndWorkspaceCwd(t *testing.T) {
	service := testService(t)
	t.Setenv("HELPER_MODE", "args")
	marker := filepath.Join(service.root, "should-not-exist")
	result, err := service.RunCommand(context.Background(), helperRequest("args", "$(touch", marker+")"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "$(touch") || !strings.Contains(result.Stdout, marker+")") {
		t.Fatalf("arguments were not passed directly: %q", result.Stdout)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell interpreted argument, marker stat error=%v", err)
	}

	t.Setenv("HELPER_MODE", "cwd")
	result, err = service.RunCommand(context.Background(), helperRequest("cwd"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != service.root {
		t.Fatalf("cwd = %q, want %q", result.Stdout, service.root)
	}
}

func TestRunCommandSafeEnvironmentFiltersGitVariables(t *testing.T) {
	for _, inherit := range []bool{false, true} {
		t.Run(fmt.Sprintf("inherit=%t", inherit), func(t *testing.T) {
			root := t.TempDir()
			cfg := config.Defaults()
			cfg.Workspace.Root = root
			cfg.Shell.InheritEnvironment = inherit
			cfg.Shell.AllowedEnvironment = []string{"PATH", "LANG", "LC_ALL", "GIT_DIR", "GIT_WORK_TREE"}
			service, err := New(cfg, canonicalRoot(t, root))
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_DIR", filepath.Join(root, "redirected.git"))
			t.Setenv("GIT_WORK_TREE", filepath.Join(root, "redirected-worktree"))
			result, err := service.RunCommand(context.Background(), RunCommandRequest{
				Executable:      "env",
				Cwd:             ".",
				Timeout:         5 * time.Second,
				SafeEnvironment: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.Stdout, "GIT_DIR=") || strings.Contains(result.Stdout, "GIT_WORK_TREE=") {
				t.Fatalf("safe environment leaked repository redirect variables: %q", result.Stdout)
			}
			if !strings.Contains(result.Stdout, "GIT_CONFIG_NOSYSTEM=1\n") || !strings.Contains(result.Stdout, "GIT_CONFIG_GLOBAL="+os.DevNull+"\n") {
				t.Fatalf("safe environment missing fixed Git config variables: %q", result.Stdout)
			}
		})
	}
}

func TestRunCommandRejectsOutsideCwdAndScrubsEnvironment(t *testing.T) {
	service := testService(t)
	t.Setenv("HELPER_MODE", "cwd")
	_, err := service.RunCommand(context.Background(), RunCommandRequest{Executable: os.Args[0], Args: []string{"-test.run=TestHelperProcess", "--"}, Cwd: filepath.Dir(service.root), Timeout: 5 * time.Second})
	if code := errorCode(err); code != errs.ErrOutsideWorkspace {
		t.Fatalf("outside cwd error = %s, want %s", code, errs.ErrOutsideWorkspace)
	}

	t.Setenv("HELPER_MODE", "env")
	t.Setenv("GRIN_TEST_SECRET", "must-not-leak")
	result, err := service.RunCommand(context.Background(), helperRequest("env"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "" {
		t.Fatalf("secret environment leaked: %q", result.Stdout)
	}
}

func TestClassifyCwd(t *testing.T) {
	service := testService(t)
	outside := t.TempDir()

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "empty", want: false},
		{name: "inside", path: service.root, want: false},
		{name: "missing inside", path: filepath.Join(service.root, "missing", "child"), want: false},
		{name: "outside existing", path: outside, want: true},
		{name: "outside missing", path: filepath.Join(outside, "missing"), want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.ClassifyCwd(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ClassifyCwd(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}

	link := filepath.Join(service.root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err := service.ClassifyCwd(filepath.Join(link, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("symlink escape was classified as inside")
	}
}

func TestClassifyCwdAcceptsConfiguredWorkspacePath(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	service, err := New(cfg, canonicalRoot(t, root))
	if err != nil {
		t.Fatal(err)
	}
	outside, err := service.ClassifyCwd(root)
	if err != nil || outside {
		t.Fatalf("ClassifyCwd(%q) = %v, err=%v", root, outside, err)
	}
}

func TestRunCommandAllowsOutsideCwdWhenAuthorized(t *testing.T) {
	service := testService(t)
	outside := t.TempDir()
	t.Setenv("HELPER_MODE", "cwd")
	result, err := service.RunCommand(context.Background(), RunCommandRequest{
		Executable:   os.Args[0],
		Args:         []string{"-test.run=TestHelperProcess", "--"},
		Cwd:          outside,
		Timeout:      5 * time.Second,
		AllowOutside: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != canonicalOutside {
		t.Fatalf("cwd = %q, want %q", result.Stdout, canonicalOutside)
	}
}

func TestYoloAllowsOutsideCwd(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	cfg.Yolo = true
	cfg.Shell.AllowedEnvironment = []string{"PATH", "GO_WANT_HELPER_PROCESS", "HELPER_MODE"}
	service, err := New(cfg, canonicalRoot(t, root))
	if err != nil {
		t.Fatal(err)
	}
	outsideClassified, err := service.ClassifyCwd(outside)
	if err != nil || !outsideClassified {
		t.Fatalf("YOLO outside cwd classification = %v, err=%v", outsideClassified, err)
	}
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("HELPER_MODE", "cwd")
	request := helperRequest("cwd")
	request.Cwd = outside
	result, err := service.RunCommand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	canonicalOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != canonicalOutside {
		t.Fatalf("YOLO cwd = %q, want %q", result.Stdout, canonicalOutside)
	}
}

func TestRunCommandTimeoutCancellationAndOutputLimit(t *testing.T) {
	service := testService(t)
	t.Setenv("HELPER_MODE", "sleep")
	request := helperRequest("sleep")
	request.Timeout = 50 * time.Millisecond
	_, err := service.RunCommand(context.Background(), request)
	if code := errorCode(err); code != errs.ErrExecutionTimeout {
		t.Fatalf("timeout error = %s, want %s", code, errs.ErrExecutionTimeout)
	}

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, runErr := service.RunCommand(ctx, helperRequest("sleep"))
		resultCh <- runErr
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if code := errorCode(<-resultCh); code != errs.ErrCancelled {
		t.Fatalf("cancel error = %s, want %s", code, errs.ErrCancelled)
	}

	t.Setenv("HELPER_MODE", "output")
	service.limits.MaxShellStdoutBytes = 10
	result, err := service.RunCommand(context.Background(), helperRequest("output"))
	if code := errorCode(err); code != errs.ErrOutputLimit {
		t.Fatalf("output error = %s, want %s", code, errs.ErrOutputLimit)
	}
	if len(result.Stdout) != 10 {
		t.Fatalf("captured output length = %d, want 10", len(result.Stdout))
	}
}

func TestProcessInspectionAndRedaction(t *testing.T) {
	service := testService(t)
	info, err := service.SystemInfo(context.Background())
	if err != nil || info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
		t.Fatalf("system info = %+v, err=%v", info, err)
	}
	processes, err := service.ListProcesses(context.Background(), ListProcessesRequest{})
	if err != nil || len(processes.Processes) == 0 {
		t.Fatalf("process list = %+v, err=%v", processes, err)
	}
	process, err := service.ProcessInfo(context.Background(), ProcessInfoRequest{PID: os.Getpid()})
	if err != nil || process.Process.PID != os.Getpid() {
		t.Fatalf("process info = %+v, err=%v", process, err)
	}
	parsed := parseProcesses("12 user app app --token abc https://u:p@example.test")
	if got := parsed[0].Command; strings.Contains(got, "abc") || strings.Contains(got, "u:p") {
		t.Fatalf("process secrets were not redacted: %q", got)
	}
}

func errorCode(err error) errs.ErrorCode {
	if typed, ok := err.(errs.Error); ok {
		return typed.Code
	}
	return ""
}
