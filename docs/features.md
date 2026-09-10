# Grin Features

This page describes the current user-facing feature set. Internal plans and
review notes live separately under `my_docs/`.

## Implemented

| Area | Current capability |
| --- | --- |
| MCP server | Loopback Streamable HTTP MCP server on `/mcp`. |
| Workspace | Explicit `--workspace` and upward discovery of the nearest `.grin/config.yaml`. |
| Filesystem | Bounded list, stat, read, search, text-write, and exact text-edit tools; workspace reads are allowed outside, while outside writes require approval. |
| Git inspection | Bounded `git.status`, `git.diff`, `git.log`, and `git.show` tools for a simple repository rooted at the active workspace. |
| Approval | Local approval for outside writes, outside shell cwd, and obvious destructive shell commands. |
| Shell | Bounded executable and argument handling; normal shell runs are allowed, while outside cwd and obvious destructive commands require approval. |
| Process inspection | Bounded, redacted process and system information. |
| TUI | Inline transcript rows, approval actions, safe summaries, resizing, and bounded overflow rendering. |
| YOLO mode | Explicit `--yolo` mode for supported workspace and approval bypasses while OS and runtime limits remain active. |
| Agent guidance | MCP server instructions point repository work to the active workspace `AGENTS.md` through `fs.read_text` when present. |

## Current boundaries

- Grin remains loopback-only and is not an operating-system sandbox.
- In `workspace` mode, outside reads are allowed; outside writes and outside
  shell cwd require approval.
- Obvious destructive commands require approval. Grin does not detect every
  possible destructive action hidden inside another program or shell script.
- Git inspection requires a real `.git` directory at the active workspace root;
  worktrees, submodules, and external Git directories are not supported by the
  dedicated Git tools. Git 2.45 or newer is required.
- `fs.edit_text` replaces one exact text occurrence, preserves the existing
  permission bits, and refuses oversized or ambiguous edits.
- The ChatGPT-to-Grin tunnel uses the external OpenAI `tunnel-client`.
- Full ChatGPT-through-tunnel acceptance is an external/manual check; local Go
  tests do not prove remote connector behavior.
- Grin is interactive in V1; a headless or stdio MCP mode is not currently
  supported.
- The TUI does not provide a user action for cancelling an already-running
  shell process.

## Deferred or intended future work

These items are deliberately outside the current V1 scope:

- A supported headless or stdio MCP mode.
- Stronger process isolation or sandboxing beyond the current OS and Grin
  controls.
- Automated end-to-end verification of the external ChatGPT and tunnel
  connection.

No future item should expand the current security or runtime architecture
without a separate approved plan.
