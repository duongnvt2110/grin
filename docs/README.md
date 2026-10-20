# Grin Documentation

Use this page to choose the shortest guide for what you are trying to do.

| If you need to... | Start here |
| --- | --- |
| Install Grin and run it locally | [README quick start](../README.md#quick-start) |
| Understand normal mode vs YOLO | [README modes](../README.md#modes) |
| See every CLI command and option | [README CLI reference](../README.md#cli) |
| Connect Grin to ChatGPT with Secure MCP Tunnel | [ChatGPT + tunnel onboarding](onboarding.md) |
| Queue, review once, loop, or manually resume ChatGPT ↔ Codex work | [Codex review](codex-review.md) |
| Inspect terminal-title Codex thread candidates | [Codex terminal discovery](codex-discovery.md) |
| Approve macOS Automation access for Ghostty, Terminal, or iTerm | [macOS Automation permission](macos-automation.md) |
| Understand the runtime architecture and request flow | [System and components](components.md) |
| Review tools, modes, and current safety boundaries | [Features and boundaries](features.md) |
| Publish Grin binaries | [Release guide](releasing.md) |

## Documentation model

The documentation is intentionally split by task:

```text
README.md
  first 10 minutes: install → init → run → understand modes/CLI

onboarding.md
  full ChatGPT + Secure MCP Tunnel tutorial

codex-review.md
  queue Codex work, run a single review or review loop, and manually resume results

codex-discovery.md
  inspect read-only terminal title candidates and their verification limits

macos-automation.md
  approve or troubleshoot macOS Automation requests used by Codex discovery

components.md
  system architecture, state/config ownership, request flows

features.md
  exact user-facing capability and boundary reference

releasing.md
  maintainer release procedure
```

Internal implementation plans and review notes belong under `my_docs/` and are
not part of the end-user documentation surface.
