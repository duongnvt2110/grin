package mcp

import (
	"context"
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"grin/internal/approval"
	"grin/internal/errs"
	"grin/internal/filesystem"
	"grin/internal/policy"
)

func registerFilesystemTools(server *sdk.Server, deps Dependencies) {
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
}
