package venueenrich

import "testing"

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

			first := NormalizeTitle(test.raw)
			second := NormalizeTitle(test.raw)
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
			if idempotent := NormalizeTitle(first); idempotent != first {
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

	if got := NormalizeTitle(raw); got != want {
		t.Fatalf("NormalizeTitle(%q) = %q, want punctuation-preserving %q", raw, got, want)
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

			left := NormalizeTitle(test.left)
			right := NormalizeTitle(test.right)
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
