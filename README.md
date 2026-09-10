# Grin

Grin is a supervised local MCP runtime for giving ChatGPT bounded access to
your workspace.

ChatGPT handles conversation and reasoning. Grin handles local tool execution,
workspace policy, approval prompts, output limits, redaction, and a visible
terminal interface.

```text
ChatGPT
   ↓
OpenAI Secure MCP Tunnel
   ↓
tunnel-client
   ↓
Grin on 127.0.0.1:8765
   ↓
your workspace
```

Grin is loopback-only. It is not an operating-system sandbox and should not be
exposed on a public address.

## Why Grin?

- Supervised local MCP tools instead of unrestricted remote execution.
- Approval before outside-workspace writes, outside shell directories, and
  obvious destructive shell commands.
- Bounded filesystem reads, edits, command execution, and process inspection.
- Redacted command and process details in the terminal UI.
- A compact TUI that shows requests, approvals, results, and failures.

## Quick start

### Install a release

macOS and Linux binaries are available for amd64 and arm64:

```zsh
curl -fsSL https://raw.githubusercontent.com/duongnvt2110/grin/main/scripts/install.sh | sh
```

The installer places `grin` in `~/.local/bin` and verifies the release
checksum. It does not install `tunnel-client`.

If no GitHub release is available yet, run Grin from a Go checkout instead:

```zsh
go run ./cmd/grin --workspace /tmp/grin-demo
```

### Run Grin locally

Start Grin with an explicit workspace:

```zsh
grin --workspace /tmp/grin-demo
```

Or run it from a project and let Grin discover the nearest
`.grin/config.yaml`:

```zsh
cd your-project
grin
```

Check readiness from another terminal:

```zsh
grin doctor --workspace /tmp/grin-demo --check-ready
curl -fsS http://127.0.0.1:8765/healthz
```

Expected health output:

```text
ok
```

## Connect ChatGPT

To use Grin from ChatGPT, keep both Grin and the official OpenAI
`tunnel-client` running. Then select the tunnel from ChatGPT connector
settings.

The complete setup covers tunnel creation, runtime API keys, workspace scope,
connector setup, verification, and troubleshooting:

→ [ChatGPT and tunnel onboarding](docs/onboarding.md)

## Capabilities

Grin currently provides:

- workspace information and bounded filesystem inspection;
- text search and exact one-match text editing;
- bounded text writes;
- supervised shell execution;
- redacted process and system inspection;
- read-only Git status, diff, log, and show operations;
- inline approval and execution results in the TUI.

See [implemented features and boundaries](docs/features.md) for the current
security model and deferred work.

For the architecture and deployment process, see:

- [Grin components](docs/components.md)
- [Release guide](docs/releasing.md)

## Safety model

In normal `workspace` mode:

- reads outside the workspace are allowed;
- writes outside the workspace require approval;
- shell commands with an outside working directory require approval;
- obvious destructive shell commands require approval;
- requests, output, execution time, and process behavior remain bounded.

Use `--yolo` only when you intentionally want to bypass Grin's workspace and
approval restrictions for supported tools. Operating-system permissions and
runtime limits still apply.

Grin is not a complete shell sandbox. Do not expose its MCP endpoint publicly.

## Development

From the repository root:

```zsh
go test ./...
go test -race ./...
go vet ./...
go mod verify
```

Local tests do not replace the real ChatGPT-through-tunnel acceptance check.
