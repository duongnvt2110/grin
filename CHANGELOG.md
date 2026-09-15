# Changelog

All notable Grin changes are documented here.

## Unreleased

No unreleased changes yet.

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
