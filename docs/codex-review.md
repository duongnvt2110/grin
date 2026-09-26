# Codex Review

Use this guide when you want ChatGPT to work with a Codex session already
running on your machine. Grin supports three human-visible behaviors:

- **Queue Only** — send one message to Codex and stop.
- **Single Review** — send one review request, retrieve the exact result, have
  ChatGPT review it once, then stop.
- **Review Loop** — repeatedly send findings back to the same Codex thread until
  the requested stop condition is reached.

All three behaviors use the same MCP tools: `codex.list`, `codex.queue`, and
`codex.turn_result`. Grin does not add server-side workflow state.

For repository-backed validation of review findings, see the [Grin Codex Review
skill](https://github.com/duongnvt2110/dotfiles/tree/main/.codex/skills/personal/grin-codex-review).

## What you need

Before starting:

1. Run Grin and connect ChatGPT through the Secure MCP Tunnel.
2. Open Codex in the workspace you want to review.
3. In Normal mode, register that workspace with Grin:

   ```zsh
   grin init --workspace /path/to/project
   ```

4. Keep the default Codex data directory at `~/.codex`.

Live Codex discovery currently supports macOS. Grin also needs the Codex CLI
and `sqlite3` available locally.

## Choose a review behavior

### Queue Only

Use Queue Only when you explicitly want ChatGPT to send a message to Codex but
not wait for or review the result.

Example:

```text
Only send this report to Codex.
Do not wait for the result and do not review it yet.
```

ChatGPT should:

```text
codex.list(workspace)
        ↓
select the intended live thread
        ↓
codex.queue(workspace, thread_id, message)
        ↓
return workspace + thread_id + request_id
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

ChatGPT should:

```text
codex.list(workspace)
        ↓
select the intended live thread
        ↓
codex.queue(workspace, thread_id, review request)
        ↓
request_id
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
ChatGPT should run `codex.list` again before sending a fresh request with a new
`request_id`.

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
codex.queue
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
ChatGPT to continue the review loop; it should call `codex.list` again and then
queue a fresh request with a new `request_id` to the selected live thread.

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
Re-run codex.list first, then queue a fresh request to the selected live thread.
```

For a Review Loop, ask:

```text
Continue the Codex review loop after the failed/interrupted turn.
Re-run codex.list first, then queue a fresh review request to the selected live
thread and continue the loop.
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

## The three tools

| Tool | What it does |
| --- | --- |
| `codex.list` | Finds currently running top-level Codex threads for one workspace. |
| `codex.queue` | Sends a message to one exact live thread and returns a `request_id`. |
| `codex.turn_result` | Reads the exact turn associated with that `request_id`. |

A successful `codex.queue` call means Codex accepted the request. It does not
mean the turn has started or finished.

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

`codex.list` only returns currently running top-level Codex sessions that
match the requested workspace.

If there is:

- **one match:** ChatGPT can use it;
- **more than one match:** choose the intended thread explicitly;
- **no match:** open Codex in that workspace and try again.

If you change workspaces, list again. Grin does not keep a server-side
conversation-to-workspace or conversation-to-thread mapping.

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

- review requests are sent only to the selected live thread;
- queued message content is omitted from Grin lifecycle/TUI summaries;
- results are read only from the exact correlated request;
- results are bounded and credential-redacted before returning to ChatGPT;
- private-key material is blocked;
- Grin does not expose arbitrary Codex history, raw SQL, reasoning, or command
  output;
- alternate `CODEX_HOME` roots are not supported.

A same-user operating-system process inspection may still briefly see the
Codex CLI `--message` argument while the queue command is running.

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

### `codex.list` returns no match

Make sure Codex is currently open in the exact target workspace.

### Several threads are returned

Choose one explicitly. Grin intentionally does not guess.

### The result stays `pending` or `inProgress`

Keep the same `workspace`, `thread_id`, and `request_id` and check again.
Do not submit another copy of the request.

### Result lookup is unsupported

Confirm that Codex is using the default `~/.codex` data directory and that
`sqlite3` is available.

For the detailed tool, policy, and security contract, see
[Features and boundaries](features.md#codex-review-tools).
