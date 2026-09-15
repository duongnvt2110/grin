# Connect Grin to ChatGPT with Secure MCP Tunnel

This is the complete first-time tutorial for connecting a local Grin process to ChatGPT.
For a local-only quick start, CLI options, and mode concepts, read the
[README](../README.md) first.

This tutorial uses **normal multi-workspace mode**. Read the
[normal vs YOLO comparison](../README.md#modes) before choosing `--yolo`.

## Setup map

| Step | Outcome |
| --- | --- |
| 1. Install Grin | `grin` is available locally |
| 2. Install `tunnel-client` | the official tunnel runtime is available |
| 3. Create/select tunnel + runtime key | you have a tunnel ID and restricted runtime API key |
| 4. Register workspaces and start Grin | local MCP endpoint is healthy/ready |
| 5. Run `tunnel-client` | the local MCP endpoint is connected to the OpenAI tunnel |
| 6. Connect ChatGPT | ChatGPT can discover Grin tools |
| 7. Run the end-to-end checks | workspace routing, approvals, tools, and TUI are verified |

## What this setup does

Grin is a supervised local MCP runtime. ChatGPT owns conversation and
reasoning; Grin owns bounded local capabilities, policy, approvals, and the
terminal interface.

The connection is:

```text
ChatGPT
  -> OpenAI Secure MCP Tunnel
  -> tunnel-client
  -> http://127.0.0.1:8765/mcp
  -> Grin
  -> local workspace
```

Grin remains loopback-only. Do not expose Grin on a public address or port.

## 1. Install Grin

Install the latest macOS or Linux binary into `~/.local/bin`:

```zsh
curl -fsSL https://raw.githubusercontent.com/duongnvt2110/grin/main/scripts/install.sh | sh
```

For a specific release:

```zsh
curl -fsSL https://raw.githubusercontent.com/duongnvt2110/grin/main/scripts/install.sh \
  | sh -s -- --version v0.1.0
```

The installer verifies the selected release archive with its SHA-256 checksum.
It supports macOS and Linux on amd64 and arm64, installs to `~/.local/bin` by
default, and does not install `tunnel-client`.

Add the default install directory to the current shell's `PATH`, then verify the
installed binary:

```zsh
export PATH="$HOME/.local/bin:$PATH"
command -v grin
grin --version
```

As a manual fallback, download the matching archive and `checksums.txt` from
the [GitHub Releases](https://github.com/duongnvt2110/grin/releases) page:

```zsh
VERSION=v0.1.0
ARCHIVE=grin_Darwin_arm64.tar.gz # choose the matching OS and architecture
BASE_URL="https://github.com/duongnvt2110/grin/releases/download/$VERSION"
mkdir -p /tmp/grin-release
cd /tmp/grin-release
curl -fsSLO "$BASE_URL/$ARCHIVE"
curl -fsSLO "$BASE_URL/checksums.txt"
grep "  $ARCHIVE$" checksums.txt | shasum -a 256 -c -
tar -xzf "$ARCHIVE"
mkdir -p ~/.local/bin
install -m 0755 grin ~/.local/bin/grin
```

On Linux, replace `shasum -a 256 -c -` with `sha256sum -c -`.

To upgrade an installed release later:

```zsh
grin upgrade
```

The command checks the latest stable GitHub release, verifies the same
GoReleaser SHA-256 checksum used by the installer, and atomically replaces the
current Grin binary. It does not use `sudo` automatically; the install directory
must be writable. Source/development builds do not self-upgrade.

If no release is available yet, clone the source and verify the checkout. Start
Grin later in the dedicated startup step:

```zsh
git clone https://github.com/duongnvt2110/grin.git
cd grin
go run ./cmd/grin --version
```

## 2. Install the official `tunnel-client`

`tunnel-client` is a separate OpenAI project. Grin does not bundle or install
it.

On macOS, the official project recommends Homebrew:

```zsh
brew install openai/tools/tunnel-client
tunnel-client --version
tunnel-client help quickstart
```

On Linux, use an official Linux amd64/arm64 release from the
[`tunnel-client` releases](https://github.com/openai/tunnel-client/releases/latest),
or build the official source:

```zsh
git clone https://github.com/openai/tunnel-client.git
cd tunnel-client
mkdir -p bin
go build -o bin/tunnel-client ./cmd/client
export PATH="$PWD/bin:$PATH"
command -v tunnel-client
tunnel-client --version
```

If you build `tunnel-client` from source, keep this shell open and use it as
Terminal B below so the temporary `PATH` entry remains available.

Keep the `tunnel-client` version/installation instructions from its official
repository authoritative; Grin only documents the integration points it needs.

## 3. Required accounts and software

- An installed Grin binary or a runnable Grin checkout.
- A workspace directory.
- Go 1.26.6 or newer when running from source.
- The official OpenAI `tunnel-client`.
- An OpenAI organization with access to Tunnels management.
- A single-operator tunnel.
- A restricted runtime API key with Tunnels Read + Use.
- ChatGPT access to the MCP connector.
- Git 2.45 or newer for the dedicated Git inspection tools.

The `tunnel-client` repository is public and Apache-2.0 licensed. The public
documentation does not state a separate per-tunnel price. OpenAI organization,
ChatGPT, or API usage may have separate plan or usage charges; confirm billing
in your organization before creating a tunnel.

### Canonical setup pages

| Page | Purpose |
| --- | --- |
| [Tunnels management](https://platform.openai.com/settings/organization/tunnels) | Create or inspect the tunnel and copy its ID. |
| [Runtime API keys](https://platform.openai.com/settings/organization/api-keys) | Create the restricted key used by the long-running client. |
| [Admin API keys](https://platform.openai.com/settings/organization/admin-keys) | Optional; only for CLI tunnel CRUD. |
| [ChatGPT connector settings](https://chatgpt.com/#settings/Connectors) | Select the same tunnel in ChatGPT. |

Do not use an admin key as the long-running runtime key.

## 4. Create the tunnel and runtime key

### Personal ChatGPT: use the default workspace

For a personal ChatGPT account, create the tunnel in the **default workspace**
shown by Tunnels management. Use that same default workspace when creating the
ChatGPT connector.

Do not create the tunnel in a team, enterprise, or unrelated organization
workspace and then try to connect it from personal ChatGPT. A workspace
mismatch can produce ChatGPT's generic `Error creating connector` message.

If a tunnel already exists in the wrong workspace, create a new tunnel in the
default workspace rather than reusing the mismatched tunnel.

### Create or select the tunnel

Open **Tunnels management**, create a tunnel, and copy the generated value:

```text
tunnel_...
```

This is `CONTROL_PLANE_TUNNEL_ID`.

### Create the runtime API key

Open **Runtime API keys** in the same default workspace and create a restricted
key with:

```text
Tunnels: Read + Use
```

Copy the key when it is shown. This is `CONTROL_PLANE_API_KEY`.

The admin key is only required when using commands such as:

```text
tunnel-client admin tunnels create
tunnel-client admin tunnels list
tunnel-client admin tunnels update
tunnel-client admin tunnels delete
```

## 5. Configure values without exposing secrets

For the recommended foreground setup, use two terminals:

- **Terminal A:** start Grin and leave it running.
- **Terminal B:** configure the tunnel values, verify Grin, and run
  `tunnel-client`.

If `~/.local/bin` is not already on your shell `PATH`, run this once in each
new terminal that needs the installed `grin` command:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

In Terminal B, enter the tunnel values without putting the runtime key in shell
history. These commands work in Bash and zsh:

```sh
printf 'Tunnel ID: '
IFS= read -r CONTROL_PLANE_TUNNEL_ID
export CONTROL_PLANE_TUNNEL_ID

printf 'Runtime API key: '
IFS= read -r -s CONTROL_PLANE_API_KEY
printf '\n'
export CONTROL_PLANE_API_KEY

export MCP_SERVER_URL=http://127.0.0.1:8765/mcp
```

Never commit, echo, log, screenshot, or paste the API key. Do not store these
values in Grin source files.

## 6. Register workspaces, start Grin, and verify readiness

Use Terminal A for Grin so you can answer approval prompts while Terminal B
remains available for verification and `tunnel-client`:

For a copy/paste disposable workspace:

```zsh
mkdir -p /tmp/grin-demo
grin init --workspace /tmp/grin-demo
```

For real multi-workspace use, register each existing project once:

```zsh
grin init --workspace /path/to/project-a
grin init --workspace /path/to/project-b
```

If Grin is being run only from source, run the same `init` command from the
Grin checkout, for example:

```zsh
(cd /path/to/grin && go run ./cmd/grin init --workspace /tmp/grin-demo)
```

The command creates `<workspace>/.grin/config.yaml` when it is missing and adds
the canonical path to `~/.grin/config.yaml`. A newly created workspace config
shows the default shell environment allow-list:

```yaml
version: 1
shell:
  allowed_environment:
    - PATH
    - LANG
    - LC_ALL
```

If the config was created by an older `grin init` and contains only the exact
legacy `version: 1` file, running `grin init` again upgrades it. Customized
configs are preserved unchanged, and omitted settings continue to use Grin's
built-in defaults. The running Grin process reads the registry for
`workspace.list` and each workspace-scoped request, so a new registration is
available without restarting Grin.

When running from inside a workspace, Grin can discover the workspace
automatically by walking upward to the nearest `.grin/config.yaml`:

```zsh
cd project/internal/mcp
grin
```

Start an installed binary explicitly:

```zsh
grin --workspace /tmp/grin-demo
```

From a source checkout, use the equivalent from the Grin repository in
Terminal A:

```zsh
go run ./cmd/grin --workspace /tmp/grin-demo
```

For an explicit YOLO session:

```zsh
grin --workspace /tmp/grin-demo --yolo
```

YOLO bypasses Grin's workspace containment and approval restrictions for
supported tools. The operating system still controls access, and Grin keeps
input validation, output limits, timeouts, cancellation, and process cleanup.
Use it only when you intentionally want those Grin restrictions bypassed.
`grin doctor --yolo` is not supported.

While Grin remains running in Terminal A, verify the local boundary from
Terminal B:

```zsh
grin doctor --workspace /tmp/grin-demo --check-ready
curl -fsS http://127.0.0.1:8765/healthz
curl -fsS http://127.0.0.1:8765/readyz
```

If Grin is being run only from source, replace the `grin doctor ...` line with
a command that runs from the Grin checkout without changing Terminal B's
working directory:

```zsh
(cd /path/to/grin && go run ./cmd/grin doctor --workspace /tmp/grin-demo --check-ready)
```

Expected results include:

```text
loopback_bind=PASS
mcp_endpoint=loopback-only
public_inbound_binding=disabled
readiness=PASS
secret_values=never printed
```

The MCP target for the tunnel client is always:

```text
http://127.0.0.1:8765/mcp
```

## 7. Run the tunnel client

The client must remain running while ChatGPT uses the connector.

### Recommended: foreground mode

Create a `tunnel-client` connection profile, validate it, and run it in the current terminal:

```zsh
tunnel-client init \
  --sample sample_mcp_remote_no_auth \
  --profile grin \
  --tunnel-id "$CONTROL_PLANE_TUNNEL_ID" \
  --control-plane-api-key-ref env:CONTROL_PLANE_API_KEY \
  --mcp-server-url "$MCP_SERVER_URL" \
  --force

tunnel-client doctor --profile grin --explain
tunnel-client run --profile grin
```

Before continuing to ChatGPT, confirm all three conditions:

- `tunnel-client doctor --profile grin --explain` succeeds.
- Grin's `http://127.0.0.1:8765/readyz` endpoint returns HTTP `200`.
- `tunnel-client run --profile grin` is still running in its terminal.

### Optional: managed background runtime

Use managed runtime supervision when you do not want to keep the tunnel
terminal attached. Do not use `nohup` or `disown`:

```zsh
tunnel-client runtimes connect \
  --alias grin \
  --tunnel-id "$CONTROL_PLANE_TUNNEL_ID" \
  --runtime-api-key env:CONTROL_PLANE_API_KEY \
  --mcp-server-url "$MCP_SERVER_URL" \
  --json

tunnel-client runtimes status grin --json
```

Only continue when the status reports the managed process as running, healthy,
and ready. Stop it with:

```zsh
tunnel-client runtimes stop grin
```

Managed tunnel supervision does not replace Grin supervision. Grin must also
remain running and ready.

## 8. Connect ChatGPT

After `tunnel-client` is healthy:

1. Open [ChatGPT connector settings](https://chatgpt.com/#settings/Connectors).
2. Confirm ChatGPT is using your personal account's default workspace.
3. Choose the Tunnel connection.
4. Select the tunnel created in that same default workspace.
5. Keep Grin and `tunnel-client` running during connector discovery and calls.

If the tunnel is not listed, check the workspace scope, connector permissions,
the tunnel ID, and the local `/readyz` result.

## 9. Test Grin end to end

Run these tests from ChatGPT in order:

1. Call `system.info`; confirm that only non-secret system information is
   returned.
2. Call `workspace.list`; copy an exact absolute path from the result.
3. Call `workspace.info` with that path; confirm that the selected workspace is
   reported.
4. Request `fs.write_text` with the same `workspace` path for `approved.txt`;
   verify it
   completes without approval.
5. Request `fs.edit_text` with the same `workspace` path to replace one exact
   occurrence in that file; verify
   the content changes and the file remains readable.
6. Request `git.status`, `git.diff`, `git.log`, and `git.show` with that
   `workspace` path when it is a simple Git repository; verify bounded results.
7. Request a harmless `shell.run` operation with that `workspace` path; verify it completes without
   approval.
8. Request an obvious destructive shell command; confirm the TUI pauses for
   approval, reject it, and verify there is no side effect.
9. Request an outside workspace write or shell `cwd`; confirm approval is
   required, then approve one and verify the operation completes.
10. Confirm the Grin TUI records each request and resolution with its workspace.

For repository searches, use `fs.search` for simple text or file-name queries.
Use `shell.run` with `rg` for regex, glob, file-type, or ignored-file searches
when `rg` is available.

For the disposable workspace above:

```zsh
test -f /tmp/grin-demo/approved.txt && echo approved_write_passed
```

Record only redacted output. Never record API keys, tunnel IDs, cookies,
authorization headers, or raw environment values.

## 10. Troubleshooting

### `tunnel-client` says the tunnel ID is missing

Set `CONTROL_PLANE_TUNNEL_ID` in the same shell that runs the client, or pass
the ID to `tunnel-client init` / `runtimes connect`.

### `tunnel-client` says the runtime key is missing

Create a Runtime API key with Tunnels Read + Use. Do not substitute an admin
key. Set it as `CONTROL_PLANE_API_KEY` without printing it.

### `alias grin is not known`

Run `tunnel-client runtimes connect ... --alias grin` first. `status` only
inspects an alias that has already been created by `connect`.

### ChatGPT says `Error creating connector`

For personal ChatGPT, verify all of these before retrying:

1. The tunnel was created in the personal account's default workspace.
2. ChatGPT connector settings are using that same default workspace.
3. The runtime API key belongs to the same workspace and has Tunnels Read +
   Use.
4. Grin `/readyz` returns `200`.
5. `tunnel-client` is running and has passed its readiness check.

Do not use the local `/.well-known/oauth-protected-resource/mcp` URL as the
connector URL. ChatGPT selects the OpenAI tunnel; the tunnel client forwards
to Grin's loopback URL:

```text
http://127.0.0.1:8765/mcp
```

### `loopback_bind=FAIL`

Stop Grin and restart it without a public bind address. Grin must use a
loopback address such as `127.0.0.1`.

### `/readyz` is unavailable

Confirm that Grin is running, the workspace is readable, and port `8765` is
available. Run `doctor --check-ready` again after startup.

### An operation does not execute

In normal mode, check the Grin TUI when the operation is expected to require
approval. Inside workspace writes and normal shell commands run after policy
and validation checks. Outside reads are allowed in normal mode. Outside
writes, outside shell `cwd`, and obvious destructive shell commands require
approval. Rejection, timeout, cancellation, policy denial, or failed approval
delivery must prevent the side effect. YOLO sessions intentionally bypass
Grin approval prompts for supported tools.

## 11. Security boundary

Grin V1 provides:

- Loopback-only MCP binding.
- In normal mode, outside reads are allowed; outside writes and outside
  shell `cwd` require approval.
- Obvious destructive shell commands require approval. Grin is not a complete
  shell or filesystem sandbox.
- `ALLOW`, `ASK`, and `DENY` policy decisions.
- Local approval for shell execution and any other operation requiring it under
  the active policy.
- Bounded request, output, timeout, and process behavior.
- Explicit shell executable and argument handling.
- Environment filtering and process metadata redaction.
- Cancellation and fail-closed approval delivery.

V1 does not sandbox malicious code already running under the same operating
system account. Kernel or administrator compromise, advanced filesystem
time-of-check/time-of-use attacks, tunnel-provider compromise, and actions
explicitly approved by the operator remain outside the V1 threat model.

## 12. Development verification

From the repository root:

```zsh
go test ./...
go test -race ./...
go vet ./...
go mod verify
go run ./cmd/grin --version
go run ./cmd/grin --help
```

Local tests do not replace the real ChatGPT-through-tunnel acceptance test.

## Official references

- [OpenAI tunnel-client repository](https://github.com/openai/tunnel-client)
- [Tunnel end-user guide](https://github.com/openai/tunnel-client/blob/master/docs/end-user-guide.md)
- [Tunnel permissions](https://github.com/openai/tunnel-client/blob/master/docs/permissions.md)
- [Tunnel architecture](https://github.com/openai/tunnel-client/blob/master/docs/architecture.md)
