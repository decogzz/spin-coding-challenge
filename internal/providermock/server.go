// Package providermock is a stand-in for the external provider. It keeps
// balances in memory, honours Idempotency-Key, and reserves a few account IDs
// that reproduce failure scenarios on demand.
package providermock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

// Scenario accounts. Any other account starts with Config.InitialBalance.
const (
	// AccountInsufficient starts with a zero balance, so every DEBIT is rejected.
	AccountInsufficient = "acc-insufficient"
	// AccountBlocked is locked by the provider and rejects every transaction.
	AccountBlocked = "acc-blocked"
	// AccountError always answers 500.
	AccountError = "acc-error"
	// AccountSlow answers after Config.SlowDelay, longer than the client's timeout.
	AccountSlow = "acc-slow"
	// AccountFlaky answers 503 to the first attempt of each idempotency key.
	AccountFlaky = "acc-flaky"
)

type Config struct {
	InitialBalance transaction.Money
	SlowDelay      time.Duration
}

type Server struct {
	cfg Config
	mux *http.ServeMux
	now func() time.Time

	mu            sync.Mutex
	balances      map[string]transaction.Money
	responses     map[string]reply
	flakyAttempts map[string]int
	sequence      int
}

type reply struct {
	status int
	body   any
}

type executeRequest struct {
	AccountID string      `json:"accountId"`
	Type      string      `json:"type"`
	Amount    json.Number `json:"amount"`
	Currency  string      `json:"currency"`
}

type executeResponse struct {
	TransactionID string            `json:"transactionId"`
	Status        string            `json:"status"`
	Balance       transaction.Money `json:"balance"`
	ExecutedAt    time.Time         `json:"executedAt"`
}

type errorResponse struct {
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewServer(cfg Config) *Server {
	s := &Server{
		cfg:           cfg,
		mux:           http.NewServeMux(),
		now:           time.Now,
		balances:      map[string]transaction.Money{AccountInsufficient: 0},
		responses:     make(map[string]reply),
		flakyAttempts: make(map[string]int),
	}
	s.mux.HandleFunc("POST /provider/v1/execute", s.execute)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) execute(w http.ResponseWriter, r *http.Request) {
	var req executeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, rejection("INVALID_REQUEST", "request body is not valid JSON"))
		return
	}
	amount, err := transaction.ParseMoney(req.Amount.String())
	if err != nil || amount <= 0 || (req.Type != "CREDIT" && req.Type != "DEBIT") || req.AccountID == "" {
		writeJSON(w, http.StatusBadRequest, rejection("INVALID_REQUEST", "accountId, type and a positive amount are required"))
		return
	}

	// A request abandoned by the client while waiting is never applied.
	if req.AccountID == AccountSlow && !wait(r.Context(), s.cfg.SlowDelay) {
		return
	}

	result := s.process(r.Header.Get("Idempotency-Key"), req.AccountID, transaction.Type(req.Type), amount)
	writeJSON(w, result.status, result.body)
}

func (s *Server) process(key, accountID string, txType transaction.Type, amount transaction.Money) reply {
	s.mu.Lock()
	defer s.mu.Unlock()

	if stored, ok := s.responses[key]; ok && key != "" {
		return stored
	}

	switch accountID {
	case AccountError:
		return reply{http.StatusInternalServerError, rejection("PROVIDER_ERROR", "internal provider error")}
	case AccountFlaky:
		s.flakyAttempts[key]++
		if s.flakyAttempts[key] == 1 {
			return reply{http.StatusServiceUnavailable, rejection("TEMPORARILY_UNAVAILABLE", "try again later")}
		}
	}

	result := s.apply(accountID, txType, amount)
	if key != "" {
		s.responses[key] = result
	}
	return result
}

func (s *Server) apply(accountID string, txType transaction.Type, amount transaction.Money) reply {
	if accountID == AccountBlocked {
		return reply{http.StatusForbidden, rejection("ACCOUNT_BLOCKED", "The account is blocked and cannot operate")}
	}

	balance, ok := s.balances[accountID]
	if !ok {
		balance = s.cfg.InitialBalance
	}
	if txType == transaction.Debit {
		if amount > balance {
			return reply{http.StatusUnprocessableEntity, rejection("INSUFFICIENT_FUNDS",
				"The account does not have enough balance to complete the transaction")}
		}
		balance -= amount
	} else {
		balance += amount
	}
	s.balances[accountID] = balance
	s.sequence++

	return reply{http.StatusOK, executeResponse{
		TransactionID: fmt.Sprintf("txn-%d", s.sequence),
		Status:        "APPROVED",
		Balance:       balance,
		ExecutedAt:    s.now().UTC().Truncate(time.Second),
	}}
}

func rejection(code, message string) errorResponse {
	return errorResponse{Status: "REJECTED", Code: code, Message: message}
}

func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
