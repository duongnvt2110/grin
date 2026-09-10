package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"grin/internal/config"
	"grin/internal/errs"
	grinruntime "grin/internal/runtime"
)

func TestArgumentBuildersUseFixedSafetyFlags(t *testing.T) {
	wantCommon := []string{"--no-pager", "--no-optional-locks", "--no-lazy-fetch", "--literal-pathspecs", "-c", "core.fsmonitor=false"}
	if got := commonArgs(); !reflect.DeepEqual(got, wantCommon) {
		t.Fatalf("commonArgs() = %#v, want %#v", got, wantCommon)
	}
	if got := statusArgs(); !reflect.DeepEqual(got, append(append([]string{}, wantCommon...), "status", "--porcelain=v1", "--branch", "--ignore-submodules=all")) {
		t.Fatalf("statusArgs() = %#v", got)
	}
	if got := diffArgs(DiffInput{Staged: true, Path: "internal/server.go"}); !reflect.DeepEqual(got, append(append([]string{}, wantCommon...), "diff", "--cached", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=all", "--", "internal/server.go")) {
		t.Fatalf("diffArgs() = %#v", got)
	}
	if got := logArgs(20); !reflect.DeepEqual(got, append(append([]string{}, wantCommon...), "log", "-n", "20", "--no-show-signature", "--no-color", "--format=oneline")) {
		t.Fatalf("logArgs() = %#v", got)
	}
	if got := showArgs(ShowInput{Revision: "HEAD", Path: "README.md"}); !reflect.DeepEqual(got, append(append([]string{}, wantCommon...), "show", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=all", "--no-show-signature", "HEAD", "--", "README.md")) {
		t.Fatalf("showArgs() = %#v", got)
	}
}

func TestNormalizeShowInputUsesHeadForEmptyRevision(t *testing.T) {
	got, err := NormalizeShowInput(ShowInput{Path: "README.md"})
	if err != nil || got.Revision != "HEAD" {
		t.Fatalf("NormalizeShowInput() = %+v, err=%v", got, err)
	}
}

func TestGitToolsUseFixtureRepository(t *testing.T) {
	root := fixtureRepository(t, true)
	service := newService(t, root)

	status, err := service.Status(context.Background())
	if err != nil || !strings.Contains(status.Output, "##") {
		t.Fatalf("status = %+v, err=%v", status, err)
	}

	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := service.Diff(context.Background(), DiffInput{Path: "note.txt"})
	if err != nil || !strings.Contains(diff.Output, "changed") {
		t.Fatalf("diff = %+v, err=%v", diff, err)
	}

	log, err := service.Log(context.Background(), LogInput{})
	if err != nil || !strings.Contains(log.Output, "fixture commit") {
		t.Fatalf("log = %+v, err=%v", log, err)
	}
	show, err := service.Show(context.Background(), ShowInput{})
	if err != nil || !strings.Contains(show.Output, "fixture") {
		t.Fatalf("show = %+v, err=%v", show, err)
	}
}

func TestGitHistoryToolsReturnNotFoundForEmptyRepository(t *testing.T) {
	root := fixtureRepository(t, false)
	service := newService(t, root)
	for name, call := range map[string]func() error{
		"log":  func() error { _, err := service.Log(context.Background(), LogInput{}); return err },
		"show": func() error { _, err := service.Show(context.Background(), ShowInput{}); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var typed errs.Error
			if err := call(); !errors.As(err, &typed) || typed.Code != errs.ErrNotFound {
				t.Fatalf("error = %v, want not_found", err)
			}
		})
	}
}

func TestGitRejectsUnsupportedRepositoryLayouts(t *testing.T) {
	t.Run("non-git", func(t *testing.T) {
		service := newService(t, t.TempDir())
		assertUnsupported(t, func() error { _, err := service.Status(context.Background()); return err })
	})
	t.Run("git-file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /tmp/external"), 0o600); err != nil {
			t.Fatal(err)
		}
		service := newService(t, root)
		assertUnsupported(t, func() error { _, err := service.Status(context.Background()); return err })
	})
}

func assertUnsupported(t *testing.T, call func() error) {
	t.Helper()
	var typed errs.Error
	if err := call(); !errors.As(err, &typed) || typed.Code != errs.ErrUnsupported {
		t.Fatalf("error = %v, want unsupported", err)
	}
}

func fixtureRepository(t *testing.T, commit bool) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.name", "Grin Test")
	runGit(t, root, "config", "user.email", "grin-test@example.invalid")
	if commit {
		if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, root, "add", "note.txt")
		runGit(t, root, "commit", "-m", "fixture commit")
	}
	return root
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func newService(t *testing.T, root string) *Service {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Workspace.Root = canonical
	executor, err := grinruntime.New(cfg, canonical)
	if err != nil {
		t.Fatal(err)
	}
	return New(executor, canonical)
}
