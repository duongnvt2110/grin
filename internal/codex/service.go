package codex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"grin/internal/errs"
	"grin/internal/redaction"
)

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

type sqliteRunner func(context.Context, string, string) ([]byte, error)

type appServerAPI interface {
	Queue(context.Context, string, string, string) error
}

type Service struct {
	sqlite       sqliteRunner
	home         func() (string, error)
	appServerFor func(string) appServerAPI
}

func New() *Service {
	return &Service{
		sqlite: runSQLite,
		home:   defaultCodexHome,
		appServerFor: func(home string) appServerAPI {
			return newAppServerClient(home)
		},
	}
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
	requestID, err := newUUID()
	if err != nil {
		return QueueResult{}, errs.New(errs.ErrInternal, "could not create request ID", false)
	}
	markedMessage := "[grin-request-id: " + requestID + "]\n" + message
	if err := s.appServerFor(home).Queue(ctx, canonical, threadID, markedMessage); err != nil {
		if ctx.Err() != nil {
			return QueueResult{}, errs.New(errs.ErrCancelled, "Codex queue was cancelled", true)
		}
		return QueueResult{}, err
	}
	return QueueResult{ThreadID: threadID, RequestID: requestID}, nil
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
	stateDB, err := resolveStateDB(ctx, s.sqlite, home)
	if err != nil {
		return TurnResult{}, err
	}
	historyDB, err := resolveThreadHistoryDB(ctx, s.sqlite, home)
	if err != nil {
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
	userHome, err := defaultUserHome()
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

func defaultUserHome() (string, error) {
	userHome, err := os.UserHomeDir()
	if err == nil && userHome != "" {
		return userHome, nil
	}
	current, err := user.Current()
	if err != nil || !filepath.IsAbs(current.HomeDir) {
		return "", errs.New(errs.ErrUnsupported, "default user home is unavailable", false)
	}
	return current.HomeDir, nil
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
