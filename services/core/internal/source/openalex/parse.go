package openalex

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

var (
	pmidPattern      = regexp.MustCompile(`^[1-9][0-9]*$`)
	pmcidPattern     = regexp.MustCompile(`^PMC[1-9][0-9]*$`)
	openAlexEntityID = regexp.MustCompile(`^[A-Z][0-9]+$`)
	orcidPattern     = regexp.MustCompile(`^[0-9]{4}-[0-9]{4}-[0-9]{4}-[0-9]{3}[0-9X]$`)
	issnPattern      = regexp.MustCompile(`^[0-9]{4}-[0-9]{3}[0-9X]$`)
	githubOwner      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	githubRepository = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

type workPayload struct {
	ID                    string              `json:"id"`
	DOI                   string              `json:"doi"`
	IDs                   workIdentifiers     `json:"ids"`
	Title                 string              `json:"title"`
	DisplayName           string              `json:"display_name"`
	PublicationDate       string              `json:"publication_date"`
	CreatedDate           string              `json:"created_date"`
	UpdatedDate           string              `json:"updated_date"`
	AbstractInvertedIndex map[string][]int    `json:"abstract_inverted_index"`
	Authorships           []authorshipPayload `json:"authorships"`
	Topics                []topicPayload      `json:"topics"`
	Keywords              []keywordPayload    `json:"keywords"`
	CitedByCount          *int                `json:"cited_by_count"`
	PrimaryLocation       *locationPayload    `json:"primary_location"`
	BestOALocation        *locationPayload    `json:"best_oa_location"`
	Locations             []locationPayload   `json:"locations"`
	OpenAccess            *openAccessPayload  `json:"open_access"`
	IsRetracted           *bool               `json:"is_retracted"`
}

type workIdentifiers struct {
	OpenAlex string `json:"openalex"`
	DOI      string `json:"doi"`
	ArXiv    string `json:"arxiv"`
	PMID     string `json:"pmid"`
	PMCID    string `json:"pmcid"`
}

type authorshipPayload struct {
	AuthorPosition  string               `json:"author_position"`
	IsCorresponding bool                 `json:"is_corresponding"`
	Author          authorPayload        `json:"author"`
	Institutions    []institutionPayload `json:"institutions"`
}

type authorPayload struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"display_name"`
	ORCID       *string `json:"orcid"`
}

type institutionPayload struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	ROR         string `json:"ror"`
	CountryCode string `json:"country_code"`
	Type        string `json:"type"`
}

type topicPayload struct {
	ID          string       `json:"id"`
	DisplayName string       `json:"display_name"`
	Score       *float64     `json:"score"`
	Subfield    levelPayload `json:"subfield"`
	Field       levelPayload `json:"field"`
	Domain      levelPayload `json:"domain"`
}

type keywordPayload struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Score       *float64 `json:"score"`
}

type levelPayload struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type sourcePayload struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	ISSNL       string   `json:"issn_l"`
	ISSN        []string `json:"issn"`
	Type        string   `json:"type"`
}

type locationPayload struct {
	IsOA           *bool          `json:"is_oa"`
	LandingPageURL string         `json:"landing_page_url"`
	PDFURL         string         `json:"pdf_url"`
	License        string         `json:"license"`
	LicenseID      string         `json:"license_id"`
	Version        string         `json:"version"`
	Source         *sourcePayload `json:"source"`
}

type openAccessPayload struct {
	IsOA                     *bool  `json:"is_oa"`
	Status                   string `json:"oa_status"`
	URL                      string `json:"oa_url"`
	AnyRepositoryHasFulltext *bool  `json:"any_repository_has_fulltext"`
}

func Parse(raw json.RawMessage) (source.Record, error) {
	rawRecord, err := source.NewRawRecord(raw)
	if err != nil {
		return source.Record{}, fmt.Errorf("parse OpenAlex record JSON: %w", err)
	}

	var work workPayload
	if err := json.Unmarshal(raw, &work); err != nil {
		return source.Record{}, fmt.Errorf("decode OpenAlex record JSON: %w", err)
	}

	requiredOpenAlexID, err := requiredWorkID(work.ID)
	if err != nil {
		return source.Record{}, err
	}

	openAlexIDs, err := normalizedPaperIdentifiers(
		paper.SchemeOpenAlex,
		[]namedValue{
			{path: "$.id", value: work.ID},
			{path: "$.ids.openalex", value: work.IDs.OpenAlex},
		},
	)
	if err != nil {
		return source.Record{}, err
	}
	dois, err := normalizedPaperIdentifiers(
		paper.SchemeDOI,
		[]namedValue{
			{path: "$.doi", value: work.DOI},
			{path: "$.ids.doi", value: work.IDs.DOI},
		},
	)
	if err != nil {
		return source.Record{}, err
	}
	arXivIDs, err := normalizedPaperIdentifiers(
		paper.SchemeArXiv,
		[]namedValue{{path: "$.ids.arxiv", value: work.IDs.ArXiv}},
	)
	if err != nil {
		return source.Record{}, err
	}

	identity, err := paper.CanonicalIdentity(paper.Identifiers{
		DOI:      dois,
		ArXiv:    arXivIDs,
		OpenAlex: openAlexIDs,
	}, "")
	if err != nil {
		return source.Record{}, fmt.Errorf(
			"resolve OpenAlex work %s identity: %w",
			requiredOpenAlexID,
			err,
		)
	}

	identifiers := []source.Identifier{{
		Scheme: source.IdentifierOpenAlex,
		Value:  requiredOpenAlexID,
	}}
	identifiers = appendIdentifiers(identifiers, source.IdentifierDOI, dois)
	identifiers = appendIdentifiers(identifiers, source.IdentifierArXiv, arXivIDs)

	if work.IDs.PMID != "" {
		pmid, err := normalizePMID(work.IDs.PMID)
		if err != nil {
			return source.Record{}, fmt.Errorf("normalize $.ids.pmid: %w", err)
		}
		identifiers = append(identifiers, source.Identifier{
			Scheme: source.IdentifierPMID,
			Value:  pmid,
		})
	}
	if work.IDs.PMCID != "" {
		pmcid, err := normalizePMCID(work.IDs.PMCID)
		if err != nil {
			return source.Record{}, fmt.Errorf("normalize $.ids.pmcid: %w", err)
		}
		identifiers = append(identifiers, source.Identifier{
			Scheme: source.IdentifierPMCID,
			Value:  pmcid,
		})
	}

	publishedAt, err := parseDate(work.PublicationDate, "$.publication_date")
	if err != nil {
		return source.Record{}, err
	}
	createdAt, err := parseDate(work.CreatedDate, "$.created_date")
	if err != nil {
		return source.Record{}, err
	}
	updatedAt, err := parseTimestamp(work.UpdatedDate, "$.updated_date")
	if err != nil {
		return source.Record{}, err
	}
	abstract, err := reconstructAbstract(work.AbstractInvertedIndex)
	if err != nil {
		return source.Record{}, fmt.Errorf("reconstruct $.abstract_inverted_index: %w", err)
	}
	authors, hasInstitutions, err := parseAuthors(work.Authorships)
	if err != nil {
		return source.Record{}, err
	}
	topics, err := parseTopics(work.Topics)
	if err != nil {
		return source.Record{}, err
	}
	keywords, err := parseKeywords(work.Keywords)
	if err != nil {
		return source.Record{}, err
	}
	venue, err := parseVenue(work.PrimaryLocation)
	if err != nil {
		return source.Record{}, err
	}
	licenses := collectLicenses(work)
	codeURLs := collectCodeURLs(work)

	scopeDecision, err := source.NewScopeDecision(
		source.ScopePending,
		source.ScopeReasonAwaitingDeterministicEvaluation,
		nil,
	)
	if err != nil {
		return source.Record{}, fmt.Errorf("create pending scope decision: %w", err)
	}

	record := source.Record{
		Source:         source.OpenAlex,
		SourceRecordID: requiredOpenAlexID,
		Identity:       identity,
		Identifiers:    identifiers,
		Raw:            rawRecord,
		Title:          work.Title,
		Abstract:       abstract,
		PublishedAt:    publishedAt,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		Authors:        authors,
		Topics:         topics,
		Keywords:       keywords,
		CitedByCount:   cloneInt(work.CitedByCount),
		Venue:          venue,
		Licenses:       licenses,
		Retracted:      cloneBool(work.IsRetracted),
		CodeURLs:       codeURLs,
		Scope:          scopeDecision,
	}
	if work.OpenAccess != nil {
		record.OpenAccess = source.OpenAccess{
			IsOA:                     cloneBool(work.OpenAccess.IsOA),
			Status:                   work.OpenAccess.Status,
			URL:                      work.OpenAccess.URL,
			AnyRepositoryHasFulltext: cloneBool(work.OpenAccess.AnyRepositoryHasFulltext),
		}
	}
	record.Evidence = buildEvidence(work, record, hasInstitutions)
	return record, nil
}

type namedValue struct {
	path  string
	value string
}

func requiredWorkID(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("required OpenAlex ID is missing")
	}
	identifier, err := paper.NewIdentifier(paper.SchemeOpenAlex, raw)
	if err != nil {
		return "", fmt.Errorf("invalid required OpenAlex ID: %w", err)
	}
	return identifier.Value(), nil
}

func normalizedPaperIdentifiers(
	scheme paper.Scheme,
	values []namedValue,
) ([]string, error) {
	normalized := make([]string, 0, len(values))
	for _, candidate := range values {
		if candidate.value == "" {
			continue
		}
		identifier, err := paper.NewIdentifier(scheme, candidate.value)
		if err != nil {
			return nil, fmt.Errorf("normalize %s: %w", candidate.path, err)
		}
		if !slices.Contains(normalized, identifier.Value()) {
			normalized = append(normalized, identifier.Value())
		}
	}
	return normalized, nil
}

func appendIdentifiers(
	identifiers []source.Identifier,
	scheme source.IdentifierScheme,
	values []string,
) []source.Identifier {
	for _, value := range values {
		identifiers = append(identifiers, source.Identifier{
			Scheme: scheme,
			Value:  value,
		})
	}
	return identifiers
}

func normalizePMID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	for _, prefix := range []string{
		"https://pubmed.ncbi.nlm.nih.gov/",
		"http://pubmed.ncbi.nlm.nih.gov/",
		"https://www.ncbi.nlm.nih.gov/pubmed/",
		"http://www.ncbi.nlm.nih.gov/pubmed/",
	} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = value[len(prefix):]
			break
		}
	}
	if strings.HasPrefix(strings.ToLower(value), "pmid:") {
		value = strings.TrimSpace(value[len("pmid:"):])
	}
	value = strings.TrimSuffix(value, "/")
	if !pmidPattern.MatchString(value) {
		return "", fmt.Errorf("invalid PMID %q", raw)
	}
	return value, nil
}

func normalizePMCID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	for _, prefix := range []string{
		"https://www.ncbi.nlm.nih.gov/pmc/articles/",
		"http://www.ncbi.nlm.nih.gov/pmc/articles/",
		"https://pmc.ncbi.nlm.nih.gov/articles/",
		"http://pmc.ncbi.nlm.nih.gov/articles/",
	} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = value[len(prefix):]
			break
		}
	}
	if strings.HasPrefix(strings.ToLower(value), "pmcid:") {
		value = strings.TrimSpace(value[len("pmcid:"):])
	}
	value = strings.ToUpper(strings.TrimSuffix(value, "/"))
	if !pmcidPattern.MatchString(value) {
		return "", fmt.Errorf("invalid PMCID %q", raw)
	}
	return value, nil
}

func parseDate(raw, path string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	value = value.UTC()
	return &value, nil
}

func parseTimestamp(raw, path string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02",
	} {
		var (
			value time.Time
			err   error
		)
		if strings.Contains(layout, "Z07:00") {
			value, err = time.Parse(layout, raw)
		} else {
			value, err = time.ParseInLocation(layout, raw, time.UTC)
		}
		if err == nil {
			value = value.UTC()
			return &value, nil
		}
	}
	return nil, fmt.Errorf("parse %s: invalid OpenAlex timestamp %q", path, raw)
}

func reconstructAbstract(index map[string][]int) (string, error) {
	if index == nil {
		return "", nil
	}

	tokens := make(map[int]string)
	for token, positions := range index {
		if token == "" {
			return "", errors.New("abstract token must not be empty")
		}
		for _, position := range positions {
			if position < 0 {
				return "", fmt.Errorf("abstract position %d must not be negative", position)
			}
			if existing, exists := tokens[position]; exists {
				return "", fmt.Errorf(
					"abstract position %d is assigned to both %q and %q",
					position,
					existing,
					token,
				)
			}
			tokens[position] = token
		}
	}
	if len(tokens) == 0 {
		return "", nil
	}

	ordered := make([]string, len(tokens))
	for position := range len(tokens) {
		token, exists := tokens[position]
		if !exists {
			return "", fmt.Errorf("abstract position %d is missing", position)
		}
		ordered[position] = token
	}
	return strings.Join(ordered, " "), nil
}

func parseAuthors(
	authorships []authorshipPayload,
) ([]source.Author, bool, error) {
	authors := make([]source.Author, 0, len(authorships))
	hasInstitutions := false
	for index, authorship := range authorships {
		authorID := ""
		var err error
		if authorship.Author.ID != "" {
			authorID, err = normalizeOpenAlexEntity(authorship.Author.ID, 'A')
			if err != nil {
				return nil, false, fmt.Errorf(
					"normalize $.authorships[%d].author.id: %w",
					index,
					err,
				)
			}
		}
		orcid := ""
		if authorship.Author.ORCID != nil && *authorship.Author.ORCID != "" {
			orcid, err = normalizeORCID(*authorship.Author.ORCID)
			if err != nil {
				return nil, false, fmt.Errorf(
					"normalize $.authorships[%d].author.orcid: %w",
					index,
					err,
				)
			}
		}

		institutions := make([]source.Institution, 0, len(authorship.Institutions))
		for institutionIndex, institution := range authorship.Institutions {
			institutionID := ""
			if institution.ID != "" {
				institutionID, err = normalizeOpenAlexEntity(institution.ID, 'I')
				if err != nil {
					return nil, false, fmt.Errorf(
						"normalize $.authorships[%d].institutions[%d].id: %w",
						index,
						institutionIndex,
						err,
					)
				}
			}
			if institution.CountryCode != "" &&
				(len(institution.CountryCode) != 2 ||
					institution.CountryCode != strings.ToUpper(institution.CountryCode)) {
				return nil, false, fmt.Errorf(
					"$.authorships[%d].institutions[%d].country_code is invalid",
					index,
					institutionIndex,
				)
			}
			institutions = append(institutions, source.Institution{
				OpenAlexID:  institutionID,
				DisplayName: institution.DisplayName,
				ROR:         institution.ROR,
				CountryCode: institution.CountryCode,
				Type:        institution.Type,
			})
		}
		if len(institutions) > 0 {
			hasInstitutions = true
		}

		authors = append(authors, source.Author{
			OpenAlexID:      authorID,
			DisplayName:     authorship.Author.DisplayName,
			ORCID:           orcid,
			Position:        index + 1,
			PositionLabel:   authorship.AuthorPosition,
			IsCorresponding: authorship.IsCorresponding,
			Institutions:    institutions,
		})
	}
	return authors, hasInstitutions, nil
}

func parseTopics(topics []topicPayload) ([]source.Topic, error) {
	result := make([]source.Topic, 0, len(topics))
	for index, topic := range topics {
		if err := validateScore(topic.Score); err != nil {
			return nil, fmt.Errorf("$.topics[%d].score: %w", index, err)
		}
		topicID := normalizeOpenAlexPathID(topic.ID)
		result = append(result, source.Topic{
			OpenAlexID:  topicID,
			DisplayName: topic.DisplayName,
			Score:       cloneFloat(topic.Score),
			Subfield: source.TopicLevel{
				ID:          normalizeOpenAlexPathID(topic.Subfield.ID),
				DisplayName: topic.Subfield.DisplayName,
			},
			Field: source.TopicLevel{
				ID:          normalizeOpenAlexPathID(topic.Field.ID),
				DisplayName: topic.Field.DisplayName,
			},
			Domain: source.TopicLevel{
				ID:          normalizeOpenAlexPathID(topic.Domain.ID),
				DisplayName: topic.Domain.DisplayName,
			},
		})
	}
	return result, nil
}

func parseKeywords(keywords []keywordPayload) ([]source.Keyword, error) {
	result := make([]source.Keyword, 0, len(keywords))
	for index, keyword := range keywords {
		if err := validateScore(keyword.Score); err != nil {
			return nil, fmt.Errorf("$.keywords[%d].score: %w", index, err)
		}
		result = append(result, source.Keyword{
			OpenAlexID:  normalizeOpenAlexPathID(keyword.ID),
			DisplayName: keyword.DisplayName,
			Score:       cloneFloat(keyword.Score),
		})
	}
	return result, nil
}

func validateScore(score *float64) error {
	if score != nil && (*score < 0 || *score > 1) {
		return fmt.Errorf("score %v must be between 0 and 1", *score)
	}
	return nil
}

func parseVenue(location *locationPayload) (*source.Venue, error) {
	if location == nil || location.Source == nil {
		return nil, nil
	}
	raw := location.Source
	openAlexID := ""
	var err error
	if raw.ID != "" {
		openAlexID, err = normalizeOpenAlexEntity(raw.ID, 'S')
		if err != nil {
			return nil, fmt.Errorf("normalize $.primary_location.source.id: %w", err)
		}
	}
	issnL, err := normalizeISSN(raw.ISSNL)
	if err != nil {
		return nil, fmt.Errorf("normalize $.primary_location.source.issn_l: %w", err)
	}
	issns := make([]string, 0, len(raw.ISSN))
	for index, value := range raw.ISSN {
		issn, err := normalizeISSN(value)
		if err != nil {
			return nil, fmt.Errorf(
				"normalize $.primary_location.source.issn[%d]: %w",
				index,
				err,
			)
		}
		if issn != "" && !slices.Contains(issns, issn) {
			issns = append(issns, issn)
		}
	}
	return &source.Venue{
		OpenAlexID:  openAlexID,
		DisplayName: raw.DisplayName,
		Type:        raw.Type,
		ISSNL:       issnL,
		ISSN:        issns,
	}, nil
}

func collectLicenses(work workPayload) []source.License {
	result := make([]source.License, 0)
	add := func(path string, location *locationPayload) {
		if location == nil || (location.License == "" && location.LicenseID == "") {
			return
		}
		license := source.License{
			Name:       location.License,
			ID:         location.LicenseID,
			URL:        licenseURL(location.LicenseID),
			SourcePath: path,
		}
		for _, existing := range result {
			if existing.Name == license.Name &&
				existing.ID == license.ID &&
				existing.URL == license.URL {
				return
			}
		}
		result = append(result, license)
	}
	add("$.primary_location", work.PrimaryLocation)
	add("$.best_oa_location", work.BestOALocation)
	for index := range work.Locations {
		add(fmt.Sprintf("$.locations[%d]", index), &work.Locations[index])
	}
	return result
}

func collectCodeURLs(work workPayload) []string {
	result := make([]string, 0)
	addLocation := func(location *locationPayload) {
		if location == nil {
			return
		}
		for _, raw := range []string{location.LandingPageURL, location.PDFURL} {
			repositoryURL, ok := canonicalGitHubRepository(raw)
			if ok && !slices.Contains(result, repositoryURL) {
				result = append(result, repositoryURL)
			}
		}
	}
	addLocation(work.PrimaryLocation)
	addLocation(work.BestOALocation)
	for index := range work.Locations {
		addLocation(&work.Locations[index])
	}
	if work.OpenAccess != nil {
		if repositoryURL, ok := canonicalGitHubRepository(work.OpenAccess.URL); ok &&
			!slices.Contains(result, repositoryURL) {
			result = append(result, repositoryURL)
		}
	}
	slices.Sort(result)
	return result
}

func canonicalGitHubRepository(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		!strings.EqualFold(parsed.Hostname(), "github.com") ||
		parsed.User != nil {
		return "", false
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) < 2 {
		return "", false
	}
	owner, err := url.PathUnescape(segments[0])
	if err != nil || !githubOwner.MatchString(owner) {
		return "", false
	}
	repository, err := url.PathUnescape(segments[1])
	if err != nil {
		return "", false
	}
	repository = strings.TrimSuffix(repository, ".git")
	if repository == "" || !githubRepository.MatchString(repository) {
		return "", false
	}
	return "https://github.com/" + owner + "/" + repository, true
}

func buildEvidence(
	work workPayload,
	record source.Record,
	hasInstitutions bool,
) []source.FieldEvidence {
	evidence := make([]source.FieldEvidence, 0)
	add := func(field, path string, present bool) {
		if !present {
			return
		}
		item := source.FieldEvidence{Field: field, SourcePath: path}
		if !slices.Contains(evidence, item) {
			evidence = append(evidence, item)
		}
	}

	add("identifiers", "$.id", true)
	add("identifiers", "$.doi", work.DOI != "")
	add("identifiers", "$.ids", work.IDs != (workIdentifiers{}))
	add("title", "$.title", work.Title != "")
	add("published_at", "$.publication_date", record.PublishedAt != nil)
	add("created_at", "$.created_date", record.CreatedAt != nil)
	add("updated_at", "$.updated_date", record.UpdatedAt != nil)
	add("abstract", "$.abstract_inverted_index", work.AbstractInvertedIndex != nil)
	add("authors", "$.authorships", len(work.Authorships) > 0)
	add("institutions", "$.authorships[*].institutions", hasInstitutions)
	add("topics", "$.topics", len(work.Topics) > 0)
	add("keywords", "$.keywords", len(work.Keywords) > 0)
	add("cited_by_count", "$.cited_by_count", record.CitedByCount != nil)
	add("venue", "$.primary_location.source", record.Venue != nil)
	add("open_access", "$.open_access", work.OpenAccess != nil)
	for _, license := range record.Licenses {
		add("licenses", license.SourcePath, true)
	}
	if record.Retracted != nil {
		add("retracted", "$.is_retracted", true)
	}
	if len(record.CodeURLs) > 0 {
		add("code_urls", "$.best_oa_location|$.locations|$.open_access", true)
	}
	return evidence
}

func normalizeOpenAlexEntity(raw string, prefix byte) (string, error) {
	value := strings.TrimSpace(raw)
	for _, candidatePrefix := range []string{
		"https://openalex.org/",
		"http://openalex.org/",
	} {
		if strings.HasPrefix(strings.ToLower(value), candidatePrefix) {
			value = value[len(candidatePrefix):]
			break
		}
	}
	value = strings.ToUpper(strings.TrimSpace(value))
	if !openAlexEntityID.MatchString(value) || value[0] != prefix {
		return "", fmt.Errorf("invalid OpenAlex %c identifier %q", prefix, raw)
	}
	return value, nil
}

func normalizeOpenAlexPathID(raw string) string {
	value := strings.TrimSpace(raw)
	for _, candidatePrefix := range []string{
		"https://openalex.org/",
		"http://openalex.org/",
	} {
		if strings.HasPrefix(strings.ToLower(value), candidatePrefix) {
			return value[len(candidatePrefix):]
		}
	}
	return value
}

func normalizeORCID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	for _, prefix := range []string{"https://orcid.org/", "http://orcid.org/"} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = value[len(prefix):]
			break
		}
	}
	value = strings.ToUpper(value)
	if !orcidPattern.MatchString(value) {
		return "", fmt.Errorf("invalid ORCID %q", raw)
	}
	return value, nil
}

func normalizeISSN(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	value := strings.ToUpper(strings.TrimSpace(raw))
	if !issnPattern.MatchString(value) {
		return "", fmt.Errorf("invalid ISSN %q", raw)
	}
	return value, nil
}

func licenseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return ""
	}
	return parsed.String()
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
