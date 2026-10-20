package mcp

import (
	"context"
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"grin/internal/approval"
	"grin/internal/errs"
	gringit "grin/internal/git"
	"grin/internal/policy"
)

func registerGitTools(server *sdk.Server, deps Dependencies) {
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
}
