// Package duration parses human-friendly durations such as "24h", "7d", "1d12h" or "3600".
package duration

import (
	"errors"
	"fmt"
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
		return check(time.Duration(secs) * time.Second)
	}
	if i := strings.IndexByte(s, 'd'); i > 0 {
		days, err := strconv.ParseInt(s[:i], 10, 64)
		if err == nil {
			d := time.Duration(days) * 24 * time.Hour
			if rest := s[i+1:]; rest != "" {
				more, err := time.ParseDuration(rest)
				if err != nil {
					return 0, fmt.Errorf("invalid duration %q", s)
				}
				d += more
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
