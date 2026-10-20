package events

import (
	"context"
	"testing"
	"time"
)

func TestBusBoundsTelemetryButDeliversApprovalReliably(t *testing.T) {
	bus := New(1)
	telemetry := bus.Subscribe()
	defer telemetry.Close()
	reliable := bus.SubscribeReliable()
	defer reliable.Close()

	bus.Publish(Event{Tool: "first", Type: EventRequestStarted})
	bus.Publish(Event{Tool: "second", Type: EventToolCompleted})
	if got := len(bus.Snapshot()); got != 1 {
		t.Fatalf("snapshot length = %d, want 1", got)
	}
	if bus.Dropped() == 0 {
		t.Fatal("expected a bounded telemetry drop")
	}

	event := Event{ID: "approval-1", Type: EventApprovalRequired, Status: StatusPending, Time: time.Now()}
	if err := bus.PublishReliable(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-reliable.Events:
		if got.ID != event.ID {
			t.Fatalf("reliable event = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reliable event")
	}
}

func TestCloseReliableSubscriptionWhilePublishIsBlocked(t *testing.T) {
	bus := New(1)
	subscription := bus.SubscribeReliable()
	defer subscription.Close()

	if err := bus.PublishReliable(context.Background(), Event{Type: EventApprovalRequired}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- bus.PublishReliable(context.Background(), Event{Type: EventApprovalResolved})
	}()

	deadline := time.After(2 * time.Second)
	for {
		bus.mu.Lock()
		pending := 0
		for _, subscriber := range bus.reliable {
			pending += subscriber.pending
		}
		bus.mu.Unlock()
		if pending != 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("publisher finished before subscription close: %v", err)
		case <-deadline:
			t.Fatal("publisher did not reach blocked send")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	subscription.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("publisher returned success despite a closed reliable subscriber")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing reliable subscriber did not unblock publisher")
	}
	select {
	case _, ok := <-subscription.Events:
		if !ok {
			t.Fatal("previously delivered approval was lost")
		}
	case <-time.After(time.Second):
		t.Fatal("previously delivered approval was unavailable")
	}
	select {
	case _, ok := <-subscription.Events:
		if ok {
			t.Fatal("subscription received another event after close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reliable event channel was not closed")
	}
}

func TestCloseReliableSubscriptionNeverAcknowledgesLateBufferedSend(t *testing.T) {
	for attempt := 0; attempt < 64; attempt++ {
		bus := New(1)
		subscription := bus.SubscribeReliable()
		if err := bus.PublishReliable(context.Background(), Event{Type: EventApprovalRequired}); err != nil {
			t.Fatal(err)
		}

		done := make(chan error, 1)
		go func() {
			done <- bus.PublishReliable(context.Background(), Event{Type: EventApprovalResolved})
		}()

		deadline := time.After(2 * time.Second)
		for {
			bus.mu.Lock()
			pending := 0
			for _, subscriber := range bus.reliable {
				pending += subscriber.pending
			}
			bus.mu.Unlock()
			if pending != 0 {
				break
			}
			select {
			case err := <-done:
				t.Fatalf("attempt %d: publish unexpectedly completed early: %v", attempt, err)
			case <-deadline:
				t.Fatalf("attempt %d: publish did not reach blocked send", attempt)
			default:
				time.Sleep(time.Millisecond)
			}
		}

		subscription.Close()
		// Make the buffer writable while the subscriber's closed signal is
		// already ready. Either select branch must report failed delivery.
		select {
		case <-subscription.Events:
		case <-time.After(time.Second):
			t.Fatalf("attempt %d: previously delivered event is missing", attempt)
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("attempt %d: closed receiver was acknowledged", attempt)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("attempt %d: publisher blocked after close", attempt)
		}
		// A concurrent send may have entered the buffer before the sender
		// observed close, but the receiver must eventually close cleanly.
		drained := make(chan struct{})
		go func() {
			for range subscription.Events {
			}
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(time.Second):
			t.Fatalf("attempt %d: receiver channel was not closed", attempt)
		}
	}
}

func TestReliableApprovalUsesOneIDBeforeFanOut(t *testing.T) {
	bus := New(4)
	telemetry := bus.Subscribe()
	defer telemetry.Close()
	reliable := bus.SubscribeReliable()
	defer reliable.Close()

	if err := bus.PublishReliable(context.Background(), Event{Type: EventApprovalRequired, Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	var telemetryEvent, reliableEvent Event
	select {
	case telemetryEvent = <-telemetry.Events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for telemetry event")
	}
	select {
	case reliableEvent = <-reliable.Events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reliable event")
	}
	if telemetryEvent.ID == "" || telemetryEvent.ID != reliableEvent.ID {
		t.Fatalf("fan-out IDs differ: telemetry=%q reliable=%q", telemetryEvent.ID, reliableEvent.ID)
	}
}
