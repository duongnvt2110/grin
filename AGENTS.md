# AGENTS.md

## Update History

Updated: 2026-09-10
Updated: 2026-09-09 15:37

## Project Engineering Guidance

Grin is a small local MCP runtime and TUI written in Go.

The goal of this file is to help agents make changes that fit the existing repository without expanding scope unnecessarily.

### Engineering scope

**Explore broadly. Prove necessity. Implement narrowly.**

- Stay focused on the requested feature.
- Prefer the smallest sufficient implementation.
- Preserve unrelated existing behavior.
- Reuse existing services, helpers, types, and package boundaries before introducing new ones.
- Do not introduce new abstractions, layers, states, frameworks, or redesigns unless the requested feature requires them.
- Before adding a change, ask: **“Would the requested feature fail, be incorrect, or be unsafe without this?”**
- If not, do not add it.
- Do not turn adjacent concerns into implementation scope.
- Stop once the requested behavior is correct and safe.

### Repository-first review and readiness

Treat the current repository as the primary source of truth for implementation reviews, planning, and technical recommendations.

Before marking a technical plan READY or PASS:

1. Inspect the relevant current implementation before recommending changes.
2. Trace every requested behavior through the actual production code path.
3. Verify validation does not block the new behavior before policy or authorization.
4. Verify required state and data are handed across every affected layer.
5. Verify ALLOW, ASK, DENY, approval, rejection, and execution paths where relevant.
6. Identify existing tests that encode behavior being changed.
7. Identify user-facing documentation that becomes inaccurate.
8. Prove each acceptance-case row against the implementation.

Do not recommend architecture based only on a plan, assumption, or generic best practice. Recommend changes only after proving they are necessary in the current repository.

A plan being minimal or well-scoped is not sufficient evidence that it is implementation-ready.

For reviews: **inspect current repo → trace production behavior → compare with requested behavior → prove correctness → remove unnecessary complexity → stop.**

### Repository conventions

Prefer extending the existing structure:

```text
cmd/grin
internal/app
internal/config
internal/filesystem
internal/runtime
internal/policy
internal/approval
internal/events
internal/mcp
internal/tui
```

General rules:

- Keep responsibilities in their existing packages.
- Prefer modifying an existing implementation over creating a parallel one.
- Avoid adding architecture for hypothetical future requirements.
- Keep configuration simple and local to the feature.
- Preserve existing validation, limits, cancellation, cleanup, and error behavior unless the requested feature explicitly changes them.
- Unknown or unsupported capabilities should remain denied unless intentionally added as part of the feature.

### Change discipline

When implementing a feature:

1. Understand the existing path first.
2. Identify the smallest code path that must change.
3. Modify only the required layers.
4. Add or update focused tests for changed behavior.
5. Do not fix unrelated issues discovered during the work unless they block correctness or safety.

### Verification

Run the narrowest relevant test first.

Examples:

```bash
go test ./internal/policy -run TestName
go test ./internal/runtime -run TestName
```

Then run broader checks when appropriate:

```bash
go test ./...
go test -race ./...
go vet ./...
```

For runtime changes, also build Grin when relevant:

```bash
GOFLAGS=-buildvcs=false go build ./cmd/grin
```

### Safety boundaries

Unless the requested feature explicitly changes them, preserve:

- MCP input and schema validation;
- normal-mode workspace boundary and approval protections;
- OS permission boundaries;
- request, file, search, output, and timeout limits;
- cancellation and process cleanup;
- command/process metadata redaction;
- unknown-tool denial.

`--yolo` may bypass Grin workspace-boundary and approval restrictions for supported tools, but it does not bypass OS permissions, validation, limits, cleanup, or runtime correctness protections.

### Documentation

Use `docs/` for user-facing project documentation, including onboarding, configuration, tunnel setup, feature status, and troubleshooting.

Use `my_docs/` for internal AI plans, reviews, implementation notes, and working documents. Do not use `my_docs/` as the user-facing documentation entry point.

Keep the root `README.md` as a concise project landing page that links to the user-facing documentation in `docs/`.

Update documentation only when the requested change affects documented behavior.

Documentation metadata:

- Keep `README.md` and files under `docs/` focused on user-facing content; do
  not add internal `Update History` sections or maintenance timestamps there.
- Give internal documents under `my_docs/` a dated filename and maintain their
  update history when they are revised.

Do not create extra design documents for small changes that are already clear from the code and request.
