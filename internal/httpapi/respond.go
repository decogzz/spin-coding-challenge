package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

const maxBodyBytes = 64 << 10

var errTrailingData = errors.New("request body must contain a single JSON object")

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errTrailingData
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Code: code, Message: message})
}

func writeViolations(w http.ResponseWriter, violations []transaction.Violation) {
	details := make([]violationBody, len(violations))
	for i, v := range violations {
		details[i] = violationBody(v)
	}
	writeJSON(w, http.StatusBadRequest, errorResponse{
		Code:    "VALIDATION_FAILED",
		Message: "the request is invalid; see details",
		Details: details,
	})
}

// writeDecodeError explains a malformed body without leaking Go type names.
func writeDecodeError(w http.ResponseWriter, err error) {
	var (
		maxBytes  *http.MaxBytesError
		syntax    *json.SyntaxError
		typeError *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &maxBytes):
		writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE",
			fmt.Sprintf("request body must not exceed %d bytes", maxBytes.Limit))
	case errors.Is(err, io.EOF):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "request body is empty")
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "request body is not valid JSON")
	case errors.As(err, &typeError):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY",
			fmt.Sprintf("field %q has the wrong type", typeError.Field))
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY",
			"unknown field "+strings.TrimPrefix(err.Error(), "json: unknown field "))
	case errors.Is(err, errTrailingData):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", err.Error())
	default:
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "request body could not be decoded")
	}
}

func (h *handler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *transaction.ValidationError
	switch {
	case errors.As(err, &verr):
		writeViolations(w, verr.Violations)
	case errors.Is(err, transaction.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "transaction not found")
	default:
		h.logger.ErrorContext(r.Context(), "request failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "an unexpected error occurred")
	}
}

// statusFor maps the outcome of an execution to an HTTP status. The transaction
// is returned in every case, so clients can always read its final state.
func statusFor(status transaction.Status) int {
	switch status {
	case transaction.StatusExecuted:
		return http.StatusCreated
	case transaction.StatusRejected:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadGateway
	}
}
