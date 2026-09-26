package transaction

import (
	"fmt"
	"unicode/utf8"
)

const (
	MinimumAmount      Money = 1_00
	MaximumDebitAmount Money = 10_000_00

	maxAccountIDLength   = 64
	maxDescriptionLength = 255
)

// Rule checks one constraint on a command and returns nil when it holds.
type Rule func(ExecuteCommand) *Violation

// DefaultRules are applied, in order, before any transaction reaches the provider.
func DefaultRules() []Rule {
	return []Rule{
		accountIDRequired,
		typeSupported,
		amountAboveMinimum,
		debitWithinLimit,
		currencySupported,
		descriptionWithinLimit,
	}
}

func accountIDRequired(cmd ExecuteCommand) *Violation {
	switch {
	case cmd.AccountID == "":
		return &Violation{Field: "accountId", Code: "REQUIRED", Message: "accountId is required"}
	case utf8.RuneCountInString(cmd.AccountID) > maxAccountIDLength:
		return &Violation{Field: "accountId", Code: "TOO_LONG",
			Message: fmt.Sprintf("accountId must be at most %d characters", maxAccountIDLength)}
	}
	return nil
}

func typeSupported(cmd ExecuteCommand) *Violation {
	if !cmd.Type.Valid() {
		return &Violation{Field: "type", Code: "UNSUPPORTED_TYPE", Message: "type must be CREDIT or DEBIT"}
	}
	return nil
}

func amountAboveMinimum(cmd ExecuteCommand) *Violation {
	if cmd.Amount <= MinimumAmount {
		return &Violation{Field: "amount", Code: "AMOUNT_TOO_LOW",
			Message: fmt.Sprintf("amount must be greater than %s", MinimumAmount)}
	}
	return nil
}

func debitWithinLimit(cmd ExecuteCommand) *Violation {
	if cmd.Type == Debit && cmd.Amount > MaximumDebitAmount {
		return &Violation{Field: "amount", Code: "DEBIT_LIMIT_EXCEEDED",
			Message: fmt.Sprintf("DEBIT amount must not exceed %s per transaction", MaximumDebitAmount)}
	}
	return nil
}

func currencySupported(cmd ExecuteCommand) *Violation {
	if cmd.Currency != CurrencyMXN {
		return &Violation{Field: "currency", Code: "UNSUPPORTED_CURRENCY",
			Message: fmt.Sprintf("currency must be %s", CurrencyMXN)}
	}
	return nil
}

func descriptionWithinLimit(cmd ExecuteCommand) *Violation {
	if utf8.RuneCountInString(cmd.Description) > maxDescriptionLength {
		return &Violation{Field: "description", Code: "TOO_LONG",
			Message: fmt.Sprintf("description must be at most %d characters", maxDescriptionLength)}
	}
	return nil
}

func checkRules(rules []Rule, cmd ExecuteCommand) error {
	var violations []Violation
	for _, rule := range rules {
		if v := rule(cmd); v != nil {
			violations = append(violations, *v)
		}
	}
	if len(violations) > 0 {
		return &ValidationError{Violations: violations}
	}
	return nil
}
