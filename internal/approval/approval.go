package approval

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"grin/internal/errs"
	"grin/internal/events"
	"grin/internal/policy"
)

type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

type ApprovalRequest struct {
	ID              string    `json:"id"`
	ToolCallID      string    `json:"tool_call_id"`
	Workspace       string    `json:"workspace,omitempty"`
	OperationDigest string    `json:"operation_digest"`
	Capability      string    `json:"capability"`
	Summary         string    `json:"summary"`
	Detail          string    `json:"detail,omitempty"`
	Risk            RiskLevel `json:"risk"`
	CreatedAt       time.Time `json:"created_at"`
}

type ApprovalDecision struct {
	ApprovalRequestID string                 `json:"approval_request_id"`
	OperationDigest   string                 `json:"operation_digest"`
	Outcome           events.ApprovalOutcome `json:"outcome"`
	ResolvedAt        time.Time              `json:"resolved_at"`
}

type pendingRequest struct {
	request ApprovalRequest
	done    chan approvalResult
}

type approvalResult struct {
	decision ApprovalDecision
	err      error
}

type Manager struct {
	publisher events.ReliablePublisher
	timeout   time.Duration
	now       func() time.Time

	mu      sync.Mutex
	pending map[string]*pendingRequest
}

const maxPendingRequests = 1024

func New(publisher events.ReliablePublisher, timeout time.Duration) *Manager {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Manager{publisher: publisher, timeout: timeout, now: time.Now, pending: make(map[string]*pendingRequest)}
}

// OperationDigest returns the SHA-256 digest of the normalized operation.
// JSON decoding removes insignificant whitespace and encoding/json sorts map
// keys, giving equivalent wire arguments the same operation identity.
func OperationDigest(operation policy.Operation) (string, error) {
	if strings.TrimSpace(operation.Tool) == "" || strings.TrimSpace(operation.Capability) == "" {
		return "", errs.New(errs.ErrInvalidInput, "approval operation tool and capability are required", false)
	}
	arguments, err := normalizeJSON(operation.Arguments)
	if err != nil {
		return "", errs.New(errs.ErrInvalidInput, "approval operation arguments are not JSON-compatible", false)
	}
	canonical, err := json.Marshal(struct {
		Tool       string `json:"tool"`
		Capability string `json:"capability"`
		Arguments  any    `json:"arguments"`
	}{operation.Tool, operation.Capability, arguments})
	if err != nil {
		return "", errs.New(errs.ErrInvalidInput, "approval operation could not be normalized", false)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func NewRequest(operation policy.Operation, capability, summary, detail string, risk RiskLevel) (ApprovalRequest, error) {
	digest, err := OperationDigest(operation)
	if err != nil {
		return ApprovalRequest{}, err
	}
	id, err := newID()
	if err != nil {
		return ApprovalRequest{}, errs.New(errs.ErrInternal, "could not create approval request ID", false)
	}
	if strings.TrimSpace(capability) == "" {
		capability = operation.Capability
	}
	return ApprovalRequest{ID: id, OperationDigest: digest, Capability: capability, Summary: summary, Detail: detail, Risk: risk, CreatedAt: time.Now().UTC()}, nil
}

// Request publishes one approval request and waits for its one-time result.
func (m *Manager) Request(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
	if m == nil || m.publisher == nil {
		return ApprovalDecision{}, errs.New(errs.ErrApprovalUnavailable, "approval delivery is unavailable", false)
	}
	if ctx == nil {
		return ApprovalDecision{}, errs.New(errs.ErrInvalidInput, "approval context is required", false)
	}
	if err := validateRequest(request); err != nil {
		return ApprovalDecision{}, err
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = m.now().UTC()
	}
	pending := &pendingRequest{request: request, done: make(chan approvalResult, 1)}
	m.mu.Lock()
	if len(m.pending) >= maxPendingRequests {
		m.mu.Unlock()
		return ApprovalDecision{}, errs.New(errs.ErrRequestLimit, "too many pending approval requests", true)
	}
	if _, exists := m.pending[request.ID]; exists {
		m.mu.Unlock()
		return ApprovalDecision{}, errs.New(errs.ErrApprovalReplay, "approval request ID is already pending", false)
	}
	m.pending[request.ID] = pending
	m.mu.Unlock()

	if err := m.publisher.PublishReliable(ctx, approvalEvent(request, events.EventApprovalRequired, events.StatusPending)); err != nil {
		m.removePending(request.ID, pending)
		if ctx.Err() != nil {
			return ApprovalDecision{}, errs.New(errs.ErrCancelled, "approval request was cancelled before delivery", true)
		}
		return ApprovalDecision{}, errs.New(errs.ErrApprovalUnavailable, "approval request could not reach the TUI", false)
	}

	timer := time.NewTimer(m.timeout)
	defer timer.Stop()
	select {
	case result := <-pending.done:
		return result.decision, result.err
	case <-ctx.Done():
		if settled, _ := m.settle(request.ID, request.OperationDigest, events.ApprovalCancelled, errs.New(errs.ErrApprovalCancelled, "approval request was cancelled", true)); settled {
			return ApprovalDecision{}, errs.New(errs.ErrApprovalCancelled, "approval request was cancelled", true)
		}
		result := <-pending.done
		return result.decision, result.err
	case <-timer.C:
		if settled, _ := m.settle(request.ID, request.OperationDigest, events.ApprovalTimedOut, errs.New(errs.ErrApprovalTimeout, "approval request timed out", true)); settled {
			return ApprovalDecision{}, errs.New(errs.ErrApprovalTimeout, "approval request timed out", true)
		}
		result := <-pending.done
		return result.decision, result.err
	}
}

func (m *Manager) Resolve(decision ApprovalDecision) error {
	if m == nil || m.publisher == nil {
		return errs.New(errs.ErrApprovalUnavailable, "approval delivery is unavailable", false)
	}
	if strings.TrimSpace(decision.ApprovalRequestID) == "" || strings.TrimSpace(decision.OperationDigest) == "" {
		return errs.New(errs.ErrInvalidInput, "approval request ID and operation digest are required", false)
	}
	if decision.Outcome != events.ApprovalAllowed && decision.Outcome != events.ApprovalRejected {
		return errs.New(errs.ErrInvalidInput, "approval resolution outcome must be allowed or rejected", false)
	}
	resultErr := error(nil)
	if decision.Outcome == events.ApprovalRejected {
		resultErr = errs.New(errs.ErrApprovalRejected, "approval was rejected", false)
	}
	if settled, err := m.settle(decision.ApprovalRequestID, decision.OperationDigest, decision.Outcome, resultErr); !settled {
		return err
	}
	return nil
}

func (m *Manager) Cancel(approvalRequestID, operationDigest string) error {
	if m == nil || m.publisher == nil {
		return errs.New(errs.ErrApprovalUnavailable, "approval delivery is unavailable", false)
	}
	if strings.TrimSpace(approvalRequestID) == "" || strings.TrimSpace(operationDigest) == "" {
		return errs.New(errs.ErrInvalidInput, "approval request ID and operation digest are required", false)
	}
	if settled, err := m.settle(approvalRequestID, operationDigest, events.ApprovalCancelled, errs.New(errs.ErrApprovalCancelled, "approval request was cancelled", true)); !settled {
		return err
	}
	return nil
}

func (m *Manager) Pending() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

func (m *Manager) settle(id, digest string, outcome events.ApprovalOutcome, resultErr error) (bool, error) {
	m.mu.Lock()
	pending, ok := m.pending[id]
	if !ok {
		m.mu.Unlock()
		return false, errs.New(errs.ErrApprovalReplay, "approval request is missing or already resolved", false)
	}
	if subtle.ConstantTimeCompare([]byte(pending.request.OperationDigest), []byte(digest)) != 1 {
		m.mu.Unlock()
		return false, errs.New(errs.ErrApprovalMismatch, "approval operation digest does not match", false)
	}
	delete(m.pending, id)
	m.mu.Unlock()

	resolvedAt := m.now().UTC()
	decision := ApprovalDecision{ApprovalRequestID: id, OperationDigest: pending.request.OperationDigest, Outcome: outcome, ResolvedAt: resolvedAt}
	if deliveryErr := m.publishResolution(pending.request, decision); deliveryErr != nil && outcome == events.ApprovalAllowed {
		resultErr = errs.New(errs.ErrApprovalUnavailable, "approval resolution could not be delivered", false)
	}
	pending.done <- approvalResult{decision: decision, err: resultErr}
	return true, nil
}

func (m *Manager) removePending(id string, expected *pendingRequest) {
	m.mu.Lock()
	if pending, ok := m.pending[id]; ok && pending == expected {
		delete(m.pending, id)
	}
	m.mu.Unlock()
}

func (m *Manager) publishResolution(request ApprovalRequest, decision ApprovalDecision) error {
	status := events.StatusFailed
	if decision.Outcome == events.ApprovalCancelled {
		status = events.StatusCancelled
	}
	if decision.Outcome == events.ApprovalAllowed {
		status = events.StatusComplete
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	return m.publisher.PublishReliable(ctx, approvalEvent(request, events.EventApprovalResolved, status, decision))
}

func approvalEvent(request ApprovalRequest, eventType events.EventType, status events.Status, decisions ...ApprovalDecision) events.Event {
	event := events.Event{ToolCallID: request.ToolCallID, Workspace: request.Workspace, ApprovalRequestID: request.ID, OperationDigest: request.OperationDigest, Type: eventType, Tool: request.Capability, Summary: request.Summary, Detail: request.Detail, Risk: string(request.Risk), Status: status, Time: request.CreatedAt}
	if len(decisions) > 0 {
		event.Time = decisions[0].ResolvedAt
		event.ApprovalOutcome = decisions[0].Outcome
	}
	return event
}

func validateRequest(request ApprovalRequest) error {
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.OperationDigest) == "" || strings.TrimSpace(request.Capability) == "" {
		return errs.New(errs.ErrInvalidInput, "approval request ID, digest, and capability are required", false)
	}
	if request.CreatedAt.IsZero() {
		return nil
	}
	if request.CreatedAt.After(time.Now().Add(time.Minute)) {
		return errs.New(errs.ErrInvalidInput, "approval request timestamp is invalid", false)
	}
	return nil
}

func normalizeJSON(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("multiple JSON values")
	}
	return normalized, nil
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate approval ID: %w", err)
	}
	return "approval-" + hex.EncodeToString(raw[:]), nil
}
