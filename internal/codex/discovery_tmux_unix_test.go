//go:build darwin || linux

package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScopedTmuxAdapterSkipsAbsentInheritedScope(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("PATH", t.TempDir())

	observations, applicable, complete, err := readScopedTmuxTitles(context.Background())
	if err != nil || applicable || !complete || len(observations) != 0 {
		t.Fatalf("unscoped tmux result = %#v, applicable %t, complete %t, err %v", observations, applicable, complete, err)
	}
}

func TestScopedTmuxAdapterReadsOnlyInheritedSocketAndSession(t *testing.T) {
	binDir := t.TempDir()
	installScopedFakeTmux(t, binDir)
	t.Setenv("PATH", binDir)
	t.Setenv("TMUX", "/tmp/grin-tmux.sock,12345,7")
	t.Setenv("TMUX_PANE", "%12")

	observations, applicable, complete, err := readScopedTmuxTitles(context.Background())
	if err != nil || !applicable || !complete || len(observations) != 2 {
		t.Fatalf("scoped tmux result = %#v, applicable %t, complete %t, err %v", observations, applicable, complete, err)
	}
	first := observations[0]
	if first.API != "tmux" || first.ID != "$7:%12" || first.Workspace != "/work/grin" || !first.Focused || !first.Foreground {
		t.Fatalf("current pane observation = %#v", first)
	}
	if observations[1].ID != "$7:%13" || observations[1].Focused || observations[1].Workspace != "/work/other" {
		t.Fatalf("sibling pane observation = %#v", observations[1])
	}
}

func TestDarwinScopedTmuxFallbackSurvivesNativeTitleAPIFailure(t *testing.T) {
	binDir := t.TempDir()
	installScopedFakeTmux(t, binDir)
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("TMUX", "/tmp/grin-tmux.sock,12345,7")
	t.Setenv("TMUX_PANE", "%12")

	observations, sources, supported, complete, _, err := readTerminalTitles(context.Background(), "darwin")
	if err != nil || !supported || complete || len(observations) != 2 {
		t.Fatalf("Darwin fallback = %#v, sources %#v, supported %t, complete %t, err %v", observations, sources, supported, complete, err)
	}
	if len(sources) != 1 || sources[0] != "tmux-current-session" {
		t.Fatalf("fallback sources = %#v", sources)
	}
}

func installScopedFakeTmux(t *testing.T, binDir string) {
	t.Helper()
	tmux := filepath.Join(binDir, "tmux")
	script := `#!/bin/sh
case "$3" in
  display-message)
    [ "$2" = "/tmp/grin-tmux.sock" ] && [ "$6" = "%12" ] || exit 9
    printf '%s\n' '$7'
    ;;
  list-panes)
    [ "$2" = "/tmp/grin-tmux.sock" ] && [ "$4" = "-s" ] && [ "$6" = '$7' ] || exit 10
    printf '%%12\03701a14001-2537-76e0-92d6-91eba... | review | grin\037/work/grin\0371\n'
    printf '%%13\03701a14001-2537-76e0-92d6-91eba... | other | grin\037/work/other\0370\n'
    ;;
  *) exit 11 ;;
esac
`
	if err := os.WriteFile(tmux, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestScopedTmuxTargetRejectsUnboundedOrMalformedScope(t *testing.T) {
	for _, test := range []struct{ tmux, pane string }{
		{"", "%1"},
		{"/tmp/socket,1,0", ""},
		{"relative/socket,1,0", "%1"},
		{"/tmp/socket,1,0", "%1;list-sessions"},
	} {
		if _, _, ok := scopedTmuxTarget(test.tmux, test.pane); ok {
			t.Errorf("accepted unscoped tmux target %#v", test)
		}
	}
}
