package bend2will

import (
	"strconv"
	"testing"
	"time"
)

// mustDuration parses a duration or fails the test.
func mustDuration(t *testing.T, s string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(s)
	if err != nil {
		t.Fatalf("ParseDuration(%q): %v", s, err)
	}
	return d
}

// itoa keeps the platform tests free of a strconv import each.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
