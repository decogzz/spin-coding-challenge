package transaction

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseMoney(t *testing.T) {
	tests := []struct {
		in      string
		want    Money
		wantErr error
	}{
		{in: "1500", want: 1500_00},
		{in: "1500.00", want: 1500_00},
		{in: "1500.5", want: 1500_50},
		{in: "0.01", want: 1},
		{in: "0", want: 0},
		{in: "007.10", want: 7_10},
		{in: "-3.25", want: -3_25},
		{in: "999999999999999.99", want: 999999999999999_99},
		{in: "1.001", wantErr: ErrAmountPrecision},
		{in: "1e3", wantErr: ErrInvalidAmount},
		{in: "1.", wantErr: ErrInvalidAmount},
		{in: ".5", wantErr: ErrInvalidAmount},
		{in: "", wantErr: ErrInvalidAmount},
		{in: "abc", wantErr: ErrInvalidAmount},
		{in: "1,000.00", wantErr: ErrInvalidAmount},
		{in: "1000000000000000", wantErr: ErrAmountTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseMoney(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseMoney(%q) error = %v, want %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseMoney(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestMoneyString(t *testing.T) {
	tests := map[Money]string{
		0:             "0.00",
		5:             "0.05",
		1500_00:       "1500.00",
		10_50:         "10.50",
		-3_25:         "-3.25",
		-5:            "-0.05",
		MinimumAmount: "1.00",
	}
	for in, want := range tests {
		if got := in.String(); got != want {
			t.Errorf("Money(%d).String() = %q, want %q", in, got, want)
		}
	}
}

func TestMoneyMarshalJSON(t *testing.T) {
	got, err := json.Marshal(struct {
		Amount Money `json:"amount"`
	}{Amount: 5500_00})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"amount":5500.00}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
