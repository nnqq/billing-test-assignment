// Package money converts decimal strings into integer minor units.
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var (
	ErrEmpty      = errors.New("empty value")
	ErrMalformed  = errors.New("malformed decimal")
	ErrOutOfRange = errors.New("value out of range")
)

// ParseMinor converts a decimal string such as "315.00" or "5.3550" into minor
// units of currency, rounding half away from zero when the input carries more
// fraction digits than that currency has. It never goes through float64:
// 1608.17 * 100 is 160816.99 in binary floating point, which truncates to the
// wrong integer.
//
// The scale comes from the currency rather than a constant: SAR has two
// fraction digits, JPY none and KWD three, so a single hardcoded 100 would
// misstate two of the three by orders of magnitude.
func ParseMinor(s, currency string) (int64, error) {
	scale, err := Exponent(currency)
	if err != nil {
		return 0, err
	}

	digits := strings.TrimSpace(s)
	if digits == "" {
		return 0, ErrEmpty
	}

	negative := false
	switch digits[0] {
	case '-':
		negative = true
		digits = digits[1:]
	case '+':
		digits = digits[1:]
	}

	intPart, fracPart, hasPoint := strings.Cut(digits, ".")
	if intPart == "" && !hasPoint {
		return 0, fmt.Errorf("parse %q: %w", s, ErrMalformed)
	}
	if hasPoint && fracPart == "" {
		return 0, fmt.Errorf("parse %q: %w", s, ErrMalformed)
	}
	if intPart == "" {
		intPart = "0"
	}

	ok := isDigits(intPart) && (fracPart == "" || isDigits(fracPart))
	if !ok {
		return 0, fmt.Errorf("parse %q: %w", s, ErrMalformed)
	}

	roundUp := false
	if len(fracPart) > scale {
		roundUp = fracPart[scale] >= '5'
		fracPart = fracPart[:scale]
	}
	fracPart += strings.Repeat("0", scale-len(fracPart))

	minor, err := strconv.ParseInt(intPart+fracPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", s, ErrOutOfRange)
	}
	if roundUp {
		if minor == math.MaxInt64 {
			return 0, fmt.Errorf("round %q: %w", s, ErrOutOfRange)
		}
		minor++
	}
	if negative {
		minor = -minor
	}
	return minor, nil
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
