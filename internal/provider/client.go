// Package provider is the HTTP adapter for the external provider that executes
// transactions and owns account balances and locks.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/sony/gobreaker/v2"

	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

const (
	executePath      = "/provider/v1/execute"
	maxResponseBytes = 1 << 20
	statusApproved   = "APPROVED"
	codeRejected     = "PROVIDER_REJECTED"
)

type Config struct {
	BaseURL string
	// AttemptTimeout bounds a single HTTP attempt, not the whole retry sequence.
	AttemptTimeout time.Duration
	// MaxAttempts counts the first attempt too; 1 disables retries.
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// BreakerFailures is how many consecutive failed executions open the circuit.
	BreakerFailures uint32
	// BreakerOpenTimeout is how long the circuit stays open before a probe is allowed.
	BreakerOpenTimeout time.Duration
}

func DefaultConfig(baseURL string) Config {
	return Config{
		BaseURL:            baseURL,
		AttemptTimeout:     2 * time.Second,
		MaxAttempts:        3,
		BaseBackoff:        100 * time.Millisecond,
		MaxBackoff:         1 * time.Second,
		BreakerFailures:    5,
		BreakerOpenTimeout: 30 * time.Second,
	}
}

// Client implements transaction.Provider over HTTP.
type Client struct {
	cfg     Config
	http    *http.Client
	breaker *gobreaker.CircuitBreaker[transaction.ExecutionResult]
	logger  *slog.Logger
}

var _ transaction.Provider = (*Client)(nil)

func NewClient(cfg Config, logger *slog.Logger) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The default of 2 idle connections per host forces constant reconnects under load.
	transport.MaxIdleConns = 200
	transport.MaxIdleConnsPerHost = 100

	c := &Client{
		cfg:    cfg,
		http:   &http.Client{Transport: transport},
		logger: logger,
	}
	c.breaker = gobreaker.NewCircuitBreaker[transaction.ExecutionResult](gobreaker.Settings{
		Name:        "provider",
		MaxRequests: 1,
		Timeout:     cfg.BreakerOpenTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= cfg.BreakerFailures
		},
		// A business rejection means the provider is healthy and answering.
		IsSuccessful: func(err error) bool {
			var rejection *transaction.RejectionError
			return err == nil || errors.As(err, &rejection)
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			logger.Warn("circuit breaker state changed",
				slog.String("breaker", name), slog.String("from", from.String()), slog.String("to", to.String()))
		},
	})
	return c
}

type executeRequest struct {
	AccountID string            `json:"accountId"`
	Type      transaction.Type  `json:"type"`
	Amount    transaction.Money `json:"amount"`
	Currency  string            `json:"currency"`
}

type executeResponse struct {
	TransactionID string      `json:"transactionId"`
	Status        string      `json:"status"`
	Balance       json.Number `json:"balance"`
	ExecutedAt    time.Time   `json:"executedAt"`
}

type errorResponse struct {
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// transientError marks a failure that another attempt might not repeat.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func (c *Client) Execute(ctx context.Context, req transaction.ExecutionRequest) (transaction.ExecutionResult, error) {
	return c.breaker.Execute(func() (transaction.ExecutionResult, error) {
		return c.executeWithRetry(ctx, req)
	})
}

// executeWithRetry is safe to retry because every attempt carries the same
// Idempotency-Key, so the provider can recognise a duplicate.
func (c *Client) executeWithRetry(ctx context.Context, req transaction.ExecutionRequest) (transaction.ExecutionResult, error) {
	body, err := json.Marshal(executeRequest{
		AccountID: req.AccountID,
		Type:      req.Type,
		Amount:    req.Amount,
		Currency:  req.Currency,
	})
	if err != nil {
		return transaction.ExecutionResult{}, fmt.Errorf("encode provider request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleep(ctx, c.backoff(attempt-1)); err != nil {
				return transaction.ExecutionResult{}, errors.Join(lastErr, err)
			}
		}
		result, err := c.attempt(ctx, req.IdempotencyKey, body)
		var transient *transientError
		if err == nil || !errors.As(err, &transient) {
			return result, err
		}
		lastErr = err
		c.logger.WarnContext(ctx, "provider attempt failed",
			slog.String("idempotency_key", req.IdempotencyKey), slog.Int("attempt", attempt), slog.Any("error", err))
	}
	return transaction.ExecutionResult{}, fmt.Errorf("provider failed after %d attempts: %w", c.cfg.MaxAttempts, lastErr)
}

func (c *Client) attempt(ctx context.Context, idempotencyKey string, body []byte) (transaction.ExecutionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.AttemptTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+executePath, bytes.NewReader(body))
	if err != nil {
		return transaction.ExecutionResult{}, fmt.Errorf("build provider request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return transaction.ExecutionResult{}, &transientError{fmt.Errorf("call provider: %w", err)}
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return transaction.ExecutionResult{}, &transientError{fmt.Errorf("read provider response: %w", err)}
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return decodeSuccess(payload)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError:
		return transaction.ExecutionResult{}, &transientError{
			fmt.Errorf("provider responded %d: %s", resp.StatusCode, summarize(payload))}
	case resp.StatusCode >= http.StatusBadRequest:
		return transaction.ExecutionResult{}, decodeRejection(resp.StatusCode, payload)
	default:
		return transaction.ExecutionResult{}, fmt.Errorf("unexpected provider status %d", resp.StatusCode)
	}
}

// decodeSuccess does not treat a malformed 200 as retryable: the provider may
// already have moved the money.
func decodeSuccess(payload []byte) (transaction.ExecutionResult, error) {
	var body executeResponse
	if err := json.Unmarshal(payload, &body); err != nil {
		return transaction.ExecutionResult{}, fmt.Errorf("decode provider response: %w", err)
	}
	if body.Status != statusApproved {
		return transaction.ExecutionResult{}, fmt.Errorf("unexpected provider status %q in successful response", body.Status)
	}
	balance, err := transaction.ParseMoney(body.Balance.String())
	if err != nil {
		return transaction.ExecutionResult{}, fmt.Errorf("decode provider balance %q: %w", body.Balance, err)
	}
	return transaction.ExecutionResult{
		ProviderTransactionID: body.TransactionID,
		Balance:               balance,
		ExecutedAt:            body.ExecutedAt,
	}, nil
}

func decodeRejection(statusCode int, payload []byte) *transaction.RejectionError {
	var body errorResponse
	if err := json.Unmarshal(payload, &body); err != nil || body.Code == "" {
		return &transaction.RejectionError{
			Code:    codeRejected,
			Message: fmt.Sprintf("provider rejected the transaction with HTTP %d", statusCode),
		}
	}
	return &transaction.RejectionError{Code: body.Code, Message: body.Message}
}

// backoff returns an exponentially growing delay with jitter so that many
// clients retrying at once do not hit the provider in lockstep.
func (c *Client) backoff(retry int) time.Duration {
	d := c.cfg.BaseBackoff << (retry - 1)
	if d <= 0 || d > c.cfg.MaxBackoff {
		d = c.cfg.MaxBackoff
	}
	half := d / 2
	return half + rand.N(half+1) //nolint:gosec // jitter does not need a cryptographic source
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func summarize(payload []byte) string {
	const maxLen = 200
	s := strings.TrimSpace(string(payload))
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
