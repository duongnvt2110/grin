package tui

import (
	"context"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"grin/internal/events"
)

func key(text string) tea.KeyPressMsg {
	if text == "enter" {
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	}
	return tea.KeyPressMsg(tea.Key{Text: text, Code: []rune(text)[0]})
}

func TestModelGroupsLifecycleAndRoutesApproval(t *testing.T) {
	readiness := NewReadiness()
	model := NewModel("/workspace", "normal", readiness)
	updated, _ := model.Update(model.Init()())
	if err := readiness.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	model = updated.(Model)
	model.addEvent(events.Event{ID: "start-1", ToolCallID: "tool-1", Type: events.EventRequestStarted, Tool: "fs.write_text", Summary: "request received"})
	model.addEvent(events.Event{ID: "approval-1", ToolCallID: "tool-1", Workspace: "/workspace-a", ApprovalRequestID: "approval-1", OperationDigest: "digest-1", Type: events.EventApprovalRequired, Tool: "fs.write_text", Summary: "write notes.txt", Detail: "path: notes.txt", Risk: "high"})
	model.addEvent(events.Event{ID: "approval-1", ToolCallID: "tool-1", ApprovalRequestID: "approval-1", OperationDigest: "digest-1", Type: events.EventApprovalRequired, Tool: "fs.write_text", Summary: "duplicate"})
	if len(model.Entries) != 1 || model.Entries[0].State != StateAwaitingApproval {
		t.Fatalf("entries = %+v", model.Entries)
	}
	if !strings.Contains(model.View().Content, "> ! fs.write_text   write notes.txt · approval required") {
		t.Fatalf("view missing approval row: %s", model.View().Content)
	}
	if !strings.Contains(model.View().Content, "· /workspace-a") {
		t.Fatalf("compact approval view missing workspace: %s", model.View().Content)
	}
	updated, _ = model.Update(key("enter"))
	model = updated.(Model)
	if !strings.Contains(model.View().Content, "path: notes.txt") || !strings.Contains(model.View().Content, "risk: high") {
		t.Fatalf("expanded approval missing safe details: %s", model.View().Content)
	}
	var handled Action
	model.SetActionSink(func(action Action) { handled = action })
	_, command := model.Update(key("a"))
	message := command()
	action, ok := message.(ActionMsg)
	if !ok || action.Action.Type != ActionApprove || action.Action.ApprovalRequestID != "approval-1" || action.Action.OperationDigest != "digest-1" {
		t.Fatalf("approval action = %#v", message)
	}
	updated, _ = model.Update(message)
	if handled.Type != ActionApprove || handled.ApprovalRequestID != "approval-1" {
		t.Fatalf("approval action was not routed: %+v", handled)
	}
	if updated.(Model).Entries[0].State != StateAwaitingApproval {
		t.Fatal("approval action changed state before lifecycle resolution")
	}
}

func TestReducerTerminalStateWinsAndAllowsSafeEnrichment(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.addEvent(events.Event{ID: "completed-1", ToolCallID: "tool-1", Type: events.EventToolCompleted, Tool: "shell.run", Summary: "tool completed"})
	model.addEvent(events.Event{ID: "approval-1", ToolCallID: "tool-1", ApprovalRequestID: "approval-1", OperationDigest: "digest-1", Type: events.EventApprovalRequired, Summary: "run sed", Detail: "command: sed", Risk: "high"})
	model.addEvent(events.Event{ID: "running-1", ToolCallID: "tool-1", Type: events.EventRequestStarted, Summary: "request received"})
	entry := model.Entries[0]
	if entry.State != StateCompleted || entry.Summary != "tool completed" || entry.ApprovalRequestID != "approval-1" || entry.Risk != "high" {
		t.Fatalf("terminal merge = %+v", entry)
	}
}

func TestReducerMapsApprovalOutcomesAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome events.ApprovalOutcome
		state   TranscriptState
	}{
		{"rejected", events.ApprovalRejected, StateRejected},
		{"timed out", events.ApprovalTimedOut, StateFailed},
		{"approval cancelled", events.ApprovalCancelled, StateCancelled},
		{"allowed", events.ApprovalAllowed, StateActive},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel("/workspace", "normal", nil)
			model.addEvent(events.Event{ID: "required", ToolCallID: "tool-1", ApprovalRequestID: "approval-1", OperationDigest: "digest-1", Type: events.EventApprovalRequired})
			model.addEvent(events.Event{ID: "resolved", ToolCallID: "tool-1", ApprovalRequestID: "approval-1", OperationDigest: "digest-1", ApprovalOutcome: test.outcome, Type: events.EventApprovalResolved})
			if model.Entries[0].State != test.state {
				t.Fatalf("state = %s, want %s", model.Entries[0].State, test.state)
			}
		})
	}
	model := NewModel("/workspace", "normal", nil)
	model.addEvent(events.Event{ID: "cancelled", ToolCallID: "tool-2", Type: events.EventCancelled})
	if model.Entries[0].State != StateCancelled {
		t.Fatalf("request cancellation state = %s", model.Entries[0].State)
	}
}

func TestAllowedApprovalBecomesActiveAndStopsApprovalActions(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.addEvent(events.Event{ID: "required", ToolCallID: "tool-1", ApprovalRequestID: "approval-1", OperationDigest: "digest-1", Type: events.EventApprovalRequired})
	model.addEvent(events.Event{ID: "allowed", ToolCallID: "tool-1", ApprovalRequestID: "approval-1", OperationDigest: "digest-1", ApprovalOutcome: events.ApprovalAllowed, Type: events.EventApprovalResolved})
	if model.Entries[0].State != StateActive {
		t.Fatalf("allowed approval state = %s, want %s", model.Entries[0].State, StateActive)
	}
	_, command := model.Update(key("a"))
	if command != nil {
		t.Fatal("allowed approval remained actionable")
	}
}

func TestExpansionOnlyAppliesToUsefulRows(t *testing.T) {
	tests := []struct {
		name  string
		entry TranscriptEntry
		want  bool
	}{
		{
			name:  "simple completed read-only row stays compact",
			entry: TranscriptEntry{Tool: "fs.read_text", Summary: "README.md", State: StateCompleted},
			want:  false,
		},
		{
			name:  "completed workspace row can reveal workspace",
			entry: TranscriptEntry{Workspace: "/workspace-a", Tool: "fs.read_text", Summary: "README.md", State: StateCompleted},
			want:  true,
		},
		{
			name:  "pending shell row expands",
			entry: TranscriptEntry{Tool: "shell.run", Summary: "go test ./...", Detail: "command: go test ./...", State: StateAwaitingApproval},
			want:  true,
		},
		{
			name:  "failed row with detail expands",
			entry: TranscriptEntry{Tool: "fs.read_text", Summary: "README.md", Detail: "reason: failed", State: StateFailed},
			want:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel("/workspace", "normal", nil)
			model.Entries = []TranscriptEntry{test.entry}
			updated, _ := model.Update(key("enter"))
			if got := updated.(Model).Entries[0].Expanded; got != test.want {
				t.Fatalf("expanded = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCompletedWorkspaceRowsShowWorkspaceInCompactView(t *testing.T) {
	model := NewModel("multi-workspace", "normal", nil)
	model.Entries = []TranscriptEntry{
		{Workspace: "/workspace-a", Tool: "fs.read_text", Summary: "README.md", State: StateCompleted},
		{Workspace: "/workspace-b", Tool: "fs.read_text", Summary: "README.md", State: StateCompleted},
	}
	view := model.View().Content
	for _, workspace := range []string{"/workspace-a", "/workspace-b"} {
		if !strings.Contains(view, "· "+workspace) {
			t.Fatalf("compact completed row missing workspace %q: %s", workspace, view)
		}
	}
}

func TestReducerDedupStateIsBounded(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.MaxEntries = 1
	for index := 0; index < 300; index++ {
		model.addEvent(events.Event{ID: "event-" + string(rune(index)), ToolCallID: "tool-" + string(rune(index)), Type: events.EventToolCompleted})
	}
	if len(model.SeenEventIDs) > 256 || len(model.SeenEventOrder) > 256 {
		t.Fatalf("dedup state is unbounded: ids=%d order=%d", len(model.SeenEventIDs), len(model.SeenEventOrder))
	}
}

func TestQuitAndNoCancelAction(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	updated, _ := model.Update(key("q"))
	if !updated.(Model).Quit {
		t.Fatal("q did not request quit")
	}
	updated, command := model.Update(key("c"))
	if command != nil || updated.(Model).Quit {
		t.Fatal("c should not cancel or emit an action")
	}
}

func TestViewUsesFixedFrameAndWidth(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.Width = 24
	model.Height = 8
	model.addEvent(events.Event{ID: "event-1", ToolCallID: "tool-1", Type: events.EventToolCompleted, Tool: "shell.run", Summary: "a very long safe summary"})
	view := model.View().Content
	if strings.Count(view, "grin  ") != 1 || strings.Count(view, "Controls:") != 1 {
		t.Fatalf("fixed frame repeated or missing: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > model.Width {
			t.Fatalf("line exceeds width: %q", line)
		}
	}
}

func TestNaturalHeightRendersAllRowsAndFooter(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.Width = 80
	model.Height = 20
	model.addEvent(events.Event{ID: "row-1", ToolCallID: "tool-1", Type: events.EventToolCompleted, Tool: "fs.read_text", Summary: "README.md"})
	model.addEvent(events.Event{ID: "row-2", ToolCallID: "tool-2", Type: events.EventToolCompleted, Tool: "fs.search", Summary: `"ApprovalOutcome"`})

	lines := strings.Split(model.View().Content, "\n")
	readmeLine, searchLine, controlsLine := -1, -1, -1
	for index, line := range lines {
		switch {
		case strings.Contains(line, "README.md"):
			readmeLine = index
		case strings.Contains(line, `"ApprovalOutcome"`):
			searchLine = index
		case strings.HasPrefix(line, "Controls:"):
			controlsLine = index
		}
	}
	if readmeLine < 0 || searchLine < 0 || controlsLine != searchLine+2 {
		t.Fatalf("natural render lines = %v", lines)
	}
}

func TestOverflowKeepsSelectedRowAndFooterVisible(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.Width = 80
	model.Height = 8
	for index := 1; index <= 4; index++ {
		model.addEvent(events.Event{ID: "row-" + strconv.Itoa(index), ToolCallID: "tool-" + strconv.Itoa(index), Type: events.EventToolCompleted, Tool: "fs.read_text", Summary: "row " + strconv.Itoa(index)})
	}

	view := model.View().Content
	if len(strings.Split(view, "\n")) > model.Height {
		t.Fatalf("overflow render exceeded height: %q", view)
	}
	if !strings.Contains(view, "> ✓ fs.read_text   row 4 · completed") || !strings.Contains(view, "Controls:") {
		t.Fatalf("selected row or footer was clipped: %s", view)
	}
}

func TestNewOrdinaryRowFollowsWhenAlreadyAtBottom(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.Width = 100
	model.Height = 8

	model.addEvent(events.Event{
		ID: "row-1", ToolCallID: "tool-1",
		Type: events.EventToolCompleted,
		Tool: "fs.read_text", Summary: "row 1",
	})
	model.addEvent(events.Event{
		ID: "row-2", ToolCallID: "tool-2",
		Type: events.EventToolCompleted,
		Tool: "fs.read_text", Summary: "row 2",
	})

	if model.Selected != 1 {
		t.Fatalf("selected = %d, want 1", model.Selected)
	}

	model.addEvent(events.Event{
		ID: "row-3", ToolCallID: "tool-3",
		Type: events.EventToolCompleted,
		Tool: "fs.read_text", Summary: "row 3",
	})

	if model.Selected != 2 {
		t.Fatalf("selected = %d, want 2", model.Selected)
	}
	if view := model.View().Content; !strings.Contains(view, "> ✓ fs.read_text   row 3 · completed") {
		t.Fatalf("new row not visible: %s", view)
	}
}

func TestNewRowsFollowLastSelectionAndPendingApproval(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.Width = 100
	model.Height = 8
	model.addEvent(events.Event{ID: "row-1", ToolCallID: "tool-1", Type: events.EventToolCompleted, Tool: "fs.read_text", Summary: "row 1"})
	model.addEvent(events.Event{ID: "row-2", ToolCallID: "tool-2", Type: events.EventToolCompleted, Tool: "fs.read_text", Summary: "row 2"})
	model.Selected = 0
	model.addEvent(events.Event{ID: "row-3", ToolCallID: "tool-3", Type: events.EventToolCompleted, Tool: "fs.read_text", Summary: "row 3"})
	if model.Selected != 0 || !strings.Contains(model.View().Content, "> ✓ fs.read_text   row 1 · completed") {
		t.Fatalf("upward selection changed: selected=%d view=%s", model.Selected, model.View().Content)
	}

	model.Selected = len(model.Entries) - 1
	model.addEvent(events.Event{ID: "approval", ToolCallID: "tool-4", ApprovalRequestID: "approval-4", OperationDigest: "digest-4", Type: events.EventApprovalRequired, Tool: "shell.run", Summary: "go test ./..."})
	view := model.View().Content
	if model.Selected != 3 || !strings.Contains(view, "> ! shell.run   go test ./... · approval required") || !strings.Contains(view, "Controls:") {
		t.Fatalf("new approval was not selected and visible: selected=%d view=%s", model.Selected, view)
	}
}

func TestViewDoesNotRequestAlternateScreen(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	if model.View().AltScreen {
		t.Fatal("natural terminal view requested alternate screen")
	}
}

func TestFooterMatchesExpansionAvailability(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.Entries = []TranscriptEntry{{Tool: "fs.read_text", Summary: "README.md", State: StateCompleted}}
	if view := model.View().Content; strings.Contains(view, "enter details") {
		t.Fatalf("simple completed row advertised expansion: %s", view)
	}

	model.Entries[0] = TranscriptEntry{Tool: "shell.run", Summary: "go test ./...", Detail: "command: go test ./...", State: StateAwaitingApproval}
	if view := model.View().Content; !strings.Contains(view, "enter details") {
		t.Fatalf("expandable row omitted details control: %s", view)
	}
}

func TestResizeAndNavigationKeepCompactFrame(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.addEvent(events.Event{ID: "event-1", ToolCallID: "tool-1", Type: events.EventToolCompleted, Tool: "fs.read_text", Summary: "README.md"})
	model.addEvent(events.Event{ID: "event-2", ToolCallID: "tool-2", Type: events.EventRequestStarted, Tool: "system.info", Summary: "request received"})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 48, Height: 8})
	model = updated.(Model)
	if model.Width != 48 || model.Height != 8 {
		t.Fatalf("resize was not applied: %dx%d", model.Width, model.Height)
	}
	updated, _ = model.Update(key("j"))
	model = updated.(Model)
	if model.Selected != 1 {
		t.Fatalf("selection = %d, want 1", model.Selected)
	}
	view := model.View().Content
	if strings.Count(view, "grin  ") != 1 || strings.Count(view, "Controls:") != 1 {
		t.Fatalf("resize produced repeated frame: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > model.Width {
			t.Fatalf("resized line exceeds width: %q", line)
		}
	}
}

func TestTrimRetainsActiveAndPendingRows(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.MaxEntries = 1
	model.addEvent(events.Event{ID: "active", ToolCallID: "tool-active", Type: events.EventRequestStarted, Tool: "system.info", Summary: "request received"})
	model.addEvent(events.Event{ID: "pending", ToolCallID: "tool-pending", ApprovalRequestID: "approval-1", OperationDigest: "digest-1", Type: events.EventApprovalRequired, Tool: "shell.run", Summary: "go test"})
	model.addEvent(events.Event{ID: "completed", ToolCallID: "tool-completed", Type: events.EventToolCompleted, Tool: "fs.read_text", Summary: "README.md"})
	if len(model.Entries) != 2 {
		t.Fatalf("active/pending rows were trimmed: %+v", model.Entries)
	}
	for _, entry := range model.Entries {
		if entry.State != StateActive && entry.State != StateAwaitingApproval {
			t.Fatalf("non-active row retained: %+v", entry)
		}
	}
}

func TestInvalidApprovalIdentityDoesNotChangeRow(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.addEvent(events.Event{ID: "required", ToolCallID: "tool-1", ApprovalRequestID: "approval-1", OperationDigest: "digest-1", Type: events.EventApprovalRequired, Summary: "go test"})
	model.addEvent(events.Event{ID: "mismatch", ToolCallID: "tool-1", ApprovalRequestID: "approval-2", OperationDigest: "digest-2", ApprovalOutcome: events.ApprovalAllowed, Type: events.EventApprovalResolved})
	entry := model.Entries[0]
	if entry.State != StateAwaitingApproval || entry.ApprovalRequestID != "approval-1" || entry.OperationDigest != "digest-1" {
		t.Fatalf("invalid approval changed row: %+v", entry)
	}
}

func TestViewportUsesSelectedRenderedBlockRange(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.Entries = []TranscriptEntry{
		{ToolCallID: "tool-1", Tool: "shell.run", Summary: "first", State: StateActive, Detail: "line one\nline two", Expanded: true},
		{ToolCallID: "tool-2", Tool: "fs.read_text", Summary: "README.md", State: StateCompleted},
	}
	model.Selected = 1
	blocks, selectedStart, selectedEnd := model.renderEntries()
	if selectedStart != 3 || selectedEnd != 4 {
		t.Fatalf("selected block = %d..%d, want 3..4", selectedStart, selectedEnd)
	}
	if got := viewportStart(len(flattenRenderedEntries(blocks)), 2, selectedStart, selectedEnd); got != 2 {
		t.Fatalf("viewport start = %d, want 2", got)
	}
}

func TestExpandedSelectedEntryRemainsVisibleNearViewportBottom(t *testing.T) {
	model := NewModel("/workspace", "normal", nil)
	model.Entries = []TranscriptEntry{
		{ToolCallID: "tool-1", Tool: "fs.read_text", Summary: "row 1", State: StateCompleted},
		{ToolCallID: "tool-2", Tool: "fs.read_text", Summary: "row 2", State: StateCompleted},
		{ToolCallID: "tool-3", Tool: "fs.read_text", Summary: "row 3", State: StateCompleted},
		{
			ToolCallID: "tool-4",
			Tool:       "shell.run",
			Summary:    "go test ./...",
			Detail:     "command: go test ./...\ncwd: /workspace\nreason: approval required",
			Risk:       "high",
			State:      StateAwaitingApproval,
			Expanded:   true,
		},
	}
	model.Selected = 3
	model.Width = 80
	model.Height = 11
	view := model.View().Content
	if !strings.Contains(view, "> ! shell.run   go test ./... · approval required") {
		t.Fatalf("selected expanded row was clipped: %s", view)
	}
}

func TestYoloHeaderIsVisible(t *testing.T) {
	model := NewModel("/workspace", "yolo", nil, true)
	view := model.View().Content
	if !strings.Contains(view, "grin [YOLO]") {
		t.Fatalf("YOLO header missing: %s", view)
	}
}
