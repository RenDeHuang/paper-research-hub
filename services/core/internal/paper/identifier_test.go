package paper

import (
	"errors"
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
		"0704.0001":                                     "0704.0001",
		"1412.9999v1":                                   "1412.9999",
		"1501.00001V12":                                 "1501.00001",
		"2401.01234v1":                                  "2401.01234",
		" arXiv:2401.01234V12 ":                         "2401.01234",
		"https://arxiv.org/abs/2401.01234v3":            "2401.01234",
		"https://arxiv.org/pdf/2401.01234v3.pdf":        "2401.01234",
		"https://arxiv.org/abs/HEP-TH/9108001v2":        "hep-th/9108001",
		"https://arxiv.org/abs/math/0703001":            "math/0703001",
		"https://arxiv.org/abs/math.CA/0611800v2":       "math/0611800",
		"http://arxiv.org/pdf/CS.AI/9901001V9.pdf":      "cs/9901001",
		"arXiv:physics.soc-ph/9901001":                  "physics/9901001",
		"https://arxiv.org/abs/cond-mat.str-el/0611001": "cond-mat/0611001",
		"https://arxiv.org/abs/astro-ph.GA/0611001.pdf": "astro-ph/0611001",
		"https://arxiv.org/abs/q-bio.NC/0611001v10.pdf": "q-bio/0611001",
		"https://arxiv.org/abs/nlin.AO/0611001V10.pdf":  "nlin/0611001",
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
		"0703.0001",
		"0704.00001",
		"1412.00001",
		"1501.0001",
		"2400.01234",
		"2413.01234",
		"2401.123",
		"2401.123456",
		"2401.00000",
		"2401.01234v",
		"2401.01234v0",
		"2401.01234v00",
		"2401.01234 v2",
		"hep-th/9107001",
		"math/0704001",
		"math/0801001",
		"math/061100",
		"math/06110001",
		"math/0611000",
		"unknown.AI/0611001",
		"hep-th.X/0611001",
		"q-fin.ST/0611001",
		"stat.ML/0611001",
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
		value  string
		key    string
	}{
		{
			name:   "doi",
			scheme: SchemeDOI,
			raw:    "doi: 10.1000/ABC",
			value:  "10.1000/abc",
			key:    "doi:10.1000/abc",
		},
		{
			name:   "arxiv",
			scheme: SchemeArXiv,
			raw:    "2401.01234v4",
			value:  "2401.01234",
			key:    "arxiv:2401.01234",
		},
		{
			name:   "openreview",
			scheme: SchemeOpenReview,
			raw:    "Forum_AbC123",
			value:  "Forum_AbC123",
			key:    "openreview:Forum_AbC123",
		},
		{
			name:   "semantic scholar",
			scheme: SchemeSemanticScholar,
			raw:    "A0B1C2D3E4F5678901234567890ABCDEFFEDCBA9",
			value:  "a0b1c2d3e4f5678901234567890abcdeffedcba9",
			key:    "s2:a0b1c2d3e4f5678901234567890abcdeffedcba9",
		},
		{
			name:   "openalex",
			scheme: SchemeOpenAlex,
			raw:    "https://openalex.org/w123",
			value:  "W123",
			key:    "openalex:W123",
		},
		{
			name:   "pmid",
			scheme: SchemePMID,
			raw:    " 12345678 ",
			value:  "12345678",
			key:    "pmid:12345678",
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
			if got.Scheme() != tt.scheme {
				t.Fatalf("Identifier.Scheme() = %q, want %q", got.Scheme(), tt.scheme)
			}
			if got.Value() != tt.value {
				t.Fatalf("Identifier.Value() = %q, want %q", got.Value(), tt.value)
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
	} {
		if identifier.Valid() {
			t.Fatalf("Identifier.Valid() = true for %#v", identifier)
		}
	}
}

func TestNormalizePMIDTrimsSurroundingWhitespace(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		" 12345 ":           "12345",
		"\t12345\n":         "12345",
		"\u00a012345\u00a0": "12345",
	} {
		raw, want := raw, want
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizePMID(raw)
			if err != nil {
				t.Fatalf("NormalizePMID(%q) error = %v", raw, err)
			}
			if got != want {
				t.Fatalf("NormalizePMID(%q) = %q, want %q", raw, got, want)
			}
		})
	}
}

func TestNewIdentifierRejectsInvalidPMIDValues(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		" \t\n ",
		"0",
		"012345",
		"+12345",
		"-12345",
		"123.45",
		"12 345",
		"12\t345",
		"PMID:12345",
		"pmid:12345",
		"https://pubmed.ncbi.nlm.nih.gov/12345/",
		"１２３４５",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := NewIdentifier(SchemePMID, raw); err == nil {
				t.Fatalf("NewIdentifier(%q, %q) = %#v, want error", SchemePMID, raw, got)
			}
		})
	}
}

func TestCanonicalIdentityRejectsConflictingPMIDsAfterTrimmingWhitespace(t *testing.T) {
	t.Parallel()

	got, err := CanonicalIdentity(Identifiers{
		PMID: []string{" 12345678 ", "\t12345678\n", " 87654321 "},
	}, "")
	if !errors.Is(err, ErrConflictingIdentifiers) {
		t.Fatalf(
			"CanonicalIdentity() = %#v, %v, want ErrConflictingIdentifiers",
			got,
			err,
		)
	}
	if got.Valid() {
		t.Fatalf("CanonicalIdentity() returned valid identity %q on normalized PMID conflict", got)
	}
}

func TestCanonicalIdentityUsesPMIDFallbackAfterDOI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		identifiers Identifiers
		want        string
	}{
		{
			name: "pmid without doi",
			identifiers: Identifiers{
				PMID: []string{"12345678"},
			},
			want: "pmid:12345678",
		},
		{
			name: "doi over pmid",
			identifiers: Identifiers{
				DOI:  []string{"10.1000/priority"},
				PMID: []string{"12345678"},
			},
			want: "doi:10.1000/priority",
		},
		{
			name: "pmid after invalid doi",
			identifiers: Identifiers{
				DOI:  []string{"invalid"},
				PMID: []string{"12345678"},
			},
			want: "pmid:12345678",
		},
		{
			name: "pmid before non-doi schemes",
			identifiers: Identifiers{
				PMID:  []string{"12345678"},
				ArXiv: []string{"2401.01234"},
			},
			want: "pmid:12345678",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CanonicalIdentity(tt.identifiers, "")
			if err != nil {
				t.Fatalf("CanonicalIdentity() error = %v", err)
			}
			if got.CanonicalKey() != tt.want {
				t.Fatalf("CanonicalIdentity() = %q, want %q", got.CanonicalKey(), tt.want)
			}
		})
	}
}

func TestCanonicalIdentityRejectsConflictingPMIDsEvenWithDOI(t *testing.T) {
	t.Parallel()

	got, err := CanonicalIdentity(Identifiers{
		DOI:  []string{"10.1000/priority"},
		PMID: []string{"12345678", "87654321"},
	}, "")
	if !errors.Is(err, ErrConflictingIdentifiers) {
		t.Fatalf(
			"CanonicalIdentity() = %#v, %v, want ErrConflictingIdentifiers",
			got,
			err,
		)
	}
	if got.Valid() {
		t.Fatalf("CanonicalIdentity() returned valid identity %q on PMID conflict", got)
	}
}

func TestCanonicalIdentityUsesDeterministicPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		identifiers Identifiers
		want        string
	}{
		{
			name: "doi over arxiv",
			identifiers: Identifiers{
				DOI:   []string{"10.1000/priority"},
				ArXiv: []string{"2401.01234"},
			},
			want: "doi:10.1000/priority",
		},
		{
			name: "arxiv over openreview without doi",
			identifiers: Identifiers{
				ArXiv:      []string{"2401.01234"},
				OpenReview: []string{"Forum_AbC123"},
			},
			want: "arxiv:2401.01234",
		},
		{
			name: "openreview over semantic scholar without doi or arxiv",
			identifiers: Identifiers{
				OpenReview:      []string{"Forum_AbC123"},
				SemanticScholar: []string{"0123456789abcdef0123456789abcdef01234567"},
			},
			want: "openreview:Forum_AbC123",
		},
		{
			name: "semantic scholar over openalex",
			identifiers: Identifiers{
				SemanticScholar: []string{"0123456789abcdef0123456789abcdef01234567"},
				OpenAlex:        []string{"W123"},
			},
			want: "s2:0123456789abcdef0123456789abcdef01234567",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CanonicalIdentity(tt.identifiers, "Planning Agents with Tool Use")
			if err != nil {
				t.Fatalf("CanonicalIdentity() error = %v", err)
			}
			if got.CanonicalKey() != tt.want {
				t.Fatalf("CanonicalIdentity() = %q, want %q", got.CanonicalKey(), tt.want)
			}
		})
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

			got, err := CanonicalIdentity(tt.identifiers, "same title")
			if err != nil {
				t.Fatalf("CanonicalIdentity() error = %v", err)
			}
			if got.CanonicalKey() != tt.want {
				t.Fatalf("CanonicalIdentity() = %q, want %q", got.CanonicalKey(), tt.want)
			}
		})
	}
}

func TestCanonicalIdentityDeduplicatesAllNormalizedCandidatesWithinScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		identifiers Identifiers
		want        string
	}{
		{
			name: "doi aliases",
			identifiers: Identifiers{
				DOI:  []string{"doi:10.1000/ABC", "https://doi.org/10.1000/abc"},
				DOIs: []string{" 10.1000/abc "},
			},
			want: "doi:10.1000/abc",
		},
		{
			name: "arxiv versions and internal subject class",
			identifiers: Identifiers{
				ArXiv:    []string{"math.CA/0611800v1", "math/0611800"},
				ArXivIDs: []string{"https://arxiv.org/abs/math/0611800v3"},
			},
			want: "arxiv:math/0611800",
		},
		{
			name: "openreview aliases",
			identifiers: Identifiers{
				OpenReview:         []string{"Forum_AbC123", " Forum_AbC123 "},
				OpenReviewForumIDs: []string{"Forum_AbC123"},
			},
			want: "openreview:Forum_AbC123",
		},
		{
			name: "semantic scholar case",
			identifiers: Identifiers{
				SemanticScholar: []string{
					"A0B1C2D3E4F5678901234567890ABCDEFFEDCBA9",
				},
				SemanticScholarPaperIDs: []string{
					"a0b1c2d3e4f5678901234567890abcdeffedcba9",
				},
			},
			want: "s2:a0b1c2d3e4f5678901234567890abcdeffedcba9",
		},
		{
			name: "openalex url and direct id",
			identifiers: Identifiers{
				OpenAlex:    []string{"https://openalex.org/w123"},
				OpenAlexIDs: []string{"W123"},
			},
			want: "openalex:W123",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CanonicalIdentity(tt.identifiers, "")
			if err != nil {
				t.Fatalf("CanonicalIdentity() error = %v", err)
			}
			if got.CanonicalKey() != tt.want {
				t.Fatalf("CanonicalIdentity() = %q, want %q", got.CanonicalKey(), tt.want)
			}
		})
	}
}

func TestCanonicalIdentityRejectsConflictingValidCandidatesWithinAnyScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		identifiers Identifiers
	}{
		{
			name: "doi",
			identifiers: Identifiers{
				DOI: []string{"10.1000/one", "10.1000/two"},
			},
		},
		{
			name: "arxiv",
			identifiers: Identifiers{
				ArXiv: []string{"2401.00001", "2401.00002"},
			},
		},
		{
			name: "openreview",
			identifiers: Identifiers{
				OpenReview: []string{"Forum_One", "Forum_Two"},
			},
		},
		{
			name: "semantic scholar",
			identifiers: Identifiers{
				SemanticScholar: []string{
					"0123456789abcdef0123456789abcdef01234567",
					"1123456789abcdef0123456789abcdef01234567",
				},
			},
		},
		{
			name: "openalex",
			identifiers: Identifiers{
				OpenAlex: []string{"W123", "W456"},
			},
		},
		{
			name: "lower priority conflict is not hidden by doi",
			identifiers: Identifiers{
				DOI:      []string{"10.1000/priority"},
				OpenAlex: []string{"W123", "W456"},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CanonicalIdentity(tt.identifiers, "")
			if !errors.Is(err, ErrConflictingIdentifiers) {
				t.Fatalf(
					"CanonicalIdentity() = %#v, %v, want ErrConflictingIdentifiers",
					got,
					err,
				)
			}
			if got.Valid() {
				t.Fatalf("CanonicalIdentity() returned valid identity %q on conflict", got)
			}
		})
	}
}

func TestSimilarTitlesNeverCreateIdentity(t *testing.T) {
	t.Parallel()

	if got, err := CanonicalIdentity(
		Identifiers{},
		"Planning Agents with Tool Use",
	); !errors.Is(err, ErrNoCanonicalIdentity) {
		t.Fatalf("CanonicalIdentity() = %#v, %v, want ErrNoCanonicalIdentity", got, err)
	}
	if got, err := CanonicalIdentity(
		Identifiers{CanonicalKey: "doi:10.1000/not-evidence"},
		"",
	); !errors.Is(err, ErrNoCanonicalIdentity) {
		t.Fatalf("CanonicalIdentity() = %#v, %v for arbitrary canonical_key input", got, err)
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
