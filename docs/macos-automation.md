# macOS Automation Permission for Codex Discovery

Grin uses macOS Automation (Apple events) to **read terminal metadata** when
discovering a local Codex session. If macOS asks whether an app may control
Ghostty, Terminal, or iTerm, this guide explains when to approve it, how to
change the decision, and how to verify discovery afterward.

This is **optional for general Grin filesystem, Git, shell, and MCP use**.
It applies to Codex terminal-title discovery on macOS.

## When and why the prompt appears

The current flow is:

```text
ChatGPT requests a Codex review, or the user runs `grin codex discover`
    ↓
Grin starts fresh, read-only Codex terminal discovery
    ↓
macOS: Grin invokes `osascript -l JavaScript -`
    ↓
The bundled script examines already-running supported terminal apps
(Ghostty / Terminal.app / iTerm2) through their AppleScript interfaces
    ↓
macOS may show an Automation permission prompt for a target terminal app
    ├─ Allow → the script can try reading that app's terminal metadata
    └─ Don't Allow → that app's metadata may be unavailable
    ↓
Grin checks title candidates against local Codex metadata
    ↓
An eligible, unresolved candidate still requires the user's explicit
thread-ID selection before `codex.queue` may send a message
```

Grin's implementation embeds
[`internal/codex/terminal_discovery/darwin.js`](../internal/codex/terminal_discovery/darwin.js)
and passes it through standard input to `osascript` in
[`internal/codex/discovery_terminal.go`](../internal/codex/discovery_terminal.go).
It only queries a terminal app **when that app is already running**. The script
reads available window/tab/session titles, identifiers, focus and selected-tab
observations; Ghostty also provides a working-directory observation.

**The discovery script does not send keystrokes, activate or open a terminal,
change a Codex session, or queue any message.** This bundled script does
not query `System Events`; it contacts the supported terminal apps directly.
macOS calls this privilege *Automation* because Apple events may control
another application; approving it grants permission at the macOS app level,
not just permission to read titles.

The requesting application shown by macOS depends on the process responsible
for the Apple events and how Grin was launched. Look at the **actual prompt**
rather than assuming it will always name `grin` or `osascript`.
The target application may be Ghostty, Terminal, or iTerm.

## Approve the macOS prompt

1. Start Grin in your usual trusted terminal, and open your intended Codex
   session in Ghostty, Terminal.app, or iTerm2.
2. From that setup, request a Codex review in ChatGPT or run the discovery
   command in a terminal:

   ```sh
   grin codex discover --workspace /absolute/path/to/your/project
   ```

3. If macOS displays an Automation dialog, **read both application names**:
   the requesting app/process and the terminal app it wants to access.
   Only click **Allow** when you recognize both and intended to use Grin's
   terminal discovery. Choose **Don't Allow** otherwise.
4. If macOS prompts separately for different terminal apps, decide for each
   one. You do not need to enable control of apps you do not use.
5. Retry the same discovery operation and inspect its returned `sources`,
   `status`, `reason`, and `candidates` fields. Do not assume the prompt
   itself proves the correct Codex thread was found.

There may be **no prompt** when permission was already decided, the terminal
is not running, or that terminal API was not reached.

Apple's instructions: [Allow apps to automate and control other apps on Mac](https://support.apple.com/en-hk/guide/mac-help/mchl108e1718/mac).

## If you previously denied access

1. Open **Apple menu → System Settings → Privacy & Security → Automation**.
2. Find the requesting application shown by the earlier dialog. Depending
   on the launch context, it may be the terminal/host application or another
   application associated with the request. Expand its entry if necessary.
3. Locate the switch for **Ghostty**, **Terminal**, or **iTerm**, as
   applicable, and enable it **only** if you trust the requesting application
   and want that access. You may also turn it off later.
4. Retry `grin codex discover --workspace /absolute/path/to/your/project`
   using the same normal Grin launch context.

macOS controls which applications appear in the Automation list. A program
that has not requested permission might not be listed. **Do not add unrelated
Full Disk Access or Accessibility privileges as a workaround**, and do not
disable System Integrity Protection or reset macOS privacy databases merely
to get terminal discovery working.

Apple's settings guide: [Allow apps to control other apps on Mac](https://support.apple.com/en-au/guide/mac-help/mchl07817563/mac).

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| No permission dialog | Confirm the terminal app is running, Grin is executing discovery, and the decision was not already stored under System Settings → Privacy & Security → Automation. |
| Permission was denied | Use the Automation settings above; enable only the required target app for the actual requester. |
| `sources` contains `ghostty-unavailable`, `terminal-app-unavailable`, or `iterm2-unavailable` | The script could not access that terminal's AppleScript interface. Possible causes include unavailable API, an app-specific error or denied permission; **this value alone does not identify the cause**. |
| `status` is `UNSUPPORTED` or `UNRESOLVED` | Check `reason`, `sources`, and `candidates`. Apple events permission is only one possible issue; Grin also requires a valid title, Codex thread metadata and workspace matching. |
| A terminal is visible but its Codex thread is wrong or stale | Check that Codex TUI's **`/status` Session ID** matches the intended exact thread ID; title/window focus alone cannot establish current-TUI ownership, especially after `/new`, `/clear`, or `/resume`. |
| A direct `osascript` command works but Grin discovery does not | Confirm the Grin launch context and its Automation settings. Running a script manually from another host application can have a different macOS permission decision. |
| No macOS terminal API is accessible | If Grin inherited a valid, scoped tmux client, its read-only tmux fallback may still return candidates. Otherwise discovery fails closed; it does not guess a recipient. |

For a source-checkout-only diagnostic, from the **repository root** you may run:

```sh
osascript -l JavaScript internal/codex/terminal_discovery/darwin.js
```

This reads terminal observations but **does not validate the resulting Codex
thread**; the installed Grin binary already embeds this script, so the source
file need not exist for normal `grin codex discover` usage. A direct
`osascript` invocation may be attributed to a different requesting
application from the one launching Grin. Prefer the Grin CLI for end-to-end
discovery diagnostics, and avoid posting raw terminal titles or local paths
publicly if they contain sensitive details.

## Two different approvals

| Permission/decision | Who makes it | What it authorizes |
| --- | --- | --- |
| **macOS Automation → Allow** | You, in the macOS dialog or System Settings | Allows the named requesting application to use Apple events with the selected target application. It is an OS privacy decision. |
| **Grin policy/approval (Normal mode)** | Grin's policy and its local TUI approval workflow | Decides whether a particular requested MCP operation may proceed according to workspace/policy rules. |
| **Codex recipient selection** | You choose an exact candidate thread ID | Targets a Codex review request. An eligible `UNRESOLVED` title is **not** automatically selected. |

Granting macOS Automation access **does not** approve an MCP filesystem edit,
shell command or Codex message. Conversely, approving a Grin tool call does not
bypass macOS Automation controls. Grin's `--yolo` mode can bypass certain
Grin workspace/approval restrictions but **cannot bypass macOS privacy
permissions**.

See [Codex terminal discovery](codex-discovery.md) for the platform evidence
and freshness limitations, and [Codex review](codex-review.md) for safe
thread selection and queue/result behavior.
