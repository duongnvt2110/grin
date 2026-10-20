# Codex Review

Use this guide when you want ChatGPT to work with Codex through Grin's MCP
server. Grin supports three human-visible behaviors:

- **Queue Only** — send one message to Codex and stop.
- **Single Review** — send one review request, retrieve the exact result, have
  ChatGPT review it once, then stop.
- **Review Loop** — repeatedly send findings back to the same Codex thread until
  the requested stop condition is reached.

The MCP tools are `codex.queue` and `codex.turn_result`. Grin
uses Codex app-server for session and turn operations. Every `codex.queue`
call performs fresh, read-only terminal discovery before sending; there is
no server-side cached recipient. Use `grin codex discover` to inspect
candidates separately. Neither method proves which thread is currently
displayed in an independent Codex TUI.

For repository-backed validation of review findings, see the [Grin Codex Review
skill](https://github.com/duongnvt2110/dotfiles/tree/main/.codex/skills/personal/grin-codex-review).

## What you need

Before starting:

1. Run Grin and connect ChatGPT through the Secure MCP Tunnel.
2. Ensure the local Codex app-server is available in the default `~/.codex`
   data directory.
3. In Normal mode, register that workspace with Grin:

   ```zsh
   grin init --workspace /path/to/project
   ```

4. Keep the default Codex data directory at `~/.codex`.

On macOS, fresh Codex discovery can invoke `osascript` and trigger a system
**Automation** prompt for a terminal app such as Ghostty, Terminal.app, or
iTerm2. See [macOS Automation permission](macos-automation.md) for what the
request means, when to select **Allow**, and how to retry after a denial.
This is a macOS permission, **not** Grin's TUI tool approval.

`sqlite3` remains required for exact turn-result lookup. For a **new
standalone send**, omit `thread_id` unless the user specifically named a UUID.
Grin discovers candidates afresh. Because title-derived candidates are
`UNRESOLVED`, even one eligible thread is never auto-selected. Candidates that
pass ambiguity checks return `selection_required` and send **nothing** until
the user explicitly chooses and submits that exact UUID. Multiple eligible
threads also require a choice. No eligible candidates or a discovery failure
prevents sending.

A supplied UUID is checked against fresh eligible discovery before sending.
For follow-ups in the same uninterrupted review loop, reuse the selected UUID
and pair each `request_id` with it for `codex.turn_result`. This prevents
accidental reuse of an old ID that is no longer a candidate, but cannot
distinguish an old ChatGPT selection when its ID is still present in live
terminal titles. Do not automatically reuse an ID from an unrelated task.

If the user explicitly requires proof of the **currently displayed TUI**,
compare against its `/status` Session ID or independent identity evidence.
Even corroborated title candidates cannot prove title freshness after
`/new`, `/clear`, or `/resume`.


### Connector schema mismatch after a Grin upgrade

The Grin MCP server accepts `codex.queue` with `workspace` and `message` while
`thread_id` is **optional**. If the ChatGPT connector unexpectedly requires
`thread_id`, its exposed tool schema is out of sync with the running MCP
server. Check the active binary and the live MCP `tools/list` schema; reconnect
or refresh the Grin MCP connection in ChatGPT, then check the schema again in
a new conversation. Do not insert a fake UUID or weaken recipient-selection
rules to satisfy a stale client schema.

Until the client is refreshed, an operator may run
`grin codex discover --workspace /absolute/workspace` in a terminal, choose a
verified candidate UUID explicitly and provide that UUID for the intended
`codex.queue` request. Grin still checks fresh discovery and eligibility
before any send. This is a manual fallback, not proof of the currently
displayed Codex TUI; use that TUI's `/status` Session ID if exact display
identity matters. A refreshed tool description alone is insufficient:
verify that `thread_id` is actually absent from `inputSchema.required`
as well as omitted from the ChatGPT tool-call requirements.

## Choose a review behavior

### Queue Only

Use Queue Only when you explicitly want ChatGPT to send a message to Codex but
not wait for or review the result.

Example:

```text
Only send this report to Codex.
Do not wait for the result and do not review it yet.
```

ChatGPT should select the recipient as described above, then:

```text
codex.queue(workspace, message)  # fresh server-side discovery
        ↓
eligible candidates → selection_required; user chooses UUID, then retry
        ↓
return workspace + resolved thread_id + request_id
        ↓
STOP
```

Keep those identifiers if you may want to review the result later.

### Single Review

Single Review is the default for ordinary requests such as:

```text
Send this report to Codex and review the result.
```

or:

```text
Ask Codex to review this implementation once.
```

ChatGPT should select the recipient as described above, then:

```text
codex.queue(workspace, review request)  # fresh discovery
        ↓
eligible candidates → selection_required; user chooses UUID, then retry
        ↓
resolved thread_id + request_id
        ↓
codex.turn_result(workspace, thread_id, request_id)
        ↓
pending / inProgress?
   ├─ yes → check the SAME request again
   └─ no
        ↓
completed result
        ↓
ChatGPT independently reviews it once
        ↓
show the assessment to the user
        ↓
STOP
```

Single Review does **not** automatically send ChatGPT's findings back to Codex.

If `codex.turn_result` returns `failed` or `interrupted`, Single Review stops.
ChatGPT must report the terminal status, preserve the `workspace`, `thread_id`,
and `request_id`, and **must not automatically queue a replacement request**.
If you want to retry, explicitly ask ChatGPT to retry the single review.
For a retry, reuse the same exact `thread_id` and send a fresh request with a
new `request_id`. If you ask it to target the currently displayed TUI, the
discovery command remains unresolved until freshness is independently proven.

### Review Loop

Use Review Loop only when you explicitly ask for repeated review, for example:

```text
Start a Codex review loop for this plan.
Continue until READY_FOR_IMPLEMENT.
```

or, after implementation:

```text
Review the implementation with Codex until NO_MATERIAL_FINDINGS.
```

ChatGPT should:

```text
codex.queue (exact selected thread)
    ↓
codex.turn_result
    ↓
ChatGPT independently reviews
    ↓
material findings?
├─ yes → send only those findings to the same Codex thread
│        ↓
│      repeat queue → result → review
└─ no  → stop condition reached
```

Before you explicitly say `IMPLEMENT`, the review loop is review/reconciliation
only and stops at `READY_FOR_IMPLEMENT`. ChatGPT must not infer or send
`IMPLEMENT` for you.

After you explicitly say `IMPLEMENT`, Codex may modify the approved scope and
the implementation review loop stops at `NO_MATERIAL_FINDINGS`.

If any queued turn returns `failed` or `interrupted`, Review Loop stops
immediately instead of queueing the next review. ChatGPT reports the terminal
status and preserves the current identifiers. To continue, explicitly ask
ChatGPT to continue the review loop; it should queue a fresh request with a
new `request_id` to the same exact thread ID.

Grin never falls back to a different thread or to the latest Codex result.

## Manual human review and resume

The review loop does not have to continue automatically. A human can stop it,
come back later, and explicitly ask ChatGPT to retrieve and review one queued
Codex result.

This is useful when ChatGPT stops after `codex.queue`, when a result was still
`pending` / `inProgress`, or when you simply want to decide when the next review
step happens.

You do **not** need to queue the Codex request again.

### Step 1: keep the queue identifiers

When `codex.queue` succeeds, keep these three values:

```text
workspace
thread_id
request_id
```

The `request_id` is the Grin correlation ID returned by `codex.queue`.

### Step 2: ask ChatGPT to review that exact result

The safest human-triggered request is:

```text
Review the Codex result for this queued request:

workspace: /path/to/project
thread_id: <codex-thread-id>
request_id: <grin-request-id>

Use codex.turn_result for this exact request.
Do not queue another copy.
```

ChatGPT should then call:

```text
codex.turn_result(
  workspace=/path/to/project,
  thread_id=<codex-thread-id>,
  request_id=<grin-request-id>
)
```

If the result is still `pending` or `inProgress`, ChatGPT should check the
same request again. When it is `completed`, ChatGPT can independently review
the returned Codex result.

### Step 3: choose whether to stop or continue

After ChatGPT reviews the completed result, the human decides what happens next.

**Review only — stop after ChatGPT's assessment:**

```text
Review this Codex result only.
Do not send anything back to Codex yet.
Show me the valid findings, invalid findings, unresolved findings, and verdict.
```

Use this when you want to inspect ChatGPT's judgment before another Codex turn.

**Review and continue the loop:**

```text
Review this Codex result independently.
If material findings remain, send only those findings back to the same Codex
thread and continue the review loop. Do not implement unless I explicitly say
IMPLEMENT.
```

This keeps the human in control while still allowing ChatGPT to resume the
existing Codex thread.

### If the Codex turn failed or was interrupted

A `failed` or `interrupted` request is terminal. Do not keep polling it expecting
it to become `completed`, and do not automatically submit a replacement.

For a Single Review, ask:

```text
Retry the failed/interrupted single Codex review.
Use the same exact thread_id and queue a fresh request.
```

For a Review Loop, ask:

```text
Continue the Codex review loop after the failed/interrupted turn.
Use the same exact thread_id, queue a fresh review request, and continue the
loop.
```

The terminal request's `workspace`, `thread_id`, and `request_id` should be kept
for traceability, but the retry uses a **new** `request_id` returned by the
fresh `codex.queue` call.

### If you are still in the same ChatGPT conversation

If ChatGPT still has the previous queue response in the conversation, a shorter
request is usually enough:

```text
Review the result from the last Codex request you queued.
Use the same workspace, thread_id, and request_id.
Do not queue it again.
```

For reliability, include the IDs explicitly when you have them.

### Why the Grin `request_id` matters

A successful `codex.queue` response returns:

```json
{
  "thread_id": "01...",
  "request_id": "55..."
}
```

Keep the **Grin `request_id`**. Grin inserts it into the queued Codex message
as a correlation marker:

```text
[grin-request-id: <request_id>]
```

`codex.turn_result` uses that marker to find the exact Codex turn later.

Do **not** substitute a Codex internal `turn_id`, `item_id`, rollout/history
record ID, or another Codex UI/history identifier. Those IDs are not accepted
by `codex.turn_result`.

If a request was sent directly inside Codex instead of through `codex.queue`,
it has no Grin request marker, so Grin cannot retrieve it by
`codex.turn_result`. In that case, paste the Codex result into ChatGPT
manually if you want ChatGPT to review it.

## Before `IMPLEMENT`

Before you explicitly say `IMPLEMENT`:

- Codex may review or reconcile the plan.
- ChatGPT independently checks each completed Codex result.
- Findings can be sent back to the same Codex thread.
- Production code must not be changed.
- When no material plan findings remain, ChatGPT stops at
  `READY_FOR_IMPLEMENT`.

ChatGPT must wait for your explicit `IMPLEMENT` command.

## After `IMPLEMENT`

After you explicitly say `IMPLEMENT`:

1. ChatGPT queues the approved implementation request to the selected Codex
   thread.
2. Codex implements the approved scope.
3. ChatGPT retrieves the exact correlated result.
4. ChatGPT independently reviews the repository changes.
5. Material findings go back to the same Codex thread.
6. The loop ends at `NO_MATERIAL_FINDINGS`.

```text
IMPLEMENT
   ↓
Codex implements
   ↓
ChatGPT reviews
   ├─ findings → Codex fixes → ChatGPT reviews again
   └─ clean    → NO_MATERIAL_FINDINGS
```

## The Codex tools

| Tool | What it does |
| --- | --- |
| `codex.queue` | Validates one exact thread, resumes it only if required, and queues a message. |
| `codex.turn_result` | Reads the exact turn associated with that `request_id`. |

A successful `codex.queue` call means Codex accepted the request. It does not
mean the turn has started or finished. For a newly loaded memory-only thread,
Codex may not have persisted its first turn yet: `codex.turn_result` relies on
SQLite history and cannot retrieve an unpersisted turn. Preserve the request
IDs; do not requeue just because result lookup is not yet available.

`codex.turn_result` may return:

| Status | Meaning | What ChatGPT does |
| --- | --- | --- |
| `pending` | The correlated turn is not visible yet. | Check the same request again. |
| `inProgress` | Codex is working on it. | Check the same request again. |
| `completed` | The final correlated response is available. | Review it independently. |
| `failed` | The Codex turn failed. | Stop, preserve the IDs, and wait for an explicit human retry/continue; do not auto-queue a replacement. |
| `interrupted` | The Codex turn was interrupted. | Stop, preserve the IDs, and wait for an explicit human retry/continue; do not auto-queue a replacement. |

Do not queue a duplicate request just because the result is still pending or in
progress.

## Choosing the correct Codex thread

### Finding a currently displayed TUI thread

Run `grin codex discover [--workspace PATH]` to inspect terminal title
candidates. It reads the OS terminal API and local Codex state only. A unique
UUID prefix, matching workspace, and top-level thread metadata are reported as
candidate evidence. Thread title and project name are context only.

For each new standalone send, `codex.queue` performs discovery inside the
server even if ChatGPT did not call the separate discovery CLI. Since
title-derived candidates are `UNRESOLVED`, Grin never queues an omitted ID
automatically. Eligible candidates that pass ambiguity checks return
`selection_required`; ChatGPT presents their thread titles, shortened IDs, and
terminal identities and asks the user to choose. The message is queued only
after the user submits that exact UUID. A supplied UUID must still be present
in fresh eligible discovery, including for follow-ups in the same review loop.
Never select by recency, result order, foreground, or focus. No eligible
candidates, ambiguous identity, or failed discovery stops before queueing.

For requests specifically demanding proof of the currently displayed TUI, a
title candidate is insufficient even after a user selects it; require
independent evidence such as the exact Session ID from `/status`.

The command returns `UNRESOLVED` for title candidates because available
terminal APIs do not prove that Codex refreshed the title after `/new`,
`/clear`, or `/resume`. It returns `UNSUPPORTED` when the OS does not expose a
usable title API. It does not drive terminal input, install tools, queue
requests, or claim `VERIFIED` from a title or foreground window alone.
An explicit user selection designates the corresponding full thread ID as the
target for the ongoing task; it does not prove which TUI is currently displayed
or authorize sending or implementing anything by itself. If the user
already asked to send a specific report, choosing its recipient completes
that targeting step without requiring another send authorization. Queueing
still requires the relevant user authorization and Grin's existing validation.
Grin enforces fresh technical eligibility and refuses ambiguous automatic
routing. The MCP client must still respect a human's choice when multiple
candidates exist; Grin cannot independently prove who made that choice.
Normal and YOLO policy behavior is unchanged.

This targets sessions through Grin's MCP/app-server connection. It does not
track which thread an independently operated Codex TUI currently displays.

### Manual recipient-selection check

Use the MCP tests with a mocked Codex service so no message reaches existing
active threads. Check zero, one, and multiple eligible candidates. Without an
ID, eligible `UNRESOLVED` candidates that pass ambiguity checks must return
`selection_required` and queue nothing. A selected UUID must match a freshly
discovered candidate, and a stale UUID must be rejected. Simulate a changed
title UUID between consecutive standalone sends and confirm the newly explicit
ID is used without a cached fallback. A real queue/result test requires separately
authorized disposable Codex sessions; mocked tests cannot verify TUI title
freshness or session ownership.

## Normal mode and YOLO

### Normal mode

The workspace must be an absolute registered Grin workspace. Grin lexically
cleans the path before comparing it with the canonical registered path.
Symlink aliases are not accepted.

### YOLO mode

Codex tools still require an explicit absolute workspace, but it may be any
existing workspace. This is different from filesystem, Git, and shell tools,
which keep YOLO's startup-workspace behavior.

## Safety and privacy

Grin keeps the Codex integration narrow:

- requests target an exact app-server thread verified for the selected workspace;
- queued message content is omitted from Grin lifecycle summaries;
- results are read only from the exact correlated request;
- results are bounded and credential-redacted before returning to ChatGPT;
- private-key material is blocked;
- Grin does not expose arbitrary Codex history, raw SQL, reasoning, or command
  output;
- alternate `CODEX_HOME` roots are not supported.


## Important limitation

Codex does not independently push a new message into a finished ChatGPT turn.

The normal flow is pull-based:

```text
ChatGPT queues work
      ↓
Codex finishes
      ↓
ChatGPT calls codex.turn_result
      ↓
ChatGPT continues the review
```

While ChatGPT is actively running the review loop, it can keep checking the
same request until the result completes.

## Troubleshooting

### ChatGPT cannot see the `codex.*` tools

Reconnect or refresh the Grin MCP connection so ChatGPT receives the current
tool catalog.

### The result stays `pending` or `inProgress`

Keep the same `workspace`, `thread_id`, and `request_id` and check again.
Do not submit another copy of the request.

### Result lookup is unsupported

Confirm that Codex is using the default `~/.codex` data directory and that
`sqlite3` is available.

For the detailed tool, policy, and security contract, see
[Features and boundaries](features.md#codex-review-tools).
