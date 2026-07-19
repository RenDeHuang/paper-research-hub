package venueenrich

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestNormalizeTitleAppliesNFKCCaseFoldingAndUnicodeWhitespaceFolding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "NFKC compatibility forms",
			raw:  "Ｔｈｅ　Ｊｏｕｒｎａｌ",
			want: "the journal",
		},
		{
			name: "Unicode case folding",
			raw:  "Straße STRASSE",
			want: "strasse strasse",
		},
		{
			name: "Unicode whitespace",
			raw:  "\u00a0Journal\t\n\u2003of\u2028Medicine\u3000",
			want: "journal of medicine",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			first := mustNormalizeTitle(t, test.raw)
			second := mustNormalizeTitle(t, test.raw)
			if first != test.want {
				t.Fatalf("NormalizeTitle(%q) = %q, want %q", test.raw, first, test.want)
			}
			if second != first {
				t.Fatalf(
					"NormalizeTitle(%q) second result = %q, want deterministic %q",
					test.raw,
					second,
					first,
				)
			}
			if idempotent := mustNormalizeTitle(t, first); idempotent != first {
				t.Fatalf(
					"NormalizeTitle(%q) = %q, want idempotent %q",
					first,
					idempotent,
					first,
				)
			}
		})
	}
}

func TestNormalizeTitlePreservesPunctuation(t *testing.T) {
	t.Parallel()

	const raw = "Journal: A/B, C.D (E)—F"
	const want = "journal: a/b, c.d (e)—f"

	if got := mustNormalizeTitle(t, raw); got != want {
		t.Fatalf("NormalizeTitle(%q) = %q, want punctuation-preserving %q", raw, got, want)
	}
}

func TestNormalizeTitleReturnsFinalNFKCAndIsIdempotentAcrossCaseFoldBoundaries(
	t *testing.T,
) {
	t.Parallel()

	const (
		cherokeeUpper = "Ꭰ"
		cherokeeLower = "ꭰ"
	)

	upper := mustNormalizeTitle(t, "ǰ "+cherokeeUpper)
	lower := mustNormalizeTitle(t, "ǰ "+cherokeeLower)
	if upper != lower {
		t.Fatalf(
			"NormalizeTitle() Cherokee case equivalents differ: %q != %q",
			upper,
			lower,
		)
	}
	if !norm.NFKC.IsNormalString(upper) {
		t.Fatalf("NormalizeTitle() result %q is not final NFKC", upper)
	}
	if second := mustNormalizeTitle(t, upper); second != upper {
		t.Fatalf("NormalizeTitle(%q) = %q, want idempotent result", upper, second)
	}
}

func TestNormalizeTitleRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		string([]byte{0xff}),
		string([]byte{0xfe}),
		string([]byte{'J', 0xff, 'A'}),
	} {
		got, err := NormalizeTitle(raw)
		if err == nil {
			t.Fatalf("NormalizeTitle(%q) = %q, want invalid UTF-8 error", raw, got)
		}
		if got != "" {
			t.Fatalf("NormalizeTitle(%q) returned %q with error, want empty result", raw, got)
		}
	}
}

func TestNormalizeTitleDoesNotMatchHyphenReplacementPunctuationDeletionOrApproximation(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name  string
		left  string
		right string
	}{
		{
			name:  "hyphen replacement",
			left:  "Journal-of-Medicine",
			right: "Journal of Medicine",
		},
		{
			name:  "punctuation deletion",
			left:  "Journal: Medicine",
			right: "Journal Medicine",
		},
		{
			name:  "approximate title",
			left:  "Journal of Medicine",
			right: "Journal of Medical Science",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			left := mustNormalizeTitle(t, test.left)
			right := mustNormalizeTitle(t, test.right)
			if left == right {
				t.Fatalf(
					"NormalizeTitle() matched forbidden variants %q and %q as %q",
					test.left,
					test.right,
					left,
				)
			}
		})
	}
}

func mustNormalizeTitle(t *testing.T, raw string) string {
	t.Helper()

	normalized, err := NormalizeTitle(raw)
	if err != nil {
		t.Fatalf("NormalizeTitle(%q) error = %v", raw, err)
	}
	return normalized
}
