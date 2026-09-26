package contract

import (
	"math"
	"math/big"
	"strconv"
)

// All money is integer minor units (paise, cents). No floats anywhere.

// MaxAmountMinor caps any single amount: 10^15 minor units
// (10 trillion rupees or dollars). Anything above is rejected as overflow
// before it can reach arithmetic.
const MaxAmountMinor int64 = 1_000_000_000_000_000

// RateScale: a rate is quoted as INR per 1 USD, times 10^6.
// Example: 83.250000 INR/USD is rateMicros = 83_250_000.
const RateScale int64 = 1_000_000

// MaxRateMicros bounds a sane USD/INR rate (1,000,000 INR per USD).
const MaxRateMicros int64 = 1_000_000 * RateScale

// ParseAmount strictly parses a positive integer amount in minor units.
// Accepts only plain decimal digits: no sign, no decimal point, no exponent,
// no whitespace, no leading zeros. Rejects zero and negatives.
func ParseAmount(field, s string) (int64, error) {
	if s == "" {
		return 0, reject(ErrInvalidAmount, "%s is empty; expected a positive integer in minor units", field)
	}
	if s[0] == '-' {
		return 0, reject(ErrInvalidAmount, "%s is negative (%q); amounts must be positive", field, s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, reject(ErrInvalidAmount, "%s %q is not a plain integer in minor units", field, s)
		}
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, reject(ErrInvalidAmount, "%s %q has leading zeros", field, s)
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// Only digits got here, so the only failure is out-of-range.
		return 0, reject(ErrAmountOverflow, "%s %q does not fit in 64 bits", field, s)
	}
	if v == 0 {
		return 0, reject(ErrInvalidAmount, "%s is zero; amounts must be positive", field)
	}
	if v > MaxAmountMinor {
		return 0, reject(ErrAmountOverflow, "%s %d exceeds the per-amount cap of %d minor units", field, v, MaxAmountMinor)
	}
	return v, nil
}

// addChecked returns a+b or an overflow rejection.
func addChecked(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, reject(ErrAmountOverflow, "balance arithmetic overflow (%d + %d)", a, b)
	}
	return a + b, nil
}

// ConvertUSDToINR returns the INR leg (paise) implied by a USD leg (cents)
// at rateMicros, rounded half-up. Both currencies have 2 decimal places,
// so paise = cents * (INR per USD). Computed with big.Int so no step can
// overflow; the result is range-checked.
func ConvertUSDToINR(usdMinor, rateMicros int64) (int64, error) {
	p := new(big.Int).Mul(big.NewInt(usdMinor), big.NewInt(rateMicros))
	p.Add(p, big.NewInt(RateScale/2))
	p.Quo(p, big.NewInt(RateScale))
	if !p.IsInt64() || p.Int64() > MaxAmountMinor {
		return 0, reject(ErrAmountOverflow, "INR leg for %d cents at rate %d exceeds the per-amount cap", usdMinor, rateMicros)
	}
	if p.Int64() <= 0 {
		return 0, reject(ErrInvalidAmount, "INR leg for %d cents at rate %d rounds to zero", usdMinor, rateMicros)
	}
	return p.Int64(), nil
}
