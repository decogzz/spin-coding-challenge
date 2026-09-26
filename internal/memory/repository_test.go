package memory_test

import (
	"testing"

	"gitlab.com/decogzz/spin-coding-challenge/internal/memory"
	"gitlab.com/decogzz/spin-coding-challenge/internal/repotest"
	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

func TestRepositoryContract(t *testing.T) {
	repotest.Run(t, func(*testing.T) transaction.Repository { return memory.NewRepository() })
}
