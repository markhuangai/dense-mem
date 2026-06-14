package fulltextquery

import (
	"strings"
	"unicode"
)

// PlainText converts user-facing search text into a Lucene-safe query string.
func PlainText(input string) string {
	var b strings.Builder
	lastSpace := true

	for _, r := range input {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}

	return strings.TrimSpace(b.String())
}
