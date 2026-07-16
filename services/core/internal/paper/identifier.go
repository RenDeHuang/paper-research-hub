package paper

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type Scheme string

const (
	SchemeDOI             Scheme = "doi"
	SchemeArXiv           Scheme = "arxiv"
	SchemeOpenReview      Scheme = "openreview"
	SchemeSemanticScholar Scheme = "s2"
	SchemeS2              Scheme = SchemeSemanticScholar
	SchemeOpenAlex        Scheme = "openalex"
)

var (
	doiURLPrefix      = regexp.MustCompile(`(?i)^https?://(?:dx\.)?doi\.org/`)
	doiLabelPrefix    = regexp.MustCompile(`(?i)^doi\s*:`)
	doiValuePattern   = regexp.MustCompile(`^10\.[0-9]{4,9}/\S+$`)
	arXivURLPrefix    = regexp.MustCompile(`(?i)^https?://arxiv\.org/(?:abs|pdf)/`)
	arXivLabelPrefix  = regexp.MustCompile(`(?i)^arxiv\s*:`)
	arXivVersion      = regexp.MustCompile(`(?i)v[0-9]+$`)
	arXivModernValue  = regexp.MustCompile(`^([0-9]{2})([0-9]{2})\.[0-9]{4,5}$`)
	arXivLegacyValue  = regexp.MustCompile(`^[a-z][a-z0-9.-]*/([0-9]{2})([0-9]{2})[0-9]{3}$`)
	openAlexURLPrefix = regexp.MustCompile(`(?i)^https?://openalex\.org/`)
	openAlexValue     = regexp.MustCompile(`^W[0-9]+$`)
	semanticScholarID = regexp.MustCompile(`^[0-9a-f]{40}$`)
	openReviewForumID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,254}$`)
)

type Identifier struct {
	Scheme Scheme
	Value  string
}

type Identifiers struct {
	DOI                     []string
	DOIs                    []string
	ArXiv                   []string
	ArXivIDs                []string
	OpenReview              []string
	OpenReviewForumIDs      []string
	SemanticScholar         []string
	SemanticScholarPaperIDs []string
	OpenAlex                []string
	OpenAlexIDs             []string

	// CanonicalKey is intentionally ignored by CanonicalIdentity. A stored or
	// source-provided key is not independent identity evidence.
	CanonicalKey string
}

func NewIdentifier(scheme Scheme, raw string) (Identifier, error) {
	var (
		value string
		err   error
	)

	switch scheme {
	case SchemeDOI:
		value, err = NormalizeDOI(raw)
	case SchemeArXiv:
		value, err = NormalizeArXiv(raw)
	case SchemeOpenReview:
		value, err = NormalizeOpenReview(raw)
	case SchemeSemanticScholar:
		value, err = NormalizeSemanticScholar(raw)
	case SchemeOpenAlex:
		value, err = NormalizeOpenAlex(raw)
	default:
		return Identifier{}, fmt.Errorf("unsupported identifier scheme %q", scheme)
	}
	if err != nil {
		return Identifier{}, err
	}

	return Identifier{Scheme: scheme, Value: value}, nil
}

func (identifier Identifier) Valid() bool {
	normalized, err := NewIdentifier(identifier.Scheme, identifier.Value)
	return err == nil && normalized == identifier
}

func (identifier Identifier) CanonicalKey() string {
	if !identifier.Valid() {
		return ""
	}
	return string(identifier.Scheme) + ":" + identifier.Value
}

func (identifier Identifier) String() string {
	return identifier.CanonicalKey()
}

func CanonicalIdentity(identifiers Identifiers, _ string) (Identifier, bool) {
	candidates := []struct {
		scheme Scheme
		values []string
	}{
		{
			scheme: SchemeDOI,
			values: joinedCandidates(identifiers.DOI, identifiers.DOIs),
		},
		{
			scheme: SchemeArXiv,
			values: joinedCandidates(identifiers.ArXiv, identifiers.ArXivIDs),
		},
		{
			scheme: SchemeOpenReview,
			values: joinedCandidates(identifiers.OpenReview, identifiers.OpenReviewForumIDs),
		},
		{
			scheme: SchemeSemanticScholar,
			values: joinedCandidates(
				identifiers.SemanticScholar,
				identifiers.SemanticScholarPaperIDs,
			),
		},
		{
			scheme: SchemeOpenAlex,
			values: joinedCandidates(identifiers.OpenAlex, identifiers.OpenAlexIDs),
		},
	}

	for _, group := range candidates {
		for _, raw := range group.values {
			identifier, err := NewIdentifier(group.scheme, raw)
			if err == nil {
				return identifier, true
			}
		}
	}

	return Identifier{}, false
}

func NormalizeDOI(raw string) (string, error) {
	normalized := strings.TrimSpace(raw)
	normalized = doiURLPrefix.ReplaceAllString(normalized, "")
	normalized = doiLabelPrefix.ReplaceAllString(normalized, "")
	normalized = strings.TrimSpace(normalized)
	normalized = strings.ToLower(normalized)

	if containsSpaceOrControl(normalized) || !doiValuePattern.MatchString(normalized) {
		return "", fmt.Errorf("invalid DOI %q", raw)
	}
	return normalized, nil
}

func NormalizeArXiv(raw string) (string, error) {
	normalized := strings.TrimSpace(raw)
	normalized = arXivURLPrefix.ReplaceAllString(normalized, "")
	normalized = arXivLabelPrefix.ReplaceAllString(normalized, "")
	normalized = strings.TrimSpace(normalized)
	if len(normalized) >= len(".pdf") &&
		strings.EqualFold(normalized[len(normalized)-len(".pdf"):], ".pdf") {
		normalized = normalized[:len(normalized)-len(".pdf")]
	}
	normalized = arXivVersion.ReplaceAllString(normalized, "")
	normalized = strings.ToLower(normalized)

	if containsSpaceOrControl(normalized) || !validArXivValue(normalized) {
		return "", fmt.Errorf("invalid arXiv identifier %q", raw)
	}
	return normalized, nil
}

func NormalizeOpenReview(raw string) (string, error) {
	normalized := strings.TrimSpace(raw)
	if containsSpaceOrControl(normalized) || !openReviewForumID.MatchString(normalized) {
		return "", fmt.Errorf("invalid OpenReview forum identifier %q", raw)
	}
	return normalized, nil
}

func NormalizeSemanticScholar(raw string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if containsSpaceOrControl(normalized) || !semanticScholarID.MatchString(normalized) {
		return "", fmt.Errorf("invalid Semantic Scholar paper identifier %q", raw)
	}
	return normalized, nil
}

func NormalizeOpenAlex(raw string) (string, error) {
	normalized := strings.TrimSpace(raw)
	normalized = openAlexURLPrefix.ReplaceAllString(normalized, "")
	normalized = strings.ToUpper(strings.TrimSpace(normalized))
	if containsSpaceOrControl(normalized) || !openAlexValue.MatchString(normalized) {
		return "", fmt.Errorf("invalid OpenAlex work identifier %q", raw)
	}
	return normalized, nil
}

func validArXivValue(value string) bool {
	if matches := arXivModernValue.FindStringSubmatch(value); matches != nil {
		return validMonth(matches[2])
	}
	if matches := arXivLegacyValue.FindStringSubmatch(value); matches != nil {
		return validMonth(matches[2])
	}
	return false
}

func validMonth(value string) bool {
	month, err := strconv.Atoi(value)
	return err == nil && month >= 1 && month <= 12
}

func containsSpaceOrControl(value string) bool {
	return value == "" || strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0
}

func joinedCandidates(groups ...[]string) []string {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	values := make([]string, 0, total)
	for _, group := range groups {
		values = append(values, group...)
	}
	return values
}
