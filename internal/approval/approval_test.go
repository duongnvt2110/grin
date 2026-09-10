package approval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"grin/internal/errs"
	"grin/internal/events"
	"grin/internal/policy"
)

func testRequest(t *testing.T) ApprovalRequest {
	t.Helper()
	request, err := NewRequest(policy.Operation{Tool: "fs.write_text", Capability: "filesystem.write", Arguments: map[string]any{"path": "note.txt", "content": "hello"}}, "fs.write_text", "write note.txt", "content omitted", RiskHigh)
	if err != nil {
		t.Fatal(err)
	}
	request.ToolCallID = "tool-test"
	return request
}

func TestOperationDigestNormalizesJSONArguments(t *testing.T) {
	first, err := OperationDigest(policy.Operation{Tool: "shell.run", Capability: "shell", Arguments: json.RawMessage(`{"args":["echo","hi"],"timeout":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := OperationDigest(policy.Operation{Tool: "shell.run", Capability: "shell", Arguments: json.RawMessage(" {\n  \"timeout\": 3, \"args\": [\"echo\", \"hi\"] } ")})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("normalized digests differ: %s != %s", first, second)
	}
}

func TestRequestFailsClosedWithoutReliableDelivery(t *testing.T) {
	manager := New(events.New(2), 20*time.Millisecond)
	_, err := manager.Request(context.Background(), testRequest(t))
	if code := errorCode(err); code != errs.ErrApprovalUnavailable {
		t.Fatalf("error code = %s, want %s", code, errs.ErrApprovalUnavailable)
	}
	if manager.Pending() != 0 {
		t.Fatal("unavailable approval remained pending")
	}
}

func TestApprovalIsBoundToDigestAndConsumedOnce(t *testing.T) {
	bus := events.New(4)
	reliable := bus.SubscribeReliable()
	defer reliable.Close()
	manager := New(bus, time.Second)
	request := testRequest(t)
	result := make(chan error, 1)
	go func() {
		_, err := manager.Request(context.Background(), request)
		result <- err
	}()
	required := <-reliable.Events
	if required.Type != events.EventApprovalRequired || required.OperationDigest != request.OperationDigest {
		t.Fatalf("required event = %+v", required)
	}
	if err := manager.Resolve(ApprovalDecision{ApprovalRequestID: request.ID, OperationDigest: strings.Repeat("0", len(request.OperationDigest)), Outcome: events.ApprovalAllowed}); errorCode(err) != errs.ErrApprovalMismatch {
		t.Fatalf("mismatch error = %v", err)
	}
	if err := manager.Resolve(ApprovalDecision{ApprovalRequestID: request.ID, OperationDigest: request.OperationDigest, Outcome: events.ApprovalAllowed}); err != nil {
		t.Fatal(err)
	}
	resolved := <-reliable.Events
	if resolved.Type != events.EventApprovalResolved || resolved.Status != events.StatusComplete {
		t.Fatalf("resolved event = %+v", resolved)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := manager.Resolve(ApprovalDecision{ApprovalRequestID: request.ID, OperationDigest: request.OperationDigest, Outcome: events.ApprovalAllowed}); errorCode(err) != errs.ErrApprovalReplay {
		t.Fatalf("replay error = %v", err)
	}
}

func TestRejectionTimeoutAndCancellationFailClosed(t *testing.T) {
	t.Run("rejection", func(t *testing.T) {
		bus := events.New(4)
		reliable := bus.SubscribeReliable()
		defer reliable.Close()
		manager := New(bus, time.Second)
		request := testRequest(t)
		result := make(chan error, 1)
		go func() { _, err := manager.Request(context.Background(), request); result <- err }()
		<-reliable.Events
		if err := manager.Resolve(ApprovalDecision{ApprovalRequestID: request.ID, OperationDigest: request.OperationDigest, Outcome: events.ApprovalRejected}); err != nil {
			t.Fatal(err)
		}
		resolved := <-reliable.Events
		if resolved.ApprovalOutcome != events.ApprovalRejected {
			t.Fatalf("rejection outcome = %s", resolved.ApprovalOutcome)
		}
		if code := errorCode(<-result); code != errs.ErrApprovalRejected {
			t.Fatalf("error code = %s", code)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		bus := events.New(4)
		reliable := bus.SubscribeReliable()
		defer reliable.Close()
		manager := New(bus, 10*time.Millisecond)
		request := testRequest(t)
		result := make(chan error, 1)
		go func() { _, err := manager.Request(context.Background(), request); result <- err }()
		<-reliable.Events
		if code := errorCode(<-result); code != errs.ErrApprovalTimeout {
			t.Fatalf("error code = %s", code)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		bus := events.New(4)
		reliable := bus.SubscribeReliable()
		defer reliable.Close()
		manager := New(bus, time.Second)
		request := testRequest(t)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { _, err := manager.Request(ctx, request); result <- err }()
		<-reliable.Events
		cancel()
		if code := errorCode(<-result); code != errs.ErrApprovalCancelled {
			t.Fatalf("error code = %s", code)
		}
		resolved := <-reliable.Events
		if resolved.Type != events.EventApprovalResolved || resolved.ApprovalOutcome != events.ApprovalCancelled {
			t.Fatalf("cancellation event = %+v", resolved)
		}
	})
}

func errorCode(err error) errs.ErrorCode {
	if typed, ok := err.(errs.Error); ok {
		return typed.Code
	}
	return ""
}
