package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	discoveryThreadA = "01a14001-2537-76e0-92d6-91eba1234567"
	discoveryThreadB = "01a14001-2537-76e0-92d6-91eba7654321"
	discoveryThreadC = "01a14001-2537-76e0-92d6-91eba9999999"
)

func TestDiscoveryTimeoutBudgetsAllowNativeTitleEnumeration(t *testing.T) {
	if terminalReadTimeout < 10*time.Second {
		t.Fatalf("terminal probe timeout = %s, want at least 10s for native title APIs", terminalReadTimeout)
	}
	if discoveryTimeout <= terminalReadTimeout {
		t.Fatalf("total discovery timeout %s must leave time after terminal enumeration timeout %s", discoveryTimeout, terminalReadTimeout)
	}
}

func TestTerminalProbeTimeoutIsNotReportedAsUnsupported(t *testing.T) {
	reason, evidence := terminalReadFailure(context.DeadlineExceeded)
	if reason != "native terminal title probe timed out; API support is unknown" || !strings.Contains(evidence, "bounded probe timeout") {
		t.Fatalf("timeout failure = %q, %q", reason, evidence)
	}
	reason, _ = terminalReadFailure(errors.New("read failed"))
	if reason != "terminal title enumeration failed" {
		t.Fatalf("other failure reason = %q", reason)
	}
}

func TestParseThreadTitleIsPositionIndependentAndStrict(t *testing.T) {
	cases := []struct {
		input, prefix, title, project string
	}{
		{"01a14001-2537-76e0-92d6-91eba... | Assess status and next step | grin", "01a14001-2537-76e0-92d6-91eba", "", ""},
		{"01a14001-2537-76e0-92d6-91eba… | Assess status and next step | grin", "01a14001-2537-76e0-92d6-91eba", "", ""},
		{discoveryThreadA + " | Full title | grin", discoveryThreadA, "", ""},
		{discoveryThreadA, discoveryThreadA, "", ""},
		{"Full title | " + discoveryThreadA + " | grin", discoveryThreadA, "", ""},
		{"grin | Full title | " + discoveryThreadA, discoveryThreadA, "", ""},
		{"Review | 01a14001-2537", "01a14001-2537", "", ""},
	}
	for _, test := range cases {
		prefix, title, project, ok := parseThreadTitle(test.input)
		if !ok || prefix != test.prefix || title != test.title || project != test.project {
			t.Errorf("parseThreadTitle(%q) = %q, %q, %q, %v; identity=%#v", test.input, prefix, title, project, ok, extractTitleIdentity(test.input))
		}
	}
	for _, malformed := range []string{
		"prefix01a14001-2537-76e0-92d6-91eba",
		"01234567suffix | title",
		"01a14001x | title",
		"01a1400 | short",
		"01a14001-a-b | malformed",
		"01a14001-2537-76e0-92d6-91ebaXYZ | title",
		"review 01234567 | title",
		"0123456789abcdef0123456789abcdef | title",
	} {
		if _, _, _, ok := parseThreadTitle(malformed); ok {
			t.Errorf("parseThreadTitle(%q) unexpectedly matched", malformed)
		}
	}
}

func TestTitleThreadIDExtractsOnlyUnambiguousFullUUIDs(t *testing.T) {
	full := "01A142BF-DEa7-7a43-a6b1-83ea32c95866"
	for _, test := range []struct {
		title string
		want  string
		found bool
	}{
		{full[:28] + "... | " + full + " | grin", strings.ToLower(full), true},
		{full[:28] + "… | " + full + " | grin", strings.ToLower(full), true},
		{"Review | " + full + " trailing | grin", strings.ToLower(full), true},
		{full[:28] + "... | 91a142bf-dea7-7a43-a6b1-83ea32c95867 | grin", "", false},
		{full[:28] + "... | 01a142bf-dea7-7a43-a6b1-83ea32c9586z | grin", "", false},
		{full + " | " + full, strings.ToLower(full), true},
		{"https://example.test/path/" + full, "", false},
		{"https://example.test/?thread=" + full, "", false},
		{"/tmp/session/" + full, "", false},
		{"Review token= " + full, "", false},
		{`Review token="` + full + `"`, "", false},
	} {
		got, found := titleThreadID(test.title)
		if found != test.found || got != test.want {
			t.Errorf("titleThreadID(%q) = %q, %v; want %q, %v", test.title, got, found, test.want, test.found)
		}
	}
}

func TestUniqueThreadRowRejectsMissingAndAmbiguousPrefixes(t *testing.T) {
	if _, status := uniqueThreadRow(nil); status != "no_match" {
		t.Fatalf("empty rows status = %q", status)
	}
	rows := []discoveryThreadRow{{ID: discoveryThreadA}, {ID: discoveryThreadB}}
	if _, status := uniqueThreadRow(rows); status != "ambiguous" {
		t.Fatalf("duplicate prefix status = %q", status)
	}
	row, status := uniqueThreadRow(rows[:1])
	if status != "" || row.ID != discoveryThreadA {
		t.Fatalf("unique row = %#v, status %q", row, status)
	}
}

func TestThreadPrefixQueryMatchesSQLiteUUIDPrefixExactly(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed")
	}
	database := filepath.Join(t.TempDir(), "state_1.sqlite")
	runTestSQL(t, sqlite, database, "CREATE TABLE threads(id TEXT, cwd TEXT, thread_source TEXT, archived INTEGER); INSERT INTO threads VALUES("+sqlLiteral(discoveryThreadA)+",'/workspace','user',0);")
	rows, err := queryRows[discoveryThreadRow](context.Background(), runSQLite, database, threadPrefixQuery("01a14001-2537-76e0-92d6-91eba"))
	if err != nil {
		t.Fatal(err)
	}
	if row, status := uniqueThreadRow(rows); status != "" || row.ID != discoveryThreadA {
		t.Fatalf("unique prefix = %#v, status %q", row, status)
	}
	runTestSQL(t, sqlite, database, "INSERT INTO threads VALUES("+sqlLiteral(discoveryThreadB)+",'/workspace','user',0);")
	rows, err = queryRows[discoveryThreadRow](context.Background(), runSQLite, database, threadPrefixQuery("01a14001-2537-76e0-92d6-91eba"))
	if err != nil {
		t.Fatal(err)
	}
	if _, status := uniqueThreadRow(rows); status != "ambiguous" {
		t.Fatalf("duplicate prefix status = %q", status)
	}
}

func TestArchivedTitleRemainsUnresolvedAndIneligible(t *testing.T) {
	workspace := t.TempDir()
	candidate := ThreadCandidate{TopLevel: "unknown"}
	if needsTopLevelCheck(&candidate, discoveryThreadRow{
		ID: discoveryThreadA, CWD: workspace, ThreadSource: "user", Archived: 1,
	}, workspace) {
		t.Fatal("archived title requested a top-level check")
	}
	if candidate.Resolution != "archived_title" || candidate.ThreadID != discoveryThreadA || candidate.TopLevel != "unknown" {
		t.Fatalf("archived candidate = %#v", candidate)
	}
}

func TestWorkspaceAndThreadSourceMustMatch(t *testing.T) {
	workspace := t.TempDir()
	other := t.TempDir()
	canonical, err := canonicalWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	otherCanonical, err := canonicalWorkspace(other)
	if err != nil {
		t.Fatal(err)
	}
	candidate := ThreadCandidate{}
	if needsTopLevelCheck(&candidate, discoveryThreadRow{ID: discoveryThreadA, CWD: otherCanonical, ThreadSource: "user"}, canonical) {
		t.Fatal("workspace mismatch requested a top-level check")
	}
	if candidate.Resolution != "workspace_mismatch" || candidate.WorkspaceMatch != "no" {
		t.Fatalf("workspace mismatch candidate = %#v", candidate)
	}
	candidate = ThreadCandidate{TerminalObservation: TerminalObservation{Workspace: otherCanonical}}
	if needsTopLevelCheck(&candidate, discoveryThreadRow{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user"}, canonical) {
		t.Fatal("terminal workspace mismatch requested a top-level check")
	}
	if candidate.Resolution != "terminal_workspace_mismatch" || candidate.WorkspaceMatch != "no" {
		t.Fatalf("terminal workspace mismatch candidate = %#v", candidate)
	}
	candidate = ThreadCandidate{}
	if needsTopLevelCheck(&candidate, discoveryThreadRow{ID: discoveryThreadA, CWD: canonical, ThreadSource: "subagent"}, canonical) {
		t.Fatal("ineligible source requested a top-level check")
	}
	if candidate.Resolution != "ineligible_thread_source" || candidate.TopLevel != "no" {
		t.Fatalf("ineligible candidate = %#v", candidate)
	}
	candidate = ThreadCandidate{TerminalObservation: TerminalObservation{Focused: false}}
	if !needsTopLevelCheck(&candidate, discoveryThreadRow{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user"}, canonical) {
		t.Fatal("eligible nonfocused title candidate skipped independent top-level metadata check")
	}
}

func TestTitleSnapshotsFollowNewClearAndResume(t *testing.T) {
	snapshots := []struct {
		command string
		title   string
		want    string
	}{
		{command: "/new", title: discoveryThreadA[:28] + "... | " + discoveryThreadA + " | grin", want: discoveryThreadA},
		{command: "/clear", title: discoveryThreadB[:28] + "... | " + discoveryThreadB + " | grin", want: discoveryThreadB},
		{command: "/resume", title: discoveryThreadA[:28] + "... | " + discoveryThreadA + " | grin", want: discoveryThreadA},
	}
	for _, snapshot := range snapshots {
		t.Run(snapshot.command, func(t *testing.T) {
			_, _, _, ok := parseThreadTitle(snapshot.title)
			threadID, exact := titleThreadID(snapshot.title)
			if !ok || !exact || threadID != snapshot.want {
				t.Fatalf("title snapshot resolved to %q, exact %v; want %q", threadID, exact, snapshot.want)
			}
		})
	}
}

func TestParseTerminalOutputForSimulatedOperatingSystems(t *testing.T) {
	fixtures := []struct {
		goos string
		data string
		api  string
		id   string
	}{
		{"darwin", `{"supported":true,"complete":true,"terminals":[{"api":"ghostty-applescript","terminal_id":"term-1","title":"` + discoveryThreadA + ` | review | grin","window_id":"window-1","tab_id":"tab-1","workspace":"/work/grin","foreground":true,"focused":true,"selected_tab":true,"active_in_tab":true}]}`, "ghostty-applescript", "term-1"},
		{"windows", `{"supported":true,"complete":true,"sources":["win32-window-title"],"terminals":[{"api":"win32-window-title","terminal_id":"0x123","title":"review | ` + discoveryThreadA + ` | grin","foreground":true,"focused":false}]}`, "win32-window-title", "0x123"},
		{"linux", "SOURCES\twayland\nSOURCE\tx11-ewmh\nx11-ewmh\t0x123\ttrue\tfalse\t\t_NET_WM_NAME(UTF8_STRING) = \"review | " + discoveryThreadA + " | grin\"\nSUPPORTED\nCOMPLETE\ttrue\n", "x11-ewmh", "0x123"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.goos, func(t *testing.T) {
			terminals, _, supported, complete, _, err := parseTerminalOutput(fixture.goos, []byte(fixture.data))
			if err != nil || !supported || !complete || len(terminals) != 1 {
				t.Fatalf("parseTerminalOutput() = %#v, supported %v, complete %v, err %v", terminals, supported, complete, err)
			}
			if terminals[0].API != fixture.api || terminals[0].ID != fixture.id || terminals[0].Title == "" {
				t.Fatalf("terminal = %#v", terminals[0])
			}
			if fixture.goos == "darwin" {
				if terminals[0].WindowID != "window-1" || terminals[0].TabID != "tab-1" ||
					terminals[0].SelectedTab == nil || !*terminals[0].SelectedTab ||
					terminals[0].ActiveInTab == nil || !*terminals[0].ActiveInTab {
					t.Fatalf("Ghostty hierarchy not preserved: %#v", terminals[0])
				}
			} else if terminals[0].WindowID != "" || terminals[0].TabID != "" ||
				terminals[0].SelectedTab != nil || terminals[0].ActiveInTab != nil {
				t.Fatalf("unsupported terminal hierarchy evidence was invented: %#v", terminals[0])
			}
		})
	}
}

// The native Win32 API needs a Windows acceptance run. This portable test
// guards the post-copy recheck and the incomplete-snapshot contract.
func TestWindowsTitleGrowthMarksDiscoveryIncomplete(t *testing.T) {
	script, err := terminalDiscoveryScripts.ReadFile("terminal_discovery/windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	copyIndex := strings.Index(text, "$copied = [GrinWindowTitles]::GetWindowText(")
	recheckIndex := strings.Index(text, "$lengthAfter = [GrinWindowTitles]::GetWindowTextLength(")
	guardIndex := strings.Index(text, "$lengthAfterError -ne 0 -or $lengthAfter -ne $copied")
	if copyIndex < 0 || recheckIndex <= copyIndex || guardIndex <= recheckIndex {
		t.Fatal("Windows adapter must recheck title length after copying and fail closed on a changed length")
	}
	// A simulated failed read must not become a complete discovery simply
	// because a different title observation was returned.
	output := `{"supported":true,"complete":false,"sources":["win32-window-title"],"terminals":[{"api":"win32-window-title","terminal_id":"0x123","title":"` + discoveryThreadA + `"}]}`
	observations, _, supported, complete, _, err := parseTerminalOutput("windows", []byte(output))
	if err != nil || !supported || complete || len(observations) != 1 {
		t.Fatalf("incomplete Windows snapshot accepted: supported=%t complete=%t observations=%#v err=%v", supported, complete, observations, err)
	}
}

func TestSelectedTabDoesNotClaimCurrentThreadProof(t *testing.T) {
	workspace := t.TempDir()
	selected, notSelected := true, false
	service := &Service{home: func() (string, error) {
		return "", errors.New("Codex home unavailable")
	}}
	result, err := service.discoverFromTitles(context.Background(), workspace, []TerminalObservation{
		{API: "ghostty-applescript", ID: "term-a", WindowID: "window-1", TabID: "tab-1", Title: discoveryThreadA[:28] + "... | " + discoveryThreadA + " | grin", SelectedTab: &selected, ActiveInTab: &selected},
		{API: "ghostty-applescript", ID: "term-b", WindowID: "window-1", TabID: "tab-1", Title: discoveryThreadB[:28] + "... | " + discoveryThreadB + " | grin", SelectedTab: &selected, ActiveInTab: &notSelected},
		{API: "ghostty-applescript", ID: "term-c", WindowID: "window-2", TabID: "tab-2", Title: discoveryThreadC[:28] + "... | " + discoveryThreadC + " | grin", SelectedTab: &selected, ActiveInTab: &selected},
	}, true)
	if err != nil || result.Status != "UNRESOLVED" || len(result.Candidates) != 3 {
		t.Fatalf("discovery = %#v, err %v", result, err)
	}
	if result.Candidates[0].SelectedTab == nil || !*result.Candidates[0].SelectedTab ||
		result.Candidates[1].SelectedTab == nil || !*result.Candidates[1].SelectedTab ||
		result.Candidates[2].SelectedTab == nil || !*result.Candidates[2].SelectedTab ||
		result.Candidates[0].ActiveInTab == nil || !*result.Candidates[0].ActiveInTab ||
		result.Candidates[1].ActiveInTab == nil || *result.Candidates[1].ActiveInTab ||
		result.Candidates[2].ActiveInTab == nil || !*result.Candidates[2].ActiveInTab {
		t.Fatalf("tab or terminal observations lost: %#v", result.Candidates)
	}
	for index, candidate := range result.Candidates {
		wantWindow, wantTab := "window-1", "tab-1"
		if index == 2 {
			wantWindow, wantTab = "window-2", "tab-2"
		}
		if candidate.WindowID != wantWindow || candidate.TabID != wantTab {
			t.Fatalf("window or tab identity lost: %#v", candidate)
		}
	}
	for _, candidate := range result.Candidates {
		if candidate.Focused || candidate.Resolution != "unverified_title_uuid" {
			t.Fatalf("selected tab was treated as active-thread evidence: %#v", candidate)
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\"selected_tab\":true") || !strings.Contains(string(data), "\"active_in_tab\":false") ||
		!strings.Contains(string(data), "\"active_in_tab\":true") ||
		!strings.Contains(string(data), "\"window_id\":\"window-1\"") ||
		!strings.Contains(string(data), "\"tab_id\":\"tab-1\"") {
		t.Fatalf("terminal hierarchy observations absent from JSON: %s", data)
	}
}

func TestTerminalEvidenceKeepsDistinctOriginsWithSameThreadPrefix(t *testing.T) {
	data := `{"supported":true,"complete":true,"sources":["ghostty","terminal-app"],"terminals":[` +
		`{"api":"ghostty-applescript","terminal_id":"surface-1","title":"` + discoveryThreadA + ` | review | grin"},` +
		`{"api":"terminal-app-applescript","terminal_id":"window:1/tty:/dev/ttys001","title":"` + discoveryThreadA + ` | review | grin"}]}`
	terminals, _, supported, complete, _, err := parseTerminalOutput("darwin", []byte(data))
	if err != nil || !supported || !complete || len(terminals) != 2 {
		t.Fatalf("multi-source observations = %#v, supported %v, complete %v, err %v", terminals, supported, complete, err)
	}
	if terminals[0].API == terminals[1].API || terminals[0].ID == terminals[1].ID {
		t.Fatalf("distinct origins or terminal IDs were merged: %#v", terminals)
	}
}

func TestWorkspaceFilterDropsOnlyKnownMismatches(t *testing.T) {
	input := []ThreadCandidate{
		{UUIDPrefix: "matching", WorkspaceMatch: "yes"},
		{UUIDPrefix: "unknown", WorkspaceMatch: "unknown"},
		{UUIDPrefix: "different", WorkspaceMatch: "no"},
	}
	filtered, removed := filterWorkspaceCandidates(input)
	if removed != 1 || len(filtered) != 2 || filtered[0].UUIDPrefix != "matching" || filtered[1].UUIDPrefix != "unknown" {
		t.Fatalf("workspace filter = %#v, removed %d", filtered, removed)
	}
}

func TestKnownTerminalWorkspaceMismatchIsFilteredWithoutCodexState(t *testing.T) {
	workspace, err := canonicalWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	otherWorkspace, err := canonicalWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{}
	result, err := service.discoverFromTitles(context.Background(), workspace, []TerminalObservation{{
		API: "fixture", ID: "terminal-1", Title: discoveryThreadA + " | unrelated title | other", Workspace: otherWorkspace,
	}}, true)
	if err != nil || len(result.Candidates) != 0 || !strings.Contains(result.Reason, "did not match") {
		t.Fatalf("known terminal mismatch = %#v, err %v", result, err)
	}
}

func TestLinuxWaylandIsUnsupported(t *testing.T) {
	_, sources, supported, complete, reason, err := parseTerminalOutput("linux", []byte("SOURCES\twayland\nUNSUPPORTED\tWayland title enumeration is unavailable and no inherited tmux pane scope was found\nCOMPLETE\tfalse\n"))
	if err != nil || supported || complete || !strings.Contains(reason, "Wayland") {
		t.Fatalf("Wayland result = supported %v, complete %v, reason %q, err %v", supported, complete, reason, err)
	}
	if len(sources) != 1 || sources[0] != "wayland" {
		t.Fatalf("unsupported source evidence = %#v", sources)
	}
}

func TestLinuxWaylandStillUsesInheritedTmuxPaneScope(t *testing.T) {
	output := "SOURCES\twayland\nSOURCE\ttmux-current-session\ntmux\t$4:%12\ttrue\ttrue\t/work/grin\t" + discoveryThreadA + " | review | grin\nSUPPORTED\nCOMPLETE\ttrue\n"
	terminals, sources, supported, complete, _, err := parseTerminalOutput("linux", []byte(output))
	if err != nil || !supported || !complete || len(terminals) != 1 {
		t.Fatalf("tmux fallback = %#v, supported %v, complete %v, err %v", terminals, supported, complete, err)
	}
	got := terminals[0]
	if got.API != "tmux" || got.ID != "$4:%12" || !got.Focused || got.Workspace != "/work/grin" || got.Title != discoveryThreadA+" | review | grin" {
		t.Fatalf("tmux observation = %#v", got)
	}
	if len(sources) != 2 || sources[1] != "tmux-current-session" {
		t.Fatalf("tmux source evidence = %#v", sources)
	}
}

func TestPositionIndependentCandidateRemainsUnresolvedAndSanitized(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed")
	}
	home := t.TempDir()
	workspace, err := canonicalWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	otherWorkspace, err := canonicalWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(home, "state_1.sqlite")
	runTestSQL(t, sqlite, database, "CREATE TABLE threads(id TEXT, cwd TEXT, thread_source TEXT, archived INTEGER); INSERT INTO threads VALUES("+sqlLiteral(discoveryThreadA)+","+sqlLiteral(workspace)+",'user',0),("+sqlLiteral(discoveryThreadB)+","+sqlLiteral(otherWorkspace)+",'user',0),("+sqlLiteral(discoveryThreadC)+",'','user',0);")
	service := &Service{sqlite: runSQLite, home: func() (string, error) { return home, nil }}
	result, err := service.discoverFromTitles(context.Background(), workspace, []TerminalObservation{
		{API: "fixture", ID: "terminal-1", Title: "Review token=discovery-secret | " + discoveryThreadA[:28] + "... | " + discoveryThreadA + " | grin", Foreground: true},
		{API: "fixture", ID: "terminal-2", Title: discoveryThreadB + " | other workspace secret | sns", Foreground: true},
		{API: "fixture", ID: "terminal-3", Title: discoveryThreadC + " | workspace unavailable | unknown", Foreground: true},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "UNRESOLVED" || len(result.Candidates) != 2 || !strings.Contains(strings.Join(result.Evidence, " "), "workspace-mismatched") {
		t.Fatalf("discovery result = %#v", result)
	}
	candidate := result.Candidates[0]
	// SQLite corroborates the workspace; without app-server proof, this is not an eligible top-level recipient.
	if candidate.ThreadID != discoveryThreadA || candidate.UUIDPrefix != discoveryThreadA || candidate.Resolution != "unique_workspace_match" || candidate.WorkspaceMatch != "state_only" || candidate.CodexWorkspace != workspace || candidate.TopLevel != "unknown" || candidate.ThreadTitle != "" || candidate.ProjectName != "" {
		t.Fatalf("candidate = %#v", candidate)
	}
	if unknown := result.Candidates[1]; unknown.ThreadID != discoveryThreadC || unknown.WorkspaceMatch != "unknown" {
		t.Fatalf("candidate with unavailable workspace was discarded or mislabeled: %#v", unknown)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "discovery-secret") || strings.Contains(string(encoded), "other workspace secret") {
		t.Fatalf("terminal title secret was not redacted: %s", encoded)
	}
}

func TestDiscoveryRejectsUnsafeAndConflictingTitleIdentities(t *testing.T) {
	workspace := t.TempDir()
	service := &Service{home: func() (string, error) { return "", errors.New("unavailable") }}
	for _, title := range []string{
		"Review token=" + discoveryThreadA,
		"Review token= " + discoveryThreadA,
		`Review token="` + discoveryThreadA + `"`,
		`{"token":"` + discoveryThreadA + `"}`,
		"https://example.test/path/" + discoveryThreadA,
		"https://example.test/?thread=" + discoveryThreadA,
		"/tmp/session/" + discoveryThreadA,
		"C:\\sessions\\" + discoveryThreadA,
		discoveryThreadA + " | " + discoveryThreadB,
		discoveryThreadA[:28] + "... | 91a142bf-dea7-7a43-a6b1-83ea32c95867",
		"-----BEGIN PRIVATE KEY----- " + discoveryThreadA,
	} {
		t.Run(title, func(t *testing.T) {
			result, err := service.discoverFromTitles(context.Background(), workspace, []TerminalObservation{{ID: "terminal", Title: title}}, true)
			if err != nil || len(result.Candidates) != 0 {
				t.Fatalf("unsafe title identity produced a candidate: result=%#v err=%v", result, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil || strings.Contains(string(encoded), "PRIVATE KEY") {
				t.Fatalf("unsafe title leaked through discovery output: %s err=%v", encoded, err)
			}
		})
	}
}

func TestDiscoveryAllowsIndependentUUIDBesideInlineSecret(t *testing.T) {
	workspace := t.TempDir()
	service := &Service{home: func() (string, error) { return "", errors.New("unavailable") }}
	result, err := service.discoverFromTitles(context.Background(), workspace, []TerminalObservation{{
		ID: "terminal", Title: "Review token=discovery-secret | " + discoveryThreadA + " | hermes",
	}}, true)
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].ThreadID != discoveryThreadA {
		t.Fatalf("independent UUID was not retained: result=%#v err=%v", result, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "discovery-secret") {
		t.Fatalf("serialized discovery leaked inline secret: %s err=%v", encoded, err)
	}
}
