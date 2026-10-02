package bend2will

import "strconv"

// quoteForMessage renders a string for an error message, so that an empty or
// whitespace-only value is visible rather than invisible.
func quoteForMessage(s string) string {
	return strconv.Quote(s)
}
