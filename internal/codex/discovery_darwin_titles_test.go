package codex

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// Run the unmodified bundled JXA script against fake objects, not a live TUI.
// Node is only a fixture runner; discovery itself does not depend on Node.
func TestDarwinRequiredTitleReadFailuresFailClosed(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable for mocked JXA script test")
	}

	const harness = `
const fs = require('fs');
const vm = require('vm');
const mode = process.argv[2];
const uuid = '01a14001-2537-76e0-92d6-91eba1234567';
const fail = () => { throw Error('synthetic title read failure'); };
const terminal = {
  id: () => 'terminal-1',
  name: mode === 'ghostty-title-fails' ? fail : () => uuid + ' | review | grin',
  workingDirectory: mode === 'ghostty-metadata-fails' ? fail : () => '/work/grin',
};
const ghosttyTab = {
  id: () => 'tab-1', selected: () => true,
  focusedTerminal: () => terminal, terminals: () => [terminal],
};
const ghostty = {
  frontmost: () => false,
  windows: () => [{id: () => 'window-1', tabs: () => [ghosttyTab]}],
};
const terminalTab = {selected: () => true, tty: () => '/dev/ttys001'};
if (mode === 'terminal-custom-fails') terminalTab.customTitle = fail;
const terminalApp = {
  frontmost: () => false,
  windows: () => [{
    id: () => 'window-1', frontmost: () => false,
    name: mode === 'terminal-fallback-fails' ? fail : () => uuid + ' | review | grin',
    tabs: () => [terminalTab],
  }],
};
const itermSession = {id: () => 'session-1'};
const itermTab = {
  currentSession: () => itermSession,
  title: mode === 'iterm-title-fails' ? fail : () => uuid + ' | review | grin',
  sessions: () => [itermSession],
};
const iterm = {
  frontmost: () => false,
  windows: () => [{
    id: () => 'window-1', frontmost: () => false,
    tabs: () => [itermTab],
  }],
};
const bundle = mode.startsWith('ghostty') ? 'com.mitchellh.ghostty'
  : mode.startsWith('iterm') ? 'com.googlecode.iterm2' : 'com.apple.Terminal';
const apps = {count: 1, objectAtIndex: () => ({bundleIdentifier: bundle})};
const context = {
  ObjC: {import() {}, unwrap: value => value},
  $: {NSWorkspace: {sharedWorkspace: {runningApplications: apps}}},
  Application: name => name === 'Ghostty' ? ghostty : name === 'iTerm' ? iterm : terminalApp,
};
const script = fs.readFileSync(process.argv[1], 'utf8');
process.stdout.write(vm.runInNewContext(script, context));
`

	for _, tc := range []struct {
		name     string
		complete bool
	}{
		{"ghostty-title-fails", false},
		{"ghostty-metadata-fails", true},
		{"iterm-title-fails", false},
		{"iterm-title-ok", true},
		{"terminal-fallback-fails", false},
		{"terminal-custom-fails", false},
		{"terminal-custom-absent", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.Command("node", "-e", harness, filepath.Join("terminal_discovery", "darwin.js"), tc.name)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("mocked JXA execution failed: %v (%s)", err, output)
			}
			var result struct {
				Complete  bool     `json:"complete"`
				Supported bool     `json:"supported"`
				Sources   []string `json:"sources"`
				Terminals []struct {
					Title string `json:"title"`
				} `json:"terminals"`
			}
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatalf("invalid JXA fixture output: %v (%s)", err, output)
			}
			if result.Complete != tc.complete {
				t.Fatalf("complete=%t, want %t (supported=%t sources=%v)", result.Complete, tc.complete, result.Supported, result.Sources)
			}
			if tc.complete && (!result.Supported || len(result.Terminals) != 1) {
				t.Fatalf("valid title disappeared: %#v", result)
			}
		})
	}
}
