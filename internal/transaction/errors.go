package transaction

import (
	"errors"
	"fmt"
	"strings"
)

var ErrNotFound = errors.New("transaction not found")

// Violation describes one invalid field or broken business rule.
type Violation struct {
	Field   string
	Code    string
	Message string
}

// ValidationError is returned when a request is rejected before reaching the provider.
type ValidationError struct {
	Violations []Violation
}

func (e *ValidationError) Error() string {
	msgs := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		msgs[i] = v.Message
	}
	return "validation failed: " + strings.Join(msgs, "; ")
}

// RejectionError is returned by a Provider when it refuses a transaction for a
// business reason (for example insufficient funds or a blocked account).
type RejectionError struct {
	Code    string
	Message string
}

func (e *RejectionError) Error() string {
	return fmt.Sprintf("provider rejected transaction: %s: %s", e.Code, e.Message)
}
