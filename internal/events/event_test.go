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
