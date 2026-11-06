// Package sessionowner provides provider-neutral, operation-scoped session custody.
// It contains no provider transport, credential discovery, SDK import or scheduler.
package sessionowner

import "errors"

// Code is a safe, fixed diagnostic code. Untrusted exception text is never a wire error.
type Code string

const (
	ProtocolMismatch       Code = "protocol_mismatch"
	UnsafeStorage          Code = "unsafe_storage"
	StaleGeneration        Code = "stale_generation"
	OwnerBusy              Code = "owner_busy"
	BudgetInsufficient     Code = "budget_insufficient"
	PermissionDenied       Code = "permission_denied"
	StorageFailure         Code = "storage_failure"
	ReconciliationRequired Code = "reconciliation_required"
	UncertainWrite         Code = "uncertain_write"
	Canceled               Code = "canceled"
	PayloadTooLarge        Code = "payload_too_large"
)

type Error struct {
	Code   Code
	Detail string
}

func (e *Error) Error() string         { return string(e.Code) }
func fail(c Code, detail string) error { return &Error{Code: c, Detail: detail} }
func ErrorCode(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return StorageFailure
}

const (
	Protocol        = "sessionowner/1"
	AdapterProtocol = "sessionowner-adapter/1"
	HardFrameLimit  = 64 << 20
	MaxDepth        = 64
	MaxNodes        = 1000000
)
