// Package memory is an in-process transaction.Repository for tests and local experiments.
package memory

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/google/uuid"

	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

var ErrAlreadyExists = errors.New("transaction already exists")

type Repository struct {
	mu    sync.RWMutex
	items map[uuid.UUID]transaction.Transaction
}

func NewRepository() *Repository {
	return &Repository{items: make(map[uuid.UUID]transaction.Transaction)}
}

func (r *Repository) Create(_ context.Context, tx *transaction.Transaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[tx.ID]; ok {
		return ErrAlreadyExists
	}
	r.items[tx.ID] = clone(*tx)
	return nil
}

func (r *Repository) Update(_ context.Context, tx *transaction.Transaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[tx.ID]; !ok {
		return transaction.ErrNotFound
	}
	r.items[tx.ID] = clone(*tx)
	return nil
}

func (r *Repository) FindByID(_ context.Context, id uuid.UUID) (transaction.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tx, ok := r.items[id]
	if !ok {
		return transaction.Transaction{}, transaction.ErrNotFound
	}
	return clone(tx), nil
}

func (r *Repository) List(_ context.Context, f transaction.Filter) ([]transaction.Transaction, error) {
	r.mu.RLock()
	matches := make([]transaction.Transaction, 0, len(r.items))
	for _, tx := range r.items {
		if matchesFilter(tx, f) {
			matches = append(matches, clone(tx))
		}
	}
	r.mu.RUnlock()

	slices.SortFunc(matches, func(a, b transaction.Transaction) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return bytes.Compare(b.ID[:], a.ID[:])
	})

	if f.Offset >= len(matches) {
		return []transaction.Transaction{}, nil
	}
	end := min(f.Offset+f.Limit, len(matches))
	return matches[f.Offset:end], nil
}

func matchesFilter(tx transaction.Transaction, f transaction.Filter) bool {
	return (f.AccountID == "" || tx.AccountID == f.AccountID) &&
		(f.Status == "" || tx.Status == f.Status) &&
		(f.Type == "" || tx.Type == f.Type)
}

func clone(tx transaction.Transaction) transaction.Transaction {
	if tx.BalanceAfter != nil {
		balance := *tx.BalanceAfter
		tx.BalanceAfter = &balance
	}
	return tx
}
