package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"grin/internal/errs"
)

const appServerReadLimit = 4 << 20
const appServerTimeout = 5 * time.Second

type appServerThread struct {
	ID                   string  `json:"id"`
	CWD                  string  `json:"cwd"`
	ParentThreadID       *string `json:"parentThreadId"`
	Ephemeral            bool    `json:"ephemeral"`
	Originator           string  `json:"originator"`
	ThreadSource         string  `json:"threadSource"`
	CanAcceptDirectInput *bool   `json:"canAcceptDirectInput"`
	Preview              string  `json:"preview"`
}

type appServerClient struct {
	socketPath string
}

func newAppServerClient(home string) appServerClient {
	return appServerClient{socketPath: filepath.Join(home, "app-server-control", "app-server-control.sock")}
}

func (c appServerClient) connect(ctx context.Context) (*appServerRPC, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, appServerError(err)
	}
	if c.socketPath == "" {
		return nil, nil, errs.New(errs.ErrUnsupported, "Codex app-server socket path is unavailable", false)
	}
	if _, err := os.Stat(c.socketPath); err != nil {
		return nil, nil, errs.New(errs.ErrUnsupported, "Codex app-server is unavailable", true)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.socketPath)
	}}
	conn, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		transport.CloseIdleConnections()
		if ctx.Err() != nil {
			return nil, nil, appServerError(ctx.Err())
		}
		return nil, nil, errs.New(errs.ErrUnsupported, "could not connect to Codex app-server", true)
	}
	closeConn := func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		transport.CloseIdleConnections()
	}
	conn.SetReadLimit(appServerReadLimit)
	rpc := &appServerRPC{ctx: ctx, conn: conn}
	var initialized map[string]json.RawMessage
	if err := rpc.call("initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "grin", "title": "Grin", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &initialized); err != nil {
		closeConn()
		return nil, nil, err
	}
	if err := rpc.notify("initialized", map[string]any{}); err != nil {
		closeConn()
		return nil, nil, appServerError(err)
	}
	return rpc, closeConn, nil
}

func (c appServerClient) Resume(ctx context.Context, cwd, threadID string) error {
	if strings.TrimSpace(cwd) == "" || !validThreadID(threadID) {
		return errs.New(errs.ErrInvalidInput, "workspace and valid thread_id are required", false)
	}
	ctx, cancel := context.WithTimeout(ctx, appServerTimeout)
	defer cancel()
	rpc, closeConn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer closeConn()
	var existing struct {
		Thread appServerThread `json:"thread"`
	}
	if err := rpc.call("thread/read", map[string]any{"threadId": threadID, "includeTurns": false}, &existing); err != nil {
		return err
	}
	if !sameTopLevelWorkspaceThread(existing.Thread, cwd, threadID) {
		return errs.New(errs.ErrNotFound, "Codex thread does not belong to the requested workspace", false)
	}
	// An already-loaded TUI thread can accept input before its first rollout is
	// persisted. Resuming it from disk would fail with "no rollout found".
	if existing.Thread.CanAcceptDirectInput != nil && *existing.Thread.CanAcceptDirectInput {
		return nil
	}
	var resumed struct {
		Thread appServerThread `json:"thread"`
	}
	if err := rpc.call("thread/resume", map[string]any{"threadId": threadID, "cwd": cwd, "excludeTurns": true}, &resumed); err != nil {
		return err
	}
	if !sameTopLevelWorkspaceThread(resumed.Thread, cwd, threadID) || resumed.Thread.CanAcceptDirectInput == nil || !*resumed.Thread.CanAcceptDirectInput {
		return errs.New(errs.ErrNotFound, "Codex thread does not belong to the requested workspace", false)
	}
	return nil
}

func sameTopLevelWorkspaceThread(thread appServerThread, cwd, threadID string) bool {
	canonical, err := canonicalWorkspace(thread.CWD)
	return err == nil && strings.EqualFold(thread.ID, threadID) && canonical == cwd && thread.ParentThreadID == nil && !thread.Ephemeral && thread.ThreadSource == "user"
}

// Queue validates an exact top-level user thread and resumes it only when
// direct input is unavailable, then adds a message through Codex app-server.
func (c appServerClient) Queue(ctx context.Context, cwd, threadID, message string) error {
	if !validThreadID(threadID) || strings.TrimSpace(message) == "" {
		return errs.New(errs.ErrInvalidInput, "valid thread_id and message are required", false)
	}
	clientMessageID, err := newUUID()
	if err != nil {
		return errs.New(errs.ErrInternal, "could not create Codex client message ID", false)
	}
	if err := c.Resume(ctx, cwd, threadID); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, appServerTimeout)
	defer cancel()
	rpc, closeConn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer closeConn()
	var result struct {
		QueuedSubmission struct {
			ID                  string
			ClientUserMessageID string
		}
	}
	err = rpc.call("thread/queue/add", map[string]any{
		"threadId": threadID,
		"input": []any{map[string]any{
			"type": "text", "text": message, "text_elements": []any{},
		}},
		"clientUserMessageId": clientMessageID,
	}, &result)
	if err != nil {
		return err
	}
	if result.QueuedSubmission.ID == "" || result.QueuedSubmission.ClientUserMessageID != clientMessageID {
		return errs.New(errs.ErrUnsupported, "Codex app-server returned mismatched queue confirmation", false)
	}
	return nil
}

type appServerRPC struct {
	ctx  context.Context
	conn *websocket.Conn
	next int
}

func (r *appServerRPC) notify(method string, params any) error {
	return wsjson.Write(r.ctx, r.conn, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (r *appServerRPC) call(method string, params, result any) error {
	r.next++
	id := strconv.Itoa(r.next)
	if err := wsjson.Write(r.ctx, r.conn, map[string]any{"jsonrpc": "2.0", "id": json.Number(id), "method": method, "params": params}); err != nil {
		return appServerError(err)
	}
	for {
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := wsjson.Read(r.ctx, r.conn, &response); err != nil {
			return appServerError(err)
		}
		responseID := strings.Trim(string(response.ID), `"`)
		if responseID != id {
			continue
		}
		if response.Error != nil {
			// Identify the failed RPC without exposing untrusted server error text.
			return errs.New(errs.ErrUnsupported, "Codex app-server "+method+" request failed", false)
		}
		if err := json.Unmarshal(response.Result, result); err != nil {
			return errs.New(errs.ErrUnsupported, "Codex app-server returned malformed thread data", false)
		}
		return nil
	}
}

func appServerError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errs.New(errs.ErrCancelled, "Codex app-server request was cancelled", true)
	}
	return errs.New(errs.ErrUnsupported, "Codex app-server communication failed", true)
}
