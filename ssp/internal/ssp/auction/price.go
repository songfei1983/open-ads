package auction

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ErrInvalidPrice reports a wire price the SSP cannot convert exactly.
var ErrInvalidPrice = errors.New("invalid bid price")

// ParseCPMMicros converts a decimal CPM string into signed micro-units with
// half-even rounding at the seventh decimal place. Floats are never used.
func ParseCPMMicros(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return 0, ErrInvalidPrice
	}
	if strings.ContainsAny(value, "eE") {
		return 0, ErrInvalidPrice
	}
	integerPart, fractionPart, hasFraction := strings.Cut(value, ".")
	if integerPart == "" {
		integerPart = "0"
	}
	if !allDigits(integerPart) || (hasFraction && !allDigits(fractionPart)) {
		return 0, ErrInvalidPrice
	}
	// Normalise to exactly six fraction digits, remembering the rounding digit.
	if len(fractionPart) > 6 {
		roundDigit := fractionPart[6]
		fractionPart = fractionPart[:6]
		if roundDigit >= '5' {
			// Banker's rounding: increment only when the current micro value is odd.
			micros, err := joinMicros(integerPart, fractionPart)
			if err != nil {
				return 0, err
			}
			if micros%2 != 0 {
				micros++
			}
			return checkOverflow(micros)
		}
	} else {
		fractionPart += strings.Repeat("0", 6-len(fractionPart))
	}
	micros, err := joinMicros(integerPart, fractionPart)
	if err != nil {
		return 0, err
	}
	return checkOverflow(micros)
}

func joinMicros(integerPart, fractionPart string) (int64, error) {
	units, err := strconv.ParseInt(integerPart, 10, 64)
	if err != nil {
		return 0, ErrInvalidPrice
	}
	if units > math.MaxInt64/1_000_000 {
		return 0, ErrInvalidPrice
	}
	fraction, err := strconv.ParseInt(fractionPart, 10, 64)
	if err != nil {
		return 0, ErrInvalidPrice
	}
	return units*1_000_000 + fraction, nil
}

func checkOverflow(micros int64) (int64, error) {
	if micros < 0 {
		return 0, ErrInvalidPrice
	}
	return micros, nil
}

// FormatCPMMicros renders micros as a stable decimal string without exponent.
func FormatCPMMicros(micros int64) string {
	if micros < 0 {
		micros = 0
	}
	return fmt.Sprintf("%d.%06d", micros/1_000_000, micros%1_000_000)
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
