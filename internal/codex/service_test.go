package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"grin/internal/errs"
)

const (
	testThreadID  = "01a0717c-c5db-7fb2-92df-0ce116432122"
	testThreadID2 = "01a0717c-c5db-7fb2-92df-0ce116432123"
	testRequestID = "01a0717c-c5db-7fb2-92df-0ce116432124"
	testTurnID    = "turn-1"
	testItemID    = "01a0717c-c5db-7fb2-92df-0ce116432126"
)

func TestListCanonicalizesRequestedAndProcessWorkspaces(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	home := t.TempDir()
	service := &Service{
		home: func() (string, error) { return home, nil },
		inspect: func(context.Context, string, string) ([]Session, bool, error) {
			return []Session{{PID: 10, ThreadID: testThreadID, Workspace: root}}, true, nil
		},
	}
	result, err := service.List(context.Background(), alias)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, _ := filepath.EvalSymlinks(root)
	if !result.Running || result.Workspace != canonicalRoot || len(result.Matches) != 1 || result.Matches[0].ThreadID != testThreadID {
		t.Fatalf("canonical workspace result did not identify the matching thread: %+v", result)
	}
}

func TestListReturnsNoMatchOnlyWhenNoProcessMatchesWorkspace(t *testing.T) {
	root := t.TempDir()
	service := &Service{
		home:    func() (string, error) { return t.TempDir(), nil },
		inspect: func(context.Context, string, string) ([]Session, bool, error) { return nil, false, nil },
	}
	result, err := service.List(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Running || len(result.Matches) != 0 {
		t.Fatalf("result = %+v, want no running match", result)
	}
}

func TestListFailsClosedForInspectionAndUncorrelatedSession(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	for _, test := range []struct {
		name    string
		inspect inspector
	}{
		{name: "inspection failure", inspect: func(context.Context, string, string) ([]Session, bool, error) {
			return nil, false, errors.New("private inspection detail")
		}},
		{name: "matching process without session", inspect: func(context.Context, string, string) ([]Session, bool, error) { return nil, true, nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{home: func() (string, error) { return home, nil }, inspect: test.inspect}
			if _, err := service.List(context.Background(), root); err == nil {
				t.Fatal("List unexpectedly returned success")
			}
		})
	}
}

func TestInspectRunningSessionsSkipsSameWorkspaceHelperWithoutRollout(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Codex process discovery is macOS-only")
	}
	workspace := t.TempDir()
	home := t.TempDir()
	sessionDir := filepath.Join(home, "sessions", "2026", "09", "21")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(sessionDir, "rollout-root.jsonl")
	writeSessionMeta(t, rollout, testThreadID, workspace, "user")

	binDir := t.TempDir()
	ps := "#!/bin/sh\ncase \"$*\" in\n  *ucomm=*) printf '4055 codex\\n76716 codex\\n' ;;\n  *) printf '4055 /Users/exe-macbo\\n76716 /Users/exe-macbo\\n' ;;\nesac\n"
	lsof := `#!/bin/sh
pid=
need_pid=false
for arg do
  if [ "$need_pid" = true ]; then pid="$arg"; need_pid=false; continue; fi
  if [ "$arg" = "-p" ]; then need_pid=true; fi
done
case "$pid" in
  4055) printf 'p4055\nfcwd\nn%s\nf12\nn%s\n' "$GRIN_TEST_WORKSPACE" "$GRIN_TEST_ROLLOUT" ;;
  76716) printf 'p76716\nfcwd\nn%s\n' "$GRIN_TEST_WORKSPACE" ;;
  *) exit 1 ;;
esac
`
	for name, contents := range map[string]string{"ps": ps, "lsof": lsof} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GRIN_TEST_WORKSPACE", workspace)
	t.Setenv("GRIN_TEST_ROLLOUT", rollout)
	t.Setenv("PATH", binDir)

	sessions, matched, err := inspectRunningSessions(context.Background(), workspace, home)
	if err != nil {
		t.Fatalf("helper process prevented discovery: %v", err)
	}
	if !matched || len(sessions) != 1 || sessions[0].PID != 4055 || sessions[0].ThreadID != testThreadID {
		t.Fatalf("sessions=%+v matched=%v; want only the TUI thread", sessions, matched)
	}
}

func TestParseLsofSessionsUsesTopLevelSessionMetadata(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	sessionDir := filepath.Join(home, "sessions", "2026", "09", "21")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rootRollout := filepath.Join(sessionDir, "rollout-with-unrelated-filename.jsonl")
	writeSessionMeta(t, rootRollout, testThreadID, workspace, "user")
	subagentRollout := filepath.Join(sessionDir, "rollout-"+testThreadID2+".jsonl")
	writeSessionMeta(t, subagentRollout, testThreadID2, workspace, "subagent")
	output := "p4055\nfcwd\nn" + workspace + "\nf12\nn" + rootRollout + "\nf13\nn" + subagentRollout + "\n"
	sessions, matched, err := parseLsofSessions(4055, output, home, workspace)
	if err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, _ := filepath.EvalSymlinks(workspace)
	if !matched || len(sessions) != 1 || sessions[0].ThreadID != testThreadID || sessions[0].Workspace != canonicalWorkspace {
		t.Fatalf("session metadata did not select only the root thread: matched=%v sessions=%+v", matched, sessions)
	}
}

func TestParseLsofSessionsReturnsAllTopLevelThreads(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "rollout-one.jsonl")
	second := filepath.Join(dir, "rollout-two.jsonl")
	writeSessionMeta(t, first, testThreadID, workspace, "user")
	writeSessionMeta(t, second, testThreadID2, workspace, "user")
	output := "fcwd\nn" + workspace + "\nf8\nn" + first + "\nf9\nn" + second + "\n"
	sessions, matched, err := parseLsofSessions(8, output, home, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !matched || len(sessions) != 2 {
		t.Fatalf("sessions = %+v, matched=%v; want both usable threads", sessions, matched)
	}
}

func TestParseLsofSessionsFailsClosedForUnverifiedRollout(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	rollout := filepath.Join(home, "sessions", "rollout-unknown-source.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSessionMeta(t, rollout, testThreadID, workspace, "unknown")
	output := "fcwd\nn" + workspace + "\nf12\nn" + rollout + "\n"
	if _, _, err := parseLsofSessions(12, output, home, workspace); err == nil {
		t.Fatal("unverified open rollout was not rejected")
	}
}

func TestParseLsofSessionsFailsClosedForRolloutOutsideCodexHome(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	otherHome := t.TempDir()
	rollout := filepath.Join(otherHome, "sessions", "rollout-outside.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSessionMeta(t, rollout, testThreadID, workspace, "user")
	output := "fcwd\nn" + workspace + "\nf12\nn" + rollout + "\n"
	if _, _, err := parseLsofSessions(12, output, home, workspace); err == nil {
		t.Fatal("rollout outside the configured Codex home was not rejected")
	}
}

func TestCodexHomeRejectsAlternateRoot(t *testing.T) {
	userHome := t.TempDir()
	defaultRoot := filepath.Join(userHome, ".codex")
	otherRoot := t.TempDir()
	if err := os.Mkdir(defaultRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)
	t.Setenv("CODEX_HOME", otherRoot)
	_, err := defaultCodexHome()
	if errorCode(err) != errs.ErrUnsupported {
		t.Fatalf("alternate CODEX_HOME error = %v, want unsupported", err)
	}
}

func TestQueueRevalidatesThreadAndPrependsRequestMarker(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	var queuedThread, queuedMessage string
	service := &Service{
		home: func() (string, error) { return home, nil },
		inspect: func(context.Context, string, string) ([]Session, bool, error) {
			return []Session{{PID: 12, ThreadID: testThreadID, Workspace: workspace}}, true, nil
		},
		queue: func(_ context.Context, threadID, message string) error {
			queuedThread, queuedMessage = threadID, message
			return nil
		},
	}
	result, err := service.Queue(context.Background(), workspace, testThreadID, "review the implementation")
	if err != nil {
		t.Fatal(err)
	}
	if !validThreadID(result.RequestID) || result.ThreadID != testThreadID || queuedThread != testThreadID {
		t.Fatal("queue result did not preserve the exact thread and UUID correlation")
	}
	if !strings.HasPrefix(queuedMessage, "[grin-request-id: "+result.RequestID+"]\nreview the implementation") {
		t.Fatal("queue did not prepend the exact request marker")
	}
}

func TestQueueRejectsNonmatchingThreadBeforeCLI(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	called := false
	service := &Service{
		home: func() (string, error) { return home, nil },
		inspect: func(context.Context, string, string) ([]Session, bool, error) {
			return []Session{{PID: 12, ThreadID: testThreadID2, Workspace: workspace}}, true, nil
		},
		queue: func(context.Context, string, string) error { called = true; return nil },
	}
	_, err := service.Queue(context.Background(), workspace, testThreadID, "review")
	if err == nil || called {
		t.Fatal("nonmatching thread was queued")
	}
}

func TestQueueRejectsThreadFromDifferentWorkspaceBeforeCLI(t *testing.T) {
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	home := t.TempDir()
	called := false
	service := &Service{
		home: func() (string, error) { return home, nil },
		inspect: func(context.Context, string, string) ([]Session, bool, error) {
			return []Session{{PID: 12, ThreadID: testThreadID, Workspace: otherWorkspace}}, true, nil
		},
		queue: func(context.Context, string, string) error { called = true; return nil },
	}
	_, err := service.Queue(context.Background(), workspace, testThreadID, "review")
	if errorCode(err) != errs.ErrNotFound || called {
		t.Fatal("thread from another workspace was queued")
	}
}

func TestQueueReturnsCLIFailureWithoutRetrying(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	queueCalls := 0
	service := &Service{
		home: func() (string, error) { return home, nil },
		inspect: func(context.Context, string, string) ([]Session, bool, error) {
			return []Session{{PID: 12, ThreadID: testThreadID, Workspace: workspace}}, true, nil
		},
		queue: func(context.Context, string, string) error {
			queueCalls++
			return errors.New("private CLI error")
		},
	}
	_, err := service.Queue(context.Background(), workspace, testThreadID, "review")
	if errorCode(err) != errs.ErrWorkspaceUnavailable || queueCalls != 1 {
		t.Fatal("Codex queue failure was not returned after exactly one CLI attempt")
	}
}

func TestTurnResultPendingAndCompletedSanitized(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     string
		text       string
		wantStatus string
		wantText   string
		pending    bool
	}{
		{name: "pending before user turn is visible", pending: true, wantStatus: TurnPending},
		{name: "in progress", status: TurnInProgress, wantStatus: TurnInProgress},
		{name: "completed", status: TurnCompleted, text: "PASSWORD=GRIN_TEST_SECRET\nurl=https://user:pass@example.test", wantStatus: TurnCompleted, wantText: "PASSWORD=<redacted>\nurl=https://<redacted>@example.test"},
		{name: "failed", status: TurnFailed, wantStatus: TurnFailed},
		{name: "interrupted", status: TurnInterrupted, wantStatus: TurnInterrupted},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			home, _ := makeHistoryFixture(t, workspace, test.status, test.text, !test.pending)
			service := &Service{home: func() (string, error) { return home, nil }, sqlite: runSQLite}
			result, err := service.TurnResult(context.Background(), workspace, testThreadID, testRequestID, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", result.Status, test.wantStatus)
			}
			if test.status == TurnCompleted && result.Text != test.wantText {
				t.Fatal("completed result was not safely redacted")
			}
			if test.status != TurnCompleted && result.Text != "" {
				t.Fatal("non-completed turn returned response text")
			}
		})
	}
}

func TestRunSQLiteWorksWithSystemSQLite(t *testing.T) {
	systemSQLite := "/usr/bin/sqlite3"
	if _, err := os.Stat(systemSQLite); err != nil {
		t.Skip("system sqlite3 is unavailable")
	}
	binDir := t.TempDir()
	if err := os.Symlink(systemSQLite, filepath.Join(binDir, "sqlite3")); err != nil {
		t.Skipf("cannot create sqlite3 PATH shim: %v", err)
	}
	t.Setenv("PATH", binDir)

	output, err := runSQLite(context.Background(), ":memory:", "SELECT 1 AS value;")
	if err != nil {
		t.Fatalf("system sqlite3 invocation failed: %v", err)
	}
	var rows []struct {
		Value int `json:"value"`
	}
	if err := json.Unmarshal(output, &rows); err != nil || len(rows) != 1 || rows[0].Value != 1 {
		t.Fatalf("system sqlite3 result = %s, err=%v", output, err)
	}
}

func TestTurnResultRejectsDuplicateMarkersPrivateKeysAndOversizedText(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		duplicate bool
		maxBytes  int64
		wantCode  errs.ErrorCode
	}{
		{name: "duplicate request markers", text: "review", duplicate: true, maxBytes: 4096, wantCode: errs.ErrUnsupported},
		{name: "private key", text: "-----BEGIN RSA PRIVATE KEY-----", maxBytes: 4096, wantCode: errs.ErrSensitiveFileBlocked},
		{name: "output limit", text: strings.Repeat("x", 32), maxBytes: 8, wantCode: errs.ErrOutputLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			home, _ := makeHistoryFixture(t, workspace, TurnCompleted, test.text, true)
			if test.duplicate {
				insertDuplicateUserTurn(t, home, testThreadID, testRequestID)
			}
			service := &Service{home: func() (string, error) { return home, nil }, sqlite: runSQLite}
			_, err := service.TurnResult(context.Background(), workspace, testThreadID, testRequestID, test.maxBytes)
			if errorCode(err) != test.wantCode {
				t.Fatalf("TurnResult error code = %q, want %q", errorCode(err), test.wantCode)
			}
		})
	}
}

func TestTurnResultRequiresRegisteredWorkspaceAssociationAndUUIDs(t *testing.T) {
	workspace := t.TempDir()
	home, _ := makeHistoryFixture(t, workspace, TurnCompleted, "ok", true)
	service := &Service{home: func() (string, error) { return home, nil }, sqlite: runSQLite}
	if _, err := service.TurnResult(context.Background(), t.TempDir(), testThreadID, testRequestID, 100); errorCode(err) != errs.ErrWorkspaceNotFound {
		t.Fatalf("workspace mismatch error code = %q", errorCode(err))
	}
	if _, err := service.TurnResult(context.Background(), workspace, "not-a-uuid", testRequestID, 100); errorCode(err) != errs.ErrInvalidInput {
		t.Fatalf("invalid UUID error code = %q", errorCode(err))
	}
}

func TestTurnResultFailsClosedForUnknownSQLiteShape(t *testing.T) {
	workspace := t.TempDir()
	home, historyDB := makeHistoryFixture(t, workspace, TurnCompleted, "ok", true)
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed")
	}
	malformedTarget := `{"type":"userMessage","content":{"text":"[grin-request-id: ` + testRequestID + `]"}}`
	runTestSQL(t, sqlite, historyDB, "UPDATE thread_items SET item_json="+sqlLiteral(malformedTarget)+" WHERE item_type='userMessage';")
	service := &Service{home: func() (string, error) { return home, nil }, sqlite: runSQLite}
	if _, err := service.TurnResult(context.Background(), workspace, testThreadID, testRequestID, 100); errorCode(err) != errs.ErrUnsupported {
		t.Fatalf("malformed message-shape error code = %q", errorCode(err))
	}

	badHome := t.TempDir()
	stateDB := filepath.Join(badHome, "state_5.sqlite")
	badHistoryDB := filepath.Join(badHome, "thread_history_1.sqlite")
	runTestSQL(t, sqlite, stateDB, "CREATE TABLE threads(id TEXT, cwd TEXT);")
	runTestSQL(t, sqlite, badHistoryDB, "CREATE TABLE thread_turns(thread_id TEXT, turn_id TEXT, status TEXT, final_agent_item_id TEXT); CREATE TABLE thread_items(thread_id TEXT, turn_id TEXT, item_id TEXT, item_type TEXT, item_json TEXT);")
	service.home = func() (string, error) { return badHome, nil }
	if _, err := service.TurnResult(context.Background(), workspace, testThreadID, testRequestID, 100); errorCode(err) != errs.ErrUnsupported {
		t.Fatalf("unsupported schema error code = %q", errorCode(err))
	}
}

func TestTurnResultRejectsMalformedFinalAgentMessage(t *testing.T) {
	workspace := t.TempDir()
	home, historyDB := makeHistoryFixture(t, workspace, TurnCompleted, "requested result", true)
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed")
	}
	malformedFinal := `{"type":"agentMessage","phase":"final_answer"}`
	runTestSQL(t, sqlite, historyDB, "UPDATE thread_items SET item_json="+sqlLiteral(malformedFinal)+" WHERE item_id="+sqlLiteral(testItemID)+";")
	service := &Service{home: func() (string, error) { return home, nil }, sqlite: runSQLite}
	if _, err := service.TurnResult(context.Background(), workspace, testThreadID, testRequestID, 4096); errorCode(err) != errs.ErrUnsupported {
		t.Fatalf("malformed final agentMessage error code = %q, want %q", errorCode(err), errs.ErrUnsupported)
	}
}

func TestTurnResultIgnoresUnrelatedMalformedUserMessages(t *testing.T) {
	workspace := t.TempDir()
	home, historyDB := makeHistoryFixture(t, workspace, TurnCompleted, "requested result", true)
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed")
	}
	unrelated := `{"type":"userMessage","content":{}}`
	runTestSQL(t, sqlite, historyDB, "INSERT INTO thread_items VALUES("+sqlLiteral(testThreadID)+",'older-turn','unrelated-user-item','userMessage',"+sqlLiteral(unrelated)+");")
	service := &Service{home: func() (string, error) { return home, nil }, sqlite: runSQLite}
	result, err := service.TurnResult(context.Background(), workspace, testThreadID, testRequestID, 4096)
	if err != nil {
		t.Fatalf("TurnResult rejected unrelated malformed history: %v", err)
	}
	if result.Status != TurnCompleted || result.Text != "requested result" {
		t.Fatalf("result = %+v, want completed correlated result", result)
	}
}

func makeHistoryFixture(t *testing.T, workspace, status, finalText string, includeUser bool) (string, string) {
	t.Helper()
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed")
	}
	home := t.TempDir()
	stateDB := filepath.Join(home, "state_5.sqlite")
	historyDB := filepath.Join(home, "thread_history_1.sqlite")
	runTestSQL(t, sqlite, stateDB, "CREATE TABLE threads(id TEXT, cwd TEXT, thread_source TEXT); INSERT INTO threads VALUES("+sqlLiteral(testThreadID)+","+sqlLiteral(workspace)+",'user');")
	runTestSQL(t, sqlite, historyDB, "CREATE TABLE thread_turns(thread_id TEXT, turn_id TEXT, status TEXT, final_agent_item_id TEXT); CREATE TABLE thread_items(thread_id TEXT, turn_id TEXT, item_id TEXT, item_type TEXT, item_json TEXT);")
	if includeUser {
		userItem, err := json.Marshal(map[string]any{"type": "userMessage", "content": []any{map[string]string{"type": "text", "text": "[grin-request-id: " + testRequestID + "]\nreview"}}})
		if err != nil {
			t.Fatal(err)
		}
		runTestSQL(t, sqlite, historyDB, "INSERT INTO thread_items VALUES("+sqlLiteral(testThreadID)+","+sqlLiteral(testTurnID)+",'01a0717c-c5db-7fb2-92df-0ce116432127','userMessage',"+sqlLiteral(string(userItem))+");")
	}
	if includeUser && status != TurnPending {
		finalID := ""
		if status == TurnCompleted {
			finalID = testItemID
		}
		runTestSQL(t, sqlite, historyDB, "INSERT INTO thread_turns VALUES("+sqlLiteral(testThreadID)+","+sqlLiteral(testTurnID)+","+sqlLiteral(status)+","+sqlLiteral(finalID)+");")
		if status == TurnCompleted {
			agentItem, err := json.Marshal(map[string]any{"type": "agentMessage", "phase": "final_answer", "text": finalText})
			if err != nil {
				t.Fatal(err)
			}
			runTestSQL(t, sqlite, historyDB, "INSERT INTO thread_items VALUES("+sqlLiteral(testThreadID)+","+sqlLiteral(testTurnID)+","+sqlLiteral(testItemID)+",'agentMessage',"+sqlLiteral(string(agentItem))+");")
		}
	}
	return home, historyDB
}

func insertDuplicateUserTurn(t *testing.T, home, threadID, requestID string) {
	t.Helper()
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed")
	}
	item, err := json.Marshal(map[string]any{"type": "userMessage", "content": []any{map[string]string{"type": "text", "text": "[grin-request-id: " + requestID + "] duplicate"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "thread_history_1.sqlite")
	runTestSQL(t, sqlite, path, "INSERT INTO thread_items VALUES("+sqlLiteral(threadID)+",'turn-duplicate','01a0717c-c5db-7fb2-92df-0ce116432129','userMessage',"+sqlLiteral(string(item))+");")
}

func runTestSQL(t *testing.T, sqlite, path, query string) {
	t.Helper()
	command := exec.Command(sqlite, path, query)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		t.Fatal("could not prepare the Codex history fixture")
	}
}

func writeSessionMeta(t *testing.T, path, threadID, workspace, source string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"id": threadID, "cwd": workspace, "thread_source": source})
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(map[string]any{"type": "session_meta", "payload": json.RawMessage(payload)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func errorCode(err error) errs.ErrorCode {
	var typed errs.Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}
