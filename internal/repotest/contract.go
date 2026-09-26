// Package repotest holds a behavioural contract that every
// transaction.Repository implementation must satisfy, so the in-memory
// repository used in unit tests cannot drift from PostgreSQL.
package repotest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

// Run executes the contract. newRepo must return an empty repository.
func Run(t *testing.T, newRepo func(t *testing.T) transaction.Repository) {
	t.Run("create then find returns the same transaction", func(t *testing.T) {
		repo := newRepo(t)
		want := pending(t, "acc-1", transaction.Credit, base)

		mustCreate(t, repo, &want)
		got, err := repo.FindByID(context.Background(), want.ID)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		assertEqual(t, got, want)
	})

	t.Run("update records the provider outcome", func(t *testing.T) {
		repo := newRepo(t)
		tx := pending(t, "acc-1", transaction.Debit, base)
		mustCreate(t, repo, &tx)

		balance := transaction.Money(4000_00)
		tx.Status = transaction.StatusExecuted
		tx.ProviderTransactionID = "txn-789"
		tx.BalanceAfter = &balance
		tx.UpdatedAt = base.Add(time.Second)
		if err := repo.Update(context.Background(), &tx); err != nil {
			t.Fatalf("Update: %v", err)
		}

		got, err := repo.FindByID(context.Background(), tx.ID)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		assertEqual(t, got, tx)
	})

	t.Run("update records a rejection", func(t *testing.T) {
		repo := newRepo(t)
		tx := pending(t, "acc-1", transaction.Debit, base)
		mustCreate(t, repo, &tx)

		tx.Status = transaction.StatusRejected
		tx.FailureCode = "INSUFFICIENT_FUNDS"
		tx.FailureMessage = "not enough balance"
		tx.UpdatedAt = base.Add(time.Second)
		if err := repo.Update(context.Background(), &tx); err != nil {
			t.Fatalf("Update: %v", err)
		}

		got, _ := repo.FindByID(context.Background(), tx.ID)
		assertEqual(t, got, tx)
	})

	t.Run("missing transactions return ErrNotFound", func(t *testing.T) {
		repo := newRepo(t)
		tx := pending(t, "acc-1", transaction.Credit, base)

		if _, err := repo.FindByID(context.Background(), tx.ID); !errors.Is(err, transaction.ErrNotFound) {
			t.Errorf("FindByID error = %v, want ErrNotFound", err)
		}
		if err := repo.Update(context.Background(), &tx); !errors.Is(err, transaction.ErrNotFound) {
			t.Errorf("Update error = %v, want ErrNotFound", err)
		}
	})

	t.Run("list filters, orders newest first and paginates", func(t *testing.T) {
		repo := newRepo(t)
		var created []transaction.Transaction
		for i, spec := range []struct {
			account string
			typ     transaction.Type
			status  transaction.Status
		}{
			{"acc-1", transaction.Credit, transaction.StatusExecuted},
			{"acc-2", transaction.Debit, transaction.StatusRejected},
			{"acc-1", transaction.Debit, transaction.StatusExecuted},
			{"acc-1", transaction.Credit, transaction.StatusFailed},
			{"acc-2", transaction.Credit, transaction.StatusExecuted},
		} {
			tx := pending(t, spec.account, spec.typ, base.Add(time.Duration(i)*time.Minute))
			tx.Status = spec.status
			mustCreate(t, repo, &tx)
			created = append(created, tx)
		}

		tests := []struct {
			name   string
			filter transaction.Filter
			want   []int
		}{
			{"everything, newest first", transaction.Filter{Limit: 10}, []int{4, 3, 2, 1, 0}},
			{"by account", transaction.Filter{AccountID: "acc-1", Limit: 10}, []int{3, 2, 0}},
			{"by status", transaction.Filter{Status: transaction.StatusExecuted, Limit: 10}, []int{4, 2, 0}},
			{"by type", transaction.Filter{Type: transaction.Debit, Limit: 10}, []int{2, 1}},
			{"combined", transaction.Filter{AccountID: "acc-1", Type: transaction.Credit, Status: transaction.StatusFailed, Limit: 10}, []int{3}},
			{"limit and offset", transaction.Filter{Limit: 2, Offset: 1}, []int{3, 2}},
			{"offset past the end", transaction.Filter{Limit: 2, Offset: 10}, nil},
			{"no match", transaction.Filter{AccountID: "acc-unknown", Limit: 10}, nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := repo.List(context.Background(), tt.filter)
				if err != nil {
					t.Fatalf("List: %v", err)
				}
				if len(got) != len(tt.want) {
					t.Fatalf("got %d transactions, want %d", len(got), len(tt.want))
				}
				for i, idx := range tt.want {
					if got[i].ID != created[idx].ID {
						t.Errorf("position %d: got %s, want transaction #%d", i, got[i].ID, idx)
					}
				}
			})
		}
	})
}

var base = time.Date(2025, 3, 15, 10, 30, 0, 123456000, time.UTC)

func pending(t *testing.T, account string, typ transaction.Type, at time.Time) transaction.Transaction {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return transaction.Transaction{
		ID:          id,
		AccountID:   account,
		Type:        typ,
		Amount:      1500_00,
		Currency:    transaction.CurrencyMXN,
		Description: "Transferencia recibida",
		Status:      transaction.StatusPending,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
}

func mustCreate(t *testing.T, repo transaction.Repository, tx *transaction.Transaction) {
	t.Helper()
	if err := repo.Create(context.Background(), tx); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func assertEqual(t *testing.T, got, want transaction.Transaction) {
	t.Helper()
	gotBalance, wantBalance := got.BalanceAfter, want.BalanceAfter
	got.BalanceAfter, want.BalanceAfter = nil, nil
	if (gotBalance == nil) != (wantBalance == nil) || (gotBalance != nil && *gotBalance != *wantBalance) {
		t.Errorf("balanceAfter = %v, want %v", gotBalance, wantBalance)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("timestamps = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
	got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}
