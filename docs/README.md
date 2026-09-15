# Grin Documentation

Use this page to choose the shortest guide for what you are trying to do.

| If you need to... | Start here |
| --- | --- |
| Install Grin and run it locally | [README quick start](../README.md#quick-start) |
| Understand normal mode vs YOLO | [README modes](../README.md#modes) |
| See every CLI command and option | [README CLI reference](../README.md#cli) |
| Connect Grin to ChatGPT with Secure MCP Tunnel | [ChatGPT + tunnel onboarding](onboarding.md) |
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

components.md
  system architecture, state/config ownership, request flows

features.md
  exact user-facing capability and boundary reference

releasing.md
  maintainer release procedure
```

Internal implementation plans and review notes belong under `my_docs/` and are
not part of the end-user documentation surface.
