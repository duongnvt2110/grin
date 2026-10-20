package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"grin/internal/errs"
)

const (
	testThreadID  = "01a0717c-c5db-7fb2-92df-0ce116432122"
	testThreadID2 = "01a0717c-c5db-7fb2-92df-0ce116432123"
	testThreadID3 = "01a0717c-c5db-7fb2-92df-0ce116432125"
	testRequestID = "01a0717c-c5db-7fb2-92df-0ce116432124"
	testTurnID    = "turn-1"
	testItemID    = "01a0717c-c5db-7fb2-92df-0ce116432126"
)

func boolPointer(value bool) *bool { return &value }

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

func TestDefaultUserHomeFallsBackToCurrentAccountWhenHomeUnset(t *testing.T) {
	t.Setenv("HOME", "")
	current, err := user.Current()
	if err != nil {
		t.Skipf("current OS account is unavailable: %v", err)
	}
	got, err := defaultUserHome()
	if err != nil {
		t.Fatal(err)
	}
	if got != current.HomeDir {
		t.Fatalf("default user home = %q, want current account home %q", got, current.HomeDir)
	}
}

func TestDiscoveryCodexHomeUsesCurrentAccountWhenHomeUnset(t *testing.T) {
	t.Setenv("HOME", "")
	current, err := user.Current()
	if err != nil {
		t.Skipf("current OS account is unavailable: %v", err)
	}
	want, err := canonicalExistingDirectory(filepath.Join(current.HomeDir, ".codex"))
	if err != nil {
		t.Skipf("current account has no Codex data directory: %v", err)
	}
	got, err := New().codexHome()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("default Codex home = %q, want same-account root %q", got, want)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	if _, err := New().codexHome(); errorCode(err) != errs.ErrUnsupported {
		t.Fatalf("alternate CODEX_HOME with HOME unset error = %v, want unsupported", err)
	}
}

func TestCodexLiveQueueAndTurnResult(t *testing.T) {
	if os.Getenv("GRIN_CODEX_LIVE_TEST") != "1" {
		t.Skip("set GRIN_CODEX_LIVE_TEST=1 with a disposable thread ID and workspace to run")
	}
	threadID := strings.TrimSpace(os.Getenv("GRIN_CODEX_LIVE_THREAD_ID"))
	workspace := strings.TrimSpace(os.Getenv("GRIN_CODEX_LIVE_WORKSPACE"))
	if threadID == "" || workspace == "" {
		t.Fatal("GRIN_CODEX_LIVE_THREAD_ID and GRIN_CODEX_LIVE_WORKSPACE are required when live test is enabled")
	}
	if !validThreadID(threadID) {
		t.Fatal("GRIN_CODEX_LIVE_THREAD_ID must be the exact UUID from the designated disposable TUI's /status")
	}
	if _, err := canonicalWorkspace(workspace); err != nil {
		t.Fatalf("GRIN_CODEX_LIVE_WORKSPACE must be an existing absolute workspace: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	service := New()
	queued, err := service.Queue(ctx, workspace, threadID, "In this disposable workspace, run sleep 10 and do not create or change files. Then reply exactly GRIN_LIVE_INTEGRATION_COMPLETE.")
	if err != nil {
		t.Fatalf("queue disposable thread %s: %v", threadID, err)
	}
	if queued.ThreadID != strings.ToLower(threadID) || !validThreadID(queued.RequestID) {
		t.Fatalf("queue acknowledgement did not preserve the target and return a valid request ID: %+v", queued)
	}
	t.Logf("accepted disposable request thread_id=%s request_id=%s", queued.ThreadID, queued.RequestID)

	deadline := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	observedInProgress := false
	for {
		result, err := service.TurnResult(ctx, workspace, queued.ThreadID, queued.RequestID, 4096)
		if err != nil {
			t.Fatalf("retrieve same request thread_id=%s request_id=%s: %v", queued.ThreadID, queued.RequestID, err)
		}
		switch result.Status {
		case TurnPending:
		case TurnInProgress:
			observedInProgress = true
		case TurnCompleted:
			if !observedInProgress {
				t.Fatalf("request completed before inProgress was observed; thread_id=%s request_id=%s", queued.ThreadID, queued.RequestID)
			}
			if strings.TrimSpace(result.Text) != "GRIN_LIVE_INTEGRATION_COMPLETE" {
				t.Fatalf("unexpected completed response for request %s: %q", queued.RequestID, result.Text)
			}
			return
		case TurnFailed, TurnInterrupted:
			t.Fatalf("request %s ended with status %s", queued.RequestID, result.Status)
		default:
			t.Fatalf("request %s returned unsupported status %q", queued.RequestID, result.Status)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for request thread_id=%s request_id=%s (observed inProgress=%t): %v", queued.ThreadID, queued.RequestID, observedInProgress, ctx.Err())
		case <-deadline.C:
		}
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

func TestResolveVersionedDatabaseSelectsHighestNumericCandidate(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"state_9.sqlite", "state_10.sqlite", "state_01.sqlite", "state_x.sqlite", "state_8.sqlite-wal", "state_backup.sqlite"} {
		if err := os.WriteFile(filepath.Join(home, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(home, "state_12.sqlite"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "state.sqlite")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "state_13.sqlite")); err != nil && !errors.Is(err, os.ErrPermission) {
		t.Fatalf("create outside symlink: %v", err)
	}
	var queried []string
	sqlite := func(_ context.Context, path, _ string) ([]byte, error) {
		queried = append(queried, filepath.Base(path))
		return schemaJSON("id", "cwd", "thread_source"), nil
	}
	path, err := resolveStateDB(context.Background(), sqlite, home)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "state_10.sqlite" || len(queried) != 1 || queried[0] != "state_10.sqlite" {
		t.Fatalf("resolved %q after querying %v; want only state_10.sqlite", path, queried)
	}
}

func TestResolveVersionedDatabaseDoesNotFallBackFromHighestCandidate(t *testing.T) {
	for _, test := range []struct {
		name      string
		output    []byte
		runnerErr error
		cancel    bool
		wantCode  errs.ErrorCode
	}{
		{name: "missing schema", output: schemaJSON("id"), wantCode: errs.ErrUnsupported},
		{name: "unreadable", runnerErr: errors.New("read failed"), wantCode: errs.ErrUnsupported},
		{name: "corrupt output", output: []byte("not json"), wantCode: errs.ErrUnsupported},
		{name: "cancelled", runnerErr: context.Canceled, cancel: true, wantCode: errs.ErrCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			for _, name := range []string{"state_8.sqlite", "state_9.sqlite"} {
				if err := os.WriteFile(filepath.Join(home, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			var cancel context.CancelFunc
			if test.cancel {
				ctx, cancel = context.WithCancel(ctx)
			}
			var queried []string
			sqlite := func(context.Context, string, string) ([]byte, error) {
				queried = append(queried, "state_9.sqlite")
				if test.cancel {
					cancel()
				}
				return test.output, test.runnerErr
			}
			_, err := resolveStateDB(ctx, sqlite, home)
			if errorCode(err) != test.wantCode {
				t.Fatalf("resolver error code = %q, want %q (err=%v)", errorCode(err), test.wantCode, err)
			}
			if len(queried) != 1 || queried[0] != "state_9.sqlite" {
				t.Fatalf("queried %v; want only the highest candidate", queried)
			}
		})
	}
}

func schemaJSON(columns ...string) []byte {
	rows := make([]map[string]string, 0, len(columns))
	for _, column := range columns {
		rows = append(rows, map[string]string{"name": column})
	}
	data, _ := json.Marshal(rows)
	return data
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

func TestTurnResultValidatesEachRequiredSchemaOnce(t *testing.T) {
	workspace := t.TempDir()
	home, _ := makeHistoryFixture(t, workspace, TurnCompleted, "finished", true)
	checks := map[string]int{}
	service := &Service{
		home: func() (string, error) { return home, nil },
		sqlite: func(ctx context.Context, path, query string) ([]byte, error) {
			if strings.HasPrefix(query, "PRAGMA table_info(") {
				checks[query]++
			}
			return runSQLite(ctx, path, query)
		},
	}
	result, err := service.TurnResult(context.Background(), workspace, testThreadID, testRequestID, 4096)
	if err != nil || result.Status != TurnCompleted || result.Text != "finished" {
		t.Fatalf("TurnResult = %+v, err %v", result, err)
	}
	for _, table := range []string{"threads", "thread_turns", "thread_items"} {
		query := "PRAGMA table_info(" + table + ")"
		if checks[query] != 1 {
			t.Fatalf("schema check %s ran %d times; want once during database resolution", table, checks[query])
		}
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
	stateDB := filepath.Join(home, "state_6.sqlite")
	historyDB := filepath.Join(home, "thread_history_2.sqlite")
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
	path := filepath.Join(home, "thread_history_2.sqlite")
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

func errorCode(err error) errs.ErrorCode {
	var typed errs.Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}
