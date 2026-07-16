package paper

import (
	"strings"
	"testing"
	"unicode"
)

func TestNormalizeDOI(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		" https://doi.org/10.1000/ABC ":   "10.1000/abc",
		"HTTPS://DX.DOI.ORG/ 10.1000/ABC": "10.1000/abc",
		"doi: 10.1000/ABC":                "10.1000/abc",
		"10.123456789/Some.Suffix_(1)":    "10.123456789/some.suffix_(1)",
	}

	for raw, want := range tests {
		raw, want := raw, want
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeDOI(raw)
			if err != nil {
				t.Fatalf("NormalizeDOI(%q) error = %v", raw, err)
			}
			if got != want {
				t.Fatalf("NormalizeDOI(%q) = %q, want %q", raw, got, want)
			}
		})
	}
}

func TestNormalizeDOIRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		" ",
		"https://example.test/10.1000/abc",
		"11.1000/abc",
		"10.123/abc",
		"10.1234567890/abc",
		"10.1000/",
		"10.1000/a b",
		"10.1000/a\tb",
		"doi: doi:10.1000/abc",
	}

	for _, raw := range tests {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := NormalizeDOI(raw); err == nil {
				t.Fatalf("NormalizeDOI(%q) = %q, want error", raw, got)
			}
		})
	}
}

func TestNormalizeArXiv(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"2401.01234v1":                             "2401.01234",
		" arXiv:2401.01234V12 ":                    "2401.01234",
		"https://arxiv.org/abs/2401.01234v3":       "2401.01234",
		"https://arxiv.org/pdf/2401.01234v3.pdf":   "2401.01234",
		"https://arxiv.org/abs/HEP-TH/9901001v2":   "hep-th/9901001",
		"http://arxiv.org/pdf/CS.AI/9901001V9.pdf": "cs.ai/9901001",
	}

	for raw, want := range tests {
		raw, want := raw, want
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeArXiv(raw)
			if err != nil {
				t.Fatalf("NormalizeArXiv(%q) error = %v", raw, err)
			}
			if got != want {
				t.Fatalf("NormalizeArXiv(%q) = %q, want %q", raw, got, want)
			}
		})
	}
}

func TestNormalizeArXivRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"2400.01234",
		"2413.01234",
		"2401.123",
		"2401.123456",
		"2401.01234v",
		"2401.01234 v2",
		"https://arxiv.org/abs/2401.01234/extra",
		"not-an-arxiv-id",
	}

	for _, raw := range tests {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := NormalizeArXiv(raw); err == nil {
				t.Fatalf("NormalizeArXiv(%q) = %q, want error", raw, got)
			}
		})
	}
}

func TestNormalizeOpenAlex(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"W1234567890":                       "W1234567890",
		"w1234567890":                       "W1234567890",
		"https://openalex.org/w1234567890":  "W1234567890",
		" HTTP://OPENALEX.ORG/W0012345678 ": "W0012345678",
	}

	for raw, want := range tests {
		raw, want := raw, want
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeOpenAlex(raw)
			if err != nil {
				t.Fatalf("NormalizeOpenAlex(%q) error = %v", raw, err)
			}
			if got != want {
				t.Fatalf("NormalizeOpenAlex(%q) = %q, want %q", raw, got, want)
			}
		})
	}
}

func TestNormalizeOpenAlexRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"A123",
		"W",
		"W12 3",
		"https://openalex.org/works/W123",
		"https://openalex.org/W123/",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := NormalizeOpenAlex(raw); err == nil {
				t.Fatalf("NormalizeOpenAlex(%q) = %q, want error", raw, got)
			}
		})
	}
}

func TestNormalizeSemanticScholar(t *testing.T) {
	t.Parallel()

	raw := "A0B1C2D3E4F5678901234567890ABCDEFFEDCBA9"
	want := "a0b1c2d3e4f5678901234567890abcdeffedcba9"

	got, err := NormalizeSemanticScholar(raw)
	if err != nil {
		t.Fatalf("NormalizeSemanticScholar(%q) error = %v", raw, err)
	}
	if got != want {
		t.Fatalf("NormalizeSemanticScholar(%q) = %q, want %q", raw, got, want)
	}
}

func TestNormalizeSemanticScholarRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"0123456789abcdef0123456789abcdef0123456",
		"0123456789abcdef0123456789abcdef012345678",
		"g123456789abcdef0123456789abcdef01234567",
		"0123456789abcdef0123456789abcdef0123 567",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := NormalizeSemanticScholar(raw); err == nil {
				t.Fatalf("NormalizeSemanticScholar(%q) = %q, want error", raw, got)
			}
		})
	}
}

func TestNormalizeOpenReviewPreservesCase(t *testing.T) {
	t.Parallel()

	raw := " Forum_AbC123-~ "
	want := "Forum_AbC123-~"

	got, err := NormalizeOpenReview(raw)
	if err != nil {
		t.Fatalf("NormalizeOpenReview(%q) error = %v", raw, err)
	}
	if got != want {
		t.Fatalf("NormalizeOpenReview(%q) = %q, want %q", raw, got, want)
	}
}

func TestNormalizeOpenReviewRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"-Forum123",
		"Forum 123",
		"Forum/123",
		strings.Repeat("a", 256),
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := NormalizeOpenReview(raw); err == nil {
				t.Fatalf("NormalizeOpenReview(%q) = %q, want error", raw, got)
			}
		})
	}
}

func TestNewIdentifierNormalizesAndValidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		scheme Scheme
		raw    string
		want   Identifier
		key    string
	}{
		{
			name:   "doi",
			scheme: SchemeDOI,
			raw:    "doi: 10.1000/ABC",
			want:   Identifier{Scheme: SchemeDOI, Value: "10.1000/abc"},
			key:    "doi:10.1000/abc",
		},
		{
			name:   "arxiv",
			scheme: SchemeArXiv,
			raw:    "2401.01234v4",
			want:   Identifier{Scheme: SchemeArXiv, Value: "2401.01234"},
			key:    "arxiv:2401.01234",
		},
		{
			name:   "openreview",
			scheme: SchemeOpenReview,
			raw:    "Forum_AbC123",
			want:   Identifier{Scheme: SchemeOpenReview, Value: "Forum_AbC123"},
			key:    "openreview:Forum_AbC123",
		},
		{
			name:   "semantic scholar",
			scheme: SchemeSemanticScholar,
			raw:    "A0B1C2D3E4F5678901234567890ABCDEFFEDCBA9",
			want: Identifier{
				Scheme: SchemeSemanticScholar,
				Value:  "a0b1c2d3e4f5678901234567890abcdeffedcba9",
			},
			key: "s2:a0b1c2d3e4f5678901234567890abcdeffedcba9",
		},
		{
			name:   "openalex",
			scheme: SchemeOpenAlex,
			raw:    "https://openalex.org/w123",
			want:   Identifier{Scheme: SchemeOpenAlex, Value: "W123"},
			key:    "openalex:W123",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NewIdentifier(tt.scheme, tt.raw)
			if err != nil {
				t.Fatalf("NewIdentifier(%q, %q) error = %v", tt.scheme, tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("NewIdentifier(%q, %q) = %#v, want %#v", tt.scheme, tt.raw, got, tt.want)
			}
			if !got.Valid() {
				t.Fatalf("NewIdentifier(%q, %q) returned invalid identifier %#v", tt.scheme, tt.raw, got)
			}
			if got.CanonicalKey() != tt.key {
				t.Fatalf("Identifier.CanonicalKey() = %q, want %q", got.CanonicalKey(), tt.key)
			}
		})
	}
}

func TestNewIdentifierRejectsUnsupportedOrInvalidIdentifiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		scheme Scheme
		raw    string
	}{
		{scheme: Scheme("pmid"), raw: "12345"},
		{scheme: SchemeDOI, raw: "not-a-doi"},
		{scheme: SchemeArXiv, raw: "not-an-arxiv-id"},
		{scheme: SchemeOpenReview, raw: "bad forum"},
		{scheme: SchemeSemanticScholar, raw: "abc"},
		{scheme: SchemeOpenAlex, raw: "Wabc"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.scheme), func(t *testing.T) {
			t.Parallel()

			if got, err := NewIdentifier(tt.scheme, tt.raw); err == nil {
				t.Fatalf("NewIdentifier(%q, %q) = %#v, want error", tt.scheme, tt.raw, got)
			}
		})
	}

	for _, identifier := range []Identifier{
		{},
		{Scheme: SchemeDOI, Value: "not-a-doi"},
		{Scheme: Scheme("pmid"), Value: "12345"},
	} {
		if identifier.Valid() {
			t.Fatalf("Identifier.Valid() = true for %#v", identifier)
		}
	}
}

func TestCanonicalIdentityUsesDeterministicPrecedence(t *testing.T) {
	t.Parallel()

	identifiers := Identifiers{
		DOI:             []string{"10.1000/priority"},
		ArXiv:           []string{"2401.01234"},
		OpenReview:      []string{"Forum_AbC123"},
		SemanticScholar: []string{"0123456789abcdef0123456789abcdef01234567"},
		OpenAlex:        []string{"W123"},
	}

	got, ok := CanonicalIdentity(identifiers, "Planning Agents with Tool Use")
	if !ok {
		t.Fatal("CanonicalIdentity() ok = false, want true")
	}
	if got.CanonicalKey() != "doi:10.1000/priority" {
		t.Fatalf("CanonicalIdentity() = %q, want DOI identity", got.CanonicalKey())
	}
}

func TestCanonicalIdentitySkipsInvalidCandidatesWithinScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		identifiers Identifiers
		want        string
	}{
		{
			name: "doi",
			identifiers: Identifiers{
				DOI:      []string{"invalid", "doi:10.1000/valid"},
				OpenAlex: []string{"W123"},
			},
			want: "doi:10.1000/valid",
		},
		{
			name: "arxiv",
			identifiers: Identifiers{
				ArXiv:    []string{"invalid", "2401.01234v3"},
				OpenAlex: []string{"W123"},
			},
			want: "arxiv:2401.01234",
		},
		{
			name: "openreview",
			identifiers: Identifiers{
				OpenReview: []string{"bad forum", "Forum_Backup123"},
				OpenAlex:   []string{"W123"},
			},
			want: "openreview:Forum_Backup123",
		},
		{
			name: "semantic scholar",
			identifiers: Identifiers{
				SemanticScholar: []string{
					"invalid",
					"A0B1C2D3E4F5678901234567890ABCDEFFEDCBA9",
				},
				OpenAlex: []string{"W123"},
			},
			want: "s2:a0b1c2d3e4f5678901234567890abcdeffedcba9",
		},
		{
			name: "openalex",
			identifiers: Identifiers{
				OpenAlex: []string{"invalid", "https://openalex.org/w123"},
			},
			want: "openalex:W123",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := CanonicalIdentity(tt.identifiers, "same title")
			if !ok {
				t.Fatal("CanonicalIdentity() ok = false, want true")
			}
			if got.CanonicalKey() != tt.want {
				t.Fatalf("CanonicalIdentity() = %q, want %q", got.CanonicalKey(), tt.want)
			}
		})
	}
}

func TestSimilarTitlesNeverCreateIdentity(t *testing.T) {
	t.Parallel()

	if got, ok := CanonicalIdentity(Identifiers{}, "Planning Agents with Tool Use"); ok {
		t.Fatalf("CanonicalIdentity() = %#v, true for title-only evidence", got)
	}
	if got, ok := CanonicalIdentity(
		Identifiers{CanonicalKey: "doi:10.1000/not-evidence"},
		"",
	); ok {
		t.Fatalf("CanonicalIdentity() = %#v, true for arbitrary canonical_key input", got)
	}
}

func FuzzNormalizeDOI(f *testing.F) {
	for _, seed := range []string{
		"",
		"doi:10.1000/ABC",
		"https://doi.org/10.1234/example",
		"10.1000/a b",
		"\x00",
		"DOI:\t10.1000/test",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		normalized, err := NormalizeDOI(raw)
		if err != nil {
			return
		}

		if normalized == "" {
			t.Fatal("NormalizeDOI() succeeded with an empty value")
		}
		if normalized != strings.ToLower(normalized) {
			t.Fatalf("NormalizeDOI() = %q, want lowercase", normalized)
		}
		if strings.IndexFunc(normalized, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) >= 0 {
			t.Fatalf("NormalizeDOI() = %q, want no whitespace or control characters", normalized)
		}

		again, err := NormalizeDOI(normalized)
		if err != nil {
			t.Fatalf("NormalizeDOI(%q) failed after successful normalization: %v", normalized, err)
		}
		if again != normalized {
			t.Fatalf("NormalizeDOI() is not idempotent: first %q, second %q", normalized, again)
		}
	})
}
