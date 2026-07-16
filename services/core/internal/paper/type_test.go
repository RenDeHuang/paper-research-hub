package paper

import "testing"

func TestNewPaperTypeAcceptsExactlyControlledValues(t *testing.T) {
	t.Parallel()

	valid := []PaperType{
		PaperTypeResearchArticle,
		PaperTypeReview,
		PaperTypePreprint,
		PaperTypeProceedingsArticle,
		PaperTypeDataset,
		PaperTypeOther,
	}
	for _, want := range valid {
		want := want
		t.Run(string(want), func(t *testing.T) {
			t.Parallel()

			got, err := NewPaperType(string(want))
			if err != nil {
				t.Fatalf("NewPaperType(%q) error = %v", want, err)
			}
			if got != want {
				t.Fatalf("NewPaperType(%q) = %q, want %q", want, got, want)
			}
			if !got.Valid() {
				t.Fatalf("PaperType(%q).Valid() = false", got)
			}
			if got.String() != string(want) {
				t.Fatalf("PaperType(%q).String() = %q", got, got.String())
			}
		})
	}

	for _, raw := range []string{
		"",
		"benchmark",
		"research-article",
		"RESEARCH_ARTICLE",
		" research_article ",
		"proceedings",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := NewPaperType(raw); err == nil {
				t.Fatalf("NewPaperType(%q) = %q, want error", raw, got)
			}
			if PaperType(raw).Valid() {
				t.Fatalf("PaperType(%q).Valid() = true", raw)
			}
		})
	}
}
