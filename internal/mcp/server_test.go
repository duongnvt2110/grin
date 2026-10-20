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
	"grin/internal/codex"
	"grin/internal/config"
	"grin/internal/errs"
	"grin/internal/events"
	"grin/internal/filesystem"
	"grin/internal/policy"
	grinruntime "grin/internal/runtime"
)

const (
	testThreadID  = "01a0717c-c5db-7fb2-92df-0ce116432122"
	testRequestID = "01a0717c-c5db-7fb2-92df-0ce116432124"
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
	normalizedInstructions := strings.Join(strings.Fields(result.Instructions), " ")
	for _, want := range []string{
		"AGENTS.md",
		"codex.queue",
		"exact thread_id",
		"previous standalone task",
		"grin codex discover",
		"terminal candidates",
		"even when there is only one eligible candidate",
		"ask the user which one to select",
		"thread title",
		"no message queued",
		"Never select by result order, recency",
		"Eligible candidates that pass ambiguity checks",
		"If none is eligible",
		"fresh, read-only discovery",
		"selection_required",
		"For every new standalone send or review",
		"omit thread_id",
		"must still match a fresh eligible discovery result",
		"same uninterrupted review",
		"Selection alone does not authorize sending a message or implementing code",
		"the earlier send authorization remains applicable",
		"server enforces technical eligibility",
		"UNRESOLVED",
		"Codex app-server",
		"READY_FOR_IMPLEMENT",
		"NO_MATERIAL_FINDINGS",
	} {
		if !strings.Contains(normalizedInstructions, strings.Join(strings.Fields(want), " ")) {
			t.Fatalf("server instructions missing %q: %s", want, result.Instructions)
		}
	}
	start := strings.Index(result.Instructions, "For Codex requests, codex.queue performs fresh")
	if start < 0 {
		t.Fatal("current-TUI discovery section is missing")
	}
	end := strings.Index(result.Instructions[start:], "Codex review supports three behaviors:")
	if end < 0 {
		t.Fatal("current-TUI discovery section boundaries are missing")
	}
	workflow := result.Instructions[start : start+end]
	if strings.Contains(result.Instructions, "codex.list") || strings.Contains(result.Instructions, "codex-thread-discovery") || !strings.Contains(workflow, "grin codex discover") {
		t.Fatalf("Codex instructions advertise an external discovery path or omit Grin discovery: %s", result.Instructions)
	}
}

func TestCodexQueueDescriptionRequiresTaskRecipientSelection(t *testing.T) {
	session, server, _, _, _, _ := newCodexMCPTest(t, false, nil)
	defer session.Close()
	defer server.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "codex.queue" {
			continue
		}
		for _, want := range []string{
			"Freshly discover Codex threads",
			"Omit thread_id to request fresh candidates",
			"UNRESOLVED candidates are never selected automatically",
			"Eligible candidates that pass ambiguity checks",
			"selection_required",
			"supplied IDs must still match fresh discovery",
			"same uninterrupted review loop",
		} {
			if !strings.Contains(tool.Description, want) {
				t.Errorf("codex.queue description missing %q: %s", want, tool.Description)
			}
		}
		return
	}
	t.Fatal("codex.queue is not registered")
}

func TestCodexQueueDelegatesValidationToService(t *testing.T) {
	session, server, fake, _, _, target := newCodexMCPTest(t, false, nil)
	defer session.Close()
	defer server.Close()
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "codex.queue",
		Arguments: workspaceArgs(target, map[string]any{"thread_id": testThreadID, "message": "review"}),
	})
	if err != nil || result.IsError || !fake.queued || fake.queuedThread != testThreadID {
		t.Fatalf("queue delegation = %+v, err=%v", result, err)
	}
}

func TestCodexQueueFreshRecipientRouting(t *testing.T) {
	const otherID = "01a0717c-c5db-7fb2-92df-0ce116432123"
	eligible := func(id, terminal string) codex.ThreadCandidate {
		return codex.ThreadCandidate{
			TerminalObservation: codex.TerminalObservation{ID: terminal},
			ThreadID:            id, ThreadTitle: "candidate " + terminal,
			WorkspaceMatch: "yes", TopLevel: "yes", Resolution: "title_uuid_correlated",
		}
	}
	for _, tc := range []struct {
		name        string
		candidates  []codex.ThreadCandidate
		requestedID string
		wantID      string
		wantChoice  bool
		choiceID    string
		wantError   bool
	}{
		{name: "unresolved singleton requires explicit selection", candidates: []codex.ThreadCandidate{eligible(testThreadID, "term-a")}, wantChoice: true, choiceID: testThreadID},
		{name: "two threads require selection", candidates: []codex.ThreadCandidate{eligible(testThreadID, "term-a"), eligible(otherID, "term-b")}, wantChoice: true, choiceID: otherID},
		{name: "duplicate unresolved thread requires explicit selection", candidates: []codex.ThreadCandidate{eligible(testThreadID, "term-a"), eligible(testThreadID, "term-b")}, wantChoice: true, choiceID: testThreadID},
		{name: "explicit singleton accepted despite unresolved status", candidates: []codex.ThreadCandidate{eligible(testThreadID, "term-a")}, requestedID: testThreadID, wantID: testThreadID},
		{name: "explicit selected thread verified afresh despite unresolved status", candidates: []codex.ThreadCandidate{eligible(testThreadID, "term-a"), eligible(otherID, "term-b")}, requestedID: otherID, wantID: otherID},
		{name: "stale explicit ID rejected", candidates: []codex.ThreadCandidate{eligible(otherID, "term-b")}, requestedID: testThreadID, wantError: true},
		{name: "no candidate rejects send", wantError: true},
		{name: "unverified title rejects send", candidates: []codex.ThreadCandidate{{ThreadID: testThreadID, WorkspaceMatch: "yes", TopLevel: "unknown", Resolution: "unverified_title_uuid"}}, wantError: true},
		{name: "one eligible plus unknown thread rejects automatic send", candidates: []codex.ThreadCandidate{
			eligible(testThreadID, "term-a"),
			{ThreadID: otherID, WorkspaceMatch: "unknown", TopLevel: "unknown", Resolution: "unverified_title_uuid"},
		}, wantError: true},
		{name: "one eligible plus another partial title rejects automatic send", candidates: []codex.ThreadCandidate{
			eligible(testThreadID, "term-a"),
			{UUIDPrefix: "01a0717c-c5db", WorkspaceMatch: "unknown", TopLevel: "unknown", Resolution: "no_match"},
		}, wantError: true},
		{name: "archived title rejects send", candidates: []codex.ThreadCandidate{{ThreadID: testThreadID, WorkspaceMatch: "yes", TopLevel: "unknown", Resolution: "archived_title"}}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, server, fake, bus, _, workspace := newCodexMCPTest(t, false, nil)
			defer session.Close()
			defer server.Close()
			fake.discovery = &codex.DiscoveryResult{Status: "UNRESOLVED", Candidates: tc.candidates}
			arguments := map[string]any{"message": "review"}
			if tc.requestedID != "" {
				arguments["thread_id"] = tc.requestedID
			}
			result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
				Name: "codex.queue", Arguments: workspaceArgs(workspace, arguments),
			})
			if err != nil || result == nil || fake.discoveryCalls != 1 {
				t.Fatalf("queue/discovery call = %+v, err %v, count %d", result, err, fake.discoveryCalls)
			}
			if tc.wantError {
				if !result.IsError || fake.queued {
					t.Fatalf("unsafe recipient was queued: %+v, queued %v", result, fake.queued)
				}
				return
			}
			if result.IsError {
				t.Fatalf("queue failed: %+v", result)
			}
			if tc.wantChoice {
				if fake.queued || fake.queueCalls != 0 || len(result.Content) == 0 ||
					!strings.Contains(result.Content[0].(*sdk.TextContent).Text, "selection_required") ||
					!strings.Contains(result.Content[0].(*sdk.TextContent).Text, tc.choiceID) {
					t.Fatalf("eligible candidates did not require explicit choice: %+v", result)
				}
				foundSelectionSummary := false
				for _, event := range bus.Snapshot() {
					if event.Tool == "codex.queue" && event.Type == events.EventToolCompleted &&
						event.Summary != "Codex recipient selection required; no message queued" {
						t.Fatalf("selection event summary = %q", event.Summary)
					}
					if event.Tool == "codex.queue" && event.Type == events.EventToolCompleted {
						foundSelectionSummary = true
					}
				}
				if !foundSelectionSummary {
					t.Fatal("selection completion event was not published")
				}
				return
			}
			if !fake.queued || fake.queuedThread != tc.wantID {
				t.Fatalf("wrong target: queued %v, target %q, want %q", fake.queued, fake.queuedThread, tc.wantID)
			}
		})
	}
}

func TestCodexQueueDoesNotCachePreviousTaskRecipient(t *testing.T) {
	const newID = "01a0717c-c5db-7fb2-92df-0ce116432123"
	session, server, fake, _, _, workspace := newCodexMCPTest(t, false, nil)
	defer session.Close()
	defer server.Close()
	for index, id := range []string{testThreadID, newID} {
		fake.discovery = &codex.DiscoveryResult{Status: "UNRESOLVED", Candidates: []codex.ThreadCandidate{{
			ThreadID: id, WorkspaceMatch: "yes", TopLevel: "yes", Resolution: "title_uuid_correlated",
		}}}
		fake.queued = false
		result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
			Name: "codex.queue", Arguments: workspaceArgs(workspace, map[string]any{"thread_id": id, "message": "review"}),
		})
		if err != nil || result.IsError || !fake.queued || fake.queuedThread != id || fake.discoveryCalls != index+1 {
			t.Fatalf("send %d reused a prior ID: result %+v, err %v, routed %q, discoveries %d", index, result, err, fake.queuedThread, fake.discoveryCalls)
		}
	}
}

func TestCodexQueueDiscoveryFailureDoesNotSend(t *testing.T) {
	session, server, fake, _, _, workspace := newCodexMCPTest(t, false, nil)
	defer session.Close()
	defer server.Close()
	fake.discoveryErr = fmt.Errorf("native title API timeout")
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "codex.queue", Arguments: workspaceArgs(workspace, map[string]any{"message": "review"}),
	})
	if err != nil || result == nil || !result.IsError || fake.queued || fake.discoveryCalls != 1 {
		t.Fatalf("discovery failure should prevent send: %+v, err %v", result, err)
	}
}

func TestCodexToolsUseExplicitWorkspaceAndNormalRegistry(t *testing.T) {
	session, server, _, _, _, _ := newCodexMCPTest(t, false, nil)
	defer session.Close()
	defer server.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"codex.queue": false, "codex.turn_result": false}
	var workspaceListDescription string
	for _, tool := range tools.Tools {
		if tool.Name == "workspace.list" {
			workspaceListDescription = tool.Description
		}
		if _, ok := wanted[tool.Name]; ok {
			wanted[tool.Name] = true
		}
	}
	if !strings.Contains(workspaceListDescription, "use this only when the task's exact target path cannot be established") || !strings.Contains(workspaceListDescription, "never choose the first or only result by default") {
		t.Fatalf("workspace.list description does not treat results as task-matched candidates: %q", workspaceListDescription)
	}
	if strings.Contains(workspaceListDescription, "call this before any workspace-scoped tool") {
		t.Fatalf("workspace.list description still mandates discovery before target resolution: %q", workspaceListDescription)
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("Codex MCP tool %q was not registered", name)
		}
	}

	for _, tool := range tools.Tools {
		if tool.Name == "codex.list" {
			t.Fatal("removed codex.list is still registered")
		}
	}
}

func TestCodexToolCatalogOmitsRemovedListInYoloMode(t *testing.T) {
	session, server, _, _, _, _ := newCodexMCPTest(t, true, nil)
	defer session.Close()
	defer server.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "codex.list" {
			t.Fatal("removed codex.list is still registered in YOLO mode")
		}
	}
}

func TestCodexTurnResultUsesWorkspaceLimitsOnlyInNormalMode(t *testing.T) {
	const targetLimit = 321
	for _, test := range []struct {
		name string
		yolo bool
		want int64
	}{
		{name: "normal uses target workspace limit", want: targetLimit},
		{name: "yolo keeps startup limit", yolo: true, want: config.Defaults().Limits.MaxShellStdoutBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, server, fake, _, _, target := newCodexMCPTest(t, test.yolo, nil)
			defer session.Close()
			defer server.Close()

			configDir := filepath.Join(target, ".grin")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatal(err)
			}
			workspaceConfig := []byte("version: 1\nlimits:\n  max_shell_stdout_bytes: 321\n")
			if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), workspaceConfig, 0o600); err != nil {
				t.Fatal(err)
			}
			fake.turnResult = codex.TurnResult{Status: codex.TurnPending}

			result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
				Name:      "codex.turn_result",
				Arguments: workspaceArgs(target, map[string]any{"thread_id": testThreadID, "request_id": testRequestID}),
			})
			if err != nil || result.IsError {
				t.Fatalf("codex.turn_result = %+v, err=%v", result, err)
			}
			if fake.resultLimit != test.want {
				t.Fatalf("Codex result limit = %d, want %d", fake.resultLimit, test.want)
			}
		})
	}
}

func TestCodexQueueDoesNotExposeMessageInEventsOrResult(t *testing.T) {
	session, server, fake, bus, _, target := newCodexMCPTest(t, false, nil)
	defer session.Close()
	defer server.Close()
	const message = "GRIN_CODEX_PRIVATE_REVIEW_MESSAGE_7F91C"
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "codex.queue",
		Arguments: workspaceArgs(target, map[string]any{"thread_id": testThreadID, "message": message}),
	})
	if err != nil || result.IsError {
		t.Fatalf("codex.queue = %+v, err=%v", result, err)
	}
	if !fake.queued || fake.queuedMessage != message || fake.queuedThread != testThreadID {
		t.Fatal("Codex service did not receive the explicitly approved queue request")
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil || strings.Contains(string(encoded), message) {
		t.Fatal("codex.queue response exposed the queued message")
	}
	for _, event := range bus.Snapshot() {
		if event.Tool == "codex.queue" && (strings.Contains(event.Summary, message) || strings.Contains(event.Detail, message)) {
			t.Fatal("Codex lifecycle event exposed the queued message")
		}
		if event.Tool == "codex.queue" && event.Workspace != target {
			t.Fatal("Codex lifecycle event omitted the selected workspace")
		}
	}
}

func TestCodexQueuePolicyDenialDoesNotQueue(t *testing.T) {
	session, server, fake, _, _, target := newCodexMCPTest(t, false, denyCodexQueue{})
	defer session.Close()
	defer server.Close()

	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "codex.queue",
		Arguments: workspaceArgs(target, map[string]any{"thread_id": testThreadID, "message": "review"}),
	})
	if err != nil || !result.IsError || fake.queued || fake.discoveryCalls != 0 {
		t.Fatal("denied Codex queue performed discovery or execution")
	}
}

func TestCodexQueueApprovalRejectionAndCancellationDoNotQueue(t *testing.T) {
	for _, outcome := range []string{"reject", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			target := t.TempDir()
			registerTestWorkspace(t, target)
			cfg := config.Defaults()
			cfg.Workspace.Root = target
			files, err := filesystem.New(target, cfg.Limits)
			if err != nil {
				t.Fatal(err)
			}
			bus := events.New(20)
			reliable := bus.SubscribeReliable()
			defer reliable.Close()
			manager := approval.New(bus, time.Second)
			fake := &fakeCodexTools{}
			httpServer := httptest.NewServer(Handler(NewServer(Dependencies{
				Filesystem: files,
				Codex:      fake,
				Config:     cfg,
				Version:    "test",
				Events:     bus,
				Approval:   manager,
				Policy:     askCodexQueue{},
			})))
			defer httpServer.Close()
			client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
			session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()

			resultChannel := make(chan *sdk.CallToolResult, 1)
			errorChannel := make(chan error, 1)
			go func() {
				result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{
					Name:      "codex.queue",
					Arguments: workspaceArgs(target, map[string]any{"thread_id": testThreadID, "message": "review"}),
				})
				resultChannel <- result
				errorChannel <- callErr
			}()
			event := waitForApprovalEvent(t, reliable.Events)
			if outcome == "reject" {
				err = manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalRejected})
			} else {
				err = manager.Cancel(event.ApprovalRequestID, event.OperationDigest)
			}
			if err != nil {
				t.Fatal(err)
			}
			result := <-resultChannel
			if callErr := <-errorChannel; callErr != nil || result == nil || !result.IsError || fake.queued || fake.discoveryCalls != 0 {
				t.Fatal("rejected or cancelled Codex queue performed discovery or execution")
			}
		})
	}
}

func TestCodexTurnResultReturnsPendingForSameRequestCorrelation(t *testing.T) {
	session, server, fake, bus, _, target := newCodexMCPTest(t, false, nil)
	defer session.Close()
	defer server.Close()
	fake.turnResult = codex.TurnResult{Status: codex.TurnPending}

	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "codex.turn_result",
		Arguments: workspaceArgs(target, map[string]any{"thread_id": testThreadID, "request_id": testRequestID}),
	})
	if err != nil || result.IsError || !strings.Contains(result.Content[0].(*sdk.TextContent).Text, `"status":"pending"`) {
		t.Fatalf("pending Codex result = %+v, err=%v", result, err)
	}
	if len(fake.resultCalls) != 1 || fake.resultCalls[0] != target+"|"+testThreadID+"|"+testRequestID {
		t.Fatal("Codex result lookup did not preserve exact request correlation")
	}
	const finalText = "GRIN_CODEX_FINAL_REVIEW_TEXT_7F91C"
	fake.turnResult = codex.TurnResult{Status: codex.TurnCompleted, Text: finalText}
	completed, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "codex.turn_result",
		Arguments: workspaceArgs(target, map[string]any{"thread_id": testThreadID, "request_id": testRequestID}),
	})
	if err != nil || completed.IsError || !strings.Contains(completed.Content[0].(*sdk.TextContent).Text, finalText) {
		t.Fatal("completed Codex result did not return the final text")
	}
	for _, event := range bus.Snapshot() {
		if event.Tool == "codex.turn_result" && (strings.Contains(event.Summary, finalText) || strings.Contains(event.Detail, finalText)) {
			t.Fatal("Codex lifecycle event included result content")
		}
	}
}

type fakeCodexTools struct {
	queueErr       error
	queued         bool
	queueCalls     int
	queuedThread   string
	queuedMessage  string
	discovery      *codex.DiscoveryResult
	discoveryErr   error
	discoveryCalls int
	turnResult     codex.TurnResult
	resultCalls    []string
	resultLimit    int64
}

func (f *fakeCodexTools) Discover(_ context.Context, workspace string) (codex.DiscoveryResult, error) {
	f.discoveryCalls++
	if f.discoveryErr != nil {
		return codex.DiscoveryResult{}, f.discoveryErr
	}
	if f.discovery != nil {
		return *f.discovery, nil
	}
	return codex.DiscoveryResult{Status: "UNRESOLVED", Candidates: []codex.ThreadCandidate{{
		TerminalObservation: codex.TerminalObservation{ID: "term-a"},
		ThreadID:            testThreadID, ThreadTitle: "default test recipient", WorkspaceMatch: "yes",
		TopLevel: "yes", Resolution: "title_uuid_correlated",
	}}}, nil
}

func (f *fakeCodexTools) Queue(_ context.Context, _, threadID, message string) (codex.QueueResult, error) {
	f.queueCalls++
	if f.queueErr != nil {
		return codex.QueueResult{}, f.queueErr
	}
	f.queued = true
	f.queuedThread = threadID
	f.queuedMessage = message
	return codex.QueueResult{ThreadID: threadID, RequestID: testRequestID}, nil
}

func (f *fakeCodexTools) TurnResult(_ context.Context, workspace, threadID, requestID string, maxBytes int64) (codex.TurnResult, error) {
	f.resultCalls = append(f.resultCalls, workspace+"|"+threadID+"|"+requestID)
	f.resultLimit = maxBytes
	return f.turnResult, nil
}

type denyCodexQueue struct{}

func (denyCodexQueue) Evaluate(_ context.Context, operation policy.Operation) policy.Result {
	if operation.Tool == "codex.queue" {
		return policy.Result{Decision: policy.Deny, Reason: "test denial"}
	}
	return policy.Result{Decision: policy.Allow, Reason: "test allow"}
}

type askCodexQueue struct{}

func (askCodexQueue) Evaluate(_ context.Context, operation policy.Operation) policy.Result {
	if operation.Tool == "codex.queue" {
		return policy.Result{Decision: policy.Ask, Reason: "test approval"}
	}
	return policy.Result{Decision: policy.Allow, Reason: "test allow"}
}

func newCodexMCPTest(t *testing.T, yolo bool, evaluator policy.Evaluator) (*sdk.ClientSession, *httptest.Server, *fakeCodexTools, *events.Bus, string, string) {
	t.Helper()
	startup := t.TempDir()
	target := t.TempDir()
	if !yolo {
		registerTestWorkspace(t, target)
	}
	cfg := config.Defaults()
	cfg.Yolo = yolo
	cfg.Workspace.Root = startup
	files, err := filesystem.New(startup, cfg.Limits, yolo)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(20)
	fake := &fakeCodexTools{}
	if evaluator == nil {
		evaluator = policy.New(yolo)
	}
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{
		Filesystem: files,
		Codex:      fake,
		Config:     cfg,
		Version:    "test",
		Events:     bus,
		Policy:     evaluator,
	})))
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           httpServer.Client(),
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		httpServer.Close()
		t.Fatal(err)
	}
	canonicalStartup, err := filepath.EvalSymlinks(startup)
	if err != nil {
		t.Fatal(err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	return session, httpServer, fake, bus, canonicalStartup, canonicalTarget
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

func TestCodexSelectorRejectsInvalidSelectedWorkspaceConfigBeforeDiscovery(t *testing.T) {
	session, server, fake, _, _, target := newCodexMCPTest(t, false, nil)
	defer session.Close()
	defer server.Close()
	if err := os.MkdirAll(filepath.Join(target, ".grin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, config.WorkspaceConfigPath), []byte("version: 1\nlimits:\n  max_processes: -1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "codex.queue", Arguments: workspaceArgs(target, map[string]any{"message": "review"}),
	})
	if err != nil || result == nil || !result.IsError || !strings.Contains(result.Content[0].(*sdk.TextContent).Text, string(errs.ErrWorkspaceUnavailable)) {
		t.Fatalf("invalid Codex workspace config result = %+v, err=%v", result, err)
	}
	if fake.discoveryCalls != 0 || fake.queueCalls != 0 {
		t.Fatalf("invalid workspace config called Codex: discover=%d queue=%d", fake.discoveryCalls, fake.queueCalls)
	}
}

func TestCodexSelectorRejectsReplacedWorkspaceSymlinkBeforeDiscovery(t *testing.T) {
	session, server, fake, _, _, target := newCodexMCPTest(t, false, nil)
	defer session.Close()
	defer server.Close()
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(target, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, target); err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "codex.queue", Arguments: map[string]any{"workspace": target, "message": "review"},
	})
	if err != nil || result == nil || !result.IsError || !strings.Contains(result.Content[0].(*sdk.TextContent).Text, string(errs.ErrWorkspaceUnavailable)) {
		t.Fatalf("replaced Codex workspace result = %+v, err=%v", result, err)
	}
	if fake.discoveryCalls != 0 || fake.queueCalls != 0 {
		t.Fatalf("replaced workspace called Codex: discover=%d queue=%d", fake.discoveryCalls, fake.queueCalls)
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

func TestStreamableHTTPSearchRedactsSensitiveMatches(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	const secret = "GRIN_SEARCH_SECRET_7F91C"
	if err := os.WriteFile(filepath.Join(root, "config.txt"), []byte("API_KEY="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(Handler(NewServer(Dependencies{
		Filesystem: files,
		Config:     cfg,
		Version:    "test",
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
		Name:      "fs.search",
		Arguments: workspaceArgs(root, map[string]any{"query": "GRIN_SEARCH_SECRET"}),
	})
	if err != nil || result.IsError || len(result.Content) == 0 {
		t.Fatalf("unexpected fs.search result: %+v, err=%v", result, err)
	}
	serialized := result.Content[0].(*sdk.TextContent).Text
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"serialized": serialized, "structured": string(structured)} {
		if strings.Contains(value, secret) {
			t.Errorf("%s search result leaked sensitive content", name)
		}
		var decoded filesystem.SearchOutput
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			t.Fatalf("%s result is not valid search JSON: %v", name, err)
		}
		if len(decoded.Matches) != 1 || decoded.Matches[0].Text != "API_KEY=<redacted>" {
			t.Errorf("%s search result did not preserve the redacted match: %+v", name, decoded.Matches)
		}
	}
}

func TestStreamableHTTPReadTextBlocksSensitiveFile(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	outside := t.TempDir()
	const secret = "GRIN_BLOCKED_SECRET_7F91C"
	path := filepath.Join(outside, ".env")
	if err := os.WriteFile(path, []byte("PASSWORD="+secret+"\n"), 0o600); err != nil {
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
			"path": path,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) == 0 {
		t.Fatalf("unexpected fs.read_text result: %+v", result)
	}
	text := result.Content[0].(*sdk.TextContent).Text
	if strings.Contains(text, secret) || !strings.Contains(text, string(errs.ErrSensitiveFileBlocked)) {
		t.Fatalf("unsafe blocked-read response: %s", text)
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(structured), secret) {
		t.Fatal("StructuredContent leaked the blocked-read sentinel")
	}

	var failed bool
	for _, event := range bus.Snapshot() {
		if event.Type == events.EventApprovalRequired {
			t.Fatal("sensitive outside-workspace read requested approval")
		}
		if event.Type == events.EventToolFailed {
			failed = true
			if event.Summary != "sensitive_file_blocked" || event.Detail != "" {
				t.Fatalf("unsafe blocked-read lifecycle event: %+v", event)
			}
		}
		if strings.Contains(event.Summary, secret) || strings.Contains(event.Detail, secret) {
			t.Fatalf("lifecycle event leaked the blocked-read sentinel: %+v", event)
		}
	}
	if !failed {
		t.Fatal("blocked read did not publish a failure lifecycle event")
	}
}

func TestStreamableHTTPReadTextBlocksCanonicalSensitiveFileBeforeApproval(t *testing.T) {
	root := t.TempDir()
	registerTestWorkspace(t, root)
	outside := t.TempDir()
	const secret = "GRIN_BLOCKED_CANONICAL_SECRET_7F91C"
	target := filepath.Join(outside, ".env")
	if err := os.WriteFile(target, []byte("PASSWORD="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outside, "safe.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
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
			"path": link,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) == 0 {
		t.Fatalf("unexpected fs.read_text result: %+v", result)
	}
	text := result.Content[0].(*sdk.TextContent).Text
	if strings.Contains(text, secret) || !strings.Contains(text, string(errs.ErrSensitiveFileBlocked)) {
		t.Fatalf("unsafe canonical blocked-read response: %s", text)
	}
	for _, event := range bus.Snapshot() {
		if event.Type == events.EventApprovalRequired {
			t.Fatal("canonical sensitive outside-workspace read requested approval")
		}
		if strings.Contains(event.Summary, secret) || strings.Contains(event.Detail, secret) {
			t.Fatalf("lifecycle event leaked the canonical blocked-read sentinel: %+v", event)
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
	if len(tools.Tools) != 18 {
		t.Fatalf("expected eighteen normal-mode tools, got %d", len(tools.Tools))
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
	foundWorkspaceList := false
	for _, tool := range tools.Tools {
		if tool.Name == "workspace.list" {
			foundWorkspaceList = true
		}
	}
	if !foundWorkspaceList {
		t.Fatal("YOLO did not register workspace.list compatibility tool")
	}

	listed, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "workspace.list", Arguments: map[string]any{}})
	if err != nil || listed.IsError || !strings.Contains(listed.Content[0].(*sdk.TextContent).Text, root) {
		t.Fatalf("YOLO workspace.list = %+v, err=%v", listed, err)
	}

	infoResult, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "workspace.info", Arguments: map[string]any{}})
	if err != nil || infoResult.IsError {
		t.Fatalf("YOLO workspace.info result = %+v, err=%v", infoResult, err)
	}
	info, ok := infoResult.StructuredContent.(map[string]any)
	if !ok || info["yolo"] != true {
		t.Fatalf("workspace.info = %#v", infoResult.StructuredContent)
	}

	legacyInfo, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "workspace.info", Arguments: workspaceArgs(root, map[string]any{})})
	if err != nil || legacyInfo.IsError {
		t.Fatalf("YOLO legacy workspace.info = %+v, err=%v", legacyInfo, err)
	}

	notePath := filepath.Join(root, "note.txt")
	if err := os.WriteFile(notePath, []byte("yolo"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyRead, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: workspaceArgs(root, map[string]any{"path": "note.txt"})})
	if err != nil || legacyRead.IsError {
		t.Fatalf("YOLO legacy fs.read_text = %+v, err=%v", legacyRead, err)
	}

	rejectedPath := filepath.Join(outside, "rejected.txt")
	mismatched, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.write_text", Arguments: workspaceArgs(outside, map[string]any{"path": rejectedPath, "content": "must not write"})})
	if err != nil || !mismatched.IsError || !strings.Contains(mismatched.Content[0].(*sdk.TextContent).Text, string(errs.ErrInvalidInput)) {
		t.Fatalf("YOLO mismatched workspace = %+v, err=%v", mismatched, err)
	}
	if _, err := os.Stat(rejectedPath); !os.IsNotExist(err) {
		t.Fatalf("mismatched workspace created %q, stat err=%v", rejectedPath, err)
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
