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
	"os"
	"path/filepath"
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

For Codex review work, determine the task's exact target workspace before
session discovery. Call codex.list with that workspace only; never use a
workspace.list result as the target merely because it is the only result. Use
only threads returned for that exact workspace. If none match, tell the user to
open Codex there; if multiple match, ask which thread to use. Keep the exact
workspace and thread_id pair for follow-up requests in the same review. Before
queueing, use that same workspace/thread pair. On workspace change, discard
the old thread selection and call codex.list for the new exact workspace. Do
not use shell.run to drive normal Codex review work.

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
queue a replacement. Retry only after the user explicitly asks; re-run
codex.list before queueing a fresh request. Do not automatically send findings
back to Codex. If the result is failed, stop, report the failure, preserve
workspace/thread_id/request_id, and do not automatically queue another request.
Retry only after the user explicitly asks; re-run codex.list before queueing a
fresh request.

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
continue; re-run codex.list before queueing a fresh request. Before the user
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
	List(context.Context, string) (codex.ListResult, error)
	Queue(context.Context, string, string, string) (codex.QueueResult, error)
	TurnResult(context.Context, string, string, string, int64) (codex.TurnResult, error)
}

func NewServer(deps Dependencies) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "grin", Version: deps.Version}, &sdk.ServerOptions{
		Instructions: serverInstructions,
		Capabilities: &sdk.ServerCapabilities{},
	})
	addTool(server, deps, true, "workspace.info", "Return metadata for the selected workspace. In normal mode, provide the exact workspace argument. This is not a discovery or selection tool; use workspace.list when the workspace path is unknown.", map[string]any{"type": "object", "additionalProperties": false}, func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
		var input emptyInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid workspace.info arguments", false)
		}
		if err := authorize(ctx, deps, toolCallID, policy.Operation{Tool: "workspace.info", Capability: "workspace.inspect", Arguments: input}, "inspect workspace", "selected workspace metadata", approval.RiskLow); err != nil {
			return nil, err
		}
		return toolResult{Value: deps.Filesystem.Info(), Summary: "selected workspace"}, nil
	})
	addTool(server, deps, false, "workspace.list", "Discover registered Grin workspaces. In normal mode, use this only when the task's exact target path cannot be established from the task or conversation; treat results as candidates matching the task, and never choose the first or only result by default. In YOLO mode, it returns the configured startup workspace for compatibility and does not enable routing.", objectSchema(map[string]any{}), func(_ context.Context, deps Dependencies, _ string, raw json.RawMessage) (any, error) {
		var input emptyInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid workspace.list arguments", false)
		}
		if deps.Config.Yolo {
			return toolResult{Value: map[string]any{"workspaces": []string{deps.Filesystem.Root()}}, Summary: "list workspaces"}, nil
		}
		workspaces, err := config.LoadRegistry()
		if err != nil {
			return nil, errs.New(errs.ErrWorkspaceUnavailable, "workspace registry is unavailable", true)
		}
		return toolResult{Value: map[string]any{"workspaces": workspaces}, Summary: "list workspaces"}, nil
	})
	addTool(server, deps, false, "codex.list", "Find currently running top-level Codex sessions for an explicit workspace. Normal mode requires a registered workspace; YOLO accepts any existing absolute workspace.", objectSchema(map[string]any{}), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
		var input emptyInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid codex.list arguments", false)
		}
		if deps.Codex == nil {
			return nil, errs.New(errs.ErrUnsupported, "Codex tools are unavailable", false)
		}
		workspace := deps.Config.Workspace.Root
		operation := policy.Operation{Tool: "codex.list", Capability: "codex.inspect", Arguments: map[string]string{"workspace": workspace}}
		if err := authorize(ctx, deps, toolCallID, operation, "list Codex sessions", "workspace: "+workspace, approval.RiskLow); err != nil {
			return nil, err
		}
		result, err := deps.Codex.List(ctx, workspace)
		if err != nil {
			return nil, err
		}
		return toolResult{Value: result, Summary: "Codex sessions listed"}, nil
	})
	addTool(server, deps, false, "codex.queue", "Queue a message to an exact currently running Codex thread. A successful response means accepted for queueing, not that execution has started.", objectSchema(map[string]any{
		"thread_id": map[string]any{"type": "string", "format": "uuid", "minLength": 36, "maxLength": 36},
		"message":   map[string]any{"type": "string", "minLength": 1},
	}, "thread_id", "message"), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
		var input struct {
			ThreadID string `json:"thread_id"`
			Message  string `json:"message"`
		}
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid codex.queue arguments", false)
		}
		if deps.Codex == nil {
			return nil, errs.New(errs.ErrUnsupported, "Codex tools are unavailable", false)
		}
		workspace := deps.Config.Workspace.Root
		live, err := deps.Codex.List(ctx, workspace)
		if err != nil {
			return nil, err
		}
		if !live.Running || !hasCodexThread(live.Matches, input.ThreadID) {
			return nil, errs.New(errs.ErrNotFound, "the requested Codex thread is not running in this workspace", false)
		}
		operation := policy.Operation{Tool: "codex.queue", Capability: "codex.queue", Arguments: map[string]string{"thread_id": input.ThreadID}}
		if err := authorize(ctx, deps, toolCallID, operation, "queue Codex review", "thread: "+input.ThreadID, approval.RiskLow); err != nil {
			return nil, err
		}
		result, err := deps.Codex.Queue(ctx, workspace, input.ThreadID, input.Message)
		if err != nil {
			return nil, err
		}
		return toolResult{Value: result, Summary: "Codex request accepted: " + result.RequestID}, nil
	})
	addTool(server, deps, false, "codex.turn_result", "Read the exact Codex turn associated with a request ID. Pending results can be retried with the same IDs.", objectSchema(map[string]any{
		"thread_id":  map[string]any{"type": "string", "format": "uuid", "minLength": 36, "maxLength": 36},
		"request_id": map[string]any{"type": "string", "format": "uuid", "minLength": 36, "maxLength": 36},
	}, "thread_id", "request_id"), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
		var input struct {
			ThreadID  string `json:"thread_id"`
			RequestID string `json:"request_id"`
		}
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid codex.turn_result arguments", false)
		}
		if deps.Codex == nil {
			return nil, errs.New(errs.ErrUnsupported, "Codex tools are unavailable", false)
		}
		workspace := deps.Config.Workspace.Root
		operation := policy.Operation{Tool: "codex.turn_result", Capability: "codex.history.read", Arguments: map[string]string{"thread_id": input.ThreadID, "request_id": input.RequestID}}
		if err := authorize(ctx, deps, toolCallID, operation, "read Codex result", "thread: "+input.ThreadID, approval.RiskLow); err != nil {
			return nil, err
		}
		result, err := deps.Codex.TurnResult(ctx, workspace, input.ThreadID, input.RequestID, deps.Config.Limits.MaxShellStdoutBytes)
		if err != nil {
			return nil, err
		}
		summary := "Codex turn " + result.Status
		return toolResult{Value: result, Summary: summary}, nil
	})
	addTool(server, deps, true, "fs.list", "List bounded entries subject to the active Grin policy.", objectSchema(map[string]any{
		"path":        map[string]any{"type": "string"},
		"depth":       map[string]any{"type": "integer", "minimum": 0},
		"max_entries": map[string]any{"type": "integer", "minimum": 1},
	}), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, true, "fs.stat", "Return bounded metadata for a path subject to the active Grin policy.", objectSchema(map[string]any{
		"path": map[string]any{"type": "string"},
	}, "path"), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, true, "fs.read_text", "Read a bounded text file subject to the active Grin policy.", objectSchema(map[string]any{
		"path":      map[string]any{"type": "string"},
		"max_bytes": map[string]any{"type": "integer", "minimum": 1},
	}, "path"), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
		var input filesystem.ReadInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid fs.read_text arguments", false)
		}
		outside, err := deps.Filesystem.ClassifyReadText(input.Path)
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
	addTool(server, deps, true, "fs.search", "Search bounded text and file names subject to the active Grin policy.", objectSchema(map[string]any{
		"path":        map[string]any{"type": "string"},
		"query":       map[string]any{"type": "string"},
		"max_results": map[string]any{"type": "integer", "minimum": 1},
	}, "query"), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, true, "fs.write_text", "Write bounded text subject to the active Grin policy.", objectSchema(map[string]any{
		"path":      map[string]any{"type": "string"},
		"content":   map[string]any{"type": "string"},
		"overwrite": map[string]any{"type": "boolean"},
	}, "path", "content"), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, true, "fs.edit_text", "Replace one exact text occurrence subject to the active Grin policy.", objectSchema(map[string]any{
		"path":     map[string]any{"type": "string"},
		"old_text": map[string]any{"type": "string"},
		"new_text": map[string]any{"type": "string"},
	}, "path", "old_text", "new_text"), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, true, "shell.run", "Run one bounded executable subject to the active Grin policy.", objectSchema(map[string]any{
		"command":    map[string]any{"type": "string", "minLength": 1},
		"args":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"cwd":        map[string]any{"type": "string"},
		"timeout_ms": map[string]any{"type": "integer", "minimum": 0},
	}, "command"), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, false, "process.list", "List bounded, redacted process metadata.", objectSchema(map[string]any{}), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, false, "process.info", "Return bounded, redacted metadata for one process.", objectSchema(map[string]any{
		"pid": map[string]any{"type": "integer", "minimum": 1},
	}, "pid"), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, false, "system.info", "Return non-secret system information.", objectSchema(map[string]any{}), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, true, "git.status", "Return bounded Git status for the workspace repository.", objectSchema(map[string]any{}), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, true, "git.diff", "Return bounded Git diff for the workspace repository.", objectSchema(map[string]any{
		"staged": map[string]any{"type": "boolean"},
		"path":   map[string]any{"type": "string"},
	}), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, true, "git.log", "Return bounded Git history for the workspace repository.", objectSchema(map[string]any{
		"max_entries": map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
	}), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	addTool(server, deps, true, "git.show", "Return bounded Git revision content for the workspace repository.", objectSchema(map[string]any{
		"revision": map[string]any{"type": "string"},
		"path":     map[string]any{"type": "string"},
	}), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
	return name == "codex.list" || name == "codex.queue" || name == "codex.turn_result"
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

func selectCodexWorkspace(base Dependencies, raw json.RawMessage) (string, json.RawMessage, Dependencies, error) {
	workspace, inner, err := splitWorkspace(raw)
	if err != nil {
		return "", nil, base, err
	}
	if !filepath.IsAbs(workspace) {
		return "", nil, base, errs.New(errs.ErrInvalidInput, "workspace must be an absolute path", false)
	}
	requested := filepath.Clean(workspace)
	if !base.Config.Yolo {
		registered, err := config.LoadRegistry()
		if err != nil {
			return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace registry is unavailable", true)
		}
		found := false
		for _, root := range registered {
			if root == requested {
				found = true
				break
			}
		}
		if !found {
			return "", nil, base, errs.New(errs.ErrWorkspaceNotFound, "workspace is not registered", false)
		}
	}
	canonical, err := filepath.EvalSymlinks(requested)
	if err != nil || filepath.Clean(canonical) != requested {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
	}
	if !base.Config.Yolo {
		selected, _, err := config.LoadWorkspace(canonical)
		if err != nil {
			return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace configuration is unavailable", true)
		}
		selected.Server = base.Config.Server
		selected.Yolo = false
		selected.Workspace.Root = canonical
		if err := config.Validate(selected); err != nil {
			return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace configuration is invalid", false)
		}
		base.Config = selected
	}
	base.Config.Workspace.Root = canonical
	return canonical, inner, base, nil
}

func hasCodexThread(matches []codex.Session, threadID string) bool {
	for _, match := range matches {
		if strings.EqualFold(match.ThreadID, threadID) {
			return true
		}
	}
	return false
}

func normalizeYOLOWorkspace(raw json.RawMessage, root string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := decode(raw, &fields); err != nil {
		return nil, errs.New(errs.ErrInvalidInput, "invalid tool arguments", false)
	}

	value, ok := fields["workspace"]
	if !ok {
		return raw, nil
	}

	var workspace string
	if err := json.Unmarshal(value, &workspace); err != nil || strings.TrimSpace(workspace) == "" {
		return nil, errs.New(errs.ErrInvalidInput, "workspace must be a non-empty string", false)
	}
	if !filepath.IsAbs(workspace) || filepath.Clean(workspace) != filepath.Clean(root) {
		return nil, errs.New(errs.ErrInvalidInput, "workspace does not match the YOLO startup workspace", false)
	}

	copied := make(map[string]json.RawMessage, len(fields)-1)
	for key, field := range fields {
		if key != "workspace" {
			copied[key] = field
		}
	}
	normalized, err := json.Marshal(copied)
	if err != nil {
		return nil, errs.New(errs.ErrInvalidInput, "invalid tool arguments", false)
	}
	return normalized, nil
}

func workspaceSchema(schema any) map[string]any {
	result, ok := schema.(map[string]any)
	if !ok {
		return map[string]any{"type": "object", "properties": map[string]any{"workspace": map[string]any{"type": "string"}}, "required": []string{"workspace"}, "additionalProperties": false}
	}
	copy := make(map[string]any, len(result)+1)
	for key, value := range result {
		copy[key] = value
	}
	properties := map[string]any{"workspace": map[string]any{"type": "string", "minLength": 1}}
	if existing, ok := copy["properties"].(map[string]any); ok {
		for key, value := range existing {
			properties[key] = value
		}
	}
	copy["properties"] = properties
	copy["required"] = append([]string{"workspace"}, stringSlice(copy["required"])...)
	copy["additionalProperties"] = false
	return copy
}

func stringSlice(value any) []string {
	values, _ := value.([]string)
	return values
}

func selectWorkspace(base Dependencies, raw json.RawMessage) (string, json.RawMessage, Dependencies, error) {
	workspace, inner, err := splitWorkspace(raw)
	if err != nil {
		return "", nil, base, err
	}
	if !filepath.IsAbs(workspace) {
		return "", nil, base, errs.New(errs.ErrInvalidInput, "workspace must be an absolute path", false)
	}
	requested := filepath.Clean(workspace)
	registered, err := config.LoadRegistry()
	if err != nil {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace registry is unavailable", true)
	}
	matched := false
	for _, root := range registered {
		if root == requested {
			matched = true
			break
		}
	}
	if !matched {
		return "", nil, base, errs.New(errs.ErrWorkspaceNotFound, "workspace is not registered", false)
	}
	canonical, err := filepath.EvalSymlinks(requested)
	if err != nil || filepath.Clean(canonical) != requested {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
	}
	selected, _, err := config.LoadWorkspace(requested)
	if err != nil {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace configuration is unavailable", true)
	}
	selected.Server = base.Config.Server
	selected.Yolo = false
	if err := config.Validate(selected); err != nil {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace configuration is invalid", false)
	}
	files, err := filesystem.New(requested, selected.Limits)
	if err != nil {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace filesystem is unavailable", true)
	}
	runtimeService, err := grinruntime.New(selected, files.Root())
	if err != nil {
		return "", nil, base, errs.New(errs.ErrWorkspaceUnavailable, "workspace runtime is unavailable", true)
	}
	selected.Workspace.Root = files.Root()
	base.Config = selected
	base.Filesystem = files
	base.Runtime = runtimeService
	base.Git = gringit.New(runtimeService, files.Root())
	base.Policy = policy.New(false)
	return files.Root(), inner, base, nil
}

func splitWorkspace(raw json.RawMessage) (string, json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := decode(raw, &fields); err != nil {
		return "", nil, errs.New(errs.ErrInvalidInput, "invalid tool arguments", false)
	}
	value, ok := fields["workspace"]
	if !ok {
		return "", nil, errs.New(errs.ErrWorkspaceRequired, "workspace is required", false)
	}
	var workspace string
	if err := json.Unmarshal(value, &workspace); err != nil || strings.TrimSpace(workspace) == "" {
		return "", nil, errs.New(errs.ErrInvalidInput, "workspace must be a non-empty string", false)
	}
	delete(fields, "workspace")
	inner, err := json.Marshal(fields)
	if err != nil {
		return "", nil, errs.New(errs.ErrInvalidInput, "invalid tool arguments", false)
	}
	return workspace, inner, nil
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
