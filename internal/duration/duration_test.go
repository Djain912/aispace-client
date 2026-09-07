package duration

import (
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"24h", 24 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"1h30m", 90 * time.Minute, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"1d12h", 36 * time.Hour, false},
		{"3600", time.Hour, false},
		{" 2d ", 48 * time.Hour, false},
		{"", 0, true},
		{"0", 0, true},
		{"-1h", 0, true},
		{"abc", 0, true},
		{"1dx", 0, true},
		{"d", 0, true},
		{"1.5d", 0, true},
		// Largest values that still fit in an int64 nanosecond duration, and the
		// first ones past the edge. These used to wrap around silently.
		{"9223372036", 9223372036 * time.Second, false},
		{"9223372037", 0, true},
		{"106751d", 106751 * 24 * time.Hour, false},
		{"106752d", 0, true},
		{"1000000000000d", 0, true},
		{"106751d23h", 2562047 * time.Hour, false},
		{"106751d24h", 0, true},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if c.err {
			if err == nil {
				t.Errorf("Parse(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSeconds(t *testing.T) {
	if s, err := Seconds("1d"); err != nil || s != 86400 {
		t.Fatalf("Seconds(1d) = %d, %v", s, err)
	}
	if s, err := Seconds("1500ms"); err != nil || s != 2 {
		t.Fatalf("Seconds(1500ms) = %d, %v; want rounding up to 2", s, err)
	}
	if _, err := Seconds("nope"); err == nil {
		t.Fatal("expected error")
	}
}

// A duration too large for time.Duration must be rejected rather than wrapping
// around into a small positive value. "1000000000000d" used to parse as roughly
// 225 years and be sent to the server as a valid X-Expires-In.
func TestParseRejectsOverflowInsteadOfWrapping(t *testing.T) {
	for _, in := range []string{"1000000000000d", "9223372036854775807", "10000000000", "106752d"} {
		got, err := Parse(in)
		if err == nil {
			t.Errorf("Parse(%q) = %v, want an error", in, got)
			continue
		}
		if !strings.Contains(err.Error(), "out of range") {
			t.Errorf("Parse(%q) error = %q, want it to mention the range", in, err)
		}
	}
}

// Overflow must not be reported as a sign problem: the old code only noticed
// wrapped values when they happened to land on a non-positive number.
func TestParseDistinguishesOverflowFromNonPositive(t *testing.T) {
	if _, err := Parse("0"); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Errorf("Parse(0) error = %v, want a positive-value error", err)
	}
	if _, err := Seconds("1000000000000d"); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("Seconds overflow error = %v, want an out-of-range error", err)
	}
}
