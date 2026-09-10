package security

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"grin/internal/approval"
	"grin/internal/config"
	"grin/internal/errs"
	"grin/internal/events"
	"grin/internal/filesystem"
	"grin/internal/mcp"
	"grin/internal/policy"
	grinruntime "grin/internal/runtime"
)

func TestSecurityHelperProcess(t *testing.T) {
	if os.Getenv("GRIN_SECURITY_HELPER") != "1" {
		return
	}
	separator := 0
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index + 1
			break
		}
	}
	if separator == 0 || separator >= len(os.Args) {
		os.Exit(2)
	}
	switch os.Args[separator] {
	case "create":
		if separator+1 >= len(os.Args) {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Args[separator+1], []byte("created"), 0o600); err != nil {
			os.Exit(3)
		}
	case "args":
		_, _ = os.Stdout.WriteString(strings.Join(os.Args[separator+1:], "\n"))
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func TestSecurityMCPDestructiveApprovalCannotBeBypassed(t *testing.T) {
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
	server := mcp.NewServer(mcp.Dependencies{Filesystem: files, Runtime: runtimeService, Config: cfg, Version: "test", Events: bus, Approval: manager, Policy: policy.New("workspace")})
	httpServer := httptest.NewServer(mcp.Handler(server))
	defer httpServer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "security-test", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	type callResult struct {
		result *sdk.CallToolResult
		err    error
	}
	outsideRoot := t.TempDir()
	target := filepath.Join(outsideRoot, "approved.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := make(chan callResult, 1)
	go func() {
		result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "shell.run", Arguments: map[string]any{
			"command": "/bin/rm",
			"args":    []string{target},
		}})
		results <- callResult{result: result, err: callErr}
	}()
	event := waitForApproval(t, reliable.Events)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("shell executed before approval, stat error=%v", err)
	}
	if err := manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalRejected}); err != nil {
		t.Fatal(err)
	}
	call := <-results
	if call.err != nil || call.result == nil || !call.result.IsError {
		t.Fatalf("rejected shell result = %+v, err=%v", call.result, call.err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("rejected shell removed target, stat error=%v", err)
	}

	approvedTarget := filepath.Join(outsideRoot, "approved-after-consent.txt")
	if err := os.WriteFile(approvedTarget, []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	results = make(chan callResult, 1)
	go func() {
		result, callErr := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "shell.run", Arguments: map[string]any{
			"command": "/bin/rm",
			"args":    []string{approvedTarget},
		}})
		results <- callResult{result: result, err: callErr}
	}()
	event = waitForApproval(t, reliable.Events)
	if err := manager.Resolve(approval.ApprovalDecision{ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, Outcome: events.ApprovalAllowed}); err != nil {
		t.Fatal(err)
	}
	call = <-results
	if call.err != nil || call.result == nil || call.result.IsError {
		t.Fatalf("approved shell result = %+v, err=%v", call.result, call.err)
	}
	if _, err := os.Stat(approvedTarget); !os.IsNotExist(err) {
		t.Fatalf("approved shell did not remove target, stat error=%v", err)
	}
}

func TestSecurityWorkspaceAndLoopbackBoundaries(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	cfg.Server.Bind = "0.0.0.0"
	if err := config.Validate(cfg); err == nil {
		t.Fatal("non-loopback bind was accepted")
	}

	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := files.ReadText(context.Background(), filesystem.ReadInput{Path: outside}, false); errorCode(err) != "outside_workspace" {
		t.Fatalf("absolute outside read error = %v", err)
	}
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := files.ReadText(context.Background(), filesystem.ReadInput{Path: "outside-link"}, false); errorCode(err) != "outside_workspace" {
		t.Fatalf("symlink outside read error = %v", err)
	}
}

func TestSecurityMCPRejectsUnknownFields(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	files, err := filesystem.New(root, cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(mcp.Dependencies{Filesystem: files, Config: cfg, Version: "test", Events: events.New(10)})
	httpServer := httptest.NewServer(mcp.Handler(server))
	defer httpServer.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "security-test", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: httpServer.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fs.read_text", Arguments: map[string]any{"path": "note.txt", "extra": true}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) == 0 || !strings.Contains(result.Content[0].(*sdk.TextContent).Text, "invalid_input") {
		t.Fatalf("unknown field result = %+v", result)
	}
}

func TestSecurityShellArgumentsAreNotInterpreted(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = root
	cfg.Shell.AllowedEnvironment = append(cfg.Shell.AllowedEnvironment, "GRIN_SECURITY_HELPER")
	t.Setenv("GRIN_SECURITY_HELPER", "1")
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	runtimeService, err := grinruntime.New(cfg, canonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "must-not-exist")
	result, err := runtimeService.RunCommand(context.Background(), grinruntime.RunCommandRequest{Executable: os.Args[0], Args: []string{"-test.run=TestSecurityHelperProcess", "--", "args", "$(touch", marker + ")"}, Cwd: ".", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "$(touch") {
		t.Fatalf("argument was not passed literally: %q", result.Stdout)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell interpreted argument, stat error=%v", err)
	}
}

func errorCode(err error) string {
	if value, ok := err.(errs.Error); ok {
		return string(value.Code)
	}
	if err == nil {
		return ""
	}
	text := err.Error()
	if index := strings.IndexByte(text, ':'); index >= 0 {
		return text[:index]
	}
	return text
}

func waitForApproval(t *testing.T, stream <-chan events.Event) events.Event {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-stream:
			if event.Type == events.EventApprovalRequired {
				return event
			}
		case <-deadline:
			t.Fatal("timed out waiting for approval")
		}
	}
}
