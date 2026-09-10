package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"grin/internal/approval"
	"grin/internal/config"
	"grin/internal/errs"
	"grin/internal/events"
	"grin/internal/filesystem"
	gringit "grin/internal/git"
	"grin/internal/policy"
	grinruntime "grin/internal/runtime"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var nextToolCallID uint64

const serverInstructions = `Grin provides local workspace capabilities subject to the active policy.

For repository coding, review, planning, or implementation tasks, read the
workspace-root AGENTS.md with fs.read_text when it exists before acting on the
repository. AGENTS.md contains workspace-specific development guidance.

Tools enforce input, resource, redaction, and runtime safety controls.
Workspace containment and approval behavior depend on the active configuration.

For simple text or file-name searches, use fs.search. For regex, glob,
file-type, or ignored-file repository searches, use rg through shell.run when
available; fall back to fs.search when rg is unavailable.`

type Dependencies struct {
	Filesystem *filesystem.Service
	Runtime    *grinruntime.Service
	Git        *gringit.Service
	Config     config.Config
	Version    string
	Events     events.Publisher
	Approval   *approval.Manager
	Policy     policy.Evaluator
}

func NewServer(deps Dependencies) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "grin", Version: deps.Version}, &sdk.ServerOptions{
		Instructions: serverInstructions,
		Capabilities: &sdk.ServerCapabilities{},
	})
	addTool(server, deps.Events, "workspace.info", "Return the configured Grin workspace.", map[string]any{"type": "object", "additionalProperties": false}, func(context.Context, string, json.RawMessage) (any, error) {
		return toolResult{Value: deps.Filesystem.Info(), Summary: "current workspace"}, nil
	})
	addTool(server, deps.Events, "fs.list", "List bounded entries subject to the active Grin policy.", objectSchema(map[string]any{
		"path":        map[string]any{"type": "string"},
		"depth":       map[string]any{"type": "integer", "minimum": 0},
		"max_entries": map[string]any{"type": "integer", "minimum": 1},
	}), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input filesystem.ListInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid fs.list arguments", false)
		}
		outside, err := deps.Filesystem.ClassifyExisting(input.Path)
		if err != nil {
			return nil, err
		}
		if outside {
			if err := authorize(ctx, deps, toolCallID, policy.Operation{Tool: "fs.list", Capability: "filesystem.read", Arguments: input, Outside: true}, "list "+displayPath(input.Path), "path: "+displayPath(input.Path), approval.RiskLow); err != nil {
				return nil, err
			}
		}
		output, err := deps.Filesystem.List(ctx, input, outside)
		if err != nil {
			return nil, err
		}
		return toolResult{Value: output, Summary: displayPath(output.Path)}, nil
	})
	addTool(server, deps.Events, "fs.stat", "Return bounded metadata for a path subject to the active Grin policy.", objectSchema(map[string]any{
		"path": map[string]any{"type": "string"},
	}, "path"), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input filesystem.StatInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid fs.stat arguments", false)
		}
		outside, err := deps.Filesystem.ClassifyExisting(input.Path)
		if err != nil {
			return nil, err
		}
		if outside {
			if err := authorize(ctx, deps, toolCallID, policy.Operation{Tool: "fs.stat", Capability: "filesystem.read", Arguments: input, Outside: true}, "stat "+displayPath(input.Path), "path: "+displayPath(input.Path), approval.RiskLow); err != nil {
				return nil, err
			}
		}
		output, err := deps.Filesystem.Stat(ctx, input, outside)
		if err != nil {
			return nil, err
		}
		return toolResult{Value: output, Summary: displayPath(output.Path)}, nil
	})
	addTool(server, deps.Events, "fs.read_text", "Read a bounded text file subject to the active Grin policy.", objectSchema(map[string]any{
		"path":      map[string]any{"type": "string"},
		"max_bytes": map[string]any{"type": "integer", "minimum": 1},
	}, "path"), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input filesystem.ReadInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid fs.read_text arguments", false)
		}
		outside, err := deps.Filesystem.ClassifyExisting(input.Path)
		if err != nil {
			return nil, err
		}
		if outside {
			if err := authorize(ctx, deps, toolCallID, policy.Operation{Tool: "fs.read_text", Capability: "filesystem.read", Arguments: input, Outside: true}, "read "+displayPath(input.Path), "path: "+displayPath(input.Path), approval.RiskLow); err != nil {
				return nil, err
			}
		}
		output, err := deps.Filesystem.ReadText(ctx, input, outside)
		if err != nil {
			return nil, err
		}
		return toolResult{Value: output, Summary: displayPath(output.Path)}, nil
	})
	addTool(server, deps.Events, "fs.search", "Search bounded text and file names subject to the active Grin policy.", objectSchema(map[string]any{
		"path":        map[string]any{"type": "string"},
		"query":       map[string]any{"type": "string"},
		"max_results": map[string]any{"type": "integer", "minimum": 1},
	}, "query"), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input filesystem.SearchInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid fs.search arguments", false)
		}
		outside, err := deps.Filesystem.ClassifyExisting(input.Path)
		if err != nil {
			return nil, err
		}
		if outside {
			if err := authorize(ctx, deps, toolCallID, policy.Operation{Tool: "fs.search", Capability: "filesystem.read", Arguments: input, Outside: true}, "search "+displayText(input.Query), "path: "+displayPath(input.Path), approval.RiskLow); err != nil {
				return nil, err
			}
		}
		output, err := deps.Filesystem.Search(ctx, input, outside)
		return toolResult{Value: output, Summary: searchSummary(input.Query, input.Path)}, err
	})
	addTool(server, deps.Events, "fs.write_text", "Write bounded text subject to the active Grin policy.", objectSchema(map[string]any{
		"path":      map[string]any{"type": "string"},
		"content":   map[string]any{"type": "string"},
		"overwrite": map[string]any{"type": "boolean"},
	}, "path", "content"), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input filesystem.WriteInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid fs.write_text arguments", false)
		}
		if err := deps.Filesystem.ValidateWrite(input); err != nil {
			return nil, err
		}
		outside, err := deps.Filesystem.ClassifyCreate(input.Path)
		if err != nil {
			return nil, err
		}
		operation := policy.Operation{Tool: "fs.write_text", Capability: "filesystem.write", Arguments: input, Outside: outside}
		if err := authorize(ctx, deps, toolCallID, operation, "write "+input.Path, "content omitted", approval.RiskHigh); err != nil {
			return nil, err
		}
		output, err := deps.Filesystem.WriteText(ctx, input, outside)
		if err != nil {
			return nil, err
		}
		return toolResult{Value: output, Summary: displayPath(output.Path)}, nil
	})
	addTool(server, deps.Events, "fs.edit_text", "Replace one exact text occurrence subject to the active Grin policy.", objectSchema(map[string]any{
		"path":     map[string]any{"type": "string"},
		"old_text": map[string]any{"type": "string"},
		"new_text": map[string]any{"type": "string"},
	}, "path", "old_text", "new_text"), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input filesystem.EditTextInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid fs.edit_text arguments", false)
		}
		if err := deps.Filesystem.ValidateEdit(input); err != nil {
			return nil, err
		}
		outside, err := deps.Filesystem.ClassifyExisting(input.Path)
		if err != nil {
			return nil, err
		}
		operation := policy.Operation{Tool: "fs.edit_text", Capability: "filesystem.write", Arguments: input, Outside: outside}
		if err := authorize(ctx, deps, toolCallID, operation, "edit "+displayPath(input.Path), "content omitted", approval.RiskHigh); err != nil {
			return nil, err
		}
		output, err := deps.Filesystem.EditText(ctx, input, outside)
		return toolResult{Value: output, Summary: "edit " + displayPath(input.Path)}, err
	})
	addTool(server, deps.Events, "shell.run", "Run one bounded executable subject to the active Grin policy.", objectSchema(map[string]any{
		"command":    map[string]any{"type": "string", "minLength": 1},
		"args":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"cwd":        map[string]any{"type": "string"},
		"timeout_ms": map[string]any{"type": "integer", "minimum": 0},
	}, "command"), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input shellRunInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid shell.run arguments", false)
		}
		if strings.TrimSpace(input.Command) == "" || input.TimeoutMS < 0 || input.TimeoutMS > math.MaxInt64/int64(time.Millisecond) {
			return nil, errs.New(errs.ErrInvalidInput, "invalid shell.run command or timeout", false)
		}
		if deps.Runtime == nil {
			return nil, errs.New(errs.ErrUnsupported, "runtime is unavailable", false)
		}
		outside, err := deps.Runtime.ClassifyCwd(input.Cwd)
		if err != nil {
			return nil, err
		}
		destructive := grinruntime.IsDestructiveCommand(input.Command, input.Args)
		operation := policy.Operation{Tool: "shell.run", Capability: "shell.execute", Arguments: input, Outside: outside, Destructive: destructive}
		detail := fmt.Sprintf("command: %s\ncwd: %s", grinruntime.RedactCommand(input.Command, input.Args), input.Cwd)
		if err := authorize(ctx, deps, toolCallID, operation, grinruntime.RedactCommand(input.Command, input.Args), detail, approval.RiskHigh); err != nil {
			return nil, err
		}
		result, err := deps.Runtime.RunCommand(ctx, grinruntime.RunCommandRequest{Executable: input.Command, Args: input.Args, Cwd: input.Cwd, Timeout: time.Duration(input.TimeoutMS) * time.Millisecond, AllowOutside: outside})
		if err != nil {
			return nil, err
		}
		return toolResult{Value: result, Summary: displayText(grinruntime.RedactCommand(input.Command, input.Args)), Detail: detail}, nil
	})
	addTool(server, deps.Events, "process.list", "List bounded, redacted process metadata.", objectSchema(map[string]any{}), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input emptyInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid process.list arguments", false)
		}
		if deps.Runtime == nil {
			return nil, errs.New(errs.ErrUnsupported, "runtime is unavailable", false)
		}
		if err := authorize(ctx, deps, toolCallID, policy.Operation{Tool: "process.list", Capability: "process.inspect", Arguments: input}, "list processes", "bounded process metadata; command secrets are redacted", approval.RiskLow); err != nil {
			return nil, err
		}
		output, err := deps.Runtime.ListProcesses(ctx, grinruntime.ListProcessesRequest{})
		if err != nil {
			return nil, err
		}
		return toolResult{Value: output, Summary: "list processes"}, nil
	})
	addTool(server, deps.Events, "process.info", "Return bounded, redacted metadata for one process.", objectSchema(map[string]any{
		"pid": map[string]any{"type": "integer", "minimum": 1},
	}, "pid"), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input processInfoInput
		if err := decode(raw, &input); err != nil || input.PID <= 0 {
			return nil, errs.New(errs.ErrInvalidInput, "invalid process.info arguments", false)
		}
		if deps.Runtime == nil {
			return nil, errs.New(errs.ErrUnsupported, "runtime is unavailable", false)
		}
		if err := authorize(ctx, deps, toolCallID, policy.Operation{Tool: "process.info", Capability: "process.inspect", Arguments: input}, fmt.Sprintf("inspect process %d", input.PID), "bounded process metadata; command secrets are redacted", approval.RiskLow); err != nil {
			return nil, err
		}
		output, err := deps.Runtime.ProcessInfo(ctx, grinruntime.ProcessInfoRequest{PID: input.PID})
		if err != nil {
			return nil, err
		}
		return toolResult{Value: output, Summary: fmt.Sprintf("process %d", input.PID)}, nil
	})
	addTool(server, deps.Events, "system.info", "Return non-secret system information.", objectSchema(map[string]any{}), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input emptyInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid system.info arguments", false)
		}
		if deps.Runtime == nil {
			return nil, errs.New(errs.ErrUnsupported, "runtime is unavailable", false)
		}
		if err := authorize(ctx, deps, toolCallID, policy.Operation{Tool: "system.info", Capability: "system.inspect", Arguments: input}, "inspect system", "OS and architecture only; no environment values", approval.RiskLow); err != nil {
			return nil, err
		}
		output, err := deps.Runtime.SystemInfo(ctx)
		if err != nil {
			return nil, err
		}
		return toolResult{Value: output, Summary: "inspect system"}, nil
	})
	addTool(server, deps.Events, "git.status", "Return bounded Git status for the workspace repository.", objectSchema(map[string]any{}), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input emptyInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid git.status arguments", false)
		}
		if deps.Git == nil {
			return nil, errs.New(errs.ErrUnsupported, "Git is unavailable", false)
		}
		operation := policy.Operation{Tool: "git.status", Capability: "git.read", Arguments: input}
		if err := authorize(ctx, deps, toolCallID, operation, "git status", "workspace Git status", approval.RiskLow); err != nil {
			return nil, err
		}
		output, err := deps.Git.Status(ctx)
		return toolResult{Value: output, Summary: "git status"}, err
	})
	addTool(server, deps.Events, "git.diff", "Return bounded Git diff for the workspace repository.", objectSchema(map[string]any{
		"staged": map[string]any{"type": "boolean"},
		"path":   map[string]any{"type": "string"},
	}), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input gringit.DiffInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid git.diff arguments", false)
		}
		if err := gringit.ValidateDiffInput(input); err != nil {
			return nil, err
		}
		if deps.Git == nil {
			return nil, errs.New(errs.ErrUnsupported, "Git is unavailable", false)
		}
		operation := policy.Operation{Tool: "git.diff", Capability: "git.read", Arguments: input}
		if err := authorize(ctx, deps, toolCallID, operation, "git diff", "workspace Git diff", approval.RiskLow); err != nil {
			return nil, err
		}
		output, err := deps.Git.Diff(ctx, input)
		return toolResult{Value: output, Summary: "git diff"}, err
	})
	addTool(server, deps.Events, "git.log", "Return bounded Git history for the workspace repository.", objectSchema(map[string]any{
		"max_entries": map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
	}), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input gringit.LogInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid git.log arguments", false)
		}
		if _, err := gringit.NormalizeLogInput(input); err != nil {
			return nil, err
		}
		if deps.Git == nil {
			return nil, errs.New(errs.ErrUnsupported, "Git is unavailable", false)
		}
		operation := policy.Operation{Tool: "git.log", Capability: "git.read", Arguments: input}
		if err := authorize(ctx, deps, toolCallID, operation, "git log", "workspace Git history", approval.RiskLow); err != nil {
			return nil, err
		}
		output, err := deps.Git.Log(ctx, input)
		return toolResult{Value: output, Summary: "git log"}, err
	})
	addTool(server, deps.Events, "git.show", "Return bounded Git revision content for the workspace repository.", objectSchema(map[string]any{
		"revision": map[string]any{"type": "string"},
		"path":     map[string]any{"type": "string"},
	}), func(ctx context.Context, toolCallID string, raw json.RawMessage) (any, error) {
		var input gringit.ShowInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid git.show arguments", false)
		}
		normalized, err := gringit.NormalizeShowInput(input)
		if err != nil {
			return nil, err
		}
		if deps.Git == nil {
			return nil, errs.New(errs.ErrUnsupported, "Git is unavailable", false)
		}
		operation := policy.Operation{Tool: "git.show", Capability: "git.read", Arguments: normalized}
		summary := "git show " + displayText(normalized.Revision)
		if normalized.Path != "" {
			summary += " " + displayPath(normalized.Path)
		}
		if err := authorize(ctx, deps, toolCallID, operation, summary, "workspace Git revision", approval.RiskLow); err != nil {
			return nil, err
		}
		output, err := deps.Git.Show(ctx, normalized)
		return toolResult{Value: output, Summary: summary}, err
	})
	return server
}

type shellRunInput struct {
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`
}

type processInfoInput struct {
	PID int `json:"pid"`
}

type emptyInput struct{}

type toolResult struct {
	Value   any
	Summary string
	Detail  string
}

func unwrapToolResult(output any) (any, string, string) {
	result, ok := output.(toolResult)
	if !ok {
		return output, "", ""
	}
	return result.Value, result.Summary, result.Detail
}

func displayPath(value string) string {
	value = displayText(value)
	if value == "" {
		return "."
	}
	return value
}

func searchSummary(query, path string) string {
	query = displayText(query)
	path = displayText(path)
	if path == "" {
		return fmt.Sprintf("%q", query)
	}
	return fmt.Sprintf("%q in %s", query, path)
}

func displayText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	const maxDisplayRunes = 160
	if len(runes) > maxDisplayRunes {
		return string(runes[:maxDisplayRunes]) + "..."
	}
	return value
}

func authorize(ctx context.Context, deps Dependencies, toolCallID string, operation policy.Operation, summary, detail string, risk approval.RiskLevel) error {
	decision := policy.Result{Decision: policy.Deny, Reason: "policy is unavailable"}
	if deps.Policy != nil {
		decision = deps.Policy.Evaluate(ctx, operation)
	}
	publish(deps.Events, events.Event{ToolCallID: toolCallID, Type: events.EventPolicyDecision, Tool: operation.Tool, Summary: decision.Reason, Status: events.StatusRunning, Time: time.Now()})
	switch decision.Decision {
	case policy.Allow:
		return nil
	case policy.Deny:
		return errs.New(errs.ErrToolDisabled, decision.Reason, false)
	case policy.Ask:
		if deps.Approval == nil {
			return errs.New(errs.ErrApprovalUnavailable, "approval is unavailable", false)
		}
		request, err := approval.NewRequest(operation, operation.Tool, summary, detail, risk)
		if err != nil {
			return err
		}
		request.Detail = strings.TrimSpace(request.Detail + "\nreason: " + decision.Reason)
		request.ToolCallID = toolCallID
		resolved, err := deps.Approval.Request(ctx, request)
		if err != nil {
			return err
		}
		if resolved.Outcome != events.ApprovalAllowed {
			return errs.New(errs.ErrApprovalRejected, "approval was rejected", false)
		}
		return nil
	default:
		return errs.New(errs.ErrToolDisabled, "invalid policy decision", false)
	}
}

type rawToolHandler func(context.Context, string, json.RawMessage) (any, error)

func addTool(server *sdk.Server, publisher events.Publisher, name, description string, inputSchema any, handler rawToolHandler) {
	server.AddTool(&sdk.Tool{Name: name, Description: description, InputSchema: inputSchema}, func(ctx context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		toolCallID := fmt.Sprintf("tool-%d", atomic.AddUint64(&nextToolCallID, 1))
		publish(publisher, events.Event{ToolCallID: toolCallID, Type: events.EventRequestStarted, Tool: name, Summary: "request received", Status: events.StatusRunning, Time: time.Now()})
		output, err := handler(ctx, toolCallID, request.Params.Arguments)
		value, displaySummary, displayDetail := unwrapToolResult(output)
		if err != nil {
			if isApprovalSettlement(err) {
				return toolError(err), nil
			}
			eventType := events.EventToolFailed
			status := events.StatusFailed
			if isCancelled(err) {
				eventType = events.EventCancelled
				status = events.StatusCancelled
			}
			summary := displaySummary
			if summary == "" {
				summary = "tool failed"
			}
			publish(publisher, events.Event{ToolCallID: toolCallID, Type: eventType, Tool: name, Summary: summary, Detail: displayDetail, Status: status, Time: time.Now()})
			return toolError(err), nil
		}
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			publish(publisher, events.Event{ToolCallID: toolCallID, Type: events.EventToolFailed, Tool: name, Summary: "tool result could not be encoded", Status: events.StatusFailed, Time: time.Now()})
			return toolError(errs.New(errs.ErrInternal, "tool result could not be encoded", false)), nil
		}
		summary := displaySummary
		if summary == "" {
			summary = completionSummary(value)
		}
		if summary == "" {
			summary = "tool completed"
		}
		publish(publisher, events.Event{ToolCallID: toolCallID, Type: events.EventToolCompleted, Tool: name, Summary: summary, Detail: displayDetail, Status: events.StatusComplete, Time: time.Now()})
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(data)}}, StructuredContent: value}, nil
	})
}

func completionSummary(output any) string {
	switch result := output.(type) {
	case filesystem.ReadOutput:
		return result.Path
	case filesystem.ListOutput:
		return result.Path
	case filesystem.StatOutput:
		return result.Path
	case filesystem.WriteOutput:
		return result.Path
	default:
		return ""
	}
}

func isCancelled(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if typed, ok := err.(errs.Error); ok {
		return typed.Code == errs.ErrCancelled
	}
	return false
}

func isApprovalSettlement(err error) bool {
	typed, ok := err.(errs.Error)
	if !ok {
		return false
	}
	switch typed.Code {
	case errs.ErrApprovalRejected, errs.ErrApprovalTimeout, errs.ErrApprovalCancelled:
		return true
	default:
		return false
	}
}

func publish(publisher events.Publisher, event events.Event) {
	if publisher != nil {
		publisher.Publish(event)
	}
}

func toolError(err error) *sdk.CallToolResult {
	var structured errs.Error
	if typed, ok := err.(errs.Error); ok {
		structured = typed
	} else {
		structured = errs.New(errs.ErrInternal, "tool execution failed", false)
	}
	data, _ := json.Marshal(structured)
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: string(data)}}, StructuredContent: structured}
}

func decode(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errs.New(errs.ErrInvalidInput, "multiple JSON values are not allowed", false)
		}
		return err
	}
	return nil
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	result := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func Handler(server *sdk.Server) http.Handler {
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		PropagateRequestCancellation: true,
		MaxRequestBodyBytes:          8 << 20,
	})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.ContentLength > maxRequestBodyBytes {
			http.Error(writer, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		originalBody := request.Body
		body, err := io.ReadAll(io.LimitReader(originalBody, maxRequestBodyBytes+1))
		_ = originalBody.Close()
		if err != nil {
			http.Error(writer, "request body could not be read", http.StatusBadRequest)
			return
		}
		if int64(len(body)) > maxRequestBodyBytes {
			http.Error(writer, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		handler.ServeHTTP(writer, request)
	})
}

const maxRequestBodyBytes int64 = 8 << 20
