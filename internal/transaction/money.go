package transaction

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Money is an amount in minor units (centavos). Keeping it as an integer
// avoids the rounding errors that floating point introduces for currency.
type Money int64

const maxWholeDigits = 15

var (
	ErrInvalidAmount   = errors.New("amount must be a plain decimal number")
	ErrAmountPrecision = errors.New("amount must have at most 2 decimal places")
	ErrAmountTooLarge  = errors.New("amount is too large")
)

// ParseMoney parses a decimal string such as "1500", "1500.5" or "-3.25".
// Exponents and more than two decimal places are rejected rather than rounded.
func ParseMoney(s string) (Money, error) {
	digits, negative := strings.CutPrefix(s, "-")
	whole, frac, hasDot := strings.Cut(digits, ".")

	if whole == "" || !isDigits(whole) || (hasDot && (frac == "" || !isDigits(frac))) {
		return 0, ErrInvalidAmount
	}
	if len(frac) > 2 {
		return 0, ErrAmountPrecision
	}
	whole = strings.TrimLeft(whole, "0")
	if len(whole) > maxWholeDigits {
		return 0, ErrAmountTooLarge
	}

	var units int64
	if whole != "" {
		w, err := strconv.ParseInt(whole, 10, 64)
		if err != nil {
			return 0, ErrAmountTooLarge
		}
		units = w * 100
	}
	if frac != "" {
		f, _ := strconv.ParseInt(frac+strings.Repeat("0", 2-len(frac)), 10, 64)
		units += f
	}
	if negative {
		units = -units
	}
	return Money(units), nil
}

func (m Money) String() string {
	sign := ""
	units := int64(m)
	if units < 0 {
		sign = "-"
		units = -units
	}
	return fmt.Sprintf("%s%d.%02d", sign, units/100, units%100)
}

// MarshalJSON writes the amount as a JSON number with two decimals, e.g. 1500.00.
func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(m.String()), nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
