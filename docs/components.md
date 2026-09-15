# Grin System and Components

This page explains how Grin is arranged at runtime, which state is global, and
how one MCP server safely routes requests to multiple local workspaces.

For installation, start with the [README](../README.md). For the complete
ChatGPT connection tutorial, use [onboarding](onboarding.md).

## System overview

```text
ChatGPT conversation(s)
        ↓
OpenAI Secure MCP Tunnel
        ↓
tunnel-client
        ↓
http://127.0.0.1:8765/mcp
        ↓
ONE Grin MCP server
        │
        ├── global tools
        │     workspace.list
        │     process.list
        │     process.info
        │     system.info
        │
        └── workspace-scoped tools
              workspace.info
              fs.*
              git.*
              shell.run
                    ↓
             workspace selector
                    ↓
            exact registry match
                    ↓
          selected workspace services
                    ↓
            policy / approval / execute
                    ↓
                   TUI
```

Grin does not create one MCP server per repository. It keeps one stable tool
catalog and selects the workspace on each normal-mode scoped request.

## Components

| Component | Responsibility |
| --- | --- |
| ChatGPT | Conversation, reasoning, and selection of connected MCP tools. |
| OpenAI Secure MCP Tunnel | Carries requests to the local machine without requiring a public Grin endpoint. |
| `tunnel-client` | Runs locally and forwards tunnel traffic to Grin's loopback MCP endpoint. |
| MCP server | Registers tools once, validates tool inputs, resolves workspace-scoped requests, and returns bounded results. |
| Workspace registry | `~/.grin/config.yaml`; lists exact canonical workspaces allowed for normal-mode routing. |
| Workspace config | `<workspace>/.grin/config.yaml`; selected requests use limits/shell settings, while the startup workspace supplies process server/TUI defaults. Normal policy is built in; YOLO is selected only by the startup CLI. |
| Policy | Decides whether a supported operation is allowed, requires approval, or is denied. |
| Approval manager | Coordinates local operator approval before an operation that requires it executes. |
| Filesystem service | Performs bounded list/stat/read/search/write/edit behavior rooted at one workspace. |
| Runtime service | Runs bounded commands and process/system inspection with timeout, cleanup, and redaction behavior. |
| Git service | Performs bounded read-only Git status/diff/log/show for one supported repository root. |
| Event bus | Carries request, policy, approval, completion, failure, and workspace display context to the TUI. |
| TUI | Shows request lifecycle rows and local approval actions. |

## State and configuration ownership

This distinction is important in multi-workspace mode:

| State/config | Owner | Lifetime | Used for |
| --- | --- | --- | --- |
| Listener bind/port | Startup Grin config | Process | The one MCP HTTP server |
| Mode (`normal` / `--yolo`) | Startup CLI | Process | Selects the MCP contract for the whole process |
| Event bus / approval manager | Grin process | Process | Shared lifecycle and local approval delivery |
| Workspace registry | `~/.grin/config.yaml` | Read live | Which canonical paths normal mode may select |
| Workspace limits/shell settings | Selected `<workspace>/.grin/config.yaml` | Request | Root-bound execution behavior for that request |
| Filesystem/runtime/Git services | Request dependency copy | Request | Execution against exactly one selected workspace |
| Conversation workspace | ChatGPT conversation | Client-side conversation context | Which workspace path ChatGPT sends on later scoped calls |

Grin intentionally stores no conversation-to-workspace mapping and has no
mutable server-side `current workspace`.

## Normal-mode request flow

### Global tools

Global tools do not need workspace routing:

```text
request
  ↓
strict tool argument validation
  ↓
startup/shared dependencies and policy when applicable
  ↓
execute
  ↓
result + lifecycle events
```

`workspace.list` is pre-selection registry discovery and reads the current
registry directly.

### Workspace-scoped tools

```text
request(workspace=/projects/api, ...)
  ↓
require non-empty absolute workspace
  ↓
read ~/.grin/config.yaml
  ↓
exact path registered?
  ├─ no  → workspace_not_found
  └─ yes
       ↓
verify the registered canonical path is still available
       ↓
load /projects/api/.grin/config.yaml or defaults
       ↓
validate selected configuration
       ↓
construct request-scoped filesystem/runtime/Git/policy
       ↓
publish request_started(workspace=/projects/api)
       ↓
existing tool validation / policy / approval behavior
       ↓
execute against /projects/api only
       ↓
result + workspace-aware lifecycle events
```

There is no fallback to the startup workspace or another registered workspace.

## Multiple conversations

A single running Grin process can serve independent conversations because the
workspace identity travels with every scoped request:

```text
Conversation A → fs.read_text(workspace=/repo/a, path=README.md)
Conversation B → fs.read_text(workspace=/repo/b, path=README.md)
```

The filesystem/runtime/Git service roots are constructed per request and are
never mutated globally.

## YOLO flow

YOLO deliberately does **not** use multi-workspace routing:

```text
grin --workspace /repo/a --yolo
        ↓
one startup workspace / existing tool schemas
        ↓
no workspace.list
no required workspace selector
no registry routing
        ↓
relative paths/default cwd/Git use /repo/a as their base
        ↓
workspace containment and Grin approval restrictions are bypassed
```

OS permissions, input validation, limits, timeout/cancellation, cleanup, and
runtime correctness checks still apply.

See the [mode table in the README](../README.md#modes) for the user-facing
comparison.

## Deployment boundary

Grin and `tunnel-client` run on the same local/private machine:

```text
local machine
  ├── Grin: 127.0.0.1:8765
  └── tunnel-client
        ↕ outbound Secure MCP Tunnel
ChatGPT
```

Grin itself must remain loopback-only. The tunnel is the connection boundary;
do not bind Grin directly to a public interface.
