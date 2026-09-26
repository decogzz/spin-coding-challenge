package transaction

import (
	"time"

	"github.com/google/uuid"
)

type Type string

const (
	Credit Type = "CREDIT"
	Debit  Type = "DEBIT"
)

func (t Type) Valid() bool {
	return t == Credit || t == Debit
}

type Status string

const (
	// StatusPending is persisted before calling the provider, so an interrupted
	// execution still leaves a record that can be reconciled.
	StatusPending  Status = "PENDING"
	StatusExecuted Status = "EXECUTED"
	StatusRejected Status = "REJECTED"
	StatusFailed   Status = "FAILED"
)

func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusExecuted, StatusRejected, StatusFailed:
		return true
	}
	return false
}

const CurrencyMXN = "MXN"

// FailureProviderUnavailable is recorded when the provider could not be reached
// or kept failing, so the outcome of the execution is unconfirmed.
const FailureProviderUnavailable = "PROVIDER_UNAVAILABLE"

type Transaction struct {
	ID                    uuid.UUID
	AccountID             string
	Type                  Type
	Amount                Money
	Currency              string
	Description           string
	Status                Status
	ProviderTransactionID string
	BalanceAfter          *Money
	FailureCode           string
	FailureMessage        string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (t *Transaction) markExecuted(result ExecutionResult, now time.Time) {
	balance := result.Balance
	t.Status = StatusExecuted
	t.ProviderTransactionID = result.ProviderTransactionID
	t.BalanceAfter = &balance
	t.UpdatedAt = now
}

func (t *Transaction) markRejected(code, message string, now time.Time) {
	t.Status = StatusRejected
	t.FailureCode = code
	t.FailureMessage = message
	t.UpdatedAt = now
}

func (t *Transaction) markFailed(code, message string, now time.Time) {
	t.Status = StatusFailed
	t.FailureCode = code
	t.FailureMessage = message
	t.UpdatedAt = now
}
