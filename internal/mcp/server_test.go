package mcp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"grin/internal/approval"
	"grin/internal/config"
	"grin/internal/events"
	"grin/internal/filesystem"
	gringit "grin/internal/git"
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

func TestStreamableHTTPReadWorkflow(t *testing.T) {
	root := t.TempDir()
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
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New("workspace", cfg.Yolo)})))
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
	if len(tools.Tools) != 15 {
		t.Fatalf("expected fifteen V1 tools, got %d", len(tools.Tools))
	}
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: map[string]any{"path": "note.txt"}})
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
	outsideResult, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: map[string]any{"path": outsideNote}})
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

	missingRead, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: map[string]any{"path": filepath.Join(outside, "missing.txt")}})
	if err != nil || !missingRead.IsError {
		t.Fatalf("missing outside read = %+v, err=%v", missingRead, err)
	}
	for _, want := range []events.EventType{events.EventRequestStarted, events.EventPolicyDecision, events.EventToolFailed} {
		event := <-subscription.Events
		if event.Type != want {
			t.Fatalf("missing outside read event type = %s, want %s", event.Type, want)
		}
	}
	searchResult, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.search", Arguments: map[string]any{"query": "hello"}})
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
	failedSearch, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.search", Arguments: map[string]any{"query": "hello", "path": filepath.Join(outside, "missing-dir")}})
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
	server := NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New("workspace")})
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
		result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: call.name, Arguments: call.args})
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

func TestStreamableHTTPRestrictedProfilesDenyOutsideOperations(t *testing.T) {
	for _, profile := range []string{"read-only", "restricted"} {
		t.Run(profile, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			note := filepath.Join(outside, "note.txt")
			if err := os.WriteFile(note, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Defaults()
			cfg.Workspace.Root = root
			cfg.Policy.Profile = profile
			files, err := filesystem.New(root, cfg.Limits)
			if err != nil {
				t.Fatal(err)
			}
			runtimeService, err := grinruntime.New(cfg, files.Root())
			if err != nil {
				t.Fatal(err)
			}
			bus := events.New(30)
			manager := approval.New(bus, time.Second)
			httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Runtime: runtimeService, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New(profile)})))
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
				{name: "fs.write_text", args: map[string]any{"path": filepath.Join(outside, "new.txt"), "content": "blocked"}},
				{name: "shell.run", args: map[string]any{"command": "pwd", "cwd": outside}},
			} {
				result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: call.name, Arguments: call.args})
				if callErr != nil || result == nil || !result.IsError {
					t.Fatalf("%s outside operation was not denied: %+v, err=%v", call.name, result, callErr)
				}
			}
			for _, event := range bus.Snapshot() {
				if event.Type == events.EventApprovalRequired {
					t.Fatal("outside operation unexpectedly requested approval")
				}
			}
			if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
				t.Fatalf("denied outside write created a file, stat error=%v", err)
			}
		})
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

func TestStreamableHTTPWriteRequiresMatchingApproval(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Policy.Profile = "restricted"
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(10)
	reliable := bus.SubscribeReliable()
	defer reliable.Close()
	manager := approval.New(bus, time.Second)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New("restricted", cfg.Yolo)})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	call := make(chan *sdk.CallToolResult, 1)
	callErr := make(chan error, 1)
	go func() {
		result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.write_text", Arguments: map[string]any{"path": "approved.txt", "content": "approved"}})
		call <- result
		callErr <- err
	}()
	event := waitForApprovalEvent(t, reliable.Events)
	if event.ToolCallID == "" || event.ApprovalRequestID == "" || event.OperationDigest == "" {
		t.Fatalf("approval event missing identity: %+v", event)
	}
	if strings.Contains(event.Detail, "timeout_ms") || strings.Contains(event.Detail, "bytes=") || !strings.Contains(event.Detail, "reason:") {
		t.Fatalf("approval detail has the wrong display scope: %q", event.Detail)
	}
	if err := manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalAllowed}); err != nil {
		t.Fatal(err)
	}
	if err := <-callErr; err != nil {
		t.Fatal(err)
	}
	if result := <-call; result.IsError {
		t.Fatalf("approved write returned error: %+v", result)
	}
	data, err := os.ReadFile(root + "/approved.txt")
	if err != nil || string(data) != "approved" {
		t.Fatalf("approved file = %q, err=%v", data, err)
	}

	call = make(chan *sdk.CallToolResult, 1)
	callErr = make(chan error, 1)
	go func() {
		result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.write_text", Arguments: map[string]any{"path": "rejected.txt", "content": "rejected"}})
		call <- result
		callErr <- err
	}()
	event = waitForApprovalEvent(t, reliable.Events)
	if err := manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalRejected}); err != nil {
		t.Fatal(err)
	}
	if err := <-callErr; err != nil {
		t.Fatal(err)
	}
	if result := <-call; !result.IsError {
		t.Fatal("rejected write returned success")
	}
	if _, err := os.Stat(root + "/rejected.txt"); !os.IsNotExist(err) {
		t.Fatalf("rejected file exists or returned unexpected error: %v", err)
	}
	for _, event := range bus.Snapshot() {
		if event.Type == events.EventToolFailed {
			t.Fatalf("approval rejection emitted tool_failed: %+v", event)
		}
	}
}

func TestStreamableHTTPWorkspaceWriteSkipsApproval(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	files, err := filesystem.New(root, cfg.Limits, cfg.Yolo)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(10)
	manager := approval.New(bus, time.Second)
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New("workspace", cfg.Yolo)})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.write_text", Arguments: map[string]any{"path": "workspace.txt", "content": "ok"}})
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
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New("workspace", cfg.Yolo)})))
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
			result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.write_text", Arguments: map[string]any{"path": path, "content": content}})
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
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Runtime: runtimeService, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New("workspace", cfg.Yolo)})))
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
		result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "shell.run", Arguments: map[string]any{"command": "pwd", "cwd": outside}})
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
		value, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "shell.run", Arguments: map[string]any{"command": "pwd", "cwd": outside}})
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

func TestStreamableHTTPYoloBypassesApprovalAndContainment(t *testing.T) {
	root := t.TempDir()
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
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Runtime: runtimeService, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New("workspace", cfg.Yolo)})))
	defer httpServer.Close()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

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
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{Filesystem: files, Runtime: runtimeService, Config: cfg, Version: "test", Events: bus, Policy: policy.New("workspace")})))
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
	edited, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.edit_text", Arguments: map[string]any{
		"path": "edit.txt", "old_text": "before", "new_text": "after",
	}})
	if callErr != nil || edited == nil || edited.IsError {
		t.Fatalf("fs.edit_text result = %+v, err=%v", edited, callErr)
	}
	data, err := os.ReadFile(filepath.Join(root, "edit.txt"))
	if err != nil || string(data) != "after" {
		t.Fatalf("edited file = %q, err=%v", data, err)
	}

	completed, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "shell.run", Arguments: map[string]any{
		"command": "echo", "args": []string{"--token", "not-for-display"}, "timeout_ms": 5000,
	}})
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

func TestGitApprovalPrecedesRepositoryValidation(t *testing.T) {
	root := t.TempDir()
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
	reliable := bus.SubscribeReliable()
	defer reliable.Close()
	manager := approval.New(bus, time.Second)
	server := NewServer(Dependencies{
		Filesystem: files,
		Runtime:    runtimeService,
		Git:        gringit.New(runtimeService, files.Root()),
		Config:     cfg,
		Version:    "test",
		Events:     bus,
		Approval:   manager,
		Policy:     policy.New("restricted"),
	})
	httpServer := httptest.NewServer(Handler(server))
	defer httpServer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	resultCh := make(chan *sdk.CallToolResult, 1)
	go func() {
		result, _ := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "git.status", Arguments: map[string]any{}})
		resultCh <- result
	}()
	event := waitForApprovalEvent(t, reliable.Events)
	if err := manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalRejected}); err != nil {
		t.Fatal(err)
	}
	result := <-resultCh
	if result == nil || !result.IsError || len(result.Content) == 0 || !strings.Contains(result.Content[0].(*sdk.TextContent).Text, "approval_rejected") {
		t.Fatalf("rejected Git approval result = %+v", result)
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
