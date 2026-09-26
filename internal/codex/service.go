package codex

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"grin/internal/errs"
	"grin/internal/redaction"
)

type Session struct {
	PID       int    `json:"pid"`
	ThreadID  string `json:"thread_id"`
	Workspace string `json:"-"`
}

type ListResult struct {
	Workspace string    `json:"workspace"`
	Running   bool      `json:"running"`
	Matches   []Session `json:"matches"`
}

type QueueResult struct {
	ThreadID  string `json:"thread_id"`
	RequestID string `json:"request_id"`
}

type TurnResult struct {
	Status string `json:"status"`
	Text   string `json:"text,omitempty"`
}

const (
	TurnPending     = "pending"
	TurnInProgress  = "inProgress"
	TurnCompleted   = "completed"
	TurnFailed      = "failed"
	TurnInterrupted = "interrupted"
)

type inspector func(context.Context, string, string) ([]Session, bool, error)
type queueRunner func(context.Context, string, string) error
type sqliteRunner func(context.Context, string, string) ([]byte, error)

type Service struct {
	inspect inspector
	queue   queueRunner
	sqlite  sqliteRunner
	home    func() (string, error)
}

func New() *Service {
	return &Service{
		inspect: inspectRunningSessions,
		queue:   runQueue,
		sqlite:  runSQLite,
		home:    defaultCodexHome,
	}
}

func (s *Service) List(ctx context.Context, workspace string) (ListResult, error) {
	canonical, err := canonicalWorkspace(workspace)
	if err != nil {
		return ListResult{}, err
	}
	home, err := s.codexHome()
	if err != nil {
		return ListResult{}, err
	}
	sessions, processMatched, err := s.inspect(ctx, canonical, home)
	if err != nil {
		return ListResult{}, err
	}
	matches := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		processWorkspace, pathErr := canonicalWorkspace(session.Workspace)
		if pathErr != nil {
			return ListResult{}, errs.New(errs.ErrUnsupported, "Codex session workspace could not be verified", false)
		}
		if processWorkspace == canonical && validThreadID(session.ThreadID) {
			matches = append(matches, Session{PID: session.PID, ThreadID: strings.ToLower(session.ThreadID), Workspace: canonical})
		}
	}
	if processMatched && len(matches) == 0 {
		return ListResult{}, errs.New(errs.ErrUnsupported, "running Codex session could not be safely identified", false)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].PID == matches[j].PID {
			return matches[i].ThreadID < matches[j].ThreadID
		}
		return matches[i].PID < matches[j].PID
	})
	return ListResult{Workspace: canonical, Running: len(matches) > 0, Matches: matches}, nil
}

func (s *Service) Queue(ctx context.Context, workspace, threadID, message string) (QueueResult, error) {
	if !validThreadID(threadID) {
		return QueueResult{}, errs.New(errs.ErrInvalidInput, "thread_id must be a UUID", false)
	}
	threadID = strings.ToLower(threadID)
	if strings.TrimSpace(message) == "" {
		return QueueResult{}, errs.New(errs.ErrInvalidInput, "message is required", false)
	}
	canonical, err := canonicalWorkspace(workspace)
	if err != nil {
		return QueueResult{}, err
	}
	home, err := s.codexHome()
	if err != nil {
		return QueueResult{}, err
	}
	sessions, processMatched, err := s.inspect(ctx, canonical, home)
	if err != nil {
		return QueueResult{}, err
	}
	if !processMatched || !hasThread(sessions, canonical, threadID) {
		return QueueResult{}, errs.New(errs.ErrNotFound, "the requested Codex thread is not running in this workspace", false)
	}
	requestID, err := newUUID()
	if err != nil {
		return QueueResult{}, errs.New(errs.ErrInternal, "could not create request ID", false)
	}
	markedMessage := "[grin-request-id: " + requestID + "]\n" + message
	if err := s.queue(ctx, threadID, markedMessage); err != nil {
		if ctx.Err() != nil {
			return QueueResult{}, errs.New(errs.ErrCancelled, "Codex queue was cancelled", true)
		}
		return QueueResult{}, errs.New(errs.ErrWorkspaceUnavailable, "Codex did not accept the queued request", true)
	}
	return QueueResult{ThreadID: threadID, RequestID: requestID}, nil
}

func hasThread(sessions []Session, workspace, threadID string) bool {
	for _, session := range sessions {
		canonical, err := canonicalWorkspace(session.Workspace)
		if err == nil && strings.EqualFold(session.ThreadID, threadID) && canonical == workspace {
			return true
		}
	}
	return false
}

func (s *Service) TurnResult(ctx context.Context, workspace, threadID, requestID string, maxBytes int64) (TurnResult, error) {
	if !validThreadID(threadID) || !validThreadID(requestID) {
		return TurnResult{}, errs.New(errs.ErrInvalidInput, "thread_id and request_id must be UUIDs", false)
	}
	if maxBytes <= 0 {
		return TurnResult{}, errs.New(errs.ErrInvalidInput, "result limit must be positive", false)
	}
	threadID = strings.ToLower(threadID)
	requestID = strings.ToLower(requestID)
	canonical, err := canonicalWorkspace(workspace)
	if err != nil {
		return TurnResult{}, err
	}
	home, err := s.codexHome()
	if err != nil {
		return TurnResult{}, err
	}
	stateDB, historyDB, err := databasePaths(home)
	if err != nil {
		return TurnResult{}, err
	}
	if err := validateTable(ctx, s.sqlite, stateDB, "threads", "id", "cwd", "thread_source"); err != nil {
		return TurnResult{}, err
	}
	if err := validateTable(ctx, s.sqlite, historyDB, "thread_turns", "thread_id", "turn_id", "status", "final_agent_item_id"); err != nil {
		return TurnResult{}, err
	}
	if err := validateTable(ctx, s.sqlite, historyDB, "thread_items", "thread_id", "turn_id", "item_id", "item_type", "item_json"); err != nil {
		return TurnResult{}, err
	}

	threadRows, err := queryRows[struct {
		CWD          string  `json:"cwd"`
		ThreadSource *string `json:"thread_source"`
	}](ctx, s.sqlite, stateDB, "SELECT cwd, thread_source FROM threads WHERE id="+sqlLiteral(threadID)+" LIMIT 1")
	if err != nil {
		return TurnResult{}, err
	}
	if len(threadRows) == 0 {
		return TurnResult{}, errs.New(errs.ErrNotFound, "Codex thread was not found", false)
	}
	threadWorkspace, err := canonicalWorkspace(threadRows[0].CWD)
	if err != nil || threadWorkspace != canonical || threadRows[0].ThreadSource == nil || *threadRows[0].ThreadSource != "user" {
		return TurnResult{}, errs.New(errs.ErrWorkspaceNotFound, "Codex thread does not belong to this workspace", false)
	}
	marker := "[grin-request-id: " + requestID + "]"
	if err := validateMatchingUserMessageShapes(ctx, s.sqlite, historyDB, threadID, marker); err != nil {
		return TurnResult{}, err
	}
	turnRows, err := queryRows[struct {
		TurnID string `json:"turn_id"`
	}](ctx, s.sqlite, historyDB, matchingTurnsQuery(threadID, marker))
	if err != nil {
		return TurnResult{}, err
	}
	if len(turnRows) == 0 {
		return TurnResult{Status: TurnPending}, nil
	}
	if len(turnRows) != 1 || turnRows[0].TurnID == "" {
		return TurnResult{}, errs.New(errs.ErrUnsupported, "request marker did not identify exactly one Codex turn", false)
	}
	turnID := strings.ToLower(turnRows[0].TurnID)
	turns, err := queryRows[struct {
		Status           string  `json:"status"`
		FinalAgentItemID *string `json:"final_agent_item_id"`
	}](ctx, s.sqlite, historyDB, "SELECT status, final_agent_item_id FROM thread_turns WHERE thread_id="+sqlLiteral(threadID)+" AND turn_id="+sqlLiteral(turnID)+" LIMIT 1")
	if err != nil {
		return TurnResult{}, err
	}
	if len(turns) == 0 {
		return TurnResult{Status: TurnPending}, nil
	}
	turn := turns[0]
	switch turn.Status {
	case TurnInProgress:
		return TurnResult{Status: TurnInProgress}, nil
	case TurnFailed, TurnInterrupted:
		return TurnResult{Status: turn.Status}, nil
	case TurnCompleted:
	default:
		return TurnResult{}, errs.New(errs.ErrUnsupported, "Codex returned an unsupported turn status", false)
	}
	if turn.FinalAgentItemID == nil || *turn.FinalAgentItemID == "" {
		return TurnResult{}, errs.New(errs.ErrUnsupported, "completed Codex turn has no final response", false)
	}
	itemRows, err := queryRows[struct {
		ItemType string  `json:"item_type"`
		Kind     *string `json:"kind"`
		TextKind *string `json:"text_kind"`
		TextSize *int64  `json:"text_size"`
		Text     *string `json:"text"`
	}](ctx, s.sqlite, historyDB, finalItemQuery(threadID, turnID, *turn.FinalAgentItemID, maxBytes))
	if err != nil {
		return TurnResult{}, err
	}
	if len(itemRows) != 1 {
		return TurnResult{}, errs.New(errs.ErrUnsupported, "Codex final response could not be verified", false)
	}
	item := itemRows[0]
	if item.ItemType != "agentMessage" || item.Kind == nil || *item.Kind != "agentMessage" || item.TextKind == nil || *item.TextKind != "text" {
		return TurnResult{}, errs.New(errs.ErrUnsupported, "Codex final response has an unsupported shape", false)
	}
	if item.TextSize == nil || *item.TextSize > maxBytes || item.Text == nil {
		return TurnResult{}, errs.New(errs.ErrOutputLimit, "Codex final response exceeds the configured limit", false)
	}
	if redaction.ContainsPrivateKey(*item.Text) {
		return TurnResult{}, errs.New(errs.ErrSensitiveFileBlocked, "Codex final response contains blocked private-key material", false)
	}
	text := redaction.SanitizeText(*item.Text)
	if int64(len(text)) > maxBytes {
		return TurnResult{}, errs.New(errs.ErrOutputLimit, "Codex final response exceeds the configured limit", false)
	}
	return TurnResult{Status: TurnCompleted, Text: text}, nil
}

func (s *Service) codexHome() (string, error) {
	var home string
	var err error
	if s.home == nil {
		home, err = defaultCodexHome()
	} else {
		home, err = s.home()
	}
	if err != nil {
		return "", err
	}
	canonical, err := canonicalExistingDirectory(home)
	if err != nil {
		return "", errs.New(errs.ErrUnsupported, "Codex data root is unavailable", false)
	}
	return canonical, nil
}

func defaultCodexHome() (string, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", errs.New(errs.ErrUnsupported, "default Codex data root is unavailable", false)
	}
	defaultRoot, err := canonicalExistingDirectory(filepath.Join(userHome, ".codex"))
	if err != nil {
		return "", errs.New(errs.ErrUnsupported, "default Codex data root is unavailable", false)
	}
	configured, set := os.LookupEnv("CODEX_HOME")
	if set && configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errs.New(errs.ErrUnsupported, "alternate Codex data roots are unsupported", false)
		}
		configuredRoot, resolveErr := canonicalExistingDirectory(configured)
		if resolveErr != nil || configuredRoot != defaultRoot {
			return "", errs.New(errs.ErrUnsupported, "alternate Codex data roots are unsupported", false)
		}
	}
	return defaultRoot, nil
}

func canonicalWorkspace(workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" || !filepath.IsAbs(workspace) {
		return "", errs.New(errs.ErrInvalidInput, "workspace must be an absolute path", false)
	}
	canonical, err := canonicalExistingDirectory(workspace)
	if err != nil {
		return "", errs.New(errs.ErrWorkspaceUnavailable, "workspace is unavailable", true)
	}
	return canonical, nil
}

func canonicalExistingDirectory(path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return filepath.Clean(canonical), nil
}

func inspectRunningSessions(ctx context.Context, workspace, home string) ([]Session, bool, error) {
	if runtime.GOOS != "darwin" {
		return nil, false, errs.New(errs.ErrUnsupported, "Codex session discovery is unsupported on this OS", false)
	}
	pids, err := codexPIDs(ctx)
	if err != nil {
		return nil, false, err
	}
	sessions := make([]Session, 0)
	processMatched := false
	for _, pid := range pids {
		var found []Session
		var matched bool
		found, matched, err = inspectDarwinProcess(ctx, pid, home, workspace)
		if err != nil {
			return nil, false, err
		}
		if matched && len(found) > 0 {
			processMatched = true
			sessions = append(sessions, found...)
		}
	}
	return sessions, processMatched, nil
}

func codexPIDs(ctx context.Context) ([]int, error) {
	command := exec.CommandContext(ctx, "ps", "-axo", "pid=,ucomm=")
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, errs.New(errs.ErrCancelled, "Codex process discovery was cancelled", true)
		}
		return nil, errs.New(errs.ErrInternal, "Codex process discovery failed", false)
	}
	return parseCodexPIDs(string(output))
}

func parseCodexPIDs(output string) ([]int, error) {
	var pids []int
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || filepath.Base(fields[1]) != "codex" {
			continue
		}
		pid, parseErr := strconv.Atoi(fields[0])
		if parseErr == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, errs.New(errs.ErrInternal, "Codex process discovery failed", false)
	}
	return pids, nil
}

func inspectDarwinProcess(ctx context.Context, pid int, home, targetWorkspace string) ([]Session, bool, error) {
	command := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-Fn")
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, errs.New(errs.ErrCancelled, "Codex process inspection was cancelled", true)
		}
		return nil, false, errs.New(errs.ErrInternal, "Codex process inspection failed", true)
	}
	return parseLsofSessions(pid, string(output), home, targetWorkspace)
}

func parseLsofSessions(pid int, output, home, targetWorkspace string) ([]Session, bool, error) {
	canonicalHome, err := canonicalExistingDirectory(home)
	if err != nil {
		return nil, false, errs.New(errs.ErrUnsupported, "Codex data root is unavailable", false)
	}
	home = canonicalHome
	targetWorkspace, err = canonicalWorkspace(targetWorkspace)
	if err != nil {
		return nil, false, err
	}
	workspace := ""
	var paths []string
	fd := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "f") {
			fd = strings.TrimPrefix(line, "f")
			continue
		}
		if !strings.HasPrefix(line, "n") {
			continue
		}
		path := strings.TrimPrefix(line, "n")
		if fd == "cwd" {
			workspace = path
		} else {
			paths = append(paths, path)
		}
	}
	if workspace == "" {
		return nil, false, errs.New(errs.ErrUnsupported, "Codex process workspace could not be inspected", true)
	}
	canonical, err := canonicalWorkspace(workspace)
	if err != nil {
		return nil, false, errs.New(errs.ErrUnsupported, "Codex process workspace could not be verified", true)
	}
	if canonical != targetWorkspace {
		return nil, false, nil
	}

	byThread := make(map[string]Session)
	for _, path := range paths {
		rollout, ok, pathErr := rolloutPath(path, home)
		if pathErr != nil {
			return nil, false, errs.New(errs.ErrUnsupported, "open Codex session could not be inspected", true)
		}
		if !ok {
			continue
		}
		meta, metaErr := firstSessionMeta(rollout)
		if metaErr != nil {
			return nil, false, errs.New(errs.ErrUnsupported, "open Codex session metadata is unavailable", true)
		}
		if meta.ThreadSource == "subagent" {
			continue
		}
		if meta.ThreadSource != "user" || !validThreadID(meta.ID) {
			return nil, false, errs.New(errs.ErrUnsupported, "Codex session metadata could not be safely identified", false)
		}
		sessionWorkspace, workspaceErr := canonicalWorkspace(meta.CWD)
		if workspaceErr != nil {
			return nil, false, errs.New(errs.ErrUnsupported, "Codex session workspace could not be verified", true)
		}
		if sessionWorkspace == targetWorkspace {
			threadID := strings.ToLower(meta.ID)
			byThread[threadID] = Session{PID: pid, ThreadID: threadID, Workspace: sessionWorkspace}
		}
	}
	result := make([]Session, 0, len(byThread))
	for _, session := range byThread {
		result = append(result, session)
	}
	return result, true, nil
}

type sessionMeta struct {
	ID           string `json:"id"`
	CWD          string `json:"cwd"`
	ThreadSource string `json:"thread_source"`
}

func firstSessionMeta(path string) (sessionMeta, error) {
	file, err := os.Open(path)
	if err != nil {
		return sessionMeta{}, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return sessionMeta{}, err
		}
		return sessionMeta{}, errors.New("session metadata was not found")
	}
	var record struct {
		Type    string      `json:"type"`
		Payload sessionMeta `json:"payload"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		return sessionMeta{}, err
	}
	if record.Type != "session_meta" || record.Payload.ID == "" || record.Payload.CWD == "" || record.Payload.ThreadSource == "" {
		return sessionMeta{}, errors.New("first rollout record is not complete session metadata")
	}
	return record.Payload, nil
}

func rolloutPath(path, home string) (string, bool, error) {
	canonicalHome, err := canonicalExistingDirectory(home)
	if err != nil {
		return "", false, err
	}
	home = canonicalHome
	clean := filepath.Clean(path)
	if filepath.Ext(clean) != ".jsonl" || !strings.HasPrefix(filepath.Base(clean), "rollout-") {
		return "", false, nil
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", false, err
	}
	relative, err := filepath.Rel(home, real)
	if err != nil {
		return "", false, err
	}
	if relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || !strings.HasPrefix(relative, "sessions"+string(filepath.Separator)) {
		return "", false, errors.New("Codex rollout is outside the supported data root")
	}
	return real, true, nil
}

func databasePaths(home string) (string, string, error) {
	canonicalHome, err := canonicalExistingDirectory(home)
	if err != nil {
		return "", "", errs.New(errs.ErrUnsupported, "Codex data root is unavailable", false)
	}
	home = canonicalHome
	stateDB, err := codexDatabasePath(home, "state_5.sqlite")
	if err != nil {
		return "", "", err
	}
	historyDB, err := codexDatabasePath(home, "thread_history_1.sqlite")
	if err != nil {
		return "", "", err
	}
	return stateDB, historyDB, nil
}

func codexDatabasePath(home, name string) (string, error) {
	path := filepath.Join(home, name)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errs.New(errs.ErrUnsupported, "Codex persisted history is unavailable", false)
	}
	relative, err := filepath.Rel(home, real)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errs.New(errs.ErrUnsupported, "Codex persisted history is outside the default data root", false)
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() {
		return "", errs.New(errs.ErrUnsupported, "Codex persisted history is unavailable", false)
	}
	return real, nil
}

func validateTable(ctx context.Context, sqlite sqliteRunner, path, table string, required ...string) error {
	rows, err := queryRows[struct {
		Name string `json:"name"`
	}](ctx, sqlite, path, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	columns := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		columns[row.Name] = struct{}{}
	}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return errs.New(errs.ErrUnsupported, "Codex database schema is not supported", false)
		}
	}
	return nil
}

func validateMatchingUserMessageShapes(ctx context.Context, sqlite sqliteRunner, path, threadID, marker string) error {
	query := "SELECT COUNT(*) AS invalid FROM thread_items WHERE thread_id=" + sqlLiteral(threadID) + " AND item_type='userMessage' AND instr(item_json," + sqlLiteral(marker) + ")>0 AND CASE WHEN json_valid(item_json)=0 THEN 1 WHEN json_type(item_json)!='object' THEN 1 WHEN json_extract(item_json,'$.type')!='userMessage' THEN 1 WHEN json_type(item_json,'$.content')!='array' THEN 1 ELSE 0 END"
	rows, err := queryRows[struct {
		Invalid int `json:"invalid"`
	}](ctx, sqlite, path, query)
	if err != nil || len(rows) != 1 || rows[0].Invalid != 0 {
		return errs.New(errs.ErrUnsupported, "Codex user-message history has an unsupported shape", false)
	}
	return nil
}

func matchingTurnsQuery(threadID, marker string) string {
	safeItem := "CASE WHEN json_valid(ti.item_json)=0 THEN '{\"content\":[]}' WHEN json_type(ti.item_json)!='object' THEN '{\"content\":[]}' WHEN json_type(ti.item_json,'$.content')!='array' THEN '{\"content\":[]}' ELSE ti.item_json END"
	return "SELECT DISTINCT ti.turn_id FROM thread_items AS ti, json_each(" + safeItem + ",'$.content') AS part WHERE ti.thread_id=" + sqlLiteral(threadID) + " AND ti.item_type='userMessage' AND json_extract(part.value,'$.type')='text' AND json_type(part.value,'$.text')='text' AND instr(json_extract(part.value,'$.text')," + sqlLiteral(marker) + ")>0 LIMIT 2"
}

func finalItemQuery(threadID, turnID, itemID string, maxBytes int64) string {
	textExpr := "json_extract(item_json,'$.text')"
	textSize := "length(CAST(" + textExpr + " AS BLOB))"
	return "SELECT item_type, json_extract(item_json,'$.type') AS kind, json_type(item_json,'$.text') AS text_kind, " + textSize + " AS text_size, CASE WHEN " + textSize + "<=" + strconv.FormatInt(maxBytes, 10) + " THEN " + textExpr + " END AS text FROM thread_items WHERE thread_id=" + sqlLiteral(threadID) + " AND turn_id=" + sqlLiteral(turnID) + " AND item_id=" + sqlLiteral(itemID) + " LIMIT 1"
}

func queryRows[T any](ctx context.Context, sqlite sqliteRunner, path, query string) ([]T, error) {
	output, err := sqlite(ctx, path, query)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errs.New(errs.ErrCancelled, "Codex history lookup was cancelled", true)
		}
		return nil, errs.New(errs.ErrUnsupported, "Codex persisted history could not be read", false)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return []T{}, nil
	}
	var rows []T
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, errs.New(errs.ErrUnsupported, "Codex persisted history has an unsupported shape", false)
	}
	return rows, nil
}

func runSQLite(ctx context.Context, path, query string) ([]byte, error) {
	command := exec.CommandContext(ctx, "sqlite3", "-init", "/dev/null", "-readonly", "-json", path, query)
	command.Stderr = io.Discard
	return command.Output()
}

func runQueue(ctx context.Context, threadID, message string) error {
	command := exec.CommandContext(ctx, "codex", "queue", "--thread", threadID, "--message", message)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func sqlLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func validThreadID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
				return false
			}
		}
	}
	return true
}
