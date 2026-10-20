//go:build darwin || linux

package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests execute the bundled Linux shell script with bounded fake native
// commands. They do not need an X server or a running tmux service.
func runLinuxScriptFixture(t *testing.T, env map[string]string, binaries map[string]string) ([]TerminalObservation, []string, bool, string, error) {
	t.Helper()
	binDir := t.TempDir()
	for name, script := range binaries {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"DISPLAY", "XDG_SESSION_TYPE", "WAYLAND_DISPLAY", "TMUX", "TMUX_PANE"} {
		t.Setenv(name, "")
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return readTerminalScript(ctx, "linux")
}

const fakeLinuxXprop = `#!/bin/sh
case "$*" in
  "-root _NET_CLIENT_LIST")
    printf '%s\n' '_NET_CLIENT_LIST(WINDOW): window id # 0x10001, 0x10002, 0x10003'
    ;;
  "-root _NET_ACTIVE_WINDOW")
    printf '%s\n' '_NET_ACTIVE_WINDOW(WINDOW): window id # 0x10002'
    ;;
  "-id 0x10001 _NET_WM_NAME")
    printf '%s\n' '_NET_WM_NAME(UTF8_STRING) = "01a14001-2537-76e0-92d6-91eba1234567 | first | grin"'
    ;;
  "-id 0x10002 _NET_WM_NAME")
    exit 1
    ;;
  "-id 0x10002 WM_NAME")
    printf '%s\n' 'WM_NAME(STRING) = "01a14001-2537-76e0-92d6-91eba7654321 | second | grin"'
    ;;
  "-id 0x10003 _NET_WM_NAME")
    printf '%s\n' '_NET_WM_NAME(UTF8_STRING) = "Unrelated terminal window"'
    ;;
  *) exit 1 ;;
esac
`

func TestLinuxScriptX11EnumeratesTwoWindowsWithoutAssumingTabIdentity(t *testing.T) {
	observations, sources, supported, reason, err := runLinuxScriptFixture(t,
		map[string]string{"DISPLAY": ":42", "XDG_SESSION_TYPE": "x11"},
		map[string]string{"xprop": fakeLinuxXprop},
	)
	if err != nil || !supported || reason != "" || len(observations) != 2 {
		t.Fatalf("X11 script = %#v, sources %v, supported %v, reason %q, err %v", observations, sources, supported, reason, err)
	}
	if !slices.Contains(sources, "x11-ewmh") {
		t.Fatalf("missing X11 source: %v", sources)
	}
	first, second := observations[0], observations[1]
	if first.API != "x11-ewmh" || first.ID != "0x10001" || first.Title != discoveryThreadA+" | first | grin" || first.Foreground || first.Focused {
		t.Errorf("first X11 window = %#v", first)
	}
	if second.API != "x11-ewmh" || second.ID != "0x10002" || second.Title != discoveryThreadB+" | second | grin" || !second.Foreground || second.Focused {
		t.Errorf("second X11 window = %#v", second)
	}
	for _, observation := range observations {
		if observation.Workspace != "" || observation.WindowID != "" || observation.TabID != "" || observation.SelectedTab != nil {
			t.Errorf("X11 script invented unavailable tab/workspace metadata: %#v", observation)
		}
	}
}

func TestLinuxScriptDesktopBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name        string
		env         map[string]string
		binaries    map[string]string
		supported   bool
		wantSources []string
		wantCount   int
		wantReason  string
	}{
		{
			name: "Wayland without X11", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0", "XDG_SESSION_TYPE": "wayland"},
			supported: false, wantSources: []string{"wayland"}, wantReason: "Wayland",
		},
		{
			name:      "Wayland with XWayland is only X11 coverage",
			env:       map[string]string{"WAYLAND_DISPLAY": "wayland-0", "XDG_SESSION_TYPE": "wayland", "DISPLAY": ":42"},
			binaries:  map[string]string{"xprop": fakeLinuxXprop},
			supported: true, wantSources: []string{"wayland", "x11-ewmh"}, wantCount: 2,
		},
		{
			name: "No usable X11 API", env: map[string]string{"DISPLAY": ":42", "XDG_SESSION_TYPE": "x11"},
			binaries:  map[string]string{"xprop": "#!/bin/sh\nexit 1\n"},
			supported: false, wantReason: "No usable X11",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			observations, sources, supported, reason, err := runLinuxScriptFixture(t, tt.env, tt.binaries)
			if err != nil || supported != tt.supported || len(observations) != tt.wantCount {
				t.Fatalf("Linux script = %#v, sources %v, supported %v, reason %q, err %v", observations, sources, supported, reason, err)
			}
			for _, source := range tt.wantSources {
				if !slices.Contains(sources, source) {
					t.Errorf("missing source %s from %v", source, sources)
				}
			}
			if tt.wantReason != "" && !strings.Contains(reason, tt.wantReason) {
				t.Fatalf("reason = %q, want containing %q", reason, tt.wantReason)
			}
		})
	}
}

const fakeLinuxTmux = `#!/bin/sh
case "$3" in
  display-message)
    [ "$2" = "/tmp/grin-test.sock" ] && [ "$6" = "%12" ] || exit 9
    printf '%s\n' '$7'
    ;;
  list-panes)
    [ "$2" = "/tmp/grin-test.sock" ] && [ "$4" = "-s" ] && [ "$6" = '$7' ] || exit 10
    printf '%%12\03701a14001-2537-76e0-92d6-91eba1234567 | first | grin\037/work/grin\0371\n'
    printf '%%13\03701a14001-2537-76e0-92d6-91eba7654321 | second | grin\037/work/other\0370\n'
    ;;
  *) exit 11 ;;
esac
`

func TestLinuxScriptTmuxOnlyInheritedSession(t *testing.T) {
	observations, sources, supported, reason, err := runLinuxScriptFixture(t,
		map[string]string{
			"XDG_SESSION_TYPE": "wayland", "WAYLAND_DISPLAY": "wayland-0",
			"TMUX": "/tmp/grin-test.sock,12345,7", "TMUX_PANE": "%12",
		},
		map[string]string{"tmux": fakeLinuxTmux},
	)
	if err != nil || !supported || len(observations) != 2 || reason != "" {
		t.Fatalf("tmux script = %#v, sources %v, supported %v, reason %q, err %v", observations, sources, supported, reason, err)
	}
	if !slices.Contains(sources, "tmux-current-session") || !slices.Contains(sources, "wayland") {
		t.Fatalf("expected scoped tmux and Wayland evidence, got %v", sources)
	}
	if observations[0].ID != "$7:%12" || !observations[0].Focused || observations[0].Workspace != "/work/grin" ||
		observations[1].ID != "$7:%13" || observations[1].Focused || observations[1].Workspace != "/work/other" {
		t.Fatalf("tmux pane identities = %#v", observations)
	}
}

func TestLinuxScriptRejectsUnscopedTmuxEnvironment(t *testing.T) {
	for _, value := range []string{"relative/test.sock,12345,7", "/tmp/grin-test.sock"} {
		t.Run(value, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "tmux-invoked")
			fake := "#!/bin/sh\nprintf 'invoked' > '" + marker + "'\nexit 1\n"
			observations, _, supported, reason, err := runLinuxScriptFixture(t,
				map[string]string{
					"XDG_SESSION_TYPE": "wayland", "WAYLAND_DISPLAY": "wayland-0",
					"TMUX": value, "TMUX_PANE": "%12",
				},
				map[string]string{"tmux": fake},
			)
			if err != nil || supported || len(observations) != 0 || !strings.Contains(reason, "Wayland") {
				t.Fatalf("unscoped tmux %q yielded %#v, supported %v, reason %q, err %v", value, observations, supported, reason, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("unscoped tmux %q invoked an external tmux command: %v", value, err)
			}
		})
	}
}

func TestLinuxParserRejectsMalformedScriptData(t *testing.T) {
	_, _, supported, _, err := parseTerminalOutput("linux", []byte("SOURCES\nbroken-record\nSUPPORTED\n"))
	if err == nil || supported {
		t.Fatalf("malformed script record accepted: supported %v, err %v", supported, err)
	}
}
