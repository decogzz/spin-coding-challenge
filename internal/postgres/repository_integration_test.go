//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/decogzz/spin-coding-challenge/internal/postgres"
	"gitlab.com/decogzz/spin-coding-challenge/internal/repotest"
	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

func TestRepositoryContract(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	schema, err := os.ReadFile("../../migrations/001_create_transactions.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(schema)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	repotest.Run(t, func(t *testing.T) transaction.Repository {
		if _, err := pool.Exec(ctx, "TRUNCATE transactions"); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return postgres.NewRepository(pool)
	})
}
