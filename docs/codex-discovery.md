# Codex Terminal Discovery

`grin codex discover [--workspace PATH]` reads terminal titles and prints
JSON candidates. The default workspace is the current directory. It never
sends terminal input and never queues a Codex request.
The top-level `sources` array identifies the native interfaces that supplied
evidence (or the unsupported desktop context, such as Wayland).

Grin extracts a **standalone full Codex UUID or a uniquely resolvable UUID
prefix at any position** in a terminal title. The `|` field order is not
significant. Extraction runs only after title redaction, including the first
field and inline credentials. UUIDs in credentials, URLs, or filesystem paths
are not thread identity evidence. Conflicting full IDs or inconsistent prefixes
are discarded rather than selecting a guessed recipient.

A full UUID is checked using read-only app-server `thread/read` when available;
SQLite metadata provides separate workspace, archive, source, and prefix
corroboration. If the app-server cannot establish top-level eligibility, a
matching title alone is not an eligible or verified thread. Prefix-only titles
must resolve to exactly one eligible local state record.
Descriptive title components are not assigned a verified thread name or project
name merely from their positions. When independently corroborated labels are
unavailable, selection uses the thread UUID and terminal ID. Terminal workspace
and Codex-state workspace are separate evidence; `workspace_match` can be
`state_only` if no terminal workspace is available.

On macOS, Grin runs the embedded terminal script through `osascript`. The
first query to a running terminal app may produce an **Automation** permission
dialog. See [macOS Automation permission](macos-automation.md) for the exact
read-only discovery flow, how to approve/deny it safely, and how to recover
from an earlier denial. This OS permission is distinct from Grin's own MCP
approval and Codex recipient selection.

## Platform support

| Platform | Read-only title source | Limits |
| --- | --- | --- |
| macOS Ghostty | AppleScript window, tab, and terminal IDs; titles, working directories, selected-tab, active-in-tab, and foreground focus observations | Queried only while Ghostty is already running. Requires Automation access, which may trigger a macOS permission prompt. |
| macOS Terminal.app | AppleScript window and tab titles, selected tab, and tab TTY identity | Queried only while Terminal is already running; per-tab working directory is unavailable. |
| macOS iTerm2 | AppleScript window/tab/session IDs, tab title, and selected session | Queried only while iTerm2 is already running; per-session working directory is unavailable. |
| macOS tmux | Pane title, ID, path, and active state from the inherited client scope | Queried only when both inherited `TMUX` and `TMUX_PANE` identify an absolute socket and pane; uses that socket and its current session only. |
| Linux X11 | EWMH window list and title via `xprop` | Requires X11, an EWMH window manager, and `xprop`; terminal tab identity and working directory are unavailable. |
| Linux tmux | Pane title, ID, path, and active state from `tmux list-panes -s` | Queried only when inherited `TMUX` has an absolute socket and session metadata and `TMUX_PANE` identifies a pane; lists panes in that session only, never server-wide. |
| Linux Wayland | No generic title API; scoped tmux remains available | Wayland does not globally short-circuit the inherited tmux adapter. XWayland discovery may find X11 windows but does not cover native Wayland windows. Without a usable X11 or scoped tmux source, the result is `UNSUPPORTED`. |
| Windows | Win32 visible top-level window titles and foreground window | A window title is a window candidate, not a selected tab. No universal Windows Terminal tab API or native tmux/WSL enumeration is assumed; working directory and Codex ownership remain unavailable. |

kitty and WezTerm remote-control interfaces are not queried unless a supported,
already-authorized interface is available; this implementation does not enable
remote control or probe sockets. Windows UI Automation tab metadata and
process-to-window correlation are not used because the available implementation
has no verified cross-terminal association for those signals.

If the native title API or required local metadata is unavailable, the result
is `UNSUPPORTED` or `UNRESOLVED` with evidence. The JSON `complete` field
is separate from `status`: it is true only when at least one applicable source
was successfully enumerated and all applicable sources were read exhaustively.
A terminal app that is not running, or an optional tmux scope that is absent,
does not make an otherwise exhaustive scan incomplete. An attempted source
that fails, times out, or reaches a scan/output limit does. Successful partial
fallback observations may still appear for diagnostics, but `codex.queue`
refuses **both explicitly selected and omitted thread IDs** when
`complete=false`. Missing/legacy completeness signals also fail closed.
This is not proof that a title is fresh after `/new`, `/clear`, or `/resume`.

Prefix collisions, archived threads, and threads that are not user-originated
remain ineligible. Candidates with a known workspace mismatch are omitted
from the default output. Missing workspace metadata is reported as unknown
and does not by itself make a candidate eligible.

Every candidate with a matching full UUID or uniquely resolved local state
record is checked with a read-only local app-server `thread/read` when available.
Ghostty reports `window_id`, `tab_id`, `terminal_id`, `selected_tab`, and
`active_in_tab` separately from `focused` (foreground focus). Multiple Ghostty
windows may each contain a selected tab; `active_in_tab` is omitted if the
native focused-terminal identity is unavailable. These fields help distinguish
terminal surfaces and splits, but neither selection nor title identity proves
which Codex conversation a TUI currently displays. Terminal.app reports
`selected_tab` even when not foreground; iTerm2 reports selected sessions;
Linux and macOS tmux report panes only from the inherited client socket and
session. X11 and Windows foreground windows do not identify a selected tab.
Metadata checks establish thread eligibility only, not displayed-TUI ownership.
A failed `thread/read` leaves top-level eligibility `unknown`.

## Verification limit

`VERIFIED` requires proof that the displayed Codex TUI still owns the resolved
thread. A live terminal title read, active-window flag, matching workspace,
and a local thread record do not prove that title freshness. In particular,
Grin has not verified how Codex updates the configured title after `/new`,
`/clear`, or `/resume`. Therefore title-derived candidates currently remain
`UNRESOLVED`; stale titles cannot be distinguished from fresh titles unless
local metadata independently rejects them, such as an archived thread.
The `/new`, `/clear`, and `/resume` parser tests use supplied title snapshots;
they do not drive Codex or claim the TUI emitted those title updates.

The result includes matching title candidates, their local resolution,
workspace and top-level checks, and the evidence available from that platform.
Do not queue a request based only on an `UNRESOLVED` candidate, even when it
is the only eligible result. When eligible candidates pass ambiguity checks,
`codex.queue` returns them for explicit recipient selection; supply the chosen
exact thread ID through the existing authorized workflow. Selection chooses a
task recipient but does not prove which thread the TUI currently displays.

## Read-only observation and manual acceptance

Linux script execution is covered by Go tests that run the bundled `linux.sh`
with fake `xprop` and `tmux` commands. They check two X11 windows, the active
X11 window, `WM_NAME` fallback, Wayland without X11, mixed XWayland, unavailable
X11, inherited tmux session scope, and malformed records. The scoped tmux
adapters (Linux script and Go fallback) use `list-panes -s -t <session>`;
`list-panes -a` ignores `-t` and must not be used for session-scoped
discovery. These are fixture tests, **not** live Linux desktop or Codex TUI
transition tests. Linux native acceptance still requires comparing a test
Codex TUI's `/status` Session ID to discovery after `/new`, `/clear`,
and `/resume`.

On macOS, run the native observation script without messaging any Codex TUI:

```sh
osascript -l JavaScript internal/codex/terminal_discovery/darwin.js
./grin codex discover --workspace /absolute/path/to/workspace
```

Use an installed `grin` binary instead of `./grin` if applicable. For a
**designated test TUI**, manually open Codex `/status` and compare its Session
ID with the corresponding `terminal_id` title candidate and Grin output.
Repeat after manually running `/new`, `/clear`, and `/resume` in that test TUI,
including before the first user message in each new thread. Also check two
same-workspace TUIs, switching tabs and windows, and splits where one tab has
multiple terminal surfaces. Record the observed ID and result for each state.
Do not run lifecycle commands against existing user sessions without their
explicit designation. A successful title match is evidence for that snapshot,
not universal proof of freshness: the overall discovery result remains
`UNRESOLVED` unless a separately justified current-TUI identity mechanism is
implemented and verified. These checks do not queue a Grin request.

Native title enumeration has a 12-second bound on Linux and Windows. macOS
allows 10 seconds for AppleScript and up to 5 seconds for the scoped tmux
fallback, within a 15-second combined bound. Complete discovery is bounded at
20 seconds. A probe timeout returns `UNRESOLVED`; `UNSUPPORTED` means no
supported API or inherited tmux scope was available.
