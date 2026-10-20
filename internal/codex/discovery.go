package codex

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"runtime"
	"strings"
	"time"

	"grin/internal/redaction"
)

//go:embed terminal_discovery/*
var terminalDiscoveryScripts embed.FS

const (
	discoveryTimeout    = 20 * time.Second
	terminalReadTimeout = 12 * time.Second
	darwinReadTimeout   = 15 * time.Second
	darwinTitleTimeout  = 10 * time.Second
	tmuxProbeTimeout    = 5 * time.Second
	maxDiscoveryOutput  = 1 << 20
	maxTerminalWindows  = 256
)

var leadingUUIDPrefix = regexp.MustCompile(`(?i)^([0-9a-f]{8}(?:-[0-9a-f]{4}){0,3}(?:-[0-9a-f]{1,12})?)(?:\.\.\.|…|[\s|]|$)`)

type TerminalObservation struct {
	API         string `json:"api"`
	ID          string `json:"terminal_id"`
	WindowID    string `json:"window_id,omitempty"`
	TabID       string `json:"tab_id,omitempty"`
	Title       string `json:"title"`
	Workspace   string `json:"workspace,omitempty"`
	Foreground  bool   `json:"foreground"`
	Focused     bool   `json:"focused"`
	SelectedTab *bool  `json:"selected_tab,omitempty"`
	ActiveInTab *bool  `json:"active_in_tab,omitempty"`
}

type ThreadCandidate struct {
	TerminalObservation
	UUIDPrefix     string `json:"uuid_prefix"`
	ThreadTitle    string `json:"thread_title,omitempty"`
	ProjectName    string `json:"project_name,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
	CodexWorkspace string `json:"codex_workspace,omitempty"`
	WorkspaceMatch string `json:"workspace_match"`
	TopLevel       string `json:"top_level"`
	Resolution     string `json:"resolution"`
}

type DiscoveryResult struct {
	Status     string            `json:"status"`
	Workspace  string            `json:"workspace,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Evidence   []string          `json:"evidence"`
	Sources    []string          `json:"sources"`
	Candidates []ThreadCandidate `json:"candidates"`
}

type terminalSnapshot struct {
	Supported bool                  `json:"supported"`
	Reason    string                `json:"reason,omitempty"`
	Terminals []TerminalObservation `json:"terminals"`
	Sources   []string              `json:"sources,omitempty"`
}

type discoveryThreadRow struct {
	ID           string `json:"id"`
	CWD          string `json:"cwd"`
	ThreadSource string `json:"thread_source"`
	Archived     int    `json:"archived"`
}

func (s *Service) Discover(ctx context.Context, workspace string) (DiscoveryResult, error) {
	canonical, err := canonicalWorkspace(workspace)
	if err != nil {
		return DiscoveryResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	result := DiscoveryResult{
		Status:     "UNRESOLVED",
		Workspace:  canonical,
		Evidence:   []string{},
		Sources:    []string{},
		Candidates: []ThreadCandidate{},
	}
	observations, sources, supported, reason, err := readTerminalTitles(ctx, runtime.GOOS)
	result.Sources = sources
	if err != nil {
		if ctx.Err() != nil {
			return DiscoveryResult{}, ctx.Err()
		}
		reason, reasonEvidence := terminalReadFailure(err)
		result.Reason = reason
		result.Evidence = append(result.Evidence, reasonEvidence)
		return result, nil
	}
	if !supported {
		result.Status = "UNSUPPORTED"
		result.Reason = reason
		result.Sources = sources
		result.Evidence = append(result.Evidence, "native terminal title enumeration is unavailable")
		return result, nil
	}
	result, err = s.discoverFromTitles(ctx, canonical, observations)
	result.Sources = sources
	return result, err
}

func terminalReadFailure(err error) (reason, evidence string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return "native terminal title probe timed out; API support is unknown", "terminal enumeration exceeded its bounded probe timeout"
	}
	return "terminal title enumeration failed", "native terminal title API could not be read"
}

func (s *Service) discoverFromTitles(ctx context.Context, canonical string, observations []TerminalObservation) (DiscoveryResult, error) {
	result := DiscoveryResult{
		Status:     "UNRESOLVED",
		Workspace:  canonical,
		Evidence:   []string{},
		Sources:    []string{},
		Candidates: []ThreadCandidate{},
	}
	result.Evidence = append(result.Evidence, "terminal titles were read through the operating system API")

	for _, terminal := range observations {
		prefix, threadTitle, projectName, ok := parseThreadTitle(terminal.Title)
		if !ok {
			continue
		}
		candidate := ThreadCandidate{
			TerminalObservation: TerminalObservation{
				API: terminal.API, ID: terminal.ID, Title: sanitizeTerminalTitle(terminal.Title),
				WindowID: terminal.WindowID, TabID: terminal.TabID,
				Workspace: terminal.Workspace, Foreground: terminal.Foreground, Focused: terminal.Focused,
				SelectedTab: terminal.SelectedTab, ActiveInTab: terminal.ActiveInTab,
			},
			UUIDPrefix:     prefix,
			ThreadTitle:    sanitizeDiscoveryText(threadTitle),
			ProjectName:    sanitizeDiscoveryText(projectName),
			WorkspaceMatch: "unknown",
			TopLevel:       "unknown",
			Resolution:     "metadata_unavailable",
		}
		if threadID, ok := titleThreadID(terminal.Title); ok {
			if strings.HasPrefix(threadID, prefix) {
				candidate.ThreadID = threadID
				candidate.Resolution = "unverified_title_uuid"
			} else {
				candidate.Resolution = "title_uuid_mismatch"
			}
		}
		if terminal.Workspace != "" {
			if terminalWorkspace, err := canonicalWorkspace(terminal.Workspace); err == nil && terminalWorkspace != canonical {
				candidate.WorkspaceMatch = "no"
				candidate.Resolution = "terminal_workspace_mismatch"
			}
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	workspaceCandidates, filteredCount := filterWorkspaceCandidates(result.Candidates)
	result.Candidates = workspaceCandidates
	if filteredCount > 0 {
		result.Evidence = append(result.Evidence, "known workspace-mismatched terminal titles were excluded from output")
	}
	if len(result.Candidates) == 0 {
		if filteredCount > 0 {
			result.Reason = "terminal title candidates did not match the requested workspace"
		} else {
			result.Reason = "no terminal title began with a Codex thread UUID"
		}
		result.Evidence = append(result.Evidence, "thread title and project name are corroborating context only")
		return result, nil
	}
	home, err := s.codexHome()
	if err != nil {
		for index := range result.Candidates {
			if result.Candidates[index].ThreadID != "" {
				result.Candidates[index].Resolution = "unverified_title_uuid"
			}
		}
		result.Reason = "Codex state metadata is unavailable"
		result.Evidence = append(result.Evidence, "thread prefixes could not be resolved against local Codex state")
		return result, nil
	}
	for index := range result.Candidates {
		candidate := &result.Candidates[index]
		if candidate.ThreadID == "" || candidate.Resolution == "terminal_workspace_mismatch" {
			continue
		}
		thread, readErr := readDiscoveryThread(ctx, home, candidate.ThreadID)
		if readErr != nil {
			if ctx.Err() != nil {
				return DiscoveryResult{}, ctx.Err()
			}
			candidate.Resolution = "unverified_title_uuid"
			result.Evidence = append(result.Evidence, "an exact UUID was observed in the title but read-only app-server thread/read could not corroborate it")
			continue
		}
		if threadWorkspace, workspaceErr := canonicalWorkspace(thread.CWD); workspaceErr == nil {
			candidate.CodexWorkspace = threadWorkspace
			if threadWorkspace != canonical {
				candidate.WorkspaceMatch = "no"
				candidate.Resolution = "workspace_mismatch"
				continue
			}
		}
		if sameTopLevelWorkspaceThread(thread, canonical, candidate.ThreadID) {
			candidate.WorkspaceMatch = "yes"
			candidate.TopLevel = "yes"
			candidate.Resolution = "title_uuid_correlated"
		} else {
			candidate.TopLevel = "no"
			candidate.Resolution = "ineligible_thread"
		}
	}
	stateDB, err := resolveStateDB(ctx, s.sqlite, home)
	if err != nil || validateTable(ctx, s.sqlite, stateDB, "threads", "id", "cwd", "thread_source", "archived") != nil {
		if ctx.Err() != nil {
			return DiscoveryResult{}, ctx.Err()
		}
		result.Reason = "Codex thread metadata is unavailable or unsupported"
		result.Evidence = append(result.Evidence, "thread prefixes could not be resolved against local Codex state")
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return DiscoveryResult{}, err
	}

	for index := range result.Candidates {
		candidate := &result.Candidates[index]
		if candidate.ThreadID != "" {
			rows, queryErr := queryRows[discoveryThreadRow](ctx, s.sqlite, stateDB, threadExactQuery(candidate.ThreadID))
			if queryErr == nil {
				row, resolution := uniqueThreadRow(rows)
				if resolution == "" && row.Archived != 0 {
					candidate.Resolution = "archived_title"
					candidate.TopLevel = "unknown"
				}
			}
			continue
		}
		if candidate.Resolution == "title_uuid_mismatch" {
			continue
		}
		rows, queryErr := queryRows[discoveryThreadRow](ctx, s.sqlite, stateDB, threadPrefixQuery(candidate.UUIDPrefix))
		if queryErr != nil {
			if ctx.Err() != nil {
				return DiscoveryResult{}, ctx.Err()
			}
			candidate.Resolution = "metadata_unavailable"
			continue
		}
		row, resolution := uniqueThreadRow(rows)
		if resolution != "" {
			candidate.Resolution = resolution
			continue
		}
		if needsTopLevelCheck(candidate, row, canonical) {
			thread, readErr := readDiscoveryThread(ctx, home, candidate.ThreadID)
			if readErr == nil {
				if threadWorkspace, err := canonicalWorkspace(thread.CWD); err == nil {
					candidate.CodexWorkspace = threadWorkspace
					if threadWorkspace != canonical {
						candidate.WorkspaceMatch = "no"
						candidate.Resolution = "workspace_mismatch"
						continue
					}
				}
				if sameTopLevelWorkspaceThread(thread, canonical, candidate.ThreadID) {
					candidate.TopLevel = "yes"
				} else {
					candidate.TopLevel = "no"
					candidate.Resolution = "ineligible_thread"
				}
			} else {
				if ctx.Err() != nil {
					return DiscoveryResult{}, ctx.Err()
				}
				candidate.Resolution = "top_level_unverified"
			}
		}
	}
	workspaceCandidates, filteredCount = filterWorkspaceCandidates(result.Candidates)
	result.Candidates = workspaceCandidates
	if filteredCount > 0 {
		result.Evidence = append(result.Evidence, "known workspace-mismatched terminal titles were excluded from output")
	}
	if len(result.Candidates) == 0 {
		result.Reason = "terminal title candidates did not match the requested workspace"
		return result, nil
	}
	result.Reason = "the native title API does not prove Codex title freshness across /new, /clear, or /resume"
	result.Evidence = append(result.Evidence,
		"UUID prefixes were resolved only when exactly one local state row matched",
		"workspace and top-level checks are corroborating eligibility evidence, not proof of the TUI's current thread",
		"current-thread freshness is unavailable, so no title-derived candidate is VERIFIED",
	)
	return result, nil
}

func titleThreadID(title string) (string, bool) {
	parts := strings.Split(title, "|")
	if len(parts) < 2 {
		return "", false
	}
	threadID := strings.TrimSpace(parts[1])
	if !validThreadID(threadID) {
		return "", false
	}
	return strings.ToLower(threadID), true
}

func filterWorkspaceCandidates(candidates []ThreadCandidate) ([]ThreadCandidate, int) {
	filtered := make([]ThreadCandidate, 0, len(candidates))
	removed := 0
	for _, candidate := range candidates {
		if candidate.WorkspaceMatch == "no" {
			removed++
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered, removed
}

func sanitizeTerminalTitle(title string) string {
	parts := strings.Split(title, "|")
	for index := 1; index < len(parts); index++ {
		parts[index] = sanitizeDiscoveryText(strings.TrimSpace(parts[index]))
	}
	return strings.Join(parts, " | ")
}

func sanitizeDiscoveryText(value string) string {
	if redaction.ContainsPrivateKey(value) {
		return "<redacted>"
	}
	value = redaction.SanitizeText(value)
	fields := strings.Fields(value)
	for index := range fields {
		fields[index] = redaction.SanitizeText(fields[index])
	}
	return strings.Join(fields, " ")
}

func uniqueThreadRow(rows []discoveryThreadRow) (discoveryThreadRow, string) {
	switch len(rows) {
	case 0:
		return discoveryThreadRow{}, "no_match"
	case 1:
		return rows[0], ""
	default:
		return discoveryThreadRow{}, "ambiguous"
	}
}

func needsTopLevelCheck(candidate *ThreadCandidate, row discoveryThreadRow, requestedWorkspace string) bool {
	if !validThreadID(row.ID) {
		candidate.Resolution = "invalid_state_id"
		return false
	}
	candidate.ThreadID = strings.ToLower(row.ID)
	if row.Archived != 0 {
		candidate.Resolution = "archived_title"
		return false
	}
	threadWorkspace, err := canonicalWorkspace(row.CWD)
	if err != nil {
		candidate.Resolution = "workspace_unavailable"
		return false
	}
	candidate.CodexWorkspace = threadWorkspace
	if threadWorkspace != requestedWorkspace {
		candidate.WorkspaceMatch = "no"
		candidate.Resolution = "workspace_mismatch"
		return false
	}
	candidate.WorkspaceMatch = "state_only"
	if candidate.Workspace != "" {
		terminalWorkspace, err := canonicalWorkspace(candidate.Workspace)
		if err != nil {
			candidate.WorkspaceMatch = "unknown"
			candidate.Resolution = "terminal_workspace_unavailable"
			return false
		}
		if terminalWorkspace != requestedWorkspace {
			candidate.WorkspaceMatch = "no"
			candidate.Resolution = "terminal_workspace_mismatch"
			return false
		}
		candidate.WorkspaceMatch = "yes"
	}
	if row.ThreadSource != "user" {
		candidate.TopLevel = "no"
		candidate.Resolution = "ineligible_thread_source"
		return false
	}
	candidate.Resolution = "unique_workspace_match"
	return true
}

func readDiscoveryThread(ctx context.Context, home, threadID string) (appServerThread, error) {
	ctx, cancel := context.WithTimeout(ctx, appServerTimeout)
	defer cancel()
	rpc, closeConn, err := newAppServerClient(home).connect(ctx)
	if err != nil {
		return appServerThread{}, err
	}
	defer closeConn()
	var result struct {
		Thread appServerThread `json:"thread"`
	}
	if err := rpc.call("thread/read", map[string]any{"threadId": threadID, "includeTurns": false}, &result); err != nil {
		return appServerThread{}, err
	}
	return result.Thread, nil
}

func threadPrefixQuery(prefix string) string {
	compact := strings.ToLower(strings.ReplaceAll(prefix, "-", ""))
	return "SELECT id, cwd, thread_source, archived FROM threads WHERE lower(replace(id, '-', '')) GLOB " + sqlLiteral(compact+"*") + " LIMIT 2"
}

func threadExactQuery(threadID string) string {
	return "SELECT id, cwd, thread_source, archived FROM threads WHERE lower(id) = " + sqlLiteral(strings.ToLower(threadID)) + " LIMIT 2"
}

func parseThreadTitle(title string) (prefix, threadTitle, projectName string, ok bool) {
	match := leadingUUIDPrefix.FindStringSubmatch(title)
	if len(match) != 2 {
		return "", "", "", false
	}
	prefix = strings.ToLower(match[1])
	compact := strings.ReplaceAll(prefix, "-", "")
	if len(compact) < 8 || len(compact) > 32 {
		return "", "", "", false
	}
	remainder := strings.TrimSpace(title[len(match[1]):])
	if strings.HasPrefix(remainder, "...") {
		remainder = strings.TrimPrefix(remainder, "...")
	}
	if strings.HasPrefix(remainder, "…") {
		remainder = strings.TrimSpace(strings.TrimPrefix(remainder, "…"))
	}
	remainder = strings.TrimSpace(remainder)
	if strings.HasPrefix(remainder, "|") {
		remainder = strings.TrimSpace(remainder[1:])
	}
	parts := strings.SplitN(remainder, "|", 2)
	if len(parts) > 0 {
		threadTitle = strings.TrimSpace(parts[0])
	}
	if len(parts) == 2 {
		projectName = strings.TrimSpace(parts[1])
	}
	return prefix, threadTitle, projectName, true
}

func (r DiscoveryResult) WriteJSON(output io.Writer) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	return encoder.Encode(r)
}
