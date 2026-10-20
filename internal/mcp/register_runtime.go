package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"grin/internal/approval"
	"grin/internal/errs"
	"grin/internal/policy"
	grinruntime "grin/internal/runtime"
)

func registerRuntimeTools(server *sdk.Server, deps Dependencies) {
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
}
