// Package duration parses human-friendly durations such as "24h", "7d", "1d12h" or "3600".
package duration

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Parse accepts Go durations (time.ParseDuration), a "d" day suffix that may be
// followed by a Go duration ("7d", "1d12h"), or a bare integer meaning seconds.
// The result must be positive.
func Parse(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if secs, err := strconv.ParseInt(s, 10, 64); err == nil {
		d, ok := scale(secs, time.Second)
		if !ok {
			return 0, rangeError(s)
		}
		return check(d)
	}
	if i := strings.IndexByte(s, 'd'); i > 0 {
		days, err := strconv.ParseInt(s[:i], 10, 64)
		if err == nil {
			d, ok := scale(days, 24*time.Hour)
			if !ok {
				return 0, rangeError(s)
			}
			if rest := s[i+1:]; rest != "" {
				more, err := time.ParseDuration(rest)
				if err != nil {
					return 0, fmt.Errorf("invalid duration %q", s)
				}
				if d, ok = add(d, more); !ok {
					return 0, rangeError(s)
				}
			}
			return check(d)
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (try 30m, 24h, 7d)", s)
	}
	return check(d)
}

// Seconds parses s and returns whole seconds, rounding up sub-second remainders.
func Seconds(s string) (int64, error) {
	d, err := Parse(s)
	if err != nil {
		return 0, err
	}
	secs := int64(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	return secs, nil
}

func check(d time.Duration) (time.Duration, error) {
	if d <= 0 {
		return 0, errors.New("duration must be positive")
	}
	return d, nil
}

// scale converts n units of size unit into a time.Duration, reporting false if
// the product does not fit. A time.Duration is int64 nanoseconds, so it spans
// only about 292 years; without this check "1000000000000d" wrapped around to a
// positive 225-year value and was accepted as valid.
func scale(n int64, unit time.Duration) (time.Duration, bool) {
	if n == 0 {
		return 0, true
	}
	limit := int64(math.MaxInt64) / int64(unit)
	if n > limit || n < -limit {
		return 0, false
	}
	return time.Duration(n) * unit, true
}

// add sums two durations, reporting false when the result wraps.
func add(a, b time.Duration) (time.Duration, bool) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}
	return sum, true
}

func rangeError(s string) error {
	return fmt.Errorf("duration %q is out of range (the maximum is about 106751d)", s)
}
