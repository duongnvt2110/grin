# Grin System and Components

This page explains how Grin is arranged at runtime, which state is global, and
how one MCP server safely routes requests to multiple local workspaces.

For installation, start with the [README](../README.md). For the complete
ChatGPT connection tutorial, use [onboarding](onboarding.md). For the practical
ChatGPT ↔ Codex workflow, use the [Codex review guide](codex-review.md).

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
        ├── workspace-scoped tools
        │     workspace.info / fs.* / git.* / shell.run
        │           ↓
        │     workspace selector → registered canonical path
        │           ↓
        │     selected workspace services → policy/approval/execute → TUI

        └── Codex review tools
              codex.list / codex.queue / codex.turn_result
                    ↓
          explicit workspace (Normal: registered;
                              YOLO: existing target)
                    ↓
          live-thread discovery / queued request /
             exact persisted result lookup (~/.codex only)
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
| Codex service | Discovers live top-level threads, queues to an exact thread, and reads its correlated result from the default local Codex history. It stores no conversation or thread-selection state. |
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
| Codex thread selection/request ID | ChatGPT conversation and Codex history | Client-side selection / persisted Codex history | Which live thread to queue and which exact result to retrieve |

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
registered workspace?
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

Filesystem, Git, and shell tools in YOLO deliberately keep the single startup
workspace. Codex review tools instead require an explicit existing target
workspace and do not apply that startup-root restriction:

```text
grin --workspace /repo/a --yolo
        ↓
filesystem / Git / shell tools use one startup workspace
        ↓
workspace.list returns the startup workspace for compatibility
no selector or registry routing for filesystem / Git / shell tools
        ↓
relative paths/default cwd/Git use /repo/a as their base
        ↓
workspace containment and Grin approval restrictions are bypassed
```

Codex tools remain explicitly workspace-scoped in YOLO mode and can target an
existing directory outside the startup workspace.

OS permissions, input validation, limits, timeout/cancellation, cleanup, and
runtime correctness checks still apply.

The Codex service inspects only live top-level sessions for discovery/queueing.
Result lookup uses the default local `~/.codex` state and history files after
validating the stored workspace; Grin keeps no thread mapping or review-loop
state.

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
