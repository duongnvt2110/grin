# Grin Features and Boundaries

This page is the user-facing capability reference for the current Grin
implementation. For installation and CLI usage, start with the
[README](../README.md).

## Operating modes

| Behavior | Normal mode | `--yolo` |
| --- | --- | --- |
| Workspace model | Multiple registered workspaces | One startup workspace |
| `workspace.list` | Available | Not registered |
| Workspace selector on scoped tools | Required | Not required |
| Registry routing | Exact registered path | Not used |
| Per-workspace configuration | Loaded for each selected request | Startup workspace configuration |
| Grin policy/approval | Active | Bypassed for supported tools |
| Workspace containment | Active according to normal policy | Bypassed |
| OS/runtime safety limits | Active | Active |

Normal mode is the default and recommended mode.

## MCP tools

### Global tools

| Tool | Purpose | Normal | YOLO |
| --- | --- | --- | --- |
| `workspace.list` | Return registered canonical workspaces | Yes | No |
| `process.list` | Return bounded/redacted process metadata | Yes | Yes |
| `process.info` | Inspect one process with bounded/redacted metadata | Yes | Yes |
| `system.info` | Return non-secret OS/system information | Yes | Yes |

### Workspace-scoped tools

In normal mode these require an exact registered `workspace` argument.

| Area | Tools | Capability |
| --- | --- | --- |
| Workspace | `workspace.info` | Selected workspace information |
| Filesystem | `fs.list`, `fs.stat`, `fs.read_text`, `fs.search` | Bounded inspection/search |
| Filesystem writes | `fs.write_text`, `fs.edit_text` | Bounded text write and exact one-match edit |
| Shell | `shell.run` | One bounded executable with explicit arguments/cwd/timeout |
| Git | `git.status`, `git.diff`, `git.log`, `git.show` | Bounded read-only repository inspection |

## Operation model

Grin classifies supported tool calls by what they do, but the exact policy path
depends on the tool as well as whether the target is inside or outside the
selected workspace.

### Operation classes

| Class | Tools | Effect |
| --- | --- | --- |
| Discovery | `workspace.list` | Reads the normal-mode workspace registry before any workspace is selected. |
| Workspace read | `workspace.info` | Reads selected-workspace metadata. |
| Filesystem read | `fs.list`, `fs.stat`, `fs.read_text`, `fs.search` | Reads or searches filesystem content/metadata. |
| Git read | `git.status`, `git.diff`, `git.log`, `git.show` | Reads repository state/history; never writes Git state. |
| Host read | `process.list`, `process.info`, `system.info` | Reads bounded/redacted host process or system information. |
| Filesystem write | `fs.write_text`, `fs.edit_text` | Creates/overwrites text or replaces one exact text occurrence. |
| Execute | `shell.run` | Starts one bounded local executable with explicit arguments/cwd/timeout. |

### Decision outcomes

```text
ALLOW → execute immediately
ASK   → pause and require local TUI approval
DENY  → return an error and do not execute
```

Normal mode uses the built-in normal policy. YOLO mode bypasses Grin's
workspace and approval restrictions for supported tools.

### Current normal-mode policy behavior

| Operation | Location | Normal mode |
| --- | --- | --- |
| `workspace.info` | selected workspace | ALLOW |
| `git.*` | selected workspace | ALLOW |
| `process.*`, `system.info` | host/global | ALLOW |
| `fs.list/stat/read_text/search` | inside selected workspace | Execute after validation; no policy decision on this path |
| `fs.list/stat/read_text/search` | outside selected workspace | ALLOW |
| `fs.write_text`, `fs.edit_text` | inside selected workspace | ALLOW |
| `fs.write_text`, `fs.edit_text` | outside selected workspace | ASK |
| `shell.run` | inside selected workspace, non-destructive | ALLOW |
| `shell.run` | outside selected workspace | ASK |
| `shell.run` | inside selected workspace, destructive | ASK |

`workspace.list` is different from the other reads: it is pre-selection
registry discovery and does not use workspace-scoped policy evaluation.

The filesystem-read row above intentionally documents the **current execution
path**: inside-workspace filesystem reads are validated and executed directly;
policy evaluation for those tools currently occurs only when the target is
classified outside the selected workspace.

### Write/execute flow

Writes and shell execution follow this high-level path:

```text
strict input validation
        ↓
classify selected workspace / outside target
        ↓
policy decision
   ┌────┼────┐
 ALLOW ASK  DENY
   │    │     └→ return error
   │    ↓
   │  local TUI approval
   │    ├─ reject/timeout/cancel → no side effect
   │    └─ allow
   ↓
execute with existing limits
        ↓
result + lifecycle event
```

For filesystem writes, validation/classification happens before approval, but
the write itself does not occur until the policy/approval path permits it.

### YOLO operation model

YOLO keeps supported operations but bypasses Grin's normal workspace-policy and
approval restrictions:

```text
supported tool
   ↓
validation / limits / OS permissions still apply
   ↓
Grin workspace/approval restriction bypass
   ↓
execute
```

YOLO does **not** make unsupported tools valid and does not bypass operating
system permissions, input validation, output/resource limits, cancellation, or
process cleanup.

## Workspace routing

`grin init` registers canonical workspace paths in `~/.grin/config.yaml`.
Normal-mode scoped tool calls must use one of those exact paths.

Grin does not:

- guess a workspace;
- silently fall back to the startup workspace;
- keep an active/current workspace on the server;
- map ChatGPT conversation IDs to workspaces;
- create a separate MCP server per workspace.

The registry is read when `workspace.list` or a scoped request needs it, so a
new `grin init` registration is visible without restarting Grin.

## Current safety behavior

Grin remains loopback-only and is not an operating-system sandbox.

With the default `workspace` policy:

- outside reads are allowed;
- outside writes require local approval;
- shell commands with an outside `cwd` require local approval;
- obvious destructive shell commands require local approval;
- request size, file/search output, shell output, execution time, and process
  behavior remain bounded.

`--yolo` intentionally bypasses Grin's workspace containment and approval
restrictions for supported tools, but it does not bypass OS permissions,
validation, output/resource limits, cancellation, or process cleanup.

## Filesystem boundaries

- `fs.read_text` redacts recognized sensitive standalone assignments and
  supported `user:password@host` URL credentials before returning content.
- Matching is conservative and key-name based; this is not a complete secret
  scanner. `fs.search`, Git output, and shell output are outside this feature.
- `fs.edit_text` replaces one exact text occurrence.
- Existing permission bits are preserved for supported text edits.
- Oversized or ambiguous edits are rejected.
- Symlink/canonical-path checks prevent a registered workspace path that has
  later been replaced by a symlink from silently routing somewhere else.

## Git boundaries

Dedicated Git tools currently expect a real `.git` directory at the selected
workspace root.

- Git 2.45 or newer is required.
- Worktrees, submodules, and external Git directories are not supported by the
  dedicated Git tool path.
- Git tools are inspection-only: status, diff, log, and show.

## TUI behavior

The inline TUI shows:

- request lifecycle rows;
- approval-required rows and allow/reject controls;
- failures/cancellation/completion;
- bounded safe summaries/details;
- workspace identity immediately for approval rows;
- workspace identity on other scoped rows when expanded;
- `multi-workspace` / `normal` labels in normal mode and `yolo` in YOLO mode.

## Agent guidance

For repository coding/review/planning/implementation requests, the MCP server
instructions tell the client to read the selected workspace's root
`AGENTS.md`, when present, through `fs.read_text` before acting.

## Current product boundaries

- The ChatGPT-to-Grin path uses the external official OpenAI `tunnel-client`.
- Full ChatGPT-through-tunnel acceptance is a manual/external check; local Go
  tests cannot prove connector behavior.
- Grin V1 is interactive; headless and stdio MCP modes are not supported.
- The TUI does not expose a user action for cancelling an already-running shell
  process.
- Obvious destructive-command detection is intentionally not a complete shell
  behavior analyzer.

## Deferred work

These are deliberately outside the current V1 scope:

- supported headless or stdio MCP operation;
- stronger process isolation/sandboxing beyond the existing OS + Grin controls;
- automated end-to-end verification of external ChatGPT/tunnel behavior.

New architecture should not be added for these items without a separate
feature decision.
