package transaction_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"gitlab.com/decogzz/spin-coding-challenge/internal/memory"
	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

type fakeProvider struct {
	calls   []transaction.ExecutionRequest
	execute func(ctx context.Context, req transaction.ExecutionRequest) (transaction.ExecutionResult, error)
}

func (f *fakeProvider) Execute(ctx context.Context, req transaction.ExecutionRequest) (transaction.ExecutionResult, error) {
	f.calls = append(f.calls, req)
	return f.execute(ctx, req)
}

func approving(balance transaction.Money) *fakeProvider {
	return &fakeProvider{execute: func(context.Context, transaction.ExecutionRequest) (transaction.ExecutionResult, error) {
		return transaction.ExecutionResult{ProviderTransactionID: "txn-789", Balance: balance}, nil
	}}
}

func failingWith(err error) *fakeProvider {
	return &fakeProvider{execute: func(context.Context, transaction.ExecutionRequest) (transaction.ExecutionResult, error) {
		return transaction.ExecutionResult{}, err
	}}
}

var fixedNow = time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)

func newService(repo transaction.Repository, provider transaction.Provider) *transaction.Service {
	return transaction.NewService(repo, provider, slog.New(slog.NewTextHandler(io.Discard, nil)),
		transaction.WithClock(func() time.Time { return fixedNow }))
}

func creditCommand() transaction.ExecuteCommand {
	return transaction.ExecuteCommand{
		AccountID:   "acc-123456",
		Type:        transaction.Credit,
		Amount:      1500_00,
		Currency:    "MXN",
		Description: "Transferencia recibida",
	}
}

func TestExecuteApproved(t *testing.T) {
	repo := memory.NewRepository()
	provider := approving(5500_00)
	svc := newService(repo, provider)

	tx, err := svc.Execute(context.Background(), creditCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if tx.Status != transaction.StatusExecuted {
		t.Errorf("status = %s, want EXECUTED", tx.Status)
	}
	if tx.ProviderTransactionID != "txn-789" {
		t.Errorf("providerTransactionId = %q, want txn-789", tx.ProviderTransactionID)
	}
	if tx.BalanceAfter == nil || *tx.BalanceAfter != 5500_00 {
		t.Errorf("balanceAfter = %v, want 5500.00", tx.BalanceAfter)
	}
	if !tx.CreatedAt.Equal(fixedNow) {
		t.Errorf("createdAt = %v, want %v", tx.CreatedAt, fixedNow)
	}

	if len(provider.calls) != 1 {
		t.Fatalf("provider called %d times, want 1", len(provider.calls))
	}
	call := provider.calls[0]
	if call.IdempotencyKey != tx.ID.String() {
		t.Errorf("idempotency key = %q, want transaction id %q", call.IdempotencyKey, tx.ID)
	}
	if call.AccountID != "acc-123456" || call.Type != transaction.Credit || call.Amount != 1500_00 || call.Currency != "MXN" {
		t.Errorf("unexpected provider request: %+v", call)
	}

	stored, err := repo.FindByID(context.Background(), tx.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.Status != transaction.StatusExecuted || *stored.BalanceAfter != 5500_00 {
		t.Errorf("stored transaction = %+v, want executed with balance", stored)
	}
}

func TestExecuteRejectedByProvider(t *testing.T) {
	repo := memory.NewRepository()
	svc := newService(repo, failingWith(&transaction.RejectionError{
		Code:    "INSUFFICIENT_FUNDS",
		Message: "The account does not have enough balance to complete the transaction",
	}))

	tx, err := svc.Execute(context.Background(), creditCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if tx.Status != transaction.StatusRejected || tx.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Errorf("got status %s code %s, want REJECTED INSUFFICIENT_FUNDS", tx.Status, tx.FailureCode)
	}
	if tx.BalanceAfter != nil {
		t.Errorf("balanceAfter = %v, want nil", *tx.BalanceAfter)
	}
	stored, _ := repo.FindByID(context.Background(), tx.ID)
	if stored.Status != transaction.StatusRejected {
		t.Errorf("stored status = %s, want REJECTED", stored.Status)
	}
}

func TestExecuteProviderUnavailable(t *testing.T) {
	repo := memory.NewRepository()
	svc := newService(repo, failingWith(errors.New("dial tcp: connection refused")))

	tx, err := svc.Execute(context.Background(), creditCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if tx.Status != transaction.StatusFailed || tx.FailureCode != transaction.FailureProviderUnavailable {
		t.Errorf("got status %s code %s, want FAILED PROVIDER_UNAVAILABLE", tx.Status, tx.FailureCode)
	}
	stored, _ := repo.FindByID(context.Background(), tx.ID)
	if stored.Status != transaction.StatusFailed {
		t.Errorf("stored status = %s, want FAILED", stored.Status)
	}
}

func TestExecuteRuleViolationNeverReachesProvider(t *testing.T) {
	repo := memory.NewRepository()
	provider := approving(0)
	svc := newService(repo, provider)
	cmd := creditCommand()
	cmd.Type = transaction.Debit
	cmd.Amount = 10_000_01

	_, err := svc.Execute(context.Background(), cmd)

	var verr *transaction.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if len(provider.calls) != 0 {
		t.Errorf("provider called %d times, want 0", len(provider.calls))
	}
	page, _ := svc.List(context.Background(), transaction.ListQuery{})
	if len(page.Items) != 0 {
		t.Errorf("%d transactions persisted, want 0", len(page.Items))
	}
}

type failingCreateRepo struct{ *memory.Repository }

func (failingCreateRepo) Create(context.Context, *transaction.Transaction) error {
	return errors.New("database is down")
}

func TestExecuteDoesNotCallProviderWhenPersistenceFails(t *testing.T) {
	provider := approving(0)
	svc := newService(failingCreateRepo{memory.NewRepository()}, provider)

	if _, err := svc.Execute(context.Background(), creditCommand()); err == nil {
		t.Fatal("expected an error")
	}
	if len(provider.calls) != 0 {
		t.Errorf("provider called %d times, want 0", len(provider.calls))
	}
}

func TestExecuteRecordsOutcomeWhenClientDisconnects(t *testing.T) {
	repo := memory.NewRepository()
	ctx, cancel := context.WithCancel(context.Background())
	provider := &fakeProvider{execute: func(ctx context.Context, _ transaction.ExecutionRequest) (transaction.ExecutionResult, error) {
		cancel()
		if err := ctx.Err(); err != nil {
			return transaction.ExecutionResult{}, err
		}
		return transaction.ExecutionResult{ProviderTransactionID: "txn-1", Balance: 100_00}, nil
	}}
	svc := newService(repo, provider)

	tx, err := svc.Execute(ctx, creditCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if tx.Status != transaction.StatusExecuted {
		t.Errorf("status = %s, want EXECUTED", tx.Status)
	}
}

func TestGetNotFound(t *testing.T) {
	svc := newService(memory.NewRepository(), approving(0))

	_, err := svc.Get(context.Background(), uuid.New())

	if !errors.Is(err, transaction.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestListPaginatesAndFilters(t *testing.T) {
	repo := memory.NewRepository()
	clock := fixedNow
	svc := transaction.NewService(repo, approving(0), slog.New(slog.NewTextHandler(io.Discard, nil)),
		transaction.WithClock(func() time.Time { clock = clock.Add(time.Second); return clock }))

	for i := range 5 {
		cmd := creditCommand()
		if i%2 == 1 {
			cmd.AccountID = "acc-other"
		}
		if _, err := svc.Execute(context.Background(), cmd); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	}

	first, err := svc.List(context.Background(), transaction.ListQuery{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(first.Items) != 2 || !first.HasMore || first.Page != 1 {
		t.Errorf("first page = %d items, hasMore %v, page %d; want 2, true, 1", len(first.Items), first.HasMore, first.Page)
	}
	if !first.Items[0].CreatedAt.After(first.Items[1].CreatedAt) {
		t.Error("items are not ordered newest first")
	}

	last, _ := svc.List(context.Background(), transaction.ListQuery{Page: 3, Limit: 2})
	if len(last.Items) != 1 || last.HasMore {
		t.Errorf("last page = %d items, hasMore %v; want 1, false", len(last.Items), last.HasMore)
	}

	filtered, _ := svc.List(context.Background(), transaction.ListQuery{AccountID: "acc-other"})
	if len(filtered.Items) != 2 || filtered.Limit != transaction.DefaultPageSize {
		t.Errorf("filtered = %d items, limit %d; want 2, %d", len(filtered.Items), filtered.Limit, transaction.DefaultPageSize)
	}
	for _, tx := range filtered.Items {
		if tx.AccountID != "acc-other" {
			t.Errorf("unexpected account %s in filtered results", tx.AccountID)
		}
	}
}

func TestListRejectsInvalidQuery(t *testing.T) {
	svc := newService(memory.NewRepository(), approving(0))

	_, err := svc.List(context.Background(), transaction.ListQuery{Status: "DONE", Type: "REFUND", Page: -1, Limit: 101})

	var verr *transaction.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if len(verr.Violations) != 4 {
		t.Errorf("got %d violations, want 4: %+v", len(verr.Violations), verr.Violations)
	}
}
