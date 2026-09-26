// Package httpapi exposes the transaction service over HTTP.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

type TransactionService interface {
	Execute(ctx context.Context, cmd transaction.ExecuteCommand) (transaction.Transaction, error)
	Get(ctx context.Context, id uuid.UUID) (transaction.Transaction, error)
	List(ctx context.Context, q transaction.ListQuery) (transaction.Page, error)
}

// Pinger reports whether a dependency is reachable; *pgxpool.Pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

type handler struct {
	svc    TransactionService
	db     Pinger
	logger *slog.Logger
}

// NewHandler returns the API's routes wrapped in request ID, access log and
// panic recovery middleware. db may be nil, in which case /readyz always succeeds.
func NewHandler(svc TransactionService, db Pinger, logger *slog.Logger) http.Handler {
	h := &handler{svc: svc, db: db, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /transactions", h.createTransaction)
	mux.HandleFunc("GET /transactions", h.listTransactions)
	mux.HandleFunc("GET /transactions/{id}", h.getTransaction)
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)

	return withRequestID(withAccessLog(logger, withRecovery(logger, mux)))
}

func (h *handler) createTransaction(w http.ResponseWriter, r *http.Request) {
	var req createTransactionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	cmd, violations := req.toCommand()
	if len(violations) > 0 {
		writeViolations(w, violations)
		return
	}

	tx, err := h.svc.Execute(r.Context(), cmd)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/transactions/"+tx.ID.String())
	writeJSON(w, statusFor(tx.Status), toResponse(tx))
}

func (h *handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var violations []transaction.Violation
	page := intParam(query, "page", &violations)
	limit := intParam(query, "limit", &violations)
	if len(violations) > 0 {
		writeViolations(w, violations)
		return
	}

	result, err := h.svc.List(r.Context(), transaction.ListQuery{
		AccountID: query.Get("accountId"),
		Status:    transaction.Status(query.Get("status")),
		Type:      transaction.Type(query.Get("type")),
		Page:      page,
		Limit:     limit,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toListResponse(result))
}

func (h *handler) getTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeViolations(w, []transaction.Violation{{Field: "id", Code: "INVALID_ID", Message: "id must be a UUID"}})
		return
	}
	tx, err := h.svc.Get(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(tx))
}

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	if h.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.db.Ping(ctx); err != nil {
			h.logger.WarnContext(ctx, "readiness check failed", slog.Any("error", err))
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "database is unreachable")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// intParam returns 0 when the parameter is absent, letting the service apply its default.
func intParam(query url.Values, name string, violations *[]transaction.Violation) int {
	raw := query.Get(name)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		*violations = append(*violations, transaction.Violation{
			Field: name, Code: "INVALID_INTEGER", Message: name + " must be an integer",
		})
	}
	return n
}
