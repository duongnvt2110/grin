package events

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

type EventType string

const (
	EventRequestStarted   EventType = "request_started"
	EventPolicyDecision   EventType = "policy_decision"
	EventApprovalRequired EventType = "approval_required"
	EventApprovalResolved EventType = "approval_resolved"
	EventToolCompleted    EventType = "tool_completed"
	EventToolFailed       EventType = "tool_failed"
	EventCancelled        EventType = "cancelled"
)

// Type and the older names remain aliases for the foundation contract.
type Type = EventType

const (
	ToolStarted     = EventRequestStarted
	ToolCompleted   = EventToolCompleted
	ToolFailed      = EventToolFailed
	ApprovalNeeded  = EventApprovalRequired
	OperationCancel = EventCancelled
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusComplete  Status = "complete"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type ApprovalOutcome string

const (
	ApprovalAllowed   ApprovalOutcome = "allowed"
	ApprovalRejected  ApprovalOutcome = "rejected"
	ApprovalTimedOut  ApprovalOutcome = "timed_out"
	ApprovalCancelled ApprovalOutcome = "cancelled"
)

type Event struct {
	ID                string          `json:"id"`
	ToolCallID        string          `json:"tool_call_id,omitempty"`
	ApprovalRequestID string          `json:"approval_request_id,omitempty"`
	OperationDigest   string          `json:"operation_digest,omitempty"`
	ApprovalOutcome   ApprovalOutcome `json:"approval_outcome,omitempty"`
	Workspace         string          `json:"workspace,omitempty"`
	Risk              string          `json:"risk,omitempty"`
	Type              EventType       `json:"type"`
	Tool              string          `json:"tool,omitempty"`
	Summary           string          `json:"summary,omitempty"`
	Detail            string          `json:"detail,omitempty"`
	Status            Status          `json:"status,omitempty"`
	Time              time.Time       `json:"time"`
	At                time.Time       `json:"-"`
}

type Publisher interface {
	Publish(Event)
}

type ReliablePublisher interface {
	PublishReliable(context.Context, Event) error
}

type Subscription struct {
	Events <-chan Event
	close  func()
}

func (s Subscription) Close() { s.close() }

type Bus struct {
	mu        sync.Mutex
	capacity  int
	entries   []Event
	nextID    uint64
	dropped   uint64
	telemetry map[uint64]chan Event
	reliable  map[uint64]chan Event
	nextSubID uint64
}

func New(capacity int) *Bus {
	if capacity < 1 {
		capacity = 1
	}
	return &Bus{capacity: capacity, telemetry: map[uint64]chan Event{}, reliable: map[uint64]chan Event{}}
}

func (b *Bus) Publish(event Event) {
	b.mu.Lock()
	event = b.prepareLocked(event)
	b.publishLocked(event)
	b.mu.Unlock()
}

func (b *Bus) PublishReliable(ctx context.Context, event Event) error {
	if event.Type != EventApprovalRequired && event.Type != EventApprovalResolved {
		return errors.New("reliable delivery is reserved for approval events")
	}
	b.mu.Lock()
	// Assign the ID once, then use that same event for telemetry and reliable
	// delivery. The reliable channel remains the control path; telemetry loss
	// cannot drop the approval.
	event = b.prepareLocked(event)
	b.publishLocked(event)
	subscribers := make([]chan Event, 0, len(b.reliable))
	for _, subscriber := range b.reliable {
		subscribers = append(subscribers, subscriber)
	}
	b.mu.Unlock()
	if len(subscribers) == 0 {
		return errors.New("reliable approval delivery is unavailable")
	}
	for _, subscriber := range subscribers {
		select {
		case subscriber <- event:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (b *Bus) prepareLocked(event Event) Event {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	if event.At.IsZero() {
		event.At = event.Time
	}
	if event.ID == "" {
		b.nextID++
		event.ID = "event-" + strconv.FormatUint(b.nextID, 10)
	}
	return event
}

func (b *Bus) publishLocked(event Event) {
	if len(b.entries) == b.capacity {
		copy(b.entries, b.entries[1:])
		b.entries[len(b.entries)-1] = event
		b.dropped++
	} else {
		b.entries = append(b.entries, event)
	}
	for _, subscriber := range b.telemetry {
		select {
		case subscriber <- event:
		default:
			b.dropped++
		}
	}
}

func (b *Bus) Subscribe() Subscription { return b.subscribe(b.telemetry, false) }

func (b *Bus) SubscribeReliable() Subscription { return b.subscribe(b.reliable, true) }

func (b *Bus) subscribe(registry map[uint64]chan Event, reliable bool) Subscription {
	b.mu.Lock()
	b.nextSubID++
	id := b.nextSubID
	buffer := b.capacity
	if reliable {
		buffer = 1
	}
	channel := make(chan Event, buffer)
	registry[id] = channel
	b.mu.Unlock()
	return Subscription{Events: channel, close: func() {
		b.mu.Lock()
		if existing, ok := registry[id]; ok {
			delete(registry, id)
			close(existing)
		}
		b.mu.Unlock()
	}}
}

func (b *Bus) Snapshot() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]Event, len(b.entries))
	copy(result, b.entries)
	return result
}

func (b *Bus) Dropped() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}
