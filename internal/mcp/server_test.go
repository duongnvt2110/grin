package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"grin/internal/approval"
	"grin/internal/config"
	"grin/internal/errs"
	"grin/internal/events"
	"grin/internal/filesystem"
	"grin/internal/policy"
	grinruntime "grin/internal/runtime"
)

func TestServerInstructionsAreExposedDuringInitialization(t *testing.T) {
	server := newTestHTTPServer(t)
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: server.URL, HTTPClient: server.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result := session.InitializeResult()
	if result == nil {
		t.Fatal("initialize result is nil")
	}
	for _, want := range []string{"AGENTS.md", "fs.read_text", "fs.search", "before acting on the"} {
		if !strings.Contains(result.Instructions, want) {
			t.Fatalf("server instructions missing %q: %s", want, result.Instructions)
		}
	}
}

func TestNormalModeRequiresWorkspaceAndListsRegistry(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(10)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus})))
	defer httpServer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "workspace.list", Arguments: map[string]any{}})
	if err != nil || listed.IsError {
		t.Fatalf("workspace.list = %+v, err=%v", listed, err)
	}
	if !strings.Contains(listed.Content[0].(*sdk.TextContent).Text, root) {
		t.Fatalf("workspace.list omitted %q: %+v", root, listed)
	}
	missing, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: map[string]any{"path": "README.md"}})
	if err != nil || !missing.IsError || !strings.Contains(missing.Content[0].(*sdk.TextContent).Text, string(errs.ErrWorkspaceRequired)) {
		t.Fatalf("missing workspace result = %+v, err=%v", missing, err)
	}
	foundSelectionFailure := false
	for _, event := range bus.Snapshot() {
		if event.Tool == "fs.read_text" && event.Type == events.EventToolFailed && event.Summary == "workspace selection failed" && event.Workspace == "" {
			foundSelectionFailure = true
		}
	}
	if !foundSelectionFailure {
		t.Fatal("missing workspace failure was not visible in the lifecycle events")
	}
	unknown, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: map[string]any{"workspace": filepath.Join(t.TempDir(), "not-registered"), "path": "README.md"}})
	if err != nil || !unknown.IsError || !strings.Contains(unknown.Content[0].(*sdk.TextContent).Text, string(errs.ErrWorkspaceNotFound)) {
		t.Fatalf("unknown workspace result = %+v, err=%v", unknown, err)
	}
	relative, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: map[string]any{"workspace": "relative", "path": "README.md"}})
	if err != nil || !relative.IsError || !strings.Contains(relative.Content[0].(*sdk.TextContent).Text, string(errs.ErrInvalidInput)) {
		t.Fatalf("relative workspace result = %+v, err=%v", relative, err)
	}
	badList, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "workspace.list", Arguments: map[string]any{"extra": true}})
	if err != nil || !badList.IsError || !strings.Contains(badList.Content[0].(*sdk.TextContent).Text, string(errs.ErrInvalidInput)) {
		t.Fatalf("workspace.list unknown field result = %+v, err=%v", badList, err)
	}
	badInfo, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "workspace.info", Arguments: workspaceArgs(root, map[string]any{"extra": true})})
	if err != nil || !badInfo.IsError || !strings.Contains(badInfo.Content[0].(*sdk.TextContent).Text, string(errs.ErrInvalidInput)) {
		t.Fatalf("workspace.info unknown field result = %+v, err=%v", badInfo, err)
	}
}

func TestNormalModeRoutesOneServerToTwoWorkspaces(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	registerTestWorkspace(t, first)
	if err := os.WriteFile(filepath.Join(first, "note.txt"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "note.txt"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Workspace.Root = first
	files, err := filesystem.New(first, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(10)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus})))
	defer httpServer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := config.RegisterWorkspace(second); err != nil {
		t.Fatal(err)
	}
	listed, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "workspace.list", Arguments: map[string]any{}})
	if err != nil || listed.IsError || !strings.Contains(listed.Content[0].(*sdk.TextContent).Text, second) {
		t.Fatalf("live workspace.list omitted newly registered workspace %q: %+v, err=%v", second, listed, err)
	}
	tests := []struct {
		root string
		want string
	}{
		{root: first, want: "first"},
		{root: second, want: "second"},
	}
	results := make(chan struct {
		root   string
		want   string
		result *sdk.CallToolResult
		err    error
	}, len(tests))
	var wait sync.WaitGroup
	for _, test := range tests {
		test := test
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: workspaceArgs(test.root, map[string]any{"path": "note.txt"})})
			results <- struct {
				root   string
				want   string
				result *sdk.CallToolResult
				err    error
			}{test.root, test.want, result, err}
		}()
	}
	wait.Wait()
	close(results)
	for result := range results {
		if result.err != nil || result.result == nil || result.result.IsError || !strings.Contains(result.result.Content[0].(*sdk.TextContent).Text, result.want) {
			t.Fatalf("workspace %q read = %+v, err=%v", result.root, result.result, result.err)
		}
	}
	seen := map[string]bool{}
	for _, event := range bus.Snapshot() {
		if event.Tool == "fs.read_text" && event.Type == events.EventRequestStarted {
			seen[event.Workspace] = true
		}
	}
	firstCanonical, _ := filepath.EvalSymlinks(first)
	secondCanonical, _ := filepath.EvalSymlinks(second)
	if !seen[firstCanonical] || !seen[secondCanonical] {
		t.Fatalf("request lifecycle workspaces = %v, want %q and %q", seen, firstCanonical, secondCanonical)
	}
}

func TestSelectedWorkspaceConfigIsValidated(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, config.WorkspaceConfigPath), []byte("version: 1\nlimits:\n  max_processes: -1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: events.New(10)})))
	defer httpServer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: workspaceArgs(root, map[string]any{"path": "README.md"})})
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(*sdk.TextContent).Text, string(errs.ErrWorkspaceUnavailable)) {
		t.Fatalf("invalid selected config result = %+v, err=%v", result, err)
	}
}

func TestWorkspaceInfoUsesSelectedWorkspace(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: events.New(10), Policy: policy.New()})))
	defer httpServer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "workspace.info", Arguments: workspaceArgs(root, map[string]any{})})
	if err != nil || result == nil || result.IsError || !strings.Contains(result.Content[0].(*sdk.TextContent).Text, root) {
		t.Fatalf("workspace.info result = %+v, err=%v", result, err)
	}
}

func TestRegisteredWorkspaceSymlinkReplacementIsUnavailable(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	registered, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: events.New(10)})))
	defer httpServer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, root); err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: map[string]any{"workspace": registered, "path": "missing.txt"}})
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(*sdk.TextContent).Text, string(errs.ErrWorkspaceUnavailable)) {
		t.Fatalf("replaced workspace result = %+v, err=%v", result, err)
	}
}

func TestStreamableHTTPReadTextRedactsSensitiveContent(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	const secret = "GRIN_TEST_SECRET_7F91C"
	content := "PASSWORD=" + secret + "\nPORT=3306\n"
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(10)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{
		Filesystem: files,
		Config:     cfg,
		Version:    "test",
		Events:     bus,
		Policy:     policy.New(false),
	})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           httpServer.Client(),
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "fs.read_text",
		Arguments: workspaceArgs(root, map[string]any{
			"path": "secret.txt",
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) == 0 {
		t.Fatalf("unexpected fs.read_text result: %+v", result)
	}

	text := result.Content[0].(*sdk.TextContent).Text
	if strings.Contains(text, secret) {
		t.Fatal("TextContent leaked the sensitive sentinel")
	}
	var decoded filesystem.ReadOutput
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("decode TextContent: %v", err)
	}
	if !strings.Contains(decoded.Content, "<redacted>") || !strings.Contains(decoded.Content, "PORT=3306") {
		t.Fatalf("unexpected decoded content: %q", decoded.Content)
	}

	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent type = %T, want map[string]any", result.StructuredContent)
	}
	structuredContent, ok := structured["content"].(string)
	if !ok {
		t.Fatalf("StructuredContent content type = %T, want string", structured["content"])
	}
	if strings.Contains(structuredContent, secret) {
		t.Fatal("StructuredContent leaked the sensitive sentinel")
	}
	if !strings.Contains(structuredContent, "<redacted>") || !strings.Contains(structuredContent, "PORT=3306") {
		t.Fatalf("unexpected structured content: %q", structuredContent)
	}
	for _, event := range bus.Snapshot() {
		if strings.Contains(event.Summary, secret) || strings.Contains(event.Detail, secret) {
			t.Fatalf("lifecycle event leaked the sensitive sentinel: %+v", event)
		}
	}
}

func TestStreamableHTTPReadWorkflow(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	if err := os.WriteFile(root+"/note.txt", []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideNote := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideNote, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(10)
	subscription := bus.Subscribe()
	defer subscription.Close()
	manager := approval.New(bus, time.Second)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New(cfg.Yolo)})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 16 {
		t.Fatalf("expected sixteen normal-mode tools, got %d", len(tools.Tools))
	}
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: workspaceArgs(root, map[string]any{"path": "note.txt"})})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.StructuredContent == nil {
		t.Fatalf("unexpected read result: %+v", result)
	}
	for _, want := range []events.EventType{events.EventRequestStarted, events.EventToolCompleted} {
		select {
		case event := <-subscription.Events:
			if event.Type != want {
				t.Fatalf("event type = %s, want %s", event.Type, want)
			}
			if event.Type == events.EventToolCompleted && event.Summary != "note.txt" {
				t.Fatalf("read completion summary = %q, want note.txt", event.Summary)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
	outsideResult, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: workspaceArgs(root, map[string]any{"path": outsideNote})})
	if err != nil {
		t.Fatal(err)
	}
	if outsideResult.IsError || outsideResult.StructuredContent == nil {
		t.Fatalf("expected outside read success: %+v", outsideResult)
	}
	for _, want := range []events.EventType{events.EventRequestStarted, events.EventPolicyDecision, events.EventToolCompleted} {
		event := <-subscription.Events
		if event.Type != want {
			t.Fatalf("outside read event type = %s, want %s", event.Type, want)
		}
	}

	missingRead, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: workspaceArgs(root, map[string]any{"path": filepath.Join(outside, "missing.txt")})})
	if err != nil || !missingRead.IsError {
		t.Fatalf("missing outside read = %+v, err=%v", missingRead, err)
	}
	for _, want := range []events.EventType{events.EventRequestStarted, events.EventPolicyDecision, events.EventToolFailed} {
		event := <-subscription.Events
		if event.Type != want {
			t.Fatalf("missing outside read event type = %s, want %s", event.Type, want)
		}
	}
	searchResult, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.search", Arguments: workspaceArgs(root, map[string]any{"query": "hello"})})
	if err != nil || searchResult.IsError {
		t.Fatalf("search result = %+v, err=%v", searchResult, err)
	}
	for _, want := range []events.EventType{events.EventRequestStarted, events.EventToolCompleted} {
		select {
		case event := <-subscription.Events:
			if event.Type != want {
				t.Fatalf("search event type = %s, want %s", event.Type, want)
			}
			if event.Type == events.EventToolCompleted && (event.Summary == "tool completed" || !strings.Contains(event.Summary, "hello")) {
				t.Fatalf("search completion summary = %q", event.Summary)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for search %s", want)
		}
	}
	failedSearch, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.search", Arguments: workspaceArgs(root, map[string]any{"query": "hello", "path": filepath.Join(outside, "missing-dir")})})
	if err != nil || !failedSearch.IsError {
		t.Fatalf("failed search result = %+v, err=%v", failedSearch, err)
	}
	for _, want := range []events.EventType{events.EventRequestStarted, events.EventPolicyDecision, events.EventToolFailed} {
		select {
		case event := <-subscription.Events:
			if event.Type != want {
				t.Fatalf("failed search event type = %s, want %s", event.Type, want)
			}
			if event.Type == events.EventToolFailed && (event.Summary == "tool failed" || !strings.Contains(event.Summary, "hello")) {
				t.Fatalf("failed search summary = %q", event.Summary)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for failed search %s", want)
		}
	}
}

func TestStreamableHTTPWorkspaceAllowsAllOutsideReads(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	outside := t.TempDir()
	note := filepath.Join(outside, "note.txt")
	if err := os.WriteFile(note, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(20)
	manager := approval.New(bus, time.Second)
	server := NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New()})
	httpServer := httptest.NewServer(Handler(server))
	defer httpServer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{name: "fs.list", args: map[string]any{"path": outside}},
		{name: "fs.stat", args: map[string]any{"path": note}},
		{name: "fs.read_text", args: map[string]any{"path": note}},
		{name: "fs.search", args: map[string]any{"path": outside, "query": "outside"}},
	} {
		result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: call.name, Arguments: workspaceArgs(root, call.args)})
		if callErr != nil || result.IsError {
			t.Fatalf("workspace outside %s = %+v, err=%v", call.name, result, callErr)
		}
	}
	for _, event := range bus.Snapshot() {
		if event.Type == events.EventApprovalRequired {
			t.Fatal("workspace outside read unexpectedly required approval")
		}
	}
}

func TestSearchSummary(t *testing.T) {
	tests := []struct {
		query string
		path  string
		want  string
	}{
		{query: "ApprovalOutcome", path: "internal", want: `"ApprovalOutcome" in internal`},
		{query: "ApprovalOutcome", want: `"ApprovalOutcome"`},
	}

	for _, test := range tests {
		if got := searchSummary(test.query, test.path); got != test.want {
			t.Fatalf("searchSummary(%q, %q) = %q, want %q", test.query, test.path, got, test.want)
		}
	}
}

func TestStreamableHTTPWorkspaceWriteSkipsApproval(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	cfg := config.Defaults()
	files, err := filesystem.New(root, cfg.Limits, cfg.Yolo)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(10)
	manager := approval.New(bus, time.Second)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New(cfg.Yolo)})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.write_text", Arguments: workspaceArgs(root, map[string]any{"path": "workspace.txt", "content": "ok"})})
	if err != nil || result.IsError {
		t.Fatalf("workspace write result = %+v, err=%v", result, err)
	}
	for _, event := range bus.Snapshot() {
		if event.Type == events.EventApprovalRequired {
			t.Fatal("workspace write unexpectedly created an approval request")
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "workspace.txt"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("workspace write = %q, err=%v", data, err)
	}
}

func TestStreamableHTTPWorkspaceOutsideWriteRequiresApproval(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	outside := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits, cfg.Yolo)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(10)
	reliable := bus.SubscribeReliable()
	defer reliable.Close()
	manager := approval.New(bus, time.Second)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New(cfg.Yolo)})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	type callResult struct {
		result *sdk.CallToolResult
		err    error
	}
	call := func(path, content string) (<-chan callResult, events.Event) {
		results := make(chan callResult, 1)
		go func() {
			result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.write_text", Arguments: workspaceArgs(root, map[string]any{"path": path, "content": content})})
			results <- callResult{result: result, err: callErr}
		}()
		return results, waitForApprovalEvent(t, reliable.Events)
	}

	rejectedPath := filepath.Join(outside, "rejected.txt")
	results, event := call(rejectedPath, "rejected")
	if !strings.Contains(event.Detail, "reason: write outside workspace") {
		t.Fatalf("outside write approval detail = %q", event.Detail)
	}
	if _, err := os.Stat(rejectedPath); !os.IsNotExist(err) {
		t.Fatalf("outside write executed before approval: %v", err)
	}
	if err := manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalRejected}); err != nil {
		t.Fatal(err)
	}
	result := <-results
	if result.err != nil || result.result == nil || !result.result.IsError {
		t.Fatalf("rejected outside write = %+v, err=%v", result.result, result.err)
	}
	if _, err := os.Stat(rejectedPath); !os.IsNotExist(err) {
		t.Fatalf("rejected outside write created a file: %v", err)
	}

	approvedPath := filepath.Join(outside, "approved.txt")
	results, event = call(approvedPath, "approved")
	if err := manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalAllowed}); err != nil {
		t.Fatal(err)
	}
	result = <-results
	if result.err != nil || result.result == nil || result.result.IsError {
		t.Fatalf("approved outside write = %+v, err=%v", result.result, result.err)
	}
	data, err := os.ReadFile(approvedPath)
	if err != nil || string(data) != "approved" {
		t.Fatalf("approved outside write = %q, err=%v", data, err)
	}
}

func TestStreamableHTTPShellOutsideCwdRequiresApproval(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits, cfg.Yolo)
	if err != nil {
		t.Fatal(err)
	}
	runtimeService, err := grinruntime.New(cfg, files.Root())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(10)
	reliable := bus.SubscribeReliable()
	defer reliable.Close()
	manager := approval.New(bus, time.Second)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Runtime: runtimeService, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New(cfg.Yolo)})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	type callResult struct {
		result *sdk.CallToolResult
		err    error
	}
	outside := filepath.Dir(root)
	results := make(chan callResult, 1)
	go func() {
		result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "shell.run", Arguments: workspaceArgs(root, map[string]any{"command": "pwd", "cwd": outside})})
		results <- callResult{result: result, err: callErr}
	}()
	event := waitForApprovalEvent(t, reliable.Events)
	if !strings.Contains(event.Detail, "reason: cwd outside workspace") {
		t.Fatalf("outside cwd approval detail = %q", event.Detail)
	}
	select {
	case result := <-results:
		t.Fatalf("outside cwd completed before approval: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	if err := manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalRejected}); err != nil {
		t.Fatal(err)
	}
	result := <-results
	if result.err != nil || result.result == nil || !result.result.IsError {
		t.Fatalf("rejected outside cwd result = %+v, err=%v", result.result, result.err)
	}

	results = make(chan callResult, 1)
	go func() {
		value, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "shell.run", Arguments: workspaceArgs(root, map[string]any{"command": "pwd", "cwd": outside})})
		results <- callResult{result: value, err: callErr}
	}()
	event = waitForApprovalEvent(t, reliable.Events)
	if err := manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalAllowed}); err != nil {
		t.Fatal(err)
	}
	result = <-results
	if result.err != nil || result.result == nil || result.result.IsError {
		t.Fatalf("approved outside cwd result = %+v, err=%v", result.result, result.err)
	}
	if len(result.result.Content) == 0 || !strings.Contains(result.result.Content[0].(*sdk.TextContent).Text, outside) {
		t.Fatalf("approved cwd result = %+v", result.result)
	}
}

func TestStreamableHTTPShellDisplayRedactsEnvironmentAssignments(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits, cfg.Yolo)
	if err != nil {
		t.Fatal(err)
	}
	runtimeService, err := grinruntime.New(cfg, files.Root())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(10)
	reliable := bus.SubscribeReliable()
	defer reliable.Close()
	manager := approval.New(bus, time.Second)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Runtime: runtimeService, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New(cfg.Yolo)})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	type callResult struct {
		result *sdk.CallToolResult
		err    error
	}
	results := make(chan callResult, 1)
	outside := filepath.Dir(root)
	go func() {
		result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "shell.run", Arguments: workspaceArgs(root, map[string]any{
			"command": "env",
			"args":    []string{"HOME=/Users/example", "GOPATH=/Users/example/go", "sh", "-c", "exit 0"},
			"cwd":     outside,
		})})
		results <- callResult{result: result, err: callErr}
	}()

	event := waitForApprovalEvent(t, reliable.Events)
	if strings.Contains(event.Summary+event.Detail, "/Users/example") {
		t.Fatalf("approval event leaked environment value: %+v", event)
	}
	if !strings.Contains(event.Summary+event.Detail, "HOME=<redacted>") || !strings.Contains(event.Summary+event.Detail, "GOPATH=<redacted>") {
		t.Fatalf("approval event omitted redacted environment names: %+v", event)
	}
	if err := manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalAllowed}); err != nil {
		t.Fatal(err)
	}
	result := <-results
	if result.err != nil || result.result == nil || result.result.IsError {
		t.Fatalf("approved shell result = %+v, err=%v", result.result, result.err)
	}

	foundCompletion := false
	for _, lifecycle := range bus.Snapshot() {
		if lifecycle.Tool != "shell.run" {
			continue
		}
		if strings.Contains(lifecycle.Summary+lifecycle.Detail, "/Users/example") {
			t.Fatalf("lifecycle event leaked environment value: %+v", lifecycle)
		}
		if lifecycle.Type == events.EventToolCompleted {
			foundCompletion = true
			if !strings.Contains(lifecycle.Summary, "HOME=<redacted>") || !strings.Contains(lifecycle.Summary, "GOPATH=<redacted>") {
				t.Fatalf("completion omitted redacted environment names: %+v", lifecycle)
			}
		}
	}
	if !foundCompletion {
		t.Fatal("shell completion event was not published")
	}
}

func TestStreamableHTTPYoloBypassesApprovalAndContainment(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "note.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	cfg.Yolo = true
	files, err := filesystem.New(root, cfg.Limits, cfg.Yolo)
	if err != nil {
		t.Fatal(err)
	}
	runtimeService, err := grinruntime.New(cfg, files.Root())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(20)
	manager := approval.New(bus, time.Second)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Runtime: runtimeService, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New(cfg.Yolo)})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "workspace.list" {
			t.Fatal("YOLO unexpectedly registered workspace.list")
		}
	}

	infoResult, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "workspace.info", Arguments: map[string]any{}})
	if err != nil || infoResult.IsError {
		t.Fatalf("YOLO workspace.info result = %+v, err=%v", infoResult, err)
	}
	info, ok := infoResult.StructuredContent.(map[string]any)
	if !ok || info["yolo"] != true {
		t.Fatalf("workspace.info = %#v", infoResult.StructuredContent)
	}

	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{name: "fs.read_text", args: map[string]any{"path": filepath.Join(outside, "note.txt")}},
		{name: "fs.write_text", args: map[string]any{"path": filepath.Join(outside, "new.txt"), "content": "yolo"}},
		{name: "shell.run", args: map[string]any{"command": "pwd", "cwd": outside}},
	} {
		result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: call.name, Arguments: call.args})
		if callErr != nil || result.IsError {
			t.Fatalf("YOLO %s result = %+v, err=%v", call.name, result, callErr)
		}
	}
	for _, event := range bus.Snapshot() {
		if event.Type == events.EventApprovalRequired {
			t.Fatal("YOLO operation unexpectedly created an approval request")
		}
	}
	data, err := os.ReadFile(filepath.Join(outside, "new.txt"))
	if err != nil || string(data) != "yolo" {
		t.Fatalf("YOLO outside write = %q, err=%v", data, err)
	}
}

func TestStreamableHTTPExecutionAndInspectionContracts(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	runtimeService, err := grinruntime.New(cfg, files.Root())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(20)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Runtime: runtimeService, Config: cfg, Version: "test", Events: bus, Policy: policy.New()})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := os.WriteFile(filepath.Join(root, "edit.txt"), []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	edited, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.edit_text", Arguments: workspaceArgs(root, map[string]any{
		"path": "edit.txt", "old_text": "before", "new_text": "after",
	})})
	if callErr != nil || edited == nil || edited.IsError {
		t.Fatalf("fs.edit_text result = %+v, err=%v", edited, callErr)
	}
	data, err := os.ReadFile(filepath.Join(root, "edit.txt"))
	if err != nil || string(data) != "after" {
		t.Fatalf("edited file = %q, err=%v", data, err)
	}

	completed, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "shell.run", Arguments: workspaceArgs(root, map[string]any{
		"command": "echo", "args": []string{"--token", "not-for-display"}, "timeout_ms": 5000,
	})})
	if callErr != nil || completed == nil || completed.IsError {
		t.Fatalf("normal shell result = %+v, err=%v", completed, callErr)
	}
	for _, event := range bus.Snapshot() {
		if event.Type == events.EventApprovalRequired {
			t.Fatal("normal shell unexpectedly required approval")
		}
	}
	if len(completed.Content) == 0 || !strings.Contains(completed.Content[0].(*sdk.TextContent).Text, "exit_code") {
		t.Fatalf("shell result was not canonical: %+v", completed)
	}
	var shellCompletion events.Event
	for _, event := range bus.Snapshot() {
		if event.Type == events.EventToolCompleted && event.Tool == "shell.run" {
			shellCompletion = event
		}
	}
	if shellCompletion.Summary == "" || shellCompletion.Summary == "tool completed" || !strings.Contains(shellCompletion.Summary, "echo") || strings.Contains(shellCompletion.Summary, "not-for-display") {
		t.Fatalf("shell completion summary = %+v", shellCompletion)
	}

	for _, call := range []struct {
		name        string
		args        map[string]any
		wantContent string
		wantSummary string
	}{
		{"system.info", map[string]any{}, "os", "inspect system"},
		{"process.list", map[string]any{}, "processes", "list processes"},
		{"process.info", map[string]any{"pid": os.Getpid()}, "process", fmt.Sprintf("process %d", os.Getpid())},
	} {
		value, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: call.name, Arguments: call.args})
		if callErr != nil || value.IsError {
			t.Fatalf("%s result = %+v, err=%v", call.name, value, callErr)
		}
		if len(value.Content) == 0 || !strings.Contains(value.Content[0].(*sdk.TextContent).Text, call.wantContent) {
			t.Fatalf("%s result missing %q: %+v", call.name, call.wantContent, value)
		}
		var completion events.Event
		for _, event := range bus.Snapshot() {
			if event.Type == events.EventToolCompleted && event.Tool == call.name {
				completion = event
			}
		}
		if completion.Summary != call.wantSummary {
			t.Fatalf("%s completion summary = %q, want %q", call.name, completion.Summary, call.wantSummary)
		}
	}
}

func waitForApprovalEvent(t *testing.T, stream <-chan events.Event) events.Event {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-stream:
			if event.Type == events.EventApprovalRequired {
				return event
			}
		case <-deadline:
			t.Fatal("timed out waiting for approval event")
		}
	}
}

func TestStreamableHTTPRejectsMalformedJSON(t *testing.T) {
	server := newTestHTTPServer(t)
	request, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader("{"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusBadRequest || response.StatusCode >= http.StatusInternalServerError {
		t.Fatalf("malformed request status = %d, want a 4xx response", response.StatusCode)
	}
}

func TestStreamableHTTPRejectsOversizedRequest(t *testing.T) {
	server := newTestHTTPServer(t)
	request, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(bytes.Repeat([]byte("x"), 8<<20+1)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want %d", response.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

func newTestHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.Defaults()
	files, err := filesystem.New(t.TempDir(), cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: events.New(10)})))
}

func registerTestWorkspace(t *testing.T, root string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if _, err := config.RegisterWorkspace(root); err != nil {
		t.Fatal(err)
	}
	workspaces, err := config.LoadRegistry()
	canonical, canonicalErr := filepath.EvalSymlinks(root)
	if err != nil || canonicalErr != nil || len(workspaces) != 1 || workspaces[0] != canonical {
		t.Fatalf("registered workspaces = %v, err=%v, want %s", workspaces, err, root)
	}
}

func workspaceArgs(root string, args map[string]any) map[string]any {
	result := make(map[string]any, len(args)+1)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		canonical = root
	}
	result["workspace"] = canonical
	for key, value := range args {
		result[key] = value
	}
	return result
}
