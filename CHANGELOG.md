# Changelog

All notable Grin changes are documented here.

## 0.4.1 - 2026-10-27

### Added

- Stable MCP tool schemas across Normal and YOLO modes, with explicit YOLO
  workspace routing for scoped tools and registry-backed `workspace.list`.
- Position-independent Codex thread-ID discovery from terminal titles while
  retaining explicit recipient selection and fail-closed handling of incomplete
  discovery.

### Fixed

- Redacted inline credentials before Codex title identity extraction and
  serialized discovery output.
- Marked malformed or incomplete terminal enumeration as incomplete so it
  cannot authorize a Codex queue request.

## 0.4.0 - 2026-10-20

### Added

- Cross-platform, read-only `grin codex discover` with macOS terminal
  AppleScript observations, Linux X11/scoped-tmux discovery, and Windows
  top-level window title observations. Discovery reports evidence and remains
  `UNRESOLVED` when the currently displayed Codex TUI cannot be verified.
- A macOS Automation permission guide for Ghostty, Terminal.app, and iTerm2
  discovery, plus updated Codex review and troubleshooting documentation.
- Apache License, Version 2.0, for Grin's open-source distribution.

### Changed

- Replaced the old `codex.list` MCP tool with fresh recipient discovery in
  `codex.queue`. Eligible title-derived candidates require explicit human
  selection of the exact thread UUID; Grin fails closed rather than selecting
  an `UNRESOLVED` candidate automatically.
- Codex queueing uses the local app-server, resuming a validated thread only
  when needed; `codex.turn_result` remains correlated to the exact request.

### Fixed

- Hardened `fs.search` to avoid opening nonregular/FIFO entries, skip
  sensitive files and private-key material, and redact recognized secrets.
- Rejected symlink-based local workspace configuration paths during
  initialization.
- Fixed reliable approval-event shutdown races and ensured failed delivery
  cannot be acknowledged as successful approval.
- Drained command stdout and stderr before process wait to avoid losing
  output under concurrent pipe activity.

### Compatibility

- Removed the `codex.list` MCP endpoint. Existing clients must use
  `codex.queue` to discover candidates and select an explicit thread ID.
- Removed the unused `limits.max_concurrent_requests` YAML option.
  Existing workspace configurations containing it must delete the key;
  Grin now rejects that unsupported setting rather than ignoring it.
- Refresh the ChatGPT Grin connector after upgrading if it continues to
  advertise a stale MCP tool schema.

## 0.3.0 - 2026-09-26

### Added

- Codex MCP tools to find live sessions, queue messages to an exact thread, and
  retrieve the result for a specific request.
- A Codex review guide covering queue-only requests, single reviews, review
  loops, workspace selection, and manual resume.

### Improved

- Codex results and filesystem reads share credential redaction; Codex message
  text is hidden from process displays.
- Workspace routing validates explicit canonical targets, with compatible
  workspace discovery behavior in YOLO mode.

### Fixed

- Blocks reads of common credential and private-key files, and blocks private
  key material found in text files or Codex results.

## 0.2.1 - 2026-09-15

### Fixed

- Clarifies normal-mode workspace discovery: `workspace.list` is used before
  scoped tools when no exact workspace path is known.
- Clarifies that `workspace.info` reports an already selected workspace and is
  not a discovery or selection tool.

## 0.2.0 - 2026-09-15

### Added

- Multi-workspace MCP routing through one Grin connector and tunnel.
- Workspace registration with `grin init` and `.grin/config.yaml`.
- TUI transcript rows with inline approval actions and workspace context.
- `grin upgrade` for updating an installed binary from GitHub Releases.
- macOS and Linux release archives with checksums and installer documentation.

### Improved

- Added filesystem, Git, shell, process, and system tool coverage for routed
  workspaces.
- Added workspace-aware policy, approval, environment filtering, output limits,
  and sensitive display redaction.
- Added CI verification and tag-triggered GoReleaser publishing.

## 0.1.0 - 2026-09-10

- Initial supervised local MCP runtime release.
- Bounded filesystem, Git inspection, shell, process, and system tools.
- Workspace policy, approval flow, redaction, output limits, and TUI support.
- macOS and Linux binaries for amd64 and arm64.
