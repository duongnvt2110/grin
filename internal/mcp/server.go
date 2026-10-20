package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"grin/internal/approval"
	"grin/internal/codex"
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

In normal mode, workspace.list is global. Every filesystem, Git, shell, and
workspace.info request must include an absolute workspace path. Grin selects
only an exact registered workspace; it never guesses, falls back, or changes
workspace state from conversation context. First determine the target project
from the task and conversation. Use its exact known workspace path directly,
including when the project is identified by name. workspace.list discovers
registered workspaces; it does not select the target for a task. When the exact
path is unknown, use workspace.list only to find candidates matching the task.
Never choose a workspace just because it is the only or first result. If no
candidate matches clearly, ask the user which workspace to use. Reuse the
established workspace only while the task remains in that project.
YOLO mode keeps the single startup workspace behavior for filesystem, Git, and
shell tools. Codex tools always require an explicit workspace: Normal mode
requires a registered workspace, while YOLO may target any existing absolute
workspace.

For Codex requests, codex.queue performs fresh, read-only discovery inside
Grin on every send, including sends with a supplied thread_id. Do not reuse a
thread_id remembered from a previous standalone task. The optional command
grin codex discover --workspace <exact-workspace> can show terminal candidates
without driving terminal input or queuing a message.

Discovery returns thread-ID candidates, titles, terminal identities,
workspace comparisons, and eligibility corroborated by local Codex app-server metadata.
For every new standalone send or review without a UUID explicitly named by
the user, call codex.queue with message and omit thread_id. Title-derived
candidates remain UNRESOLVED, so Grin never auto-selects them, even when there
is only one eligible candidate. Eligible candidates that pass ambiguity checks
return selection_required with no message queued. Show those candidates (thread
title, shortened ID, and terminal identity) and ask the user which one to select; then
call codex.queue again with the selected full UUID. If none is eligible or
discovery is unavailable or identity is ambiguous, stop without sending. Never
select by result order, recency, window foreground, or terminal focus.
Supply thread_id only when the user explicitly names/selects it for this task
or as a follow-up in the same uninterrupted review loop. Even supplied UUIDs
must still match a fresh eligible discovery result before queueing.

Title-derived candidates remain UNRESOLVED because native APIs do not prove
freshness across /new, /clear, or /resume. A user's explicit candidate choice
designates a task recipient, not proof of the currently displayed TUI. If the
user specifically requests proof of the currently displayed TUI, require its
exact Session ID from /status in that TUI or independent identity evidence.
Selection alone does not authorize sending a message or implementing code. If
the user already asked to send a report and is now selecting its recipient,
the earlier send authorization remains applicable; no duplicate authorization
is needed.
Reuse the exact selected UUID for follow-ups in the same uninterrupted
review loop, without asking again, but let Grin validate it against fresh
discovery on every queue request. Keep each request_id paired with the returned
thread_id for codex.turn_result. Never reuse a recipient from an earlier
standalone task. The server enforces technical eligibility and a fresh
candidate match; the MCP client remains responsible for respecting the
human's explicit choice when multiple recipients exist.

Codex review supports three behaviors:

QUEUE ONLY: use this only when the user explicitly asks to send/queue a message
without waiting for or reviewing the result. Call codex.queue, return the
workspace, thread_id, and request_id, then stop.

SINGLE REVIEW: use this for a normal request to send something to Codex, ask
Codex to review once, or review with Codex when the user did not explicitly ask
for a repeated loop. Call codex.queue, then immediately call codex.turn_result
with the returned thread_id and request_id. While the result is pending or
inProgress, continue checking the same request; do not queue a duplicate. When
completed, independently review the Codex result once, present the assessment
to the user, and stop. If the result is interrupted, stop, report the
interruption, preserve workspace/thread_id/request_id, and do not automatically
queue a replacement. Retry only after the user explicitly asks. For an
explicit-thread review, reuse the same exact thread_id for a retry of that
request. For a current-TUI request, rerun grin codex discover and require an exact
thread ID from the user or independent proof if it remains unresolved. Do not automatically send findings
back to Codex. If the result is failed, stop, report the failure, preserve
workspace/thread_id/request_id, and do not automatically queue another request.
Retry only after the user explicitly asks. For an explicit-thread review,
reuse the same exact thread_id for a retry of that request. For a current-TUI
request, rerun grin codex discover; do not queue an
unresolved candidate.

REVIEW LOOP: use this only when the user explicitly asks to start/continue a
review loop, review until no material findings remain, or reach a named loop
stop condition. Call codex.queue, retrieve the exact result with
codex.turn_result, and independently review it. If material findings remain,
send only those findings back to the same Codex thread and repeat the
queue/result/review cycle. If any result is interrupted, break the automatic
loop, report the interruption, preserve workspace/thread_id/request_id, and do
not queue the next review request. If any result is failed, break the automatic
loop, report the failure, preserve workspace/thread_id/request_id, and do not
queue the next review request. Resume only after the user explicitly asks to
continue. For an explicit-thread review, reuse the same exact thread_id for
this review loop. For a current-TUI review loop, keep using the exact thread
UUID selected for that loop; do not ask again or switch recipients. Before the user
explicitly says IMPLEMENT, Codex work is review/reconciliation only and the
loop stops at READY_FOR_IMPLEMENT. Do not generate, infer, or queue IMPLEMENT;
wait for the user's explicit command. Only after that command may Codex modify
the approved production scope. After implementation, continue independent
review and corrections until NO_MATERIAL_FINDINGS.

A successful codex.queue call alone does not complete SINGLE REVIEW or REVIEW
LOOP. If tool execution cannot continue before a result becomes terminal,
return the workspace, thread_id, and request_id so the user can manually resume
that exact request later.

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
	Codex      codexTools
	Config     config.Config
	Version    string
	Events     events.Publisher
	Approval   *approval.Manager
	Policy     policy.Evaluator
}

type codexTools interface {
	Discover(context.Context, string) (codex.DiscoveryResult, error)
	Queue(context.Context, string, string, string) (codex.QueueResult, error)
	TurnResult(context.Context, string, string, string, int64) (codex.TurnResult, error)
}

type codexRecipientOption struct {
	ThreadID    string `json:"thread_id"`
	ThreadTitle string `json:"thread_title,omitempty"`
	TerminalID  string `json:"terminal_id,omitempty"`
}

// selectFreshCodexRecipient intentionally does not use persisted user choices
// or a previous request's thread ID. The caller performs discovery on every
// queue call, including when the user supplied a thread ID explicitly.
func selectFreshCodexRecipient(fresh codex.DiscoveryResult, requested string) (string, []codexRecipientOption, error) {
	options := make([]codexRecipientOption, 0, len(fresh.Candidates))
	seen := make(map[string]bool)
	unverified := make([]codex.ThreadCandidate, 0)
	if fresh.Status != "UNSUPPORTED" {
		for _, candidate := range fresh.Candidates {
			if candidate.WorkspaceMatch == "no" || candidate.TopLevel == "no" || candidate.Resolution == "archived_title" {
				continue
			}
			if candidate.ThreadID == "" || candidate.TopLevel != "yes" ||
				(candidate.WorkspaceMatch != "yes" && candidate.WorkspaceMatch != "state_only") ||
				(candidate.Resolution != "title_uuid_correlated" && candidate.Resolution != "unique_workspace_match") {
				unverified = append(unverified, candidate)
				continue
			}
			id := strings.ToLower(candidate.ThreadID)
			if seen[id] {
				continue
			}
			seen[id] = true
			options = append(options, codexRecipientOption{
				ThreadID: id, ThreadTitle: candidate.ThreadTitle, TerminalID: candidate.ID,
			})
		}
	}
	if requested != "" {
		for _, option := range options {
			if strings.EqualFold(option.ThreadID, requested) {
				return option.ThreadID, nil, nil
			}
		}
		return "", nil, errs.New(errs.ErrNotFound, "supplied Codex thread is absent from fresh eligible discovery; no message queued", false)
	}
	switch len(options) {
	case 0:
		return "", nil, errs.New(errs.ErrNotFound, "fresh discovery found no eligible Codex threads; no message queued", false)
	case 1:
		for _, candidate := range unverified {
			if candidate.ThreadID == "" || !strings.EqualFold(candidate.ThreadID, options[0].ThreadID) {
				return "", nil, errs.New(errs.ErrUnsupported, "another Codex terminal has unverified thread identity; automatic send was stopped", false)
			}
		}
		if fresh.Status == "UNRESOLVED" {
			return "", options, nil
		}
		return options[0].ThreadID, nil, nil
	default:
		return "", options, nil
	}
}

func NewServer(deps Dependencies) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "grin", Version: deps.Version}, &sdk.ServerOptions{
		Instructions: serverInstructions,
		Capabilities: &sdk.ServerCapabilities{},
	})

	registerWorkspaceTools(server, deps)
	registerCodexTools(server, deps)
	registerFilesystemTools(server, deps)
	registerRuntimeTools(server, deps)
	registerGitTools(server, deps)

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
	workspace := ""
	if isCodexTool(operation.Tool) || (!deps.Config.Yolo && isWorkspaceScopedTool(operation.Tool)) {
		workspace = deps.Config.Workspace.Root
	}
	publish(deps.Events, events.Event{ToolCallID: toolCallID, Workspace: workspace, Type: events.EventPolicyDecision, Tool: operation.Tool, Summary: decision.Reason, Status: events.StatusRunning, Time: time.Now()})
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
		request.Workspace = workspace
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

func isWorkspaceScopedTool(name string) bool {
	return name == "workspace.info" || strings.HasPrefix(name, "fs.") || strings.HasPrefix(name, "git.") || name == "shell.run" || isCodexTool(name)
}

func isCodexTool(name string) bool {
	return name == "codex.queue" || name == "codex.turn_result"
}

type rawToolHandler func(context.Context, Dependencies, string, json.RawMessage) (any, error)

func addTool(server *sdk.Server, base Dependencies, workspaceScoped bool, name, description string, inputSchema any, handler rawToolHandler) {
	if isCodexTool(name) || (workspaceScoped && !base.Config.Yolo) {
		inputSchema = workspaceSchema(inputSchema)
	}
	server.AddTool(&sdk.Tool{Name: name, Description: description, InputSchema: inputSchema}, func(ctx context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		toolCallID := fmt.Sprintf("tool-%d", atomic.AddUint64(&nextToolCallID, 1))
		deps := base
		workspace := ""
		raw := request.Params.Arguments
		if isCodexTool(name) {
			var err error
			workspace, raw, deps, err = selectCodexWorkspace(base, raw)
			if err != nil {
				publish(base.Events, events.Event{ToolCallID: toolCallID, Type: events.EventToolFailed, Tool: name, Summary: "workspace selection failed", Status: events.StatusFailed, Time: time.Now()})
				return toolError(err), nil
			}
		} else if workspaceScoped && base.Config.Yolo {
			var err error
			raw, err = normalizeYOLOWorkspace(raw, base.Filesystem.Root())
			if err != nil {
				publish(base.Events, events.Event{ToolCallID: toolCallID, Type: events.EventToolFailed, Tool: name, Summary: "workspace compatibility failed", Status: events.StatusFailed, Time: time.Now()})
				return toolError(err), nil
			}
		}
		if workspaceScoped && !base.Config.Yolo {
			var err error
			workspace, raw, deps, err = selectWorkspace(base, raw)
			if err != nil {
				publish(base.Events, events.Event{ToolCallID: toolCallID, Type: events.EventToolFailed, Tool: name, Summary: "workspace selection failed", Status: events.StatusFailed, Time: time.Now()})
				return toolError(err), nil
			}
		}
		publish(deps.Events, events.Event{ToolCallID: toolCallID, Workspace: workspace, Type: events.EventRequestStarted, Tool: name, Summary: "request received", Status: events.StatusRunning, Time: time.Now()})
		output, err := handler(ctx, deps, toolCallID, raw)
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
			if isSensitiveFileBlocked(err) {
				summary = "sensitive_file_blocked"
				displayDetail = ""
			}
			if summary == "" {
				summary = "tool failed"
			}
			publish(deps.Events, events.Event{ToolCallID: toolCallID, Workspace: workspace, Type: eventType, Tool: name, Summary: summary, Detail: displayDetail, Status: status, Time: time.Now()})
			return toolError(err), nil
		}
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			publish(deps.Events, events.Event{ToolCallID: toolCallID, Workspace: workspace, Type: events.EventToolFailed, Tool: name, Summary: "tool result could not be encoded", Status: events.StatusFailed, Time: time.Now()})
			return toolError(errs.New(errs.ErrInternal, "tool result could not be encoded", false)), nil
		}
		summary := displaySummary
		if summary == "" {
			summary = completionSummary(value)
		}
		if summary == "" {
			summary = "tool completed"
		}
		publish(deps.Events, events.Event{ToolCallID: toolCallID, Workspace: workspace, Type: events.EventToolCompleted, Tool: name, Summary: summary, Detail: displayDetail, Status: events.StatusComplete, Time: time.Now()})
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

func isSensitiveFileBlocked(err error) bool {
	var typed errs.Error
	return errors.As(err, &typed) && typed.Code == errs.ErrSensitiveFileBlocked
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
