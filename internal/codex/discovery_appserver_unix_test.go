//go:build darwin || linux

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestTitleUUIDDiscoveryWithoutSQLiteRecord(t *testing.T) {
	workspace := t.TempDir()
	canonical, err := canonicalWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		thread    appServerThread
		readError bool
		failRead  bool
		want      string
		workspace string
		topLevel  string
	}{
		{name: "corroborated", thread: appServerThread{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user"}, want: "title_uuid_correlated", workspace: "yes", topLevel: "yes"},
		{name: "wrong workspace", thread: appServerThread{ID: discoveryThreadA, CWD: t.TempDir(), ThreadSource: "user"}, want: "workspace_mismatch", workspace: "no", topLevel: "unknown"},
		{name: "child", thread: appServerThread{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user", ParentThreadID: ptrString(discoveryThreadB)}, want: "ineligible_thread", workspace: "unknown", topLevel: "no"},
		{name: "ephemeral", thread: appServerThread{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user", Ephemeral: true}, want: "ineligible_thread", workspace: "unknown", topLevel: "no"},
		{name: "non user", thread: appServerThread{ID: discoveryThreadA, CWD: canonical, ThreadSource: "subagent"}, want: "ineligible_thread", workspace: "unknown", topLevel: "no"},
		{name: "app server absent", readError: true, want: "unverified_title_uuid", workspace: "unknown", topLevel: "unknown"},
		{name: "read failure", readError: true, failRead: true, want: "unverified_title_uuid", workspace: "unknown", topLevel: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, err := os.MkdirTemp("/tmp", "g-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(home)
			if !test.readError || test.failRead {
				serveDiscoveryThread(t, home, test.thread, test.failRead)
			}
			service := &Service{home: func() (string, error) { return home, nil }, sqlite: func(context.Context, string, string) ([]byte, error) { return nil, errors.New("no persisted state") }}
			result, err := service.discoverFromTitles(context.Background(), canonical, []TerminalObservation{{Title: discoveryThreadA[:28] + "... | " + strings.ToUpper(discoveryThreadA) + " | grin"}}, true)
			if err != nil || len(result.Candidates) != 1 {
				t.Fatalf("discovery = %#v, err %v", result, err)
			}
			candidate := result.Candidates[0]
			if candidate.ThreadID != discoveryThreadA || candidate.Resolution != test.want || candidate.WorkspaceMatch != test.workspace || candidate.TopLevel != test.topLevel {
				t.Fatalf("candidate = %#v", candidate)
			}
		})
	}
}

func TestTitleUUIDDiscoveryHonorsKnownArchivedState(t *testing.T) {
	workspace := t.TempDir()
	canonical, err := canonicalWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		archived   int
		hasState   bool
		resolution string
		topLevel   string
	}{
		{name: "archived", archived: 1, hasState: true, resolution: "archived_title", topLevel: "unknown"},
		{name: "active", hasState: true, resolution: "title_uuid_correlated", topLevel: "yes"},
		{name: "missing state row", resolution: "title_uuid_correlated", topLevel: "yes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := shortDiscoveryHome(t)
			thread := appServerThread{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user"}
			serveDiscoveryThread(t, home, thread, false)
			var rows []discoveryThreadRow
			if test.hasState {
				rows = []discoveryThreadRow{{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user", Archived: test.archived}}
			}
			sqlite, _ := discoveryStateRunner(t, home, map[string][]discoveryThreadRow{
				threadExactQuery(discoveryThreadA): rows,
			})
			service := &Service{home: func() (string, error) { return home, nil }, sqlite: sqlite}
			result, err := service.discoverFromTitles(context.Background(), canonical, []TerminalObservation{{
				Title: discoveryThreadA[:28] + "... | " + strings.ToUpper(discoveryThreadA) + " | grin",
			}}, true)
			if err != nil || len(result.Candidates) != 1 {
				t.Fatalf("discovery = %#v, err %v", result, err)
			}
			candidate := result.Candidates[0]
			if candidate.ThreadID != discoveryThreadA || candidate.Resolution != test.resolution || candidate.TopLevel != test.topLevel {
				t.Fatalf("candidate = %#v", candidate)
			}
		})
	}
}

func TestConflictingTitleUUIDDoesNotFallBackToOlderPrefix(t *testing.T) {
	workspace := t.TempDir()
	canonical, err := canonicalWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	home := shortDiscoveryHome(t)
	serveDiscoveryThread(t, home, appServerThread{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user"}, false)
	prefix := discoveryThreadA[:28]
	sqlite, queries := discoveryStateRunner(t, home, map[string][]discoveryThreadRow{
		threadPrefixQuery(prefix): {{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user"}},
	})
	service := &Service{home: func() (string, error) { return home, nil }, sqlite: sqlite}
	result, err := service.discoverFromTitles(context.Background(), canonical, []TerminalObservation{{
		Title: prefix + "... | 01a142bf-dea7-7a43-a6b1-83ea32c95866 | grin",
	}}, true)
	if err != nil || len(result.Candidates) != 0 {
		t.Fatalf("discovery = %#v, err %v", result, err)
	}
	if !strings.Contains(strings.Join(result.Evidence, " "), "conflicting") {
		t.Fatalf("conflicting title identity was not reported: %#v", result)
	}
	for _, query := range *queries {
		if query == threadPrefixQuery(prefix) {
			t.Fatal("conflicting full UUID triggered SQLite prefix fallback")
		}
	}
}

func TestDescriptiveTitleStillUsesSQLitePrefixFallback(t *testing.T) {
	workspace := t.TempDir()
	canonical, err := canonicalWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	home := shortDiscoveryHome(t)
	serveDiscoveryThread(t, home, appServerThread{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user"}, false)
	prefix := discoveryThreadA[:28]
	sqlite, queries := discoveryStateRunner(t, home, map[string][]discoveryThreadRow{
		threadPrefixQuery(prefix): {{ID: discoveryThreadA, CWD: canonical, ThreadSource: "user"}},
	})
	service := &Service{home: func() (string, error) { return home, nil }, sqlite: sqlite}
	result, err := service.discoverFromTitles(context.Background(), canonical, []TerminalObservation{{
		Title: prefix + "... | A descriptive thread title | grin",
	}}, true)
	if err != nil || len(result.Candidates) != 1 {
		t.Fatalf("discovery = %#v, err %v", result, err)
	}
	candidate := result.Candidates[0]
	if candidate.ThreadID != discoveryThreadA || candidate.Resolution != "unique_workspace_match" {
		t.Fatalf("descriptive-title candidate = %#v", candidate)
	}
	for _, query := range *queries {
		if query == threadPrefixQuery(prefix) {
			return
		}
	}
	t.Fatal("descriptive title did not use SQLite prefix fallback")
}

func discoveryStateRunner(t *testing.T, home string, responses map[string][]discoveryThreadRow) (sqliteRunner, *[]string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "state_1.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	queries := []string{}
	columns := []byte(`[{"name":"id"},{"name":"cwd"},{"name":"thread_source"},{"name":"archived"}]`)
	return func(_ context.Context, _, query string) ([]byte, error) {
		if query == "PRAGMA table_info(threads)" {
			return columns, nil
		}
		queries = append(queries, query)
		rows, ok := responses[query]
		if !ok {
			return []byte("[]"), nil
		}
		return json.Marshal(rows)
	}, &queries
}

func shortDiscoveryHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "g-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

func ptrString(value string) *string { return &value }

func serveDiscoveryThread(t *testing.T, home string, thread appServerThread, failRead bool) {
	t.Helper()
	dir := filepath.Join(home, "app-server-control")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "app-server-control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if wsjson.Read(r.Context(), conn, &request) != nil {
				return
			}
			if len(request.ID) == 0 {
				continue
			}
			response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}}
			if request.Method == "thread/read" && failRead {
				response = map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"message": "not persisted"}}
			}
			if request.Method == "thread/read" && !failRead {
				response["result"] = map[string]any{"thread": thread}
			}
			if wsjson.Write(r.Context(), conn, response) != nil {
				return
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = listener.Close()
	})
}

func TestReadDiscoveryThreadDoesNotResumeOrQueue(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "g-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	socketDir := filepath.Join(home, "app-server-control")
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDir, "app-server-control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	workspace, err := canonicalWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	methods := make(chan string, 4)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if wsjson.Read(r.Context(), conn, &request) != nil {
				return
			}
			if len(request.ID) == 0 {
				continue
			}
			methods <- request.Method
			result := any(map[string]any{})
			if request.Method == "thread/read" {
				result = map[string]any{"thread": map[string]any{
					"id": discoveryThreadA, "cwd": workspace, "threadSource": "user", "ephemeral": false,
				}}
			}
			if wsjson.Write(r.Context(), conn, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
				return
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	thread, err := readDiscoveryThread(context.Background(), home, discoveryThreadA)
	if err != nil {
		t.Fatal(err)
	}
	if !sameTopLevelWorkspaceThread(thread, workspace, discoveryThreadA) {
		t.Fatalf("thread/read result was not recognized as top-level: %+v", thread)
	}
	if got := <-methods; got != "initialize" {
		t.Fatalf("first app-server method = %q", got)
	}
	if got := <-methods; got != "thread/read" {
		t.Fatalf("second app-server method = %q, want read-only thread/read", got)
	}
	select {
	case method := <-methods:
		t.Fatalf("unexpected state-changing app-server method %q", method)
	default:
	}
}
