package providermock_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/decogzz/spin-coding-challenge/internal/provider"
	"gitlab.com/decogzz/spin-coding-challenge/internal/providermock"
	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

func newMock(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(providermock.NewServer(providermock.Config{
		InitialBalance: 1000_00,
		SlowDelay:      300 * time.Millisecond,
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newClient exercises the mock through the real provider client, so these
// tests double as an end-to-end check of the resilience behaviour.
func newClient(baseURL string) *provider.Client {
	cfg := provider.DefaultConfig(baseURL)
	cfg.AttemptTimeout = 100 * time.Millisecond
	cfg.BaseBackoff = time.Millisecond
	cfg.MaxBackoff = 5 * time.Millisecond
	return provider.NewClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func execute(client *provider.Client, key, account string, txType transaction.Type, amount transaction.Money) (transaction.ExecutionResult, error) {
	return client.Execute(context.Background(), transaction.ExecutionRequest{
		IdempotencyKey: key, AccountID: account, Type: txType, Amount: amount, Currency: "MXN",
	})
}

func rejectionCode(err error) string {
	var rejection *transaction.RejectionError
	if errors.As(err, &rejection) {
		return rejection.Code
	}
	return ""
}

func TestBalancesAreTracked(t *testing.T) {
	client := newClient(newMock(t).URL)

	credit, err := execute(client, "k1", "acc-1", transaction.Credit, 500_00)
	if err != nil || credit.Balance != 1500_00 {
		t.Fatalf("credit: balance %s, err %v; want 1500.00", credit.Balance, err)
	}
	debit, err := execute(client, "k2", "acc-1", transaction.Debit, 200_50)
	if err != nil || debit.Balance != 1299_50 {
		t.Fatalf("debit: balance %s, err %v; want 1299.50", debit.Balance, err)
	}
	if credit.ProviderTransactionID == debit.ProviderTransactionID {
		t.Error("each execution needs its own provider transaction id")
	}

	_, err = execute(client, "k3", "acc-1", transaction.Debit, 5000_00)
	if code := rejectionCode(err); code != "INSUFFICIENT_FUNDS" {
		t.Errorf("overdraft: error %v, want INSUFFICIENT_FUNDS", err)
	}
}

func TestIdempotencyKeyAppliesOnce(t *testing.T) {
	client := newClient(newMock(t).URL)

	first, err := execute(client, "same-key", "acc-1", transaction.Credit, 100_00)
	if err != nil {
		t.Fatal(err)
	}
	second, err := execute(client, "same-key", "acc-1", transaction.Credit, 100_00)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("replay returned %+v, want %+v", second, first)
	}
	third, _ := execute(client, "other-key", "acc-1", transaction.Credit, 100_00)
	if third.Balance != 1200_00 {
		t.Errorf("balance = %s, want 1200.00 (the replay must not be applied twice)", third.Balance)
	}
}

func TestScenarioAccounts(t *testing.T) {
	client := newClient(newMock(t).URL)

	if _, err := execute(client, "a", providermock.AccountInsufficient, transaction.Debit, 10_00); rejectionCode(err) != "INSUFFICIENT_FUNDS" {
		t.Errorf("%s debit: %v, want INSUFFICIENT_FUNDS", providermock.AccountInsufficient, err)
	}
	if _, err := execute(client, "b", providermock.AccountInsufficient, transaction.Credit, 10_00); err != nil {
		t.Errorf("%s credit: %v, want success", providermock.AccountInsufficient, err)
	}
	if _, err := execute(client, "c", providermock.AccountBlocked, transaction.Credit, 10_00); rejectionCode(err) != "ACCOUNT_BLOCKED" {
		t.Errorf("%s: %v, want ACCOUNT_BLOCKED", providermock.AccountBlocked, err)
	}
	if _, err := execute(client, "d", providermock.AccountError, transaction.Credit, 10_00); err == nil || rejectionCode(err) != "" {
		t.Errorf("%s: %v, want a non-rejection failure", providermock.AccountError, err)
	}
	if _, err := execute(client, "e", providermock.AccountFlaky, transaction.Credit, 10_00); err != nil {
		t.Errorf("%s: %v, want success after a retry", providermock.AccountFlaky, err)
	}
	if _, err := execute(client, "f", providermock.AccountSlow, transaction.Credit, 10_00); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%s: %v, want a timeout", providermock.AccountSlow, err)
	}
}

func TestFlakyAccountFailsFirstAttemptPerKey(t *testing.T) {
	srv := newMock(t)
	post := func(key string) int {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/provider/v1/execute",
			strings.NewReader(`{"accountId":"acc-flaky","type":"CREDIT","amount":10.00,"currency":"MXN"}`))
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if got := post("k1"); got != http.StatusServiceUnavailable {
		t.Errorf("first attempt = %d, want 503", got)
	}
	if got := post("k1"); got != http.StatusOK {
		t.Errorf("second attempt = %d, want 200", got)
	}
	if got := post("k2"); got != http.StatusServiceUnavailable {
		t.Errorf("new key first attempt = %d, want 503", got)
	}
}

func TestInvalidRequest(t *testing.T) {
	srv := newMock(t)
	for _, body := range []string{`not json`, `{"accountId":"a","type":"REFUND","amount":10}`, `{"accountId":"a","type":"CREDIT","amount":-1}`} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/provider/v1/execute", strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s: status %d, want 400", body, resp.StatusCode)
		}
	}
}
