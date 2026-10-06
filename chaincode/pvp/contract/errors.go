package contract

import (
	"errors"
	"fmt"
)

// Error codes. Every rejection the chaincode can make has its own code, so a
// client (and a judge) can see exactly which defence refused a transaction.
// The peer returns Error() verbatim; the gateway parses the code prefix.
const (
	// Input validation.
	ErrInvalidInput   = "ERR_INVALID_INPUT"
	ErrInvalidAmount  = "ERR_INVALID_AMOUNT"  // negative, zero, malformed
	ErrAmountOverflow = "ERR_AMOUNT_OVERFLOW" // above the per-amount cap or int64

	// Identity and authorisation.
	ErrUnauthorized      = "ERR_UNAUTHORIZED"       // submitter is not a bank
	ErrForgedInstruction = "ERR_FORGED_INSTRUCTION" // bank A instructing as bank B

	// Trade lifecycle / idempotency.
	ErrDuplicateInstruction = "ERR_DUPLICATE_INSTRUCTION"
	ErrInstructionMismatch  = "ERR_INSTRUCTION_MISMATCH"
	ErrUnilateral           = "ERR_UNILATERAL"      // settle with one side's instruction only
	ErrAlreadySettled       = "ERR_ALREADY_SETTLED" // double-settle
	ErrAlreadyMatched       = "ERR_ALREADY_MATCHED" // withdrawing matched trade
	ErrReplay               = "ERR_REPLAY"          // re-instructing a settled trade
	ErrTradeNotFound        = "ERR_TRADE_NOT_FOUND"
	ErrInsufficientFunds    = "ERR_INSUFFICIENT_FUNDS"
	ErrGridlock             = "ERR_GRIDLOCK"

	// FX oracle attestation.
	ErrAttestationUnsigned     = "ERR_ATTESTATION_UNSIGNED"
	ErrAttestationBadSignature = "ERR_ATTESTATION_BAD_SIGNATURE" // tampered, or signed by a non-pinned key
	ErrAttestationStale        = "ERR_ATTESTATION_STALE"
	ErrAttestationUnknown      = "ERR_ATTESTATION_UNKNOWN"
	ErrRateMismatch            = "ERR_RATE_MISMATCH" // amounts not consistent with the attested rate

	// Netting.
	ErrBatch = "ERR_BATCH"

	// Ledger integrity.
	ErrInvariantViolation = "ERR_INVARIANT_VIOLATION"
	ErrAlreadyInitialized = "ERR_ALREADY_INITIALIZED"
	ErrNotInitialized     = "ERR_NOT_INITIALIZED"
	ErrInternal           = "ERR_INTERNAL"
)

// CodedError is a rejection with a stable machine-readable code.
type CodedError struct {
	Code string
	Msg  string
}

func (e *CodedError) Error() string { return e.Code + ": " + e.Msg }

func reject(code, format string, args ...any) error {
	return &CodedError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// CodeOf returns the rejection code of err, or "" if err is not a CodedError.
func CodeOf(err error) string {
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}
