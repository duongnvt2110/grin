package errs

type ErrorCode string

const (
	ErrInvalidInput        ErrorCode = "invalid_input"
	ErrOutsideWorkspace    ErrorCode = "outside_workspace"
	ErrApprovalRejected    ErrorCode = "approval_rejected"
	ErrApprovalTimeout     ErrorCode = "approval_timeout"
	ErrApprovalCancelled   ErrorCode = "approval_cancelled"
	ErrApprovalUnavailable ErrorCode = "approval_unavailable"
	ErrApprovalReplay      ErrorCode = "approval_replay"
	ErrApprovalMismatch    ErrorCode = "approval_mismatch"
	ErrExecutionTimeout    ErrorCode = "execution_timeout"
	ErrCancelled           ErrorCode = "cancelled"
	ErrOutputLimit         ErrorCode = "output_limit"
	ErrFileExists          ErrorCode = "file_exists"
	ErrRequestLimit        ErrorCode = "request_limit_exceeded"
	ErrToolDisabled        ErrorCode = "tool_disabled"
	ErrUnsupported         ErrorCode = "unsupported"
	ErrNotFound            ErrorCode = "not_found"
	ErrPermissionDenied    ErrorCode = "permission_denied"
	ErrInternal            ErrorCode = "internal_error"
)

type Error struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
	RequestID string    `json:"request_id"`
}

func (e Error) Error() string { return string(e.Code) + ": " + e.Message }

func New(code ErrorCode, message string, retryable bool) Error {
	return Error{Code: code, Message: message, Retryable: retryable}
}
