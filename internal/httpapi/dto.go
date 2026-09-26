package httpapi

import (
	"encoding/json"
	"errors"
	"time"

	"gitlab.com/decogzz/spin-coding-challenge/internal/transaction"
)

type createTransactionRequest struct {
	AccountID   string      `json:"accountId"`
	Type        string      `json:"type"`
	Amount      json.Number `json:"amount"`
	Currency    string      `json:"currency"`
	Description string      `json:"description"`
}

// toCommand converts the wire format; business validation happens in the service.
func (r createTransactionRequest) toCommand() (transaction.ExecuteCommand, []transaction.Violation) {
	cmd := transaction.ExecuteCommand{
		AccountID:   r.AccountID,
		Type:        transaction.Type(r.Type),
		Currency:    r.Currency,
		Description: r.Description,
	}
	if r.Amount == "" {
		return cmd, []transaction.Violation{{Field: "amount", Code: "REQUIRED", Message: "amount is required"}}
	}
	amount, err := transaction.ParseMoney(r.Amount.String())
	if err != nil {
		code := "INVALID_AMOUNT"
		if errors.Is(err, transaction.ErrAmountPrecision) {
			code = "INVALID_PRECISION"
		}
		return cmd, []transaction.Violation{{Field: "amount", Code: code, Message: err.Error()}}
	}
	cmd.Amount = amount
	return cmd, nil
}

type transactionResponse struct {
	ID                    string             `json:"id"`
	AccountID             string             `json:"accountId"`
	Type                  transaction.Type   `json:"type"`
	Amount                transaction.Money  `json:"amount"`
	Currency              string             `json:"currency"`
	Description           string             `json:"description"`
	Status                transaction.Status `json:"status"`
	ProviderTransactionID string             `json:"providerTransactionId,omitempty"`
	BalanceAfter          *transaction.Money `json:"balanceAfter,omitempty"`
	FailureCode           string             `json:"failureCode,omitempty"`
	FailureMessage        string             `json:"failureMessage,omitempty"`
	CreatedAt             time.Time          `json:"createdAt"`
	UpdatedAt             time.Time          `json:"updatedAt"`
}

func toResponse(tx transaction.Transaction) transactionResponse {
	return transactionResponse{
		ID:                    tx.ID.String(),
		AccountID:             tx.AccountID,
		Type:                  tx.Type,
		Amount:                tx.Amount,
		Currency:              tx.Currency,
		Description:           tx.Description,
		Status:                tx.Status,
		ProviderTransactionID: tx.ProviderTransactionID,
		BalanceAfter:          tx.BalanceAfter,
		FailureCode:           tx.FailureCode,
		FailureMessage:        tx.FailureMessage,
		CreatedAt:             tx.CreatedAt,
		UpdatedAt:             tx.UpdatedAt,
	}
}

type listResponse struct {
	Data       []transactionResponse `json:"data"`
	Pagination paginationResponse    `json:"pagination"`
}

type paginationResponse struct {
	Page    int  `json:"page"`
	Limit   int  `json:"limit"`
	HasMore bool `json:"hasMore"`
}

func toListResponse(page transaction.Page) listResponse {
	data := make([]transactionResponse, len(page.Items))
	for i, tx := range page.Items {
		data[i] = toResponse(tx)
	}
	return listResponse{
		Data:       data,
		Pagination: paginationResponse{Page: page.Page, Limit: page.Limit, HasMore: page.HasMore},
	}
}

type errorResponse struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details []violationBody `json:"details,omitempty"`
}

type violationBody struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
