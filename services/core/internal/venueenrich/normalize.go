package venueenrich

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var titleCaseFolder = cases.Fold()

func NormalizeTitle(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", errors.New("title must be valid UTF-8")
	}

	folded := canonicalCaseFold(raw)

	var normalized strings.Builder
	normalized.Grow(len(folded))

	pendingSpace := false
	wroteRune := false
	for _, current := range folded {
		if unicode.IsSpace(current) {
			pendingSpace = wroteRune
			continue
		}
		if pendingSpace {
			normalized.WriteByte(' ')
			pendingSpace = false
		}
		normalized.WriteRune(current)
		wroteRune = true
	}
	return normalized.String(), nil
}

func canonicalCaseFold(raw string) string {
	current := norm.NFKC.String(raw)
	seen := make(map[string]int)
	sequence := make([]string, 0, 2)

	// Reapply fold plus NFKC until stable. If the case table exposes an
	// inverse-fold cycle, every member resolves to the same smallest NFKC form.
	for {
		next := norm.NFKC.String(titleCaseFolder.String(current))
		if next == current {
			return next
		}
		if cycleStart, exists := seen[next]; exists {
			canonical := next
			for _, candidate := range sequence[cycleStart:] {
				if candidate < canonical {
					canonical = candidate
				}
			}
			return canonical
		}
		seen[next] = len(sequence)
		sequence = append(sequence, next)
		current = next
	}
}
