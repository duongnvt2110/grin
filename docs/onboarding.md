# Grin

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

## Install Grin

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

As a manual fallback, download the matching archive and `checksums.txt` from
the [GitHub Releases](https://github.com/duongnvt2110/grin/releases) page:

```zsh
ARCHIVE=grin_Darwin_arm64.tar.gz # choose the matching OS and architecture
grep "  $ARCHIVE$" checksums.txt | shasum -a 256 -c -
tar -xzf "$ARCHIVE"
mkdir -p ~/.local/bin
install -m 0755 grin ~/.local/bin/grin
```

On Linux, replace `shasum -a 256 -c -` with `sha256sum -c -`.

## Required accounts and software

- An installed Grin binary or a runnable Grin checkout.
- A workspace directory.
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

## Canonical setup pages

| Page | Purpose |
| --- | --- |
| [Tunnels management](https://platform.openai.com/settings/organization/tunnels) | Create or inspect the tunnel and copy its ID. |
| [Runtime API keys](https://platform.openai.com/settings/organization/api-keys) | Create the restricted key used by the long-running client. |
| [Admin API keys](https://platform.openai.com/settings/organization/admin-keys) | Optional; only for CLI tunnel CRUD. |
| [ChatGPT connector settings](https://chatgpt.com/#settings/Connectors) | Select the same tunnel in ChatGPT. |

Do not use an admin key as the long-running runtime key.

## Create the tunnel and runtime key

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

## Configure values without exposing secrets

In the terminal where you will run Grin and `tunnel-client`, enter the values
without putting the runtime key in shell history:

```zsh
read -r "CONTROL_PLANE_TUNNEL_ID?Tunnel ID: "
export CONTROL_PLANE_TUNNEL_ID

read -r -s "CONTROL_PLANE_API_KEY?Runtime API key: "
export CONTROL_PLANE_API_KEY
echo

export MCP_SERVER_URL=http://127.0.0.1:8765/mcp
```

Never commit, echo, log, screenshot, or paste the API key. Do not store these
values in Grin source files.

## Start and verify Grin

Use a visible terminal for Grin so you can answer approval prompts:

When running from inside a workspace, Grin can discover the workspace
automatically by walking upward to the nearest `.grin/config.yaml`:

```zsh
cd project/internal/mcp
grin
```

You can also select a workspace explicitly:

```zsh
go run ./cmd/grin --workspace /tmp/grin-demo
```

Or run an installed binary:

```zsh
grin --workspace /tmp/grin-demo
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

In a second terminal, verify the local boundary:

```zsh
go run ./cmd/grin doctor --workspace /tmp/grin-demo --check-ready
curl -fsS http://127.0.0.1:8765/healthz
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

## Run the tunnel client

The client must remain running while ChatGPT uses the connector.

### Foreground mode

Create a profile, validate it, and run it in the current terminal:

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

### Managed background mode

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

## Connect ChatGPT

After `tunnel-client` is healthy:

1. Open [ChatGPT connector settings](https://chatgpt.com/#settings/Connectors).
2. Confirm ChatGPT is using your personal account's default workspace.
3. Choose the Tunnel connection.
4. Select the tunnel created in that same default workspace.
5. Keep Grin and `tunnel-client` running during connector discovery and calls.

If the tunnel is not listed, check the workspace scope, connector permissions,
the tunnel ID, and the local `/readyz` result.

## Test Grin end to end

Run these tests from ChatGPT in order:

1. Call `system.info`; confirm that only non-secret system information is
   returned.
2. Call `workspace.info`; confirm that the configured workspace is reported.
3. Request `fs.write_text` for `approved.txt` inside the workspace; verify it
   completes without approval.
4. Request `fs.edit_text` to replace one exact occurrence in that file; verify
   the content changes and the file remains readable.
5. Request `git.status`, `git.diff`, `git.log`, and `git.show` when the
   workspace is a simple Git repository; verify bounded results.
6. Request a harmless `shell.run` operation; verify it completes without
   approval.
7. Request an obvious destructive shell command; confirm the TUI pauses for
   approval, reject it, and verify there is no side effect.
8. Request an outside workspace write or shell `cwd`; confirm approval is
   required, then approve one and verify the operation completes.
9. Confirm the Grin TUI records each request and resolution.

For repository searches, use `fs.search` for simple text or file-name queries.
Use `shell.run` with `rg` for regex, glob, file-type, or ignored-file searches
when `rg` is available.

For the disposable workspace above:

```zsh
test -f /tmp/grin-demo/approved.txt && echo approved_write_passed
```

Record only redacted output. Never record API keys, tunnel IDs, cookies,
authorization headers, or raw environment values.

## Troubleshooting

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
and validation checks. Outside reads are allowed in `workspace` mode. Outside
writes, outside shell `cwd`, and obvious destructive shell commands require
approval. Rejection, timeout, cancellation, policy denial, or failed approval
delivery must prevent the side effect. YOLO sessions intentionally bypass
Grin approval prompts for supported tools.

## Security boundary

Grin V1 provides:

- Loopback-only MCP binding.
- In `workspace` mode, outside reads are allowed; outside writes and outside
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

## Development verification

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
