package transaction

import (
	"errors"
	"strings"
	"testing"
)

func validCommand() ExecuteCommand {
	return ExecuteCommand{
		AccountID:   "acc-123456",
		Type:        Credit,
		Amount:      1500_00,
		Currency:    CurrencyMXN,
		Description: "Transferencia recibida",
	}
}

func TestDefaultRules(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(*ExecuteCommand)
		wantCodes []string
	}{
		{name: "valid credit", modify: func(*ExecuteCommand) {}},
		{name: "amount equal to minimum is rejected", modify: func(c *ExecuteCommand) { c.Amount = 1_00 }, wantCodes: []string{"AMOUNT_TOO_LOW"}},
		{name: "amount just above minimum is accepted", modify: func(c *ExecuteCommand) { c.Amount = 1_01 }},
		{name: "zero amount is rejected", modify: func(c *ExecuteCommand) { c.Amount = 0 }, wantCodes: []string{"AMOUNT_TOO_LOW"}},
		{name: "negative amount is rejected", modify: func(c *ExecuteCommand) { c.Amount = -50_00 }, wantCodes: []string{"AMOUNT_TOO_LOW"}},
		{name: "debit at the limit is accepted", modify: func(c *ExecuteCommand) { c.Type = Debit; c.Amount = 10_000_00 }},
		{name: "debit above the limit is rejected", modify: func(c *ExecuteCommand) { c.Type = Debit; c.Amount = 10_000_01 }, wantCodes: []string{"DEBIT_LIMIT_EXCEEDED"}},
		{name: "credit has no upper limit", modify: func(c *ExecuteCommand) { c.Amount = 5_000_000_00 }},
		{name: "non-MXN currency is rejected", modify: func(c *ExecuteCommand) { c.Currency = "USD" }, wantCodes: []string{"UNSUPPORTED_CURRENCY"}},
		{name: "lowercase currency is rejected", modify: func(c *ExecuteCommand) { c.Currency = "mxn" }, wantCodes: []string{"UNSUPPORTED_CURRENCY"}},
		{name: "missing account", modify: func(c *ExecuteCommand) { c.AccountID = "" }, wantCodes: []string{"REQUIRED"}},
		{name: "account too long", modify: func(c *ExecuteCommand) { c.AccountID = strings.Repeat("a", 65) }, wantCodes: []string{"TOO_LONG"}},
		{name: "unknown type", modify: func(c *ExecuteCommand) { c.Type = "REFUND" }, wantCodes: []string{"UNSUPPORTED_TYPE"}},
		{name: "description too long", modify: func(c *ExecuteCommand) { c.Description = strings.Repeat("x", 256) }, wantCodes: []string{"TOO_LONG"}},
		{
			name:      "all violations are reported together",
			modify:    func(c *ExecuteCommand) { c.Type = Debit; c.Amount = 20_000_00; c.Currency = "EUR"; c.AccountID = "" },
			wantCodes: []string{"REQUIRED", "DEBIT_LIMIT_EXCEEDED", "UNSUPPORTED_CURRENCY"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := validCommand()
			tt.modify(&cmd)

			err := checkRules(DefaultRules(), cmd)

			if len(tt.wantCodes) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
			got := make([]string, len(verr.Violations))
			for i, v := range verr.Violations {
				got[i] = v.Code
			}
			if strings.Join(got, ",") != strings.Join(tt.wantCodes, ",") {
				t.Errorf("violation codes = %v, want %v", got, tt.wantCodes)
			}
		})
	}
}
