package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// moneyScale is the number of sub-units per coin. Money is stored as integer
// cents so arithmetic stays exact; JSON (un)marshaling converts to and from the
// decimal coin unit (cents / moneyScale).
const MoneyScale = 100

// Cents represents a monetary amount stored as integer cents (0.01 coin).
// On the wire it serializes as a decimal coin value (e.g. 12.5) and decodes
// any decimal coefficient by rounding to the nearest cent.
type Cents int64

// NewCents converts a decimal coin amount (e.g. 1500 cents for 15.0) from an
// int64 cent count.
//
// Use FromCoins for user-supplied decimal input.
func NewCents(cents int64) Cents { return Cents(cents) }

// FromCoins parses a decimal coin amount into integer cents, rounding to the
// nearest cent. Values beyond 2 decimal places are rounded, not rejected.
func (c *Cents) FromCoins(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return errors.New("invalid money amount")
	}
	if v > math.MaxInt64/MoneyScale || v < math.MinInt64/MoneyScale {
		return errors.New("money amount out of range")
	}
	*c = Cents(math.Round(v * MoneyScale))
	return nil
}

// Int64 returns the raw cent count.
func (c Cents) Int64() int64 { return int64(c) }

// Coins returns the value as a decimal coin amount.
func (c Cents) Coins() float64 { return float64(c) / MoneyScale }

// MarshalJSON emits the value in decimal coin units (e.g. 12.5).
func (c Cents) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(c.Coins(), 'f', -1, 64)), nil
}

// UnmarshalJSON accepts a decimal coin amount as a JSON number or string,
// converting it to integer cents.
func (c *Cents) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if len(s) == 0 {
		return errors.New("empty money amount")
	}
	if s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "null" {
		*c = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("invalid money amount %q", string(b))
	}
	return c.FromCoins(f)
}
