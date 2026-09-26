package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"gitlab.com/decogzz/spin-coding-challenge/internal/httpapi"
	"gitlab.com/decogzz/spin-coding-challenge/internal/memory"
	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

type stubProvider struct {
	err error
}

func (p stubProvider) Execute(_ context.Context, req transaction.ExecutionRequest) (transaction.ExecutionResult, error) {
	if p.err != nil {
		return transaction.ExecutionResult{}, p.err
	}
	return transaction.ExecutionResult{ProviderTransactionID: "txn-789", Balance: 4000_00 + req.Amount}, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newServer(t *testing.T, provider transaction.Provider) *httptest.Server {
	t.Helper()
	svc := transaction.NewService(memory.NewRepository(), provider, discardLogger())
	srv := httptest.NewServer(httpapi.NewHandler(svc, nil, discardLogger()))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details []struct {
		Field string `json:"field"`
		Code  string `json:"code"`
	} `json:"details"`
}

const validBody = `{"accountId":"acc-123456","type":"CREDIT","amount":1500.00,"currency":"MXN","description":"Transferencia recibida"}`

func TestCreateTransactionExecuted(t *testing.T) {
	srv := newServer(t, stubProvider{})

	resp, raw := do(t, http.MethodPost, srv.URL+"/transactions", validBody)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", resp.StatusCode, raw)
	}
	body := decode[map[string]any](t, raw)
	for field, want := range map[string]any{
		"accountId":             "acc-123456",
		"type":                  "CREDIT",
		"currency":              "MXN",
		"description":           "Transferencia recibida",
		"status":                "EXECUTED",
		"providerTransactionId": "txn-789",
	} {
		if body[field] != want {
			t.Errorf("%s = %v, want %v", field, body[field], want)
		}
	}
	if !strings.Contains(string(raw), `"amount":1500.00`) || !strings.Contains(string(raw), `"balanceAfter":5500.00`) {
		t.Errorf("amounts must be numbers with two decimals: %s", raw)
	}
	id, err := uuid.Parse(body["id"].(string))
	if err != nil {
		t.Fatalf("id is not a UUID: %v", body["id"])
	}
	if got := resp.Header.Get("Location"); got != "/transactions/"+id.String() {
		t.Errorf("Location = %q", got)
	}
	if _, ok := body["failureCode"]; ok {
		t.Error("failureCode must be omitted on success")
	}

	getResp, getRaw := do(t, http.MethodGet, srv.URL+"/transactions/"+id.String(), "")
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", getResp.StatusCode)
	}
	if got := decode[map[string]any](t, getRaw); got["status"] != "EXECUTED" || got["id"] != id.String() {
		t.Errorf("GET returned %s", getRaw)
	}
}

func TestCreateTransactionProviderOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantTx     string
		wantCode   string
	}{
		{
			name:       "rejected by provider",
			err:        &transaction.RejectionError{Code: "INSUFFICIENT_FUNDS", Message: "not enough balance"},
			wantStatus: http.StatusUnprocessableEntity,
			wantTx:     "REJECTED",
			wantCode:   "INSUFFICIENT_FUNDS",
		},
		{
			name:       "provider unavailable",
			err:        errors.New("connection refused"),
			wantStatus: http.StatusBadGateway,
			wantTx:     "FAILED",
			wantCode:   transaction.FailureProviderUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(t, stubProvider{err: tt.err})

			resp, raw := do(t, http.MethodPost, srv.URL+"/transactions", validBody)

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", resp.StatusCode, tt.wantStatus, raw)
			}
			body := decode[map[string]any](t, raw)
			if body["status"] != tt.wantTx || body["failureCode"] != tt.wantCode {
				t.Errorf("got status %v code %v, want %s %s", body["status"], body["failureCode"], tt.wantTx, tt.wantCode)
			}
			if _, ok := body["balanceAfter"]; ok {
				t.Error("balanceAfter must be omitted when the transaction did not execute")
			}
		})
	}
}

func TestCreateTransactionValidation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantFields []string
	}{
		{"amount equal to minimum", `{"accountId":"a","type":"CREDIT","amount":1.00,"currency":"MXN"}`, 400, "VALIDATION_FAILED", []string{"amount"}},
		{"debit above limit", `{"accountId":"a","type":"DEBIT","amount":10000.01,"currency":"MXN"}`, 400, "VALIDATION_FAILED", []string{"amount"}},
		{"unsupported currency", `{"accountId":"a","type":"CREDIT","amount":10,"currency":"USD"}`, 400, "VALIDATION_FAILED", []string{"currency"}},
		{"several violations", `{"type":"REFUND","amount":0.5,"currency":"USD"}`, 400, "VALIDATION_FAILED", []string{"accountId", "type", "amount", "currency"}},
		{"missing amount", `{"accountId":"a","type":"CREDIT","currency":"MXN"}`, 400, "VALIDATION_FAILED", []string{"amount"}},
		{"too many decimals", `{"accountId":"a","type":"CREDIT","amount":10.001,"currency":"MXN"}`, 400, "VALIDATION_FAILED", []string{"amount"}},
		{"amount as exponent", `{"accountId":"a","type":"CREDIT","amount":1e3,"currency":"MXN"}`, 400, "VALIDATION_FAILED", []string{"amount"}},
		{"malformed JSON", `{"accountId":`, 400, "INVALID_REQUEST_BODY", nil},
		{"empty body", ``, 400, "INVALID_REQUEST_BODY", nil},
		{"unknown field", `{"accountId":"a","type":"CREDIT","amount":10,"currency":"MXN","extra":1}`, 400, "INVALID_REQUEST_BODY", nil},
		{"wrong field type", `{"accountId":123,"type":"CREDIT","amount":10,"currency":"MXN"}`, 400, "INVALID_REQUEST_BODY", nil},
		{"trailing data", validBody + `{}`, 400, "INVALID_REQUEST_BODY", nil},
		{"body too large", `{"description":"` + strings.Repeat("x", 70_000) + `"}`, 413, "PAYLOAD_TOO_LARGE", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(t, stubProvider{err: errors.New("provider must not be called")})

			resp, raw := do(t, http.MethodPost, srv.URL+"/transactions", tt.body)

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", resp.StatusCode, tt.wantStatus, raw)
			}
			body := decode[errorBody](t, raw)
			if body.Code != tt.wantCode || body.Message == "" {
				t.Errorf("error = %+v, want code %s with a message", body, tt.wantCode)
			}
			var fields []string
			for _, d := range body.Details {
				fields = append(fields, d.Field)
			}
			if strings.Join(fields, ",") != strings.Join(tt.wantFields, ",") {
				t.Errorf("violation fields = %v, want %v", fields, tt.wantFields)
			}
		})
	}
}

func TestListTransactions(t *testing.T) {
	srv := newServer(t, stubProvider{})
	for _, body := range []string{
		`{"accountId":"acc-1","type":"CREDIT","amount":100,"currency":"MXN"}`,
		`{"accountId":"acc-2","type":"DEBIT","amount":200,"currency":"MXN"}`,
		`{"accountId":"acc-1","type":"DEBIT","amount":300,"currency":"MXN"}`,
	} {
		if resp, raw := do(t, http.MethodPost, srv.URL+"/transactions", body); resp.StatusCode != http.StatusCreated {
			t.Fatalf("seed failed: %d %s", resp.StatusCode, raw)
		}
	}

	type listBody struct {
		Data []struct {
			AccountID string `json:"accountId"`
			Type      string `json:"type"`
		} `json:"data"`
		Pagination struct {
			Page    int  `json:"page"`
			Limit   int  `json:"limit"`
			HasMore bool `json:"hasMore"`
		} `json:"pagination"`
	}

	t.Run("defaults", func(t *testing.T) {
		resp, raw := do(t, http.MethodGet, srv.URL+"/transactions", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d; body %s", resp.StatusCode, raw)
		}
		body := decode[listBody](t, raw)
		if len(body.Data) != 3 || body.Pagination.Page != 1 || body.Pagination.Limit != 20 || body.Pagination.HasMore {
			t.Errorf("unexpected page: %s", raw)
		}
	})

	t.Run("filters", func(t *testing.T) {
		_, raw := do(t, http.MethodGet, srv.URL+"/transactions?accountId=acc-1&type=DEBIT&status=EXECUTED", "")
		body := decode[listBody](t, raw)
		if len(body.Data) != 1 || body.Data[0].AccountID != "acc-1" || body.Data[0].Type != "DEBIT" {
			t.Errorf("unexpected filtered page: %s", raw)
		}
	})

	t.Run("pagination", func(t *testing.T) {
		_, raw := do(t, http.MethodGet, srv.URL+"/transactions?page=1&limit=2", "")
		body := decode[listBody](t, raw)
		if len(body.Data) != 2 || !body.Pagination.HasMore {
			t.Errorf("unexpected first page: %s", raw)
		}
		_, raw = do(t, http.MethodGet, srv.URL+"/transactions?page=2&limit=2", "")
		body = decode[listBody](t, raw)
		if len(body.Data) != 1 || body.Pagination.HasMore {
			t.Errorf("unexpected second page: %s", raw)
		}
	})

	t.Run("empty result is an empty array", func(t *testing.T) {
		_, raw := do(t, http.MethodGet, srv.URL+"/transactions?accountId=nobody", "")
		if !strings.Contains(string(raw), `"data":[]`) {
			t.Errorf("want an empty data array, got %s", raw)
		}
	})

	for name, query := range map[string]string{
		"non-integer page":  "page=abc",
		"limit above max":   "limit=101",
		"zero-based page":   "page=-1",
		"unknown status":    "status=DONE",
		"unknown type":      "type=REFUND",
		"non-integer limit": "limit=ten",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			resp, raw := do(t, http.MethodGet, srv.URL+"/transactions?"+query, "")
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body %s", resp.StatusCode, raw)
			}
		})
	}
}

func TestGetTransactionErrors(t *testing.T) {
	srv := newServer(t, stubProvider{})

	resp, raw := do(t, http.MethodGet, srv.URL+"/transactions/"+uuid.NewString(), "")
	if resp.StatusCode != http.StatusNotFound || decode[errorBody](t, raw).Code != "NOT_FOUND" {
		t.Errorf("unknown id: status %d body %s, want 404 NOT_FOUND", resp.StatusCode, raw)
	}

	resp, raw = do(t, http.MethodGet, srv.URL+"/transactions/not-a-uuid", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid id: status %d body %s, want 400", resp.StatusCode, raw)
	}
}

type pinger struct{ err error }

func (p pinger) Ping(context.Context) error { return p.err }

func TestHealthEndpoints(t *testing.T) {
	svc := transaction.NewService(memory.NewRepository(), stubProvider{}, discardLogger())

	healthy := httptest.NewServer(httpapi.NewHandler(svc, pinger{}, discardLogger()))
	defer healthy.Close()
	if resp, _ := do(t, http.MethodGet, healthy.URL+"/healthz", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodGet, healthy.URL+"/readyz", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("/readyz = %d, want 200", resp.StatusCode)
	}

	dbDown := httptest.NewServer(httpapi.NewHandler(svc, pinger{err: errors.New("down")}, discardLogger()))
	defer dbDown.Close()
	if resp, _ := do(t, http.MethodGet, dbDown.URL+"/readyz", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/readyz with database down = %d, want 503", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodGet, dbDown.URL+"/healthz", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz must not depend on the database, got %d", resp.StatusCode)
	}
}

func TestRequestIDIsPropagated(t *testing.T) {
	srv := newServer(t, stubProvider{})

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/healthz", nil)
	req.Header.Set("X-Request-ID", "req-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-ID"); got != "req-123" {
		t.Errorf("X-Request-ID = %q, want req-123", got)
	}

	resp, _ = do(t, http.MethodGet, srv.URL+"/healthz", "")
	if _, err := uuid.Parse(resp.Header.Get("X-Request-ID")); err != nil {
		t.Errorf("generated X-Request-ID %q is not a UUID", resp.Header.Get("X-Request-ID"))
	}
}

type panickingService struct{ httpapi.TransactionService }

func (panickingService) List(context.Context, transaction.ListQuery) (transaction.Page, error) {
	panic("boom")
}

type failingService struct{ httpapi.TransactionService }

func (failingService) List(context.Context, transaction.ListQuery) (transaction.Page, error) {
	return transaction.Page{}, errors.New("database exploded with secrets")
}

func TestUnexpectedErrorsReturnGeneric500(t *testing.T) {
	for name, svc := range map[string]httpapi.TransactionService{
		"panic": panickingService{},
		"error": failingService{},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(httpapi.NewHandler(svc, nil, discardLogger()))
			defer srv.Close()

			resp, raw := do(t, http.MethodGet, srv.URL+"/transactions", "")

			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", resp.StatusCode)
			}
			if body := decode[errorBody](t, raw); body.Code != "INTERNAL_ERROR" || strings.Contains(body.Message, "secrets") {
				t.Errorf("unexpected error body: %s", raw)
			}
		})
	}
}
