package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"

	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

func testConfig(baseURL string) Config {
	return Config{
		BaseURL:            baseURL,
		AttemptTimeout:     500 * time.Millisecond,
		MaxAttempts:        3,
		BaseBackoff:        time.Millisecond,
		MaxBackoff:         5 * time.Millisecond,
		BreakerFailures:    3,
		BreakerOpenTimeout: time.Minute,
	}
}

func newTestClient(cfg Config) *Client {
	return NewClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func sampleRequest() transaction.ExecutionRequest {
	return transaction.ExecutionRequest{
		IdempotencyKey: "0192b4a0-0000-7000-8000-000000000001",
		AccountID:      "acc-123456",
		Type:           transaction.Credit,
		Amount:         1500_00,
		Currency:       "MXN",
	}
}

// countingServer answers with the responses in order, repeating the last one.
func countingServer(t *testing.T, responses ...func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1))
		responses[min(n, len(responses))-1](w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func respond(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

const approvedBody = `{"transactionId":"txn-789","status":"APPROVED","balance":5500.00,"executedAt":"2025-03-15T10:30:00Z"}`

const insufficientFundsBody = `{"status":"REJECTED","code":"INSUFFICIENT_FUNDS","message":"The account does not have enough balance to complete the transaction"}`

func TestExecuteApproved(t *testing.T) {
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/provider/v1/execute" {
			t.Errorf("got %s %s, want POST /provider/v1/execute", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Idempotency-Key"); got != sampleRequest().IdempotencyKey {
			t.Errorf("Idempotency-Key = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		raw, _ := io.ReadAll(r.Body)
		want := `{"accountId":"acc-123456","type":"CREDIT","amount":1500.00,"currency":"MXN"}`
		if string(raw) != want {
			t.Errorf("body = %s, want %s", raw, want)
		}
		respond(http.StatusOK, approvedBody)(w, r)
	})

	result, err := newTestClient(testConfig(srv.URL)).Execute(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := transaction.ExecutionResult{
		ProviderTransactionID: "txn-789",
		Balance:               5500_00,
		ExecutedAt:            time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC),
	}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestExecuteRejectionIsNotRetried(t *testing.T) {
	srv, hits := countingServer(t, respond(http.StatusUnprocessableEntity, insufficientFundsBody))

	_, err := newTestClient(testConfig(srv.URL)).Execute(context.Background(), sampleRequest())

	var rejection *transaction.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("error = %v, want *RejectionError", err)
	}
	if rejection.Code != "INSUFFICIENT_FUNDS" {
		t.Errorf("code = %q, want INSUFFICIENT_FUNDS", rejection.Code)
	}
	if hits.Load() != 1 {
		t.Errorf("provider hit %d times, want 1", hits.Load())
	}
}

func TestExecuteRejectionWithoutBodyGetsGenericCode(t *testing.T) {
	srv, _ := countingServer(t, respond(http.StatusForbidden, ""))

	_, err := newTestClient(testConfig(srv.URL)).Execute(context.Background(), sampleRequest())

	var rejection *transaction.RejectionError
	if !errors.As(err, &rejection) || rejection.Code != codeRejected {
		t.Fatalf("error = %v, want rejection with code %s", err, codeRejected)
	}
}

func TestExecuteRetriesTransientFailures(t *testing.T) {
	tests := map[string]int{
		"server error":      http.StatusInternalServerError,
		"unavailable":       http.StatusServiceUnavailable,
		"too many requests": http.StatusTooManyRequests,
	}
	for name, status := range tests {
		t.Run(name, func(t *testing.T) {
			srv, hits := countingServer(t,
				respond(status, `{"status":"REJECTED","code":"INTERNAL","message":"boom"}`),
				respond(status, ""),
				respond(http.StatusOK, approvedBody),
			)

			result, err := newTestClient(testConfig(srv.URL)).Execute(context.Background(), sampleRequest())
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if result.ProviderTransactionID != "txn-789" {
				t.Errorf("providerTransactionId = %q", result.ProviderTransactionID)
			}
			if hits.Load() != 3 {
				t.Errorf("provider hit %d times, want 3", hits.Load())
			}
		})
	}
}

func TestExecuteReusesIdempotencyKeyAcrossRetries(t *testing.T) {
	var keys []string
	srv, _ := countingServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			respond(http.StatusBadGateway, "")(w, r)
		},
		func(w http.ResponseWriter, r *http.Request) {
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			respond(http.StatusOK, approvedBody)(w, r)
		},
	)

	if _, err := newTestClient(testConfig(srv.URL)).Execute(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(keys) != 2 || keys[0] != keys[1] || keys[0] == "" {
		t.Errorf("idempotency keys = %v, want the same key twice", keys)
	}
}

func TestExecuteGivesUpAfterMaxAttempts(t *testing.T) {
	srv, hits := countingServer(t, respond(http.StatusInternalServerError, ""))

	_, err := newTestClient(testConfig(srv.URL)).Execute(context.Background(), sampleRequest())

	var rejection *transaction.RejectionError
	if err == nil || errors.As(err, &rejection) {
		t.Fatalf("error = %v, want a non-rejection failure", err)
	}
	if hits.Load() != 3 {
		t.Errorf("provider hit %d times, want 3", hits.Load())
	}
}

func TestExecuteTimesOutEachAttempt(t *testing.T) {
	release := make(chan struct{})
	srv, hits := countingServer(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	cfg := testConfig(srv.URL)
	cfg.AttemptTimeout = 50 * time.Millisecond

	start := time.Now()
	_, err := newTestClient(cfg).Execute(context.Background(), sampleRequest())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if hits.Load() != 3 {
		t.Errorf("provider hit %d times, want 3", hits.Load())
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v, want each attempt bounded by the attempt timeout", elapsed)
	}
}

func TestExecuteMalformedSuccessIsNotRetried(t *testing.T) {
	srv, hits := countingServer(t, respond(http.StatusOK, `{"status":"APPROVED","balance":"lots"}`))

	_, err := newTestClient(testConfig(srv.URL)).Execute(context.Background(), sampleRequest())

	if err == nil {
		t.Fatal("expected an error")
	}
	if hits.Load() != 1 {
		t.Errorf("provider hit %d times, want 1", hits.Load())
	}
}

func TestExecuteNetworkErrorIsRetried(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	_, err := newTestClient(testConfig(url)).Execute(context.Background(), sampleRequest())

	var transient *transientError
	if !errors.As(err, &transient) {
		t.Fatalf("error = %v, want a transient network error", err)
	}
}

func TestCircuitOpensAfterConsecutiveFailures(t *testing.T) {
	srv, hits := countingServer(t, respond(http.StatusInternalServerError, ""))
	cfg := testConfig(srv.URL)
	cfg.MaxAttempts = 1
	cfg.BreakerFailures = 2
	client := newTestClient(cfg)

	for range 2 {
		if _, err := client.Execute(context.Background(), sampleRequest()); err == nil {
			t.Fatal("expected an error")
		}
	}
	_, err := client.Execute(context.Background(), sampleRequest())

	if !errors.Is(err, gobreaker.ErrOpenState) {
		t.Fatalf("error = %v, want gobreaker.ErrOpenState", err)
	}
	if hits.Load() != 2 {
		t.Errorf("provider hit %d times, want 2 (the open circuit must short-circuit)", hits.Load())
	}
}

func TestRejectionsDoNotOpenCircuit(t *testing.T) {
	srv, hits := countingServer(t, respond(http.StatusUnprocessableEntity, insufficientFundsBody))
	cfg := testConfig(srv.URL)
	cfg.BreakerFailures = 2
	client := newTestClient(cfg)

	for range 5 {
		_, err := client.Execute(context.Background(), sampleRequest())
		var rejection *transaction.RejectionError
		if !errors.As(err, &rejection) {
			t.Fatalf("error = %v, want *RejectionError", err)
		}
	}
	if hits.Load() != 5 {
		t.Errorf("provider hit %d times, want 5", hits.Load())
	}
}

func TestBackoffStaysWithinBounds(t *testing.T) {
	client := newTestClient(Config{BaseBackoff: 100 * time.Millisecond, MaxBackoff: time.Second})
	for retry, maxDelay := range map[int]time.Duration{1: 100 * time.Millisecond, 2: 200 * time.Millisecond, 5: time.Second, 70: time.Second} {
		for range 50 {
			d := client.backoff(retry)
			if d < maxDelay/2 || d > maxDelay {
				t.Fatalf("backoff(%d) = %v, want between %v and %v", retry, d, maxDelay/2, maxDelay)
			}
		}
	}
}

func TestRequestAmountIsSentAsNumber(t *testing.T) {
	raw, err := json.Marshal(executeRequest{Amount: 10_00})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["amount"].(float64); !ok {
		t.Errorf("amount encoded as %T, want a JSON number", decoded["amount"])
	}
}
