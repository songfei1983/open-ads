package domain

import (
	"errors"
	"fmt"
	"math"
)

// Micros represents monetary amounts in micro-units of the primary currency (1 unit = 1,000,000 micros).
// Stored as BIGINT SIGNED in MariaDB.
type Micros int64

const (
	MicrosPerUnit Micros = 1_000_000
)

// ErrNegativeAmount indicates a negative amount where only non-negative values are permitted.
var ErrNegativeAmount = errors.New("monetary amount must be non-negative")

// ValidateNonNegative verifies amount >= 0.
func (m Micros) ValidateNonNegative() error {
	if m < 0 {
		return fmt.Errorf("%w: %d", ErrNegativeAmount, m)
	}
	return nil
}

// RoundHalfEven performs banker's rounding to nearest integer.
func RoundHalfEven(val float64) int64 {
	floor := math.Floor(val)
	diff := val - floor

	if diff > 0.5 {
		return int64(floor) + 1
	}
	if diff < 0.5 {
		return int64(floor)
	}
	if int64(floor)%2 != 0 {
		return int64(floor) + 1
	}
	return int64(floor)
}

// CalculateSpendFromCPM calculates spend in micros from CPM in micros and quantity.
func CalculateSpendFromCPM(cpmMicros Micros, quantity int64) (Micros, error) {
	if err := cpmMicros.ValidateNonNegative(); err != nil {
		return 0, err
	}
	if quantity < 0 {
		return 0, errors.New("quantity must be non-negative")
	}
	if quantity == 0 {
		return 0, nil
	}

	val := float64(cpmMicros) * float64(quantity) / 1000.0
	rounded := RoundHalfEven(val)
	return Micros(rounded), nil
}
