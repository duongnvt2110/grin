# Changelog

All notable Grin changes are documented here.

## Unreleased

No unreleased changes yet.

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
