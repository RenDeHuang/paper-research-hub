package abstractanalysis

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/text/unicode/norm"
)

func ValidateEvidence(abstract string, result Result) error {
	normalizedAbstract := normalizeEvidenceText(abstract)
	if normalizedAbstract == "" {
		return errors.New("abstract evidence input is required")
	}
	if err := result.ValidateShape(); err != nil {
		return err
	}

	for _, field := range result.fields() {
		if field.Value.State == StateNotReported {
			continue
		}
		seen := make(map[string]struct{}, len(field.Value.Evidence))
		for index, snippet := range field.Value.Evidence {
			if strings.TrimSpace(snippet) == "" {
				return fmt.Errorf(
					"abstract route field %s evidence %d must not be empty",
					field.Name,
					index+1,
				)
			}
			if snippet != strings.TrimSpace(snippet) {
				return fmt.Errorf(
					"abstract route field %s evidence %d must be trimmed",
					field.Name,
					index+1,
				)
			}
			normalizedSnippet := normalizeEvidenceText(snippet)
			if _, duplicate := seen[normalizedSnippet]; duplicate {
				return fmt.Errorf(
					"abstract route field %s has duplicate evidence %q",
					field.Name,
					snippet,
				)
			}
			seen[normalizedSnippet] = struct{}{}
			if !strings.Contains(normalizedAbstract, normalizedSnippet) {
				return fmt.Errorf(
					"abstract route field %s evidence %d was not found in the input abstract",
					field.Name,
					index+1,
				)
			}
		}
	}
	return nil
}

func normalizeEvidenceText(value string) string {
	return strings.Join(strings.Fields(norm.NFC.String(value)), " ")
}
