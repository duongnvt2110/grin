package tui

import (
	"context"
	"io"
	"strconv"
	"strings"
	"sync"

	"grin/internal/events"

	tea "charm.land/bubbletea/v2"
)

type Mode string

const Inline Mode = "inline"

type ActionType string

const (
	ActionApprove ActionType = "approve"
	ActionReject  ActionType = "reject"
	ActionQuit    ActionType = "quit"
)

type Action struct {
	Type              ActionType
	EventID           string
	ApprovalRequestID string
	OperationDigest   string
}

type EventMsg struct{ Event events.Event }
type ActionMsg struct{ Action Action }

type Readiness struct {
	once sync.Once
	done chan struct{}
}

func NewReadiness() *Readiness { return &Readiness{done: make(chan struct{})} }

func (r *Readiness) Signal() { r.once.Do(func() { close(r.done) }) }

func (r *Readiness) Wait(ctx context.Context) error {
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type TranscriptState string

const (
	StateActive           TranscriptState = "active"
	StateAwaitingApproval TranscriptState = "awaiting_approval"
	StateCompleted        TranscriptState = "completed"
	StateRejected         TranscriptState = "rejected"
	StateFailed           TranscriptState = "failed"
	StateCancelled        TranscriptState = "cancelled"
)

func (s TranscriptState) IsTerminal() bool {
	return s == StateCompleted || s == StateRejected || s == StateFailed || s == StateCancelled
}

type TranscriptEntry struct {
	EventID           string
	ToolCallID        string
	Workspace         string
	Tool              string
	Summary           string
	Detail            string
	Risk              string
	State             TranscriptState
	ApprovalRequestID string
	OperationDigest   string
	ApprovalOutcome   events.ApprovalOutcome
	Expanded          bool
}

type renderedEntry struct {
	Lines []string
}

type Model struct {
	Workspace string
	Mode      string
	Yolo      bool
	Connected bool

	Entries        []TranscriptEntry
	Selected       int
	SeenEventIDs   map[string]struct{}
	SeenEventOrder []string
	MaxEntries     int
	Help           bool
	Quit           bool
	Width          int
	Height         int

	readiness  *Readiness
	stream     <-chan events.Event
	reliable   <-chan events.Event
	actionSink func(Action)
}

func NewModel(workspace, mode string, readiness *Readiness, yolo ...bool) Model {
	enabled := len(yolo) > 0 && yolo[0]
	return Model{Workspace: workspace, Mode: mode, Yolo: enabled, Connected: true, SeenEventIDs: map[string]struct{}{}, MaxEntries: 1000, readiness: readiness}
}

func (m *Model) Attach(stream <-chan events.Event) { m.stream = stream }

func (m *Model) AttachReliable(stream <-chan events.Event) { m.reliable = stream }

func (m *Model) SetActionSink(sink func(Action)) { m.actionSink = sink }

func (m Model) Init() tea.Cmd { return func() tea.Msg { return readinessMsg{} } }

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case readinessMsg:
		if m.readiness != nil {
			m.readiness.Signal()
		}
		return m, nextEvent(m.stream, m.reliable)
	case EventMsg:
		m.addEvent(msg.Event)
		return m, nextEvent(m.stream, m.reliable)
	case ActionMsg:
		if m.actionSink != nil {
			m.actionSink(msg.Action)
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.Width, m.Height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		return m.updateKey(msg.Keystroke())
	default:
		return m, nil
	}
}

func (m Model) updateKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "ctrl+c":
		m.Quit = true
		return m, tea.Quit
	case "j", "down":
		if m.Selected < len(m.Entries)-1 {
			m.Selected++
		}
	case "k", "up":
		if m.Selected > 0 {
			m.Selected--
		}
	case "enter":
		if entry := m.selectedEntry(); entry != nil {
			if canExpand(*entry) {
				entry.Expanded = !entry.Expanded
			}
		}
	case "a", "r":
		entry := m.selectedEntry()
		if entry == nil || entry.State != StateAwaitingApproval || entry.ApprovalRequestID == "" || entry.OperationDigest == "" {
			return m, nil
		}
		action := Action{EventID: entry.EventID, ApprovalRequestID: entry.ApprovalRequestID, OperationDigest: entry.OperationDigest}
		if key == "a" {
			action.Type = ActionApprove
		} else {
			action.Type = ActionReject
		}
		return m, func() tea.Msg { return ActionMsg{Action: action} }
	case "?":
		m.Help = !m.Help
	}
	return m, nil
}

func canExpand(entry TranscriptEntry) bool {
	if entry.State == StateAwaitingApproval || entry.Workspace != "" {
		return true
	}
	if entry.Detail == "" {
		return false
	}
	if entry.Tool == "shell.run" || entry.Tool == "fs.write_text" {
		return true
	}
	return entry.State == StateFailed || entry.State == StateRejected || entry.State == StateCancelled
}

func (m *Model) addEvent(event events.Event) {
	if event.ID == "" || m.seen(event.ID) {
		return
	}
	if event.Time.IsZero() {
		event.Time = event.At
	}
	state, known := stateForEvent(event)
	if event.ToolCallID == "" {
		if !known {
			wasAtBottom := len(m.Entries) == 0 || m.Selected == len(m.Entries)-1
			m.Entries = append(m.Entries, entryFrom(event, state))
			m.trimEntries()
			if wasAtBottom {
				m.Selected = len(m.Entries) - 1
			}
		}
		return
	}
	if (event.Type == events.EventApprovalRequired || event.Type == events.EventApprovalResolved) && (event.ApprovalRequestID == "" || event.OperationDigest == "") {
		return
	}
	index := m.findEntry(event.ToolCallID)
	if index < 0 {
		wasAtBottom := len(m.Entries) == 0 || m.Selected == len(m.Entries)-1
		m.Entries = append(m.Entries, entryFrom(event, state))
		index = len(m.Entries) - 1
		if wasAtBottom {
			m.Selected = index
		}
	} else {
		entry := m.Entries[index]
		if event.ApprovalRequestID != "" && entry.ApprovalRequestID != "" && event.ApprovalRequestID != entry.ApprovalRequestID {
			return
		}
		if event.OperationDigest != "" && entry.OperationDigest != "" && event.OperationDigest != entry.OperationDigest {
			return
		}
		m.mergeEvent(&m.Entries[index], event, state, known)
	}
	if event.Type == events.EventApprovalRequired && state == StateAwaitingApproval {
		m.Selected = index
	}
	m.trimEntries()
	if m.Selected >= len(m.Entries) {
		m.Selected = len(m.Entries) - 1
	}
}

func (m *Model) seen(id string) bool {
	if m.SeenEventIDs == nil {
		m.SeenEventIDs = map[string]struct{}{}
	}
	if _, exists := m.SeenEventIDs[id]; exists {
		return true
	}
	m.SeenEventIDs[id] = struct{}{}
	m.SeenEventOrder = append(m.SeenEventOrder, id)
	limit := m.MaxEntries * 4
	if limit < 256 {
		limit = 256
	}
	for len(m.SeenEventOrder) > limit {
		oldest := m.SeenEventOrder[0]
		m.SeenEventOrder = m.SeenEventOrder[1:]
		delete(m.SeenEventIDs, oldest)
	}
	return false
}

func (m *Model) findEntry(toolCallID string) int {
	for index := range m.Entries {
		if m.Entries[index].ToolCallID == toolCallID {
			return index
		}
	}
	return -1
}

func entryFrom(event events.Event, state TranscriptState) TranscriptEntry {
	return TranscriptEntry{EventID: event.ID, ToolCallID: event.ToolCallID, Workspace: event.Workspace, Tool: event.Tool, Summary: event.Summary, Detail: event.Detail, Risk: event.Risk, State: state, ApprovalRequestID: event.ApprovalRequestID, OperationDigest: event.OperationDigest, ApprovalOutcome: event.ApprovalOutcome}
}

func (m *Model) mergeEvent(entry *TranscriptEntry, event events.Event, next TranscriptState, known bool) {
	if entry.Tool == "" {
		entry.Tool = event.Tool
	}
	if entry.Workspace == "" {
		entry.Workspace = event.Workspace
	}
	if entry.Summary == "" || (!entry.State.IsTerminal() && event.Summary != "" && !genericSummary(event.Summary)) {
		entry.Summary = event.Summary
	}
	if entry.Detail == "" {
		entry.Detail = event.Detail
	}
	if entry.Risk == "" {
		entry.Risk = event.Risk
	}
	if entry.ApprovalRequestID == "" {
		entry.ApprovalRequestID = event.ApprovalRequestID
	}
	if entry.OperationDigest == "" {
		entry.OperationDigest = event.OperationDigest
	}
	if entry.ApprovalOutcome == "" {
		entry.ApprovalOutcome = event.ApprovalOutcome
	}
	if !known || entry.State.IsTerminal() {
		return
	}
	if entry.State == StateAwaitingApproval && next == StateActive && event.Type != events.EventApprovalResolved {
		return
	}
	if entry.State == StateAwaitingApproval && next == StateActive && event.Type == events.EventApprovalResolved && event.ApprovalOutcome == events.ApprovalAllowed {
		entry.State = StateActive
		return
	}
	if entry.State == StateActive && next == StateAwaitingApproval {
		entry.State = next
		return
	}
	if next.IsTerminal() {
		entry.State = next
		return
	}
	if entry.State == "" {
		entry.State = next
	}
}

func (m *Model) trimEntries() {
	max := m.MaxEntries
	if max < 1 {
		max = 1
	}
	for len(m.Entries) > max {
		remove := -1
		for index, entry := range m.Entries {
			if entry.State.IsTerminal() {
				remove = index
				break
			}
		}
		if remove < 0 {
			return
		}
		m.Entries = append(m.Entries[:remove], m.Entries[remove+1:]...)
		if m.Selected > remove {
			m.Selected--
		}
	}
}

func stateForEvent(event events.Event) (TranscriptState, bool) {
	switch event.Type {
	case events.EventRequestStarted, events.EventPolicyDecision:
		return StateActive, true
	case events.EventApprovalRequired:
		return StateAwaitingApproval, true
	case events.EventApprovalResolved:
		switch event.ApprovalOutcome {
		case events.ApprovalRejected:
			return StateRejected, true
		case events.ApprovalTimedOut:
			return StateFailed, true
		case events.ApprovalCancelled:
			return StateCancelled, true
		default:
			return StateActive, true
		}
	case events.EventToolCompleted:
		return StateCompleted, true
	case events.EventToolFailed:
		return StateFailed, true
	case events.EventCancelled:
		return StateCancelled, true
	default:
		return "", false
	}
}

func (m Model) selectedEntry() *TranscriptEntry {
	if m.Selected < 0 || m.Selected >= len(m.Entries) {
		return nil
	}
	return &m.Entries[m.Selected]
}

func (m Model) View() tea.View {
	connection := "disconnected"
	if m.Connected {
		connection = "connected"
	}
	mode := ""
	if m.Yolo {
		mode = " [YOLO]"
	}
	header := "grin" + mode + "  " + m.Workspace + "                                  " + connection
	headerLines := []string{header, ""}
	controls := "Controls: j/↓ next  k/↑ previous"
	if entry := m.selectedEntry(); entry != nil && canExpand(*entry) {
		controls += "  enter details"
	}
	controls += "  a allow  r reject  ? help  q quit"
	footerLines := []string{"", controls}
	if m.Help {
		footerLines = append(footerLines, "Select the row marked >. Approval actions apply only to a pending approval.")
	}
	footerLines = append(footerLines, "", "────────────────────────────────────────────────────────────", connection+"   "+m.Workspace+"   "+m.Mode+"   active: "+activeCount(m.Entries))
	blocks, selectedStartLine, selectedEndLine := m.renderEntries()
	body := flattenRenderedEntries(blocks)
	available := len(body)
	if m.Height > 0 {
		available = m.Height - len(headerLines) - len(footerLines)
		if available < 0 {
			available = 0
		}
	}
	if len(body) > available {
		start := viewportStart(len(body), available, selectedStartLine, selectedEndLine)
		if start+available > len(body) {
			start = len(body) - available
		}
		if start < 0 {
			start = 0
		}
		body = body[start : start+available]
	}
	lines := append(headerLines, body...)
	lines = append(lines, footerLines...)
	if m.Height > 0 && len(lines) > m.Height {
		lines = lines[:m.Height]
	}
	for index := range lines {
		lines[index] = truncate(lines[index], m.Width)
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = false
	return view
}

func truncate(line string, width int) string {
	if width <= 0 || len([]rune(line)) <= width {
		return line
	}
	return string([]rune(line)[:width])
}

func (m Model) renderEntries() ([]renderedEntry, int, int) {
	blocks := make([]renderedEntry, 0, len(m.Entries))
	selectedStartLine, selectedEndLine := -1, -1
	lineOffset := 0
	for index, entry := range m.Entries {
		marker, icon := " ", "·"
		if index == m.Selected {
			marker = ">"
		}
		switch entry.State {
		case StateCompleted:
			icon = "✓"
		case StateRejected, StateFailed:
			icon = "✗"
		case StateCancelled:
			icon = "-"
		case StateAwaitingApproval:
			icon = "!"
		}
		label := ""
		if entry.State == StateAwaitingApproval {
			label = "approval required"
		} else if entry.State.IsTerminal() {
			label = string(entry.State)
		}
		line := marker + " " + icon + " " + entry.Tool + "   " + entry.Summary
		if label != "" {
			line += " · " + label
		}
		if entry.Workspace != "" {
			line += " · " + entry.Workspace
		}
		block := renderedEntry{Lines: []string{line}}
		if entry.Expanded {
			if entry.Detail != "" {
				for _, line := range strings.Split(entry.Detail, "\n") {
					block.Lines = append(block.Lines, "    "+line)
				}
			}
			if entry.Risk != "" {
				block.Lines = append(block.Lines, "    risk: "+entry.Risk)
			}
			if entry.State == StateAwaitingApproval {
				block.Lines = append(block.Lines, "    [a] allow  [r] reject  [Enter] details")
			}
		}
		if index == m.Selected {
			selectedStartLine = lineOffset
			selectedEndLine = lineOffset + len(block.Lines)
		}
		blocks = append(blocks, block)
		lineOffset += len(block.Lines)
	}
	return blocks, selectedStartLine, selectedEndLine
}

func flattenRenderedEntries(blocks []renderedEntry) []string {
	lines := make([]string, 0)
	for _, block := range blocks {
		lines = append(lines, block.Lines...)
	}
	return lines
}

func viewportStart(totalLines, viewportLines, selectedStartLine, selectedEndLine int) int {
	if viewportLines <= 0 || selectedStartLine < 0 || selectedEndLine <= selectedStartLine {
		return 0
	}
	if selectedEndLine-selectedStartLine > viewportLines {
		return selectedStartLine
	}
	if selectedEndLine > viewportLines {
		return selectedEndLine - viewportLines
	}
	return 0
}

func genericSummary(summary string) bool {
	return summary == "request received" || summary == "tool completed" || summary == "tool failed"
}

func activeCount(entries []TranscriptEntry) string {
	active := 0
	for _, entry := range entries {
		if entry.State == StateActive || entry.State == StateAwaitingApproval {
			active++
		}
	}
	return strconv.Itoa(active)
}

type readinessMsg struct{}

func nextEvent(stream, reliable <-chan events.Event) tea.Cmd {
	if stream == nil && reliable == nil {
		return nil
	}
	return func() tea.Msg {
		for stream != nil || reliable != nil {
			select {
			case event, ok := <-stream:
				if ok {
					return EventMsg{Event: event}
				}
				stream = nil
			case event, ok := <-reliable:
				if ok {
					return EventMsg{Event: event}
				}
				reliable = nil
			}
		}
		return nil
	}
}

func Start(ctx context.Context, model Model, input io.Reader, output io.Writer) (*tea.Program, <-chan error) {
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output))
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	return program, done
}
