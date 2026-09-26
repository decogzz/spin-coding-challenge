package transaction

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Repository persists transactions. Update must return ErrNotFound when the
// transaction does not exist, and FindByID must do the same.
type Repository interface {
	Create(ctx context.Context, tx *Transaction) error
	Update(ctx context.Context, tx *Transaction) error
	FindByID(ctx context.Context, id uuid.UUID) (Transaction, error)
	List(ctx context.Context, filter Filter) ([]Transaction, error)
}

// Filter selects transactions ordered from newest to oldest. Empty fields match everything.
type Filter struct {
	AccountID string
	Status    Status
	Type      Type
	Limit     int
	Offset    int
}

// Provider executes a transaction against the external system that owns
// balances and account locks. A business refusal must be returned as a
// *RejectionError; any other error means the outcome could not be confirmed.
type Provider interface {
	Execute(ctx context.Context, req ExecutionRequest) (ExecutionResult, error)
}

type ExecutionRequest struct {
	// IdempotencyKey lets the provider deduplicate retries of the same transaction.
	IdempotencyKey string
	AccountID      string
	Type           Type
	Amount         Money
	Currency       string
}

type ExecutionResult struct {
	ProviderTransactionID string
	Balance               Money
	ExecutedAt            time.Time
}
