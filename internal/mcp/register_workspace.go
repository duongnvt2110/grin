package mcp

import (
	"context"
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"grin/internal/approval"
	"grin/internal/config"
	"grin/internal/errs"
	"grin/internal/policy"
)

func registerWorkspaceTools(server *sdk.Server, deps Dependencies) {
	addTool(server, deps, true, "workspace.info", "Return metadata for the selected workspace. The workspace argument is optional in YOLO mode and required in normal mode. This is not a discovery or selection tool; use workspace.list when the workspace path is unknown.", map[string]any{"type": "object", "additionalProperties": false}, func(ctx context.Context, deps Dependencies, toolCallID string, raw json.RawMessage) (any, error) {
		var input emptyInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid workspace.info arguments", false)
		}
		if err := authorize(ctx, deps, toolCallID, policy.Operation{Tool: "workspace.info", Capability: "workspace.inspect", Arguments: input}, "inspect workspace", "selected workspace metadata", approval.RiskLow); err != nil {
			return nil, err
		}
		return toolResult{Value: deps.Filesystem.Info(), Summary: "selected workspace"}, nil
	})
	addTool(server, deps, false, "workspace.list", "Discover registered Grin workspaces. Use this only when the task's exact target path cannot be established from the task or conversation; treat results as candidates matching the task, and never choose the first or only result by default.", objectSchema(map[string]any{}), func(_ context.Context, deps Dependencies, _ string, raw json.RawMessage) (any, error) {
		var input emptyInput
		if err := decode(raw, &input); err != nil {
			return nil, errs.New(errs.ErrInvalidInput, "invalid workspace.list arguments", false)
		}
		workspaces, err := config.LoadRegistry()
		if err != nil {
			return nil, errs.New(errs.ErrWorkspaceUnavailable, "workspace registry is unavailable", true)
		}
		return toolResult{Value: map[string]any{"workspaces": workspaces}, Summary: "list workspaces"}, nil
	})
}
