package codex

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"grin/internal/errs"
)

func TestAppServerQueueRPC(t *testing.T) {
	for _, tc := range []struct {
		name              string
		readDirectInput   *bool
		wrongWorkspace    bool
		resumeError       bool
		resumeDirectInput bool
		replyMode         string
		wantError         errs.ErrorCode
		wantResume        bool
		wantQueued        bool
	}{
		{name: "unloaded thread resumes then queues", resumeDirectInput: true, replyMode: "accepted", wantResume: true, wantQueued: true},
		{name: "direct input false resumes then queues", readDirectInput: boolPointer(false), resumeDirectInput: true, replyMode: "accepted", wantResume: true, wantQueued: true},
		{name: "loaded memory-only thread skips resume", readDirectInput: boolPointer(true), resumeError: true, replyMode: "accepted", wantQueued: true},
		{name: "loaded thread wrong workspace is rejected", readDirectInput: boolPointer(true), wrongWorkspace: true, wantError: errs.ErrNotFound},
		{name: "unloaded thread resume error stops send", resumeError: true, wantResume: true, wantError: errs.ErrUnsupported},
		{name: "unloaded resumed thread cannot accept input", wantResume: true, wantError: errs.ErrNotFound},
		{name: "loaded thread missing queue confirmation", readDirectInput: boolPointer(true), wantQueued: true, wantError: errs.ErrUnsupported},
		{name: "loaded thread mismatched queue confirmation", readDirectInput: boolPointer(true), replyMode: "mismatch", wantQueued: true, wantError: errs.ErrUnsupported},
		{name: "loaded thread queue RPC failure", readDirectInput: boolPointer(true), replyMode: "rpc_error", wantQueued: true, wantError: errs.ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "server.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			sent := make(chan map[string]json.RawMessage, 1)
			methods := make(chan string, 8)
			workspace, err := canonicalWorkspace(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close(websocket.StatusNormalClosure, "")
				for {
					var req struct {
						ID     json.RawMessage
						Method string
						Params map[string]json.RawMessage
					}
					if wsjson.Read(r.Context(), conn, &req) != nil {
						return
					}
					if len(req.ID) == 0 {
						continue
					}
					if req.Method != "initialize" {
						methods <- req.Method
					}
					result := any(map[string]any{})
					var rpcError any
					switch req.Method {
					case "thread/read":
						cwd := workspace
						if tc.wrongWorkspace {
							cwd = t.TempDir()
						}
						result = map[string]any{"thread": map[string]any{
							"id": testThreadID, "cwd": cwd, "threadSource": "user",
							"canAcceptDirectInput": tc.readDirectInput,
						}}
					case "thread/resume":
						if tc.resumeError {
							rpcError = map[string]any{"code": -32600, "message": "no rollout found for thread id"}
						} else {
							result = map[string]any{"thread": map[string]any{
								"id": testThreadID, "cwd": workspace, "threadSource": "user",
								"canAcceptDirectInput": tc.resumeDirectInput,
							}}
						}
					case "thread/queue/add":
						if tc.replyMode == "rpc_error" {
							rpcError = map[string]any{"code": -32600, "message": "server rejection"}
						}
						sent <- req.Params
						var clientID string
						_ = json.Unmarshal(req.Params["clientUserMessageId"], &clientID)
						if tc.replyMode == "mismatch" {
							clientID = "wrong-client-id"
						}
						if tc.replyMode != "" {
							result = map[string]any{"queuedSubmission": map[string]any{
								"id": "queued-item-1", "clientUserMessageId": clientID,
							}}
						}
					}
					response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
					if rpcError != nil {
						response["error"] = rpcError
					} else {
						response["result"] = result
					}
					if wsjson.Write(r.Context(), conn, response) != nil {
						return
					}
				}
			})}
			go func() { _ = server.Serve(listener) }()
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = server.Shutdown(ctx)
			}()
			message := "[grin-request-id: 188da36e-e0a0-4a5a-905b-19720afbf87c]\nreview this plan"
			err = (appServerClient{socketPath: socket}).Queue(context.Background(), workspace, testThreadID, message)
			if tc.wantError != "" {
				if got := errorCode(err); got != tc.wantError {
					t.Fatalf("Queue error code = %q (%v), want %q", got, err, tc.wantError)
				}
				if tc.resumeError && tc.wantResume && !strings.Contains(err.Error(), "thread/resume") {
					t.Fatalf("resume error hides failing operation: %v", err)
				}
				if tc.replyMode == "rpc_error" && !strings.Contains(err.Error(), "thread/queue/add") {
					t.Fatalf("queue error hides failing operation: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			expectedMethods := []string{"thread/read"}
			if tc.wantResume {
				expectedMethods = append(expectedMethods, "thread/resume")
			}
			if tc.wantQueued {
				expectedMethods = append(expectedMethods, "thread/queue/add")
			}
			var actualMethods []string
			for len(methods) > 0 {
				actualMethods = append(actualMethods, <-methods)
			}
			if !reflect.DeepEqual(actualMethods, expectedMethods) {
				t.Fatalf("app-server methods = %v, want %v", actualMethods, expectedMethods)
			}
			if !tc.wantQueued {
				if len(sent) != 0 {
					t.Fatal("failed validation/resume still queued a request")
				}
				return
			}
			select {
			case got := <-sent:
				var threadID, clientID string
				_ = json.Unmarshal(got["threadId"], &threadID)
				_ = json.Unmarshal(got["clientUserMessageId"], &clientID)
				if threadID != testThreadID || !validThreadID(clientID) {
					t.Fatalf("invalid target or client ID: %q / %q", threadID, clientID)
				}
				var inputs []map[string]json.RawMessage
				if err := json.Unmarshal(got["input"], &inputs); err != nil || len(inputs) != 1 {
					t.Fatalf("invalid input JSON: %v %+v", err, inputs)
				}
				var textValue, kind string
				_ = json.Unmarshal(inputs[0]["text"], &textValue)
				_ = json.Unmarshal(inputs[0]["type"], &kind)
				if kind != "text" || textValue != message || string(inputs[0]["text_elements"]) != "[]" {
					t.Fatalf("bad queue input: %+v", inputs)
				}
			default:
				t.Fatal("queue RPC was not sent")
			}
		})
	}
}

func TestAppServerQueueRejectsInvalidInputBeforeDial(t *testing.T) {
	client := appServerClient{socketPath: filepath.Join(t.TempDir(), "missing.sock")}
	for _, tc := range []struct{ id, msg string }{
		{"invalid-thread", "hello"},
		{testThreadID, "   "},
	} {
		if got := errorCode(client.Queue(context.Background(), t.TempDir(), tc.id, tc.msg)); got != errs.ErrInvalidInput {
			t.Fatalf("Queue(%q,%q) returned code %q", tc.id, tc.msg, got)
		}
	}
}

func TestAppServerClientCancelledBeforeConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := appServerClient{socketPath: filepath.Join(t.TempDir(), "missing.sock")}
	if got := errorCode(client.Queue(ctx, t.TempDir(), testThreadID, "hello")); got != errs.ErrCancelled {
		t.Fatalf("queue canceled: got %q, want ErrCancelled", got)
	}
}
