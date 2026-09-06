package duration

import (
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
