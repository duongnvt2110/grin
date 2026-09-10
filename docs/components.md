# Grin Components

Grin is a local MCP runtime. Each component has one focused responsibility:

```text
ChatGPT
    ↓ MCP tunnel
tunnel-client
    ↓ http://127.0.0.1:8765/mcp
Grin MCP server
    ├── input validation
    ├── policy evaluation
    ├── local approval
    ├── filesystem and Git inspection
    └── bounded command execution
            ↓
          TUI
```

## Components

| Component | Responsibility |
| --- | --- |
| ChatGPT | Conversation, reasoning, and requests to connected MCP tools. |
| OpenAI Secure MCP Tunnel | Connects ChatGPT to the local MCP endpoint without exposing Grin publicly. |
| `tunnel-client` | Runs locally and forwards tunnel traffic to Grin. |
| MCP server | Exposes Grin tools through `/mcp`, validates inputs, and returns bounded results. |
| Policy | Decides whether an operation is allowed, requires approval, or is denied. |
| Approval | Pauses operations that require local human confirmation. |
| Filesystem service | Performs bounded reads, writes, searches, and exact text edits. |
| Git service | Performs bounded read-only status, diff, log, and show operations for a supported repository root. |
| Runtime service | Runs bounded commands and process inspection with timeouts, output limits, cleanup, and redaction. |
| TUI | Shows requests, approval prompts, results, and failures in the local terminal. |

## Request flow

Every MCP request follows the same high-level path:

```text
request
  ↓
validate arguments
  ↓
classify workspace and operation
  ↓
allow, ask for approval, or deny
  ↓
execute with limits
  ↓
publish a bounded result
  ↓
render the lifecycle in the TUI
```

Approval is performed before an operation that requires it executes. The TUI
formats safe summaries; it does not inspect arbitrary secrets, raw command
arguments, or unbounded output.

## Deployment boundary

Grin is intended to run on the same machine as the workspace:

```text
local machine
  ├── Grin: 127.0.0.1:8765
  └── tunnel-client
        ↕ secure OpenAI tunnel
ChatGPT
```

Grin must remain loopback-only. The tunnel client is the public connection
boundary; Grin itself should not be bound to a public interface.
