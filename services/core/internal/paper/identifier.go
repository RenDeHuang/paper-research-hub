package paper

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
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
	SchemePMID            Scheme = "pmid"
)

var (
	ErrNoCanonicalIdentity    = errors.New("no canonical identity")
	ErrConflictingIdentifiers = errors.New("conflicting identifiers")
)

var (
	doiURLPrefix      = regexp.MustCompile(`(?i)^https?://(?:dx\.)?doi\.org/`)
	doiLabelPrefix    = regexp.MustCompile(`(?i)^doi\s*:`)
	doiValuePattern   = regexp.MustCompile(`^10\.[0-9]{4,9}/\S+$`)
	arXivURLPrefix    = regexp.MustCompile(`(?i)^https?://arxiv\.org/(?:abs|pdf)/`)
	arXivLabelPrefix  = regexp.MustCompile(`(?i)^arxiv\s*:`)
	arXivVersion      = regexp.MustCompile(`(?i)v([0-9]+)$`)
	arXivModernValue  = regexp.MustCompile(`^([0-9]{2})([0-9]{2})\.([0-9]+)$`)
	arXivLegacyValue  = regexp.MustCompile(`^([a-z][a-z0-9.-]*)/([0-9]{2})([0-9]{2})([0-9]{3})$`)
	arXivSubjectClass = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	openAlexURLPrefix = regexp.MustCompile(`(?i)^https?://openalex\.org/`)
	openAlexValue     = regexp.MustCompile(`^W[0-9]+$`)
	semanticScholarID = regexp.MustCompile(`^[0-9a-f]{40}$`)
	openReviewForumID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,254}$`)
	// PubMed PMIDs are positive ASCII decimals; zero and leading zeroes are not canonical.
	pmidValue = regexp.MustCompile(`^[1-9][0-9]*$`)
)

type Identifier struct {
	scheme Scheme
	value  string
}

type Identifiers struct {
	DOI                     []string
	DOIs                    []string
	PMID                    []string
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
	case SchemePMID:
		value, err = NormalizePMID(raw)
	default:
		return Identifier{}, fmt.Errorf("unsupported identifier scheme %q", scheme)
	}
	if err != nil {
		return Identifier{}, err
	}

	return Identifier{scheme: scheme, value: value}, nil
}

func (identifier Identifier) Valid() bool {
	normalized, err := NewIdentifier(identifier.scheme, identifier.value)
	return err == nil && normalized == identifier
}

func (identifier Identifier) Scheme() Scheme {
	return identifier.scheme
}

func (identifier Identifier) Value() string {
	return identifier.value
}

func (identifier Identifier) CanonicalKey() string {
	if !identifier.Valid() {
		return ""
	}
	return string(identifier.scheme) + ":" + identifier.value
}

func (identifier Identifier) String() string {
	return identifier.CanonicalKey()
}

func CanonicalIdentity(identifiers Identifiers, _ string) (Identifier, error) {
	candidates := []struct {
		scheme Scheme
		values []string
	}{
		{
			scheme: SchemeDOI,
			values: joinedCandidates(identifiers.DOI, identifiers.DOIs),
		},
		{
			scheme: SchemePMID,
			values: identifiers.PMID,
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

	resolved := make([]Identifier, len(candidates))
	for index, group := range candidates {
		unique := make(map[string]Identifier)
		for _, raw := range group.values {
			identifier, err := NewIdentifier(group.scheme, raw)
			if err == nil {
				unique[identifier.Value()] = identifier
			}
		}

		if len(unique) > 1 {
			values := make([]string, 0, len(unique))
			for value := range unique {
				values = append(values, value)
			}
			sort.Strings(values)
			return Identifier{}, fmt.Errorf(
				"%w: scheme %s has values %s",
				ErrConflictingIdentifiers,
				group.scheme,
				strings.Join(values, ", "),
			)
		}
		for _, identifier := range unique {
			resolved[index] = identifier
		}
	}

	for _, identifier := range resolved {
		if identifier.Valid() {
			return identifier, nil
		}
	}

	return Identifier{}, ErrNoCanonicalIdentity
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
	normalized = strings.ToLower(normalized)
	normalized, err := removeArXivVersion(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid arXiv identifier %q: %w", raw, err)
	}

	if containsSpaceOrControl(normalized) {
		return "", fmt.Errorf("invalid arXiv identifier %q", raw)
	}
	if modern, ok := normalizeModernArXiv(normalized); ok {
		return modern, nil
	}
	if legacy, ok := normalizeLegacyArXiv(normalized); ok {
		return legacy, nil
	}
	return "", fmt.Errorf("invalid arXiv identifier %q", raw)
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

func NormalizePMID(raw string) (string, error) {
	if !pmidValue.MatchString(raw) {
		return "", fmt.Errorf("invalid PMID %q", raw)
	}
	return raw, nil
}

func removeArXivVersion(value string) (string, error) {
	matches := arXivVersion.FindStringSubmatchIndex(value)
	if matches == nil {
		return value, nil
	}
	version := value[matches[2]:matches[3]]
	if version == "" || version[0] == '0' {
		return "", errors.New("version suffix must be v1 or greater")
	}
	return value[:matches[0]], nil
}

func normalizeModernArXiv(value string) (string, bool) {
	matches := arXivModernValue.FindStringSubmatch(value)
	if matches == nil || !validMonth(matches[2]) || !positiveDigits(matches[3]) {
		return "", false
	}

	dateCode, err := strconv.Atoi(matches[1] + matches[2])
	if err != nil || dateCode < 704 {
		return "", false
	}
	if dateCode <= 1412 && len(matches[3]) != 4 {
		return "", false
	}
	if dateCode >= 1501 && len(matches[3]) != 5 {
		return "", false
	}
	return value, true
}

func normalizeLegacyArXiv(value string) (string, bool) {
	matches := arXivLegacyValue.FindStringSubmatch(value)
	if matches == nil ||
		!validLegacyArXivDate(matches[2], matches[3]) ||
		!positiveDigits(matches[4]) {
		return "", false
	}

	archive, ok := normalizeLegacyArXivArchive(matches[1])
	if !ok {
		return "", false
	}
	return archive + "/" + matches[2] + matches[3] + matches[4], true
}

func normalizeLegacyArXivArchive(value string) (string, bool) {
	if archive, ok := legacyArXivArchives[value]; ok {
		return archive, true
	}

	archive, subjectClass, found := strings.Cut(value, ".")
	if !found || strings.Contains(subjectClass, ".") ||
		!arXivSubjectClass.MatchString(subjectClass) {
		return "", false
	}
	preferred, ok := legacyArXivSubjectArchives[archive]
	return preferred, ok
}

func validLegacyArXivDate(yearValue string, monthValue string) bool {
	year, yearErr := strconv.Atoi(yearValue)
	month, monthErr := strconv.Atoi(monthValue)
	if yearErr != nil || monthErr != nil || month < 1 || month > 12 {
		return false
	}
	if year >= 91 {
		return year > 91 || month >= 8
	}
	if year <= 7 {
		return year < 7 || month <= 3
	}
	return false
}

func positiveDigits(value string) bool {
	return value != "" && strings.Trim(value, "0") != ""
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

var legacyArXivArchives = map[string]string{
	"acc-phys": "acc-phys",
	"adap-org": "adap-org",
	"alg-geom": "alg-geom",
	"ao-sci":   "ao-sci",
	"astro-ph": "astro-ph",
	"atom-ph":  "atom-ph",
	"bayes-an": "bayes-an",
	"chao-dyn": "chao-dyn",
	"chem-ph":  "chem-ph",
	"cmp-lg":   "cmp-lg",
	"comp-gas": "comp-gas",
	"cond-mat": "cond-mat",
	"cs":       "cs",
	"dg-ga":    "dg-ga",
	"funct-an": "funct-an",
	"gr-qc":    "gr-qc",
	"hep-ex":   "hep-ex",
	"hep-lat":  "hep-lat",
	"hep-ph":   "hep-ph",
	"hep-th":   "hep-th",
	"math":     "math",
	"math-ph":  "math-ph",
	"mtrl-th":  "mtrl-th",
	"nlin":     "nlin",
	"nucl-ex":  "nucl-ex",
	"nucl-th":  "nucl-th",
	"patt-sol": "patt-sol",
	"physics":  "physics",
	"plasm-ph": "plasm-ph",
	"q-alg":    "q-alg",
	"q-bio":    "q-bio",
	"quant-ph": "quant-ph",
	"solv-int": "solv-int",
	"supr-con": "supr-con",
}

var legacyArXivSubjectArchives = map[string]string{
	"astro-ph": "astro-ph",
	"cond-mat": "cond-mat",
	"cs":       "cs",
	"math":     "math",
	"nlin":     "nlin",
	"physics":  "physics",
	"q-bio":    "q-bio",
}
