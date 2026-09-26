package transaction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

type ExecuteCommand struct {
	AccountID   string
	Type        Type
	Amount      Money
	Currency    string
	Description string
}

// ListQuery selects a page of transactions. Zero Page and Limit mean the defaults.
type ListQuery struct {
	AccountID string
	Status    Status
	Type      Type
	Page      int
	Limit     int
}

type Page struct {
	Items   []Transaction
	Page    int
	Limit   int
	HasMore bool
}

type Service struct {
	repo     Repository
	provider Provider
	rules    []Rule
	logger   *slog.Logger
	now      func() time.Time
	newID    func() (uuid.UUID, error)
}

type Option func(*Service)

func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

func WithIDGenerator(newID func() (uuid.UUID, error)) Option {
	return func(s *Service) { s.newID = newID }
}

func NewService(repo Repository, provider Provider, logger *slog.Logger, opts ...Option) *Service {
	s := &Service{
		repo:     repo,
		provider: provider,
		rules:    DefaultRules(),
		logger:   logger,
		now:      time.Now,
		newID:    uuid.NewV7,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Execute validates the command, records it as PENDING, runs it against the
// provider and records the outcome. A provider rejection or failure is not an
// error: it is reflected in the returned transaction's status.
func (s *Service) Execute(ctx context.Context, cmd ExecuteCommand) (Transaction, error) {
	if err := checkRules(s.rules, cmd); err != nil {
		return Transaction{}, err
	}

	id, err := s.newID()
	if err != nil {
		return Transaction{}, fmt.Errorf("generate transaction id: %w", err)
	}
	now := s.timestamp()
	tx := Transaction{
		ID:          id,
		AccountID:   cmd.AccountID,
		Type:        cmd.Type,
		Amount:      cmd.Amount,
		Currency:    cmd.Currency,
		Description: cmd.Description,
		Status:      StatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.Create(ctx, &tx); err != nil {
		return Transaction{}, fmt.Errorf("persist pending transaction: %w", err)
	}

	// Once the transaction is recorded, a client disconnect must not abort the
	// provider call or the final update halfway; the provider client bounds
	// how long this can take.
	ctx = context.WithoutCancel(ctx)

	result, err := s.provider.Execute(ctx, ExecutionRequest{
		IdempotencyKey: tx.ID.String(),
		AccountID:      tx.AccountID,
		Type:           tx.Type,
		Amount:         tx.Amount,
		Currency:       tx.Currency,
	})
	s.applyOutcome(ctx, &tx, result, err)

	if err := s.repo.Update(ctx, &tx); err != nil {
		return Transaction{}, fmt.Errorf("persist transaction outcome: %w", err)
	}
	return tx, nil
}

func (s *Service) applyOutcome(ctx context.Context, tx *Transaction, result ExecutionResult, err error) {
	now := s.timestamp()
	var rejection *RejectionError
	switch {
	case err == nil:
		tx.markExecuted(result, now)
	case errors.As(err, &rejection):
		tx.markRejected(rejection.Code, rejection.Message, now)
	default:
		s.logger.ErrorContext(ctx, "provider execution failed",
			slog.String("transaction_id", tx.ID.String()), slog.Any("error", err))
		tx.markFailed(FailureProviderUnavailable,
			"the provider could not be reached; the transaction was not confirmed", now)
	}
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Transaction, error) {
	return s.repo.FindByID(ctx, id)
}

// List returns one page of transactions, newest first. It asks the repository
// for one extra row to know whether another page exists without a COUNT query.
func (s *Service) List(ctx context.Context, q ListQuery) (Page, error) {
	q, err := normalizeListQuery(q)
	if err != nil {
		return Page{}, err
	}
	items, err := s.repo.List(ctx, Filter{
		AccountID: q.AccountID,
		Status:    q.Status,
		Type:      q.Type,
		Limit:     q.Limit + 1,
		Offset:    (q.Page - 1) * q.Limit,
	})
	if err != nil {
		return Page{}, fmt.Errorf("list transactions: %w", err)
	}
	hasMore := len(items) > q.Limit
	if hasMore {
		items = items[:q.Limit]
	}
	return Page{Items: items, Page: q.Page, Limit: q.Limit, HasMore: hasMore}, nil
}

func normalizeListQuery(q ListQuery) (ListQuery, error) {
	if q.Page == 0 {
		q.Page = 1
	}
	if q.Limit == 0 {
		q.Limit = DefaultPageSize
	}

	var violations []Violation
	if q.Status != "" && !q.Status.Valid() {
		violations = append(violations, Violation{Field: "status", Code: "UNSUPPORTED_STATUS",
			Message: "status must be one of PENDING, EXECUTED, REJECTED, FAILED"})
	}
	if q.Type != "" && !q.Type.Valid() {
		violations = append(violations, Violation{Field: "type", Code: "UNSUPPORTED_TYPE",
			Message: "type must be CREDIT or DEBIT"})
	}
	if q.Page < 1 {
		violations = append(violations, Violation{Field: "page", Code: "OUT_OF_RANGE",
			Message: "page must be 1 or greater"})
	}
	if q.Limit < 1 || q.Limit > MaxPageSize {
		violations = append(violations, Violation{Field: "limit", Code: "OUT_OF_RANGE",
			Message: fmt.Sprintf("limit must be between 1 and %d", MaxPageSize)})
	}
	if len(violations) > 0 {
		return q, &ValidationError{Violations: violations}
	}
	return q, nil
}

// timestamp is truncated to microseconds, the precision PostgreSQL stores, so
// the response to a POST matches what a later GET returns.
func (s *Service) timestamp() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}
