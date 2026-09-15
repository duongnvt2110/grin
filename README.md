# Grin

Grin is a local MCP runtime that lets ChatGPT work with your local repositories
through supervised filesystem, Git, shell, process, and system tools.

```text
ChatGPT
   ↓
Secure MCP Tunnel
   ↓
tunnel-client
   ↓
Grin · 127.0.0.1:8765
   ↓
your local workspaces
```

Grin keeps local execution on your machine. It adds workspace routing, policy,
approvals, limits, redaction, and a visible terminal UI between ChatGPT and your
local tools.

> Grin is not an OS sandbox. Keep its MCP endpoint loopback-only and do not
> expose it directly to the public internet.

[Quick start](#quick-start) · [How it works](#how-it-works) · [Modes](#modes) · [CLI](#cli) · [Connect ChatGPT](docs/onboarding.md) · [Docs](docs/README.md)

## Quick start

### 1. Install

macOS and Linux, amd64 and arm64:

```zsh
curl -fsSL https://raw.githubusercontent.com/duongnvt2110/grin/main/scripts/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"

grin --version
```

From source:

```zsh
git clone https://github.com/duongnvt2110/grin.git
cd grin
go run ./cmd/grin --help
```

### 2. Register a workspace

```zsh
grin init --workspace /path/to/project
```

Register more projects the same way:

```zsh
grin init --workspace /path/to/project-a
grin init --workspace /path/to/project-b
```

`grin init` creates the local `.grin/config.yaml` when needed and adds the
canonical workspace path to `~/.grin/config.yaml`.

### 3. Run Grin

```zsh
cd /path/to/project
grin
```

or:

```zsh
grin --workspace /path/to/project
```

Grin serves MCP on:

```text
http://127.0.0.1:8765/mcp
```

Verify it from another terminal:

```zsh
grin doctor --workspace /path/to/project --check-ready
curl -fsS http://127.0.0.1:8765/healthz
```

### 4. Connect ChatGPT

Run the official OpenAI `tunnel-client`, connect it to Grin's local MCP URL,
then select that tunnel in ChatGPT.

→ [Full ChatGPT + Secure MCP Tunnel tutorial](docs/onboarding.md)

## Why Grin?

- One local MCP server can serve multiple registered repositories.
- Workspace selection is explicit per request; Grin does not guess or keep a
  mutable server-side "current workspace".
- Local approval can stop operations before side effects occur.
- Filesystem, shell, process, and Git output are bounded.
- Command/process details are redacted before display where appropriate.
- The TUI shows requests, approvals, failures, and results locally.

## How it works

Normal mode uses one MCP server and one shared tool catalog:

```text
ChatGPT tool call
        ↓
workspace-scoped?
   ┌────┴────┐
   │         │
   no       yes
   │         ↓
   │    workspace=/repo/a
   │         ↓
   │    exact match in ~/.grin/config.yaml
   │         ↓
   │    load /repo/a/.grin/config.yaml
   │         ↓
   │    filesystem / runtime / Git / policy for /repo/a
   │         ↓
   └────→ policy / approval when required
             ↓
           execute
             ↓
        result + TUI
```

Example with two conversations:

```text
Conversation A → fs.read_text(workspace=/repo/a, path=README.md)
Conversation B → fs.read_text(workspace=/repo/b, path=README.md)
```

There is no conversation-to-workspace map inside Grin and no fallback to a
random or startup workspace.

→ [System architecture and state ownership](docs/components.md)

## Modes

| | Normal mode | `--yolo` |
| --- | --- | --- |
| Workspace model | Multiple registered workspaces | One startup workspace |
| `workspace.list` | Yes | No |
| `workspace` on scoped tools | Required | Not required |
| Registry routing | Exact registered path | Not used |
| Normal policy / approval | Active | Bypassed for supported tools |
| Workspace containment | Active | Bypassed |
| OS permissions / limits / cleanup | Active | Active |
| Recommended for | Normal use | Explicit trusted/local testing |

Normal mode is the default.

`--yolo` does not grant extra operating-system privileges. It only bypasses
Grin's own workspace and approval restrictions for supported tools.

→ [Full feature, operation, and policy model](docs/features.md)

## CLI

### Run the MCP server

```text
grin [--workspace PATH] [--port PORT] [--yolo]
```

| Option | Purpose |
| --- | --- |
| `--workspace PATH` | Select the startup workspace. In normal mode this is not an implicit tool-call fallback. |
| `--port PORT` | Override the default MCP port `8765`. |
| `--yolo` | Use single-workspace YOLO behavior and bypass Grin workspace/approval restrictions for supported tools. |
| `--help` | Show help. |
| `--version` | Print the version. |

### Initialize/register a workspace

```text
grin init [--workspace PATH]
```

If `--workspace` is omitted, Grin uses the nearest discovered workspace or the
current directory.

### Diagnose the local runtime

```text
grin doctor [--workspace PATH] [--port PORT] [--check-ready]
```

`--check-ready` also verifies Grin's `/readyz` endpoint.

### Upgrade Grin

```text
grin upgrade
```

`grin upgrade` checks the latest stable GitHub release, verifies the existing
GoReleaser SHA-256 checksum, and replaces the installed binary when a newer
release is available. Source/development builds do not self-upgrade.

### Installer

```text
install.sh [--version VERSION] [--dir DIRECTORY]
```

Use `--version` for a specific release or `--dir` for a custom install path.

## Tools at a glance

| Scope | Tools |
| --- | --- |
| Workspace discovery | `workspace.list` |
| Workspace info | `workspace.info` |
| Filesystem | `fs.list`, `fs.stat`, `fs.read_text`, `fs.search`, `fs.write_text`, `fs.edit_text` |
| Git | `git.status`, `git.diff`, `git.log`, `git.show` |
| Shell | `shell.run` |
| Host inspection | `process.list`, `process.info`, `system.info` |

In normal mode, `workspace.info`, `fs.*`, `git.*`, and `shell.run` require an
exact registered workspace path.

## Safety

In normal mode:

- outside reads are allowed;
- outside writes require local approval;
- shell commands using an outside `cwd` require approval;
- obvious destructive shell commands require approval;
- requests, file/search output, shell output, execution time, and process
  behavior remain bounded.

For the exact READ / WRITE / EXECUTE operation matrix:

→ [Features and operation model](docs/features.md#operation-model)

## Documentation

| Need | Read |
| --- | --- |
| Find the right guide | [Documentation index](docs/README.md) |
| Connect ChatGPT | [ChatGPT + tunnel tutorial](docs/onboarding.md) |
| Understand architecture and request flow | [System and components](docs/components.md) |
| Understand modes, tools, READ/WRITE/EXECUTE, and boundaries | [Features and boundaries](docs/features.md) |
| Publish a release | [Release guide](docs/releasing.md) |

## Development

```zsh
go test ./...
go test -race ./...
go vet ./...
go mod verify
GOFLAGS=-buildvcs=false go build ./cmd/grin
```
