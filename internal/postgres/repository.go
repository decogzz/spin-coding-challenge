// Package postgres implements transaction.Repository on PostgreSQL using pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

const columns = `id, account_id, type, amount_cents, currency, description, status,
	provider_transaction_id, balance_after_cents, failure_code, failure_message, created_at, updated_at`

type Repository struct {
	pool *pgxpool.Pool
}

var _ transaction.Repository = (*Repository)(nil)

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, tx *transaction.Transaction) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO transactions (`+columns+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		tx.ID, tx.AccountID, string(tx.Type), int64(tx.Amount), tx.Currency, tx.Description, string(tx.Status),
		nullIfEmpty(tx.ProviderTransactionID), balanceCents(tx.BalanceAfter),
		nullIfEmpty(tx.FailureCode), nullIfEmpty(tx.FailureMessage), tx.CreatedAt, tx.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert transaction: %w", err)
	}
	return nil
}

// Update records the provider outcome; the request fields are immutable.
func (r *Repository) Update(ctx context.Context, tx *transaction.Transaction) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE transactions
		 SET status = $2, provider_transaction_id = $3, balance_after_cents = $4,
		     failure_code = $5, failure_message = $6, updated_at = $7
		 WHERE id = $1`,
		tx.ID, string(tx.Status), nullIfEmpty(tx.ProviderTransactionID), balanceCents(tx.BalanceAfter),
		nullIfEmpty(tx.FailureCode), nullIfEmpty(tx.FailureMessage), tx.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("update transaction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return transaction.ErrNotFound
	}
	return nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (transaction.Transaction, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+columns+` FROM transactions WHERE id = $1`, id)
	tx, err := scanTransaction(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return transaction.Transaction{}, transaction.ErrNotFound
	}
	if err != nil {
		return transaction.Transaction{}, fmt.Errorf("find transaction: %w", err)
	}
	return tx, nil
}

func (r *Repository) List(ctx context.Context, f transaction.Filter) ([]transaction.Transaction, error) {
	var (
		conditions []string
		args       []any
	)
	where := func(column string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if f.AccountID != "" {
		where("account_id", f.AccountID)
	}
	if f.Status != "" {
		where("status", string(f.Status))
	}
	if f.Type != "" {
		where("type", string(f.Type))
	}

	query := `SELECT ` + columns + ` FROM transactions`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, " AND ")
	}
	args = append(args, f.Limit, f.Offset)
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (transaction.Transaction, error) {
		return scanTransaction(row)
	})
	if err != nil {
		return nil, fmt.Errorf("scan transactions: %w", err)
	}
	return items, nil
}

func scanTransaction(row pgx.Row) (transaction.Transaction, error) {
	var (
		tx                                     transaction.Transaction
		txType, status                         string
		amount                                 int64
		providerID, failureCode, failureReason *string
		balance                                *int64
	)
	err := row.Scan(&tx.ID, &tx.AccountID, &txType, &amount, &tx.Currency, &tx.Description, &status,
		&providerID, &balance, &failureCode, &failureReason, &tx.CreatedAt, &tx.UpdatedAt)
	if err != nil {
		return transaction.Transaction{}, err
	}

	tx.Type = transaction.Type(txType)
	tx.Status = transaction.Status(status)
	tx.Amount = transaction.Money(amount)
	tx.ProviderTransactionID = deref(providerID)
	tx.FailureCode = deref(failureCode)
	tx.FailureMessage = deref(failureReason)
	if balance != nil {
		b := transaction.Money(*balance)
		tx.BalanceAfter = &b
	}
	tx.CreatedAt = tx.CreatedAt.UTC()
	tx.UpdatedAt = tx.UpdatedAt.UTC()
	return tx, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func balanceCents(m *transaction.Money) *int64 {
	if m == nil {
		return nil
	}
	cents := int64(*m)
	return &cents
}
