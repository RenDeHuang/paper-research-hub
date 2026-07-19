package venueenrich

import (
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var titleCaseFolder = cases.Fold()

func NormalizeTitle(raw string) string {
	folded := titleCaseFolder.String(norm.NFKC.String(raw))

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
	return normalized.String()
}
