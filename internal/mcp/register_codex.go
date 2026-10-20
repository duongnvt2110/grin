package mcp

import (
	"context"
	"encoding/json"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"grin/internal/approval"
	"grin/internal/errs"
	"grin/internal/policy"
)

func registerCodexTools(server *sdk.Server, deps Dependencies) {
	addTool(server, deps, false, "codex.queue", "Freshly discover Codex threads in the selected workspace before every send; do not reuse a remembered thread_id. Omit thread_id to request fresh candidates: UNRESOLVED candidates are never selected automatically, even when there is one eligible thread. Eligible candidates that pass ambiguity checks return selection_required without sending. Supply thread_id only when the user explicitly named or selected that UUID, or for the same uninterrupted review loop; supplied IDs must still match fresh discovery. Discovery corroborates eligibility but cannot prove the displayed TUI title is fresh. Queue acceptance does not mean execution has started.", objectSchema(map[string]any{
		"thread_id": map[string]any{"type": "string", "format": "uuid", "minLength": 36, "maxLength": 36},
		"message":   map[string]any{"type": "string", "minLength": 1},
	}, "message"), func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
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
		if strings.TrimSpace(input.Message) == "" {
			return nil, errs.New(errs.ErrInvalidInput, "Codex message is required", false)
		}
		operation := policy.Operation{Tool: "codex.queue", Capability: "codex.queue", Arguments: map[string]string{"thread_id": input.ThreadID}}
		detail := "discover eligible Codex recipient"
		if input.ThreadID != "" {
			detail = "thread: " + input.ThreadID
		}
		if err := authorize(ctx, deps, toolCallID, operation, "queue Codex review", detail, approval.RiskLow); err != nil {
			return nil, err
		}
		fresh, err := deps.Codex.Discover(ctx, workspace)
		if err != nil {
			return nil, errs.New(errs.ErrUnsupported, "fresh Codex discovery failed; no message queued", true)
		}
		threadID, choices, err := selectFreshCodexRecipient(fresh, input.ThreadID)
		if err != nil {
			return nil, err
		}
		if len(choices) != 0 {
			return toolResult{Value: map[string]any{"status": "selection_required", "candidates": choices}, Summary: "Codex recipient selection required; no message queued"}, nil
		}
		result, err := deps.Codex.Queue(ctx, workspace, threadID, input.Message)
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
}
