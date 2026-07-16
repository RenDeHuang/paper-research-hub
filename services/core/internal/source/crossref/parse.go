package crossref

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

type workPayload struct {
	DOI                 string              `json:"DOI"`
	Title               []string            `json:"title"`
	Publisher           string              `json:"publisher"`
	Authors             []authorPayload     `json:"author"`
	ContainerTitle      []string            `json:"container-title"`
	ShortContainerTitle []string            `json:"short-container-title"`
	ISSNs               []string            `json:"ISSN"`
	ISSNTypes           []issnTypePayload   `json:"issn-type"`
	Published           *partialDatePayload `json:"published"`
	PublishedOnline     *partialDatePayload `json:"published-online"`
	Created             *timestampPayload   `json:"created"`
	Indexed             *timestampPayload   `json:"indexed"`
	Abstract            string              `json:"abstract"`
	Licenses            []licensePayload    `json:"license"`
	UpdateTo            []updateToPayload   `json:"update-to"`
}

type authorPayload struct {
	Given        string               `json:"given"`
	Family       string               `json:"family"`
	Name         string               `json:"name"`
	ORCID        string               `json:"ORCID"`
	Affiliations []affiliationPayload `json:"affiliation"`
}

type affiliationPayload struct {
	Name string `json:"name"`
}

type issnTypePayload struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

type partialDatePayload struct {
	DateParts json.RawMessage `json:"date-parts"`
}

type timestampPayload struct {
	DateTime json.RawMessage `json:"date-time"`
}

type licensePayload struct {
	URL string `json:"URL"`
}

type updateToPayload struct {
	DOI   string `json:"DOI"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

const (
	MaxJATSDepth     = 64
	MaxJATSElements  = 10_000
	MaxJATSTextBytes = 1 << 20
)

type JATSLimitKind string

const (
	JATSLimitDepth     JATSLimitKind = "depth"
	JATSLimitElements  JATSLimitKind = "elements"
	JATSLimitTextBytes JATSLimitKind = "text_bytes"
)

type JATSLimitError struct {
	Kind   JATSLimitKind
	Limit  int
	Actual int
}

func (err *JATSLimitError) Error() string {
	return fmt.Sprintf(
		"JATS %s limit exceeded: actual %d, limit %d",
		err.Kind,
		err.Actual,
		err.Limit,
	)
}

func Parse(raw json.RawMessage) (source.Record, error) {
	if err := validateUniqueJSONObjects(raw); err != nil {
		return source.Record{}, fmt.Errorf("validate Crossref item JSON: %w", err)
	}
	rawRecord, err := source.NewRawRecord(raw)
	if err != nil {
		return source.Record{}, fmt.Errorf("parse Crossref item JSON: %w", err)
	}

	var work workPayload
	if err := json.Unmarshal(raw, &work); err != nil {
		return source.Record{}, fmt.Errorf("decode Crossref item JSON: %w", err)
	}
	if strings.TrimSpace(work.DOI) == "" {
		return source.Record{}, errors.New("required Crossref DOI is missing")
	}
	identity, err := paper.NewIdentifier(paper.SchemeDOI, work.DOI)
	if err != nil {
		return source.Record{}, fmt.Errorf("invalid required Crossref DOI: %w", err)
	}

	title := firstText(work.Title)
	publisher := normalizeText(work.Publisher)
	abstract, err := parseAbstract(work.Abstract)
	if err != nil {
		return source.Record{}, fmt.Errorf("parse Crossref $.abstract: %w", err)
	}
	authors, err := parseAuthors(work.Authors)
	if err != nil {
		return source.Record{}, err
	}
	venue, venueEvidence, err := parseVenue(work)
	if err != nil {
		return source.Record{}, err
	}
	publishedDate, publishedAt, err := parsePartialDate(work.Published, "$.published")
	if err != nil {
		return source.Record{}, err
	}
	electronicDate, electronicAt, err := parsePartialDate(
		work.PublishedOnline,
		"$.published-online",
	)
	if err != nil {
		return source.Record{}, err
	}
	createdAt, err := parseTimestamp(work.Created, "$.created")
	if err != nil {
		return source.Record{}, err
	}
	updatedAt, err := parseTimestamp(work.Indexed, "$.indexed")
	if err != nil {
		return source.Record{}, err
	}
	licenses, err := parseLicenses(work.Licenses)
	if err != nil {
		return source.Record{}, err
	}
	relations, err := parseRelations(work.UpdateTo)
	if err != nil {
		return source.Record{}, err
	}

	scope, err := source.NewScopeDecision(
		source.ScopePending,
		source.ScopeReasonAwaitingDeterministicEvaluation,
		nil,
	)
	if err != nil {
		return source.Record{}, fmt.Errorf("create Crossref scope decision: %w", err)
	}

	record := source.Record{
		Source:         source.Crossref,
		SourceRecordID: identity.Value(),
		Identity:       identity,
		Identifiers: []source.Identifier{{
			Scheme: source.IdentifierDOI,
			Value:  identity.Value(),
		}},
		Raw:                       rawRecord,
		Title:                     title,
		Publisher:                 publisher,
		Abstract:                  abstract,
		PublishedAt:               publishedAt,
		PublishedDate:             publishedDate,
		ElectronicPublishedAt:     electronicAt,
		ElectronicPublicationDate: electronicDate,
		CreatedAt:                 createdAt,
		UpdatedAt:                 updatedAt,
		Authors:                   authors,
		Relations:                 relations,
		Venue:                     venue,
		Licenses:                  licenses,
		Scope:                     scope,
	}
	record.Evidence = buildEvidence(record, venueEvidence)
	return record, nil
}

func parseAuthors(values []authorPayload) ([]source.Author, error) {
	authors := make([]source.Author, 0, len(values))
	for index, value := range values {
		given := normalizeText(value.Given)
		family := normalizeText(value.Family)
		collective := normalizeText(value.Name)
		orcid, err := normalizeORCID(value.ORCID)
		if err != nil {
			return nil, fmt.Errorf("parse Crossref $.author[%d].ORCID: %w", index, err)
		}

		affiliations := make([]string, 0, len(value.Affiliations))
		for _, affiliation := range value.Affiliations {
			name := normalizeText(affiliation.Name)
			if name != "" && !slices.Contains(affiliations, name) {
				affiliations = append(affiliations, name)
			}
		}

		displayName := normalizeText(strings.TrimSpace(given + " " + family))
		collectiveName := ""
		if displayName == "" {
			displayName = collective
			collectiveName = collective
		}
		if displayName == "" && orcid == "" && len(affiliations) == 0 {
			continue
		}
		authors = append(authors, source.Author{
			DisplayName:    displayName,
			LastName:       family,
			ForeName:       given,
			CollectiveName: collectiveName,
			ORCID:          orcid,
			Position:       index + 1,
			Affiliations:   affiliations,
		})
	}
	return authors, nil
}

type venueEvidenceFlags struct {
	title     bool
	short     bool
	issns     bool
	issnTypes bool
}

func parseVenue(
	work workPayload,
) (*source.Venue, venueEvidenceFlags, error) {
	title := firstText(work.ContainerTitle)
	shortTitle := firstText(work.ShortContainerTitle)
	issns, err := normalizeRecordISSNs(work.ISSNs)
	if err != nil {
		return nil, venueEvidenceFlags{}, err
	}
	issnDetails, err := parseISSNTypes(work.ISSNTypes, issns)
	if err != nil {
		return nil, venueEvidenceFlags{}, err
	}

	evidence := venueEvidenceFlags{
		title:     title != "",
		short:     shortTitle != "",
		issns:     len(issns) > 0,
		issnTypes: len(issnDetails) > 0,
	}
	if !evidence.title && !evidence.short && !evidence.issns && !evidence.issnTypes {
		return nil, evidence, nil
	}
	return &source.Venue{
		DisplayName:     title,
		ISOAbbreviation: shortTitle,
		ISSN:            issns,
		ISSNDetails:     issnDetails,
	}, evidence, nil
}

func normalizeRecordISSNs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	for index, raw := range values {
		value := strings.ToUpper(strings.TrimSpace(raw))
		if !validISSN(value) {
			return nil, fmt.Errorf("invalid Crossref $.ISSN[%d] value %q", index, raw)
		}
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result, nil
}

func parseISSNTypes(
	values []issnTypePayload,
	issns []string,
) ([]source.VenueISSN, error) {
	details := make([]source.VenueISSN, 0, len(values))
	typesByISSN := make(map[string]string, len(values))
	for index, value := range values {
		issn := strings.ToUpper(strings.TrimSpace(value.Value))
		if !validISSN(issn) {
			return nil, fmt.Errorf(
				"invalid Crossref $.issn-type[%d].value %q",
				index,
				value.Value,
			)
		}
		if !slices.Contains(issns, issn) {
			return nil, fmt.Errorf(
				"Crossref $.issn-type[%d] value %q is absent from $.ISSN",
				index,
				issn,
			)
		}

		var normalizedType string
		switch strings.TrimSpace(value.Type) {
		case "print":
			normalizedType = "Print"
		case "electronic":
			normalizedType = "Electronic"
		default:
			return nil, fmt.Errorf(
				"invalid Crossref $.issn-type[%d].type %q",
				index,
				value.Type,
			)
		}
		if existing, exists := typesByISSN[issn]; exists {
			if existing != normalizedType {
				return nil, fmt.Errorf(
					"conflicting Crossref issn-type values for %s: %s and %s",
					issn,
					existing,
					normalizedType,
				)
			}
			continue
		}
		typesByISSN[issn] = normalizedType
		details = append(details, source.VenueISSN{
			Value: issn,
			Type:  normalizedType,
		})
	}
	return details, nil
}

func parsePartialDate(
	payload *partialDatePayload,
	path string,
) (*source.SourceDate, *time.Time, error) {
	if payload == nil {
		return nil, nil, nil
	}
	if len(payload.DateParts) == 0 ||
		bytes.Equal(bytes.TrimSpace(payload.DateParts), []byte("null")) {
		return nil, nil, fmt.Errorf("Crossref %s requires non-null date-parts", path)
	}

	var candidates [][]int
	if err := json.Unmarshal(payload.DateParts, &candidates); err != nil {
		return nil, nil, fmt.Errorf("decode Crossref %s.date-parts: %w", path, err)
	}
	if len(candidates) != 1 || len(candidates[0]) < 1 || len(candidates[0]) > 3 {
		return nil, nil, fmt.Errorf(
			"Crossref %s.date-parts must contain exactly one date with year, optional month, and optional day",
			path,
		)
	}

	parts := candidates[0]
	year := parts[0]
	if year < 1 || year > 9999 {
		return nil, nil, fmt.Errorf("Crossref %s.date-parts has invalid year %d", path, year)
	}
	date := &source.SourceDate{
		Year:      year,
		Precision: source.DatePrecisionYear,
	}
	if len(parts) >= 2 {
		month := parts[1]
		if month < 1 || month > 12 {
			return nil, nil, fmt.Errorf(
				"Crossref %s.date-parts has invalid month %d",
				path,
				month,
			)
		}
		date.Month = time.Month(month)
		date.Precision = source.DatePrecisionMonth
	}
	if len(parts) == 2 {
		return date, nil, nil
	}
	if len(parts) == 1 {
		return date, nil, nil
	}

	day := parts[2]
	candidate := time.Date(year, date.Month, day, 0, 0, 0, 0, time.UTC)
	if day < 1 ||
		candidate.Year() != year ||
		candidate.Month() != date.Month ||
		candidate.Day() != day {
		return nil, nil, fmt.Errorf(
			"Crossref %s.date-parts has invalid day %d",
			path,
			day,
		)
	}
	date.Day = day
	date.Precision = source.DatePrecisionDay
	return date, &candidate, nil
}

func parseTimestamp(payload *timestampPayload, path string) (*time.Time, error) {
	if payload == nil {
		return nil, nil
	}
	if len(payload.DateTime) == 0 ||
		bytes.Equal(bytes.TrimSpace(payload.DateTime), []byte("null")) {
		return nil, fmt.Errorf("Crossref %s requires date-time", path)
	}
	var raw string
	if err := json.Unmarshal(payload.DateTime, &raw); err != nil {
		return nil, fmt.Errorf("decode Crossref %s.date-time: %w", path, err)
	}
	value, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("parse Crossref %s.date-time %q: %w", path, raw, err)
	}
	value = value.UTC()
	return &value, nil
}

func parseAbstract(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	isJATS, err := hasAllowedLeadingJATSRoot(value)
	if err != nil {
		return "", err
	}
	if !isJATS {
		return normalizeText(value), nil
	}
	return parseJATSAbstract(value)
}

func hasAllowedLeadingJATSRoot(value string) (bool, error) {
	if !strings.HasPrefix(value, "<") {
		return false, nil
	}

	decoder := xml.NewDecoder(strings.NewReader(value))
	decoder.Strict = true
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("decode leading abstract XML token: %w", err)
		}
		switch typed := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(typed)) == "" {
				continue
			}
			return false, nil
		case xml.StartElement:
			if !validJATSRoot(typed.Name) {
				return false, fmt.Errorf(
					"abstract XML root %q is not an allowed JATS root",
					typed.Name.Local,
				)
			}
			return true, nil
		case xml.Comment, xml.Directive, xml.ProcInst:
			return false, errors.New("abstract contains markup before an allowed JATS root")
		}
	}
}

func parseJATSAbstract(value string) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(value))
	decoder.Strict = true
	depth := 0
	roots := 0
	elements := 0
	textBytes := 0
	var text strings.Builder

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("decode JATS XML: %w", err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots > 1 {
					return "", errors.New("JATS abstract must contain exactly one root element")
				}
			}
			if !validJATSElement(typed.Name) {
				return "", fmt.Errorf(
					"JATS abstract contains unsupported element %q",
					typed.Name.Local,
				)
			}
			depth++
			if depth > MaxJATSDepth {
				return "", &JATSLimitError{
					Kind:   JATSLimitDepth,
					Limit:  MaxJATSDepth,
					Actual: depth,
				}
			}
			elements++
			if elements > MaxJATSElements {
				return "", &JATSLimitError{
					Kind:   JATSLimitElements,
					Limit:  MaxJATSElements,
					Actual: elements,
				}
			}
			if isJATSBlockElement(typed.Name.Local) && text.Len() > 0 {
				text.WriteByte(' ')
			}
		case xml.EndElement:
			if isJATSBlockElement(typed.Name.Local) && text.Len() > 0 {
				text.WriteByte(' ')
			}
			depth--
		case xml.CharData:
			textBytes += len(typed)
			if textBytes > MaxJATSTextBytes {
				return "", &JATSLimitError{
					Kind:   JATSLimitTextBytes,
					Limit:  MaxJATSTextBytes,
					Actual: textBytes,
				}
			}
			if depth == 0 {
				if strings.TrimSpace(string(typed)) != "" {
					return "", errors.New("JATS abstract contains text outside its root element")
				}
				continue
			}
			text.Write([]byte(typed))
		case xml.Comment, xml.Directive, xml.ProcInst:
			return "", errors.New("JATS abstract contains unsupported markup")
		}
	}
	if roots != 1 || depth != 0 {
		return "", errors.New("JATS abstract must contain exactly one complete root element")
	}
	return normalizeText(text.String()), nil
}

func validJATSRoot(name xml.Name) bool {
	if !validJATSElement(name) {
		return false
	}
	switch name.Local {
	case "abstract", "p", "sec":
		return true
	default:
		return false
	}
}

func isJATSBlockElement(local string) bool {
	switch local {
	case "abstract",
		"break",
		"disp-formula",
		"label",
		"list",
		"list-item",
		"p",
		"sec",
		"title":
		return true
	default:
		return false
	}
}

func validJATSElement(name xml.Name) bool {
	switch name.Space {
	case "jats", "http://www.ncbi.nlm.nih.gov/JATS1", "http://jats.nlm.nih.gov":
	default:
		return false
	}
	switch name.Local {
	case "abstract",
		"alternatives",
		"bold",
		"break",
		"disp-formula",
		"email",
		"ext-link",
		"inline-formula",
		"italic",
		"label",
		"list",
		"list-item",
		"monospace",
		"named-content",
		"p",
		"sc",
		"sec",
		"styled-content",
		"sub",
		"sup",
		"tex-math",
		"title",
		"underline",
		"uri",
		"xref":
		return true
	default:
		return false
	}
}

func parseLicenses(values []licensePayload) ([]source.License, error) {
	licenses := make([]source.License, 0, len(values))
	for index, value := range values {
		rawURL := strings.TrimSpace(value.URL)
		parsed, err := url.Parse(rawURL)
		if err != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Hostname() == "" ||
			parsed.User != nil {
			return nil, fmt.Errorf(
				"Crossref $.license[%d].URL must be an absolute HTTP(S) URL without credentials",
				index,
			)
		}
		licenses = append(licenses, source.License{
			URL:        parsed.String(),
			SourcePath: fmt.Sprintf("$.license[%d].URL", index),
		})
	}
	return licenses, nil
}

func parseRelations(values []updateToPayload) ([]source.Relation, error) {
	relations := make([]source.Relation, 0, len(values))
	for index, value := range values {
		if strings.TrimSpace(value.DOI) == "" {
			return nil, fmt.Errorf("Crossref $.update-to[%d] requires DOI", index)
		}
		target, err := paper.NewIdentifier(paper.SchemeDOI, value.DOI)
		if err != nil {
			return nil, fmt.Errorf(
				"parse Crossref $.update-to[%d].DOI: %w",
				index,
				err,
			)
		}
		relations = append(relations, source.Relation{
			Type:     normalizeText(value.Type),
			TargetID: target.Value(),
			Note:     normalizeText(value.Label),
		})
	}
	return relations, nil
}

func buildEvidence(
	record source.Record,
	venue venueEvidenceFlags,
) []source.FieldEvidence {
	evidence := []source.FieldEvidence{{
		Field:      "identifiers",
		SourcePath: "$.DOI",
	}}
	add := func(field, path string, present bool) {
		if present {
			evidence = append(evidence, source.FieldEvidence{
				Field:      field,
				SourcePath: path,
			})
		}
	}

	add("title", "$.title[0]", record.Title != "")
	add("publisher", "$.publisher", record.Publisher != "")
	add("abstract", "$.abstract", record.Abstract != "")
	add("authors", "$.author", len(record.Authors) > 0)
	add("venue", "$.container-title[0]", venue.title)
	add("venue", "$.short-container-title[0]", venue.short)
	add("venue", "$.ISSN", venue.issns)
	add("venue", "$.issn-type", venue.issnTypes)
	add("published_date", "$.published.date-parts", record.PublishedDate != nil)
	add("published_at", "$.published.date-parts", record.PublishedAt != nil)
	add(
		"electronic_publication_date",
		"$.published-online.date-parts",
		record.ElectronicPublicationDate != nil,
	)
	add(
		"electronic_published_at",
		"$.published-online.date-parts",
		record.ElectronicPublishedAt != nil,
	)
	add("created_at", "$.created.date-time", record.CreatedAt != nil)
	add("updated_at", "$.indexed.date-time", record.UpdatedAt != nil)
	for _, license := range record.Licenses {
		add("licenses", license.SourcePath, true)
	}
	add("relations", "$.update-to", len(record.Relations) > 0)
	return evidence
}

func normalizeORCID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			!strings.EqualFold(parsed.Hostname(), "orcid.org") ||
			parsed.User != nil ||
			parsed.RawQuery != "" ||
			parsed.Fragment != "" {
			return "", fmt.Errorf("invalid ORCID %q", raw)
		}
		value = strings.Trim(parsed.Path, "/")
	}
	value = strings.ToUpper(value)
	if len(value) != 19 ||
		value[4] != '-' ||
		value[9] != '-' ||
		value[14] != '-' {
		return "", fmt.Errorf("invalid ORCID %q", raw)
	}
	digits := strings.ReplaceAll(value, "-", "")
	if len(digits) != 16 {
		return "", fmt.Errorf("invalid ORCID %q", raw)
	}
	total := 0
	for _, character := range digits[:15] {
		if character < '0' || character > '9' {
			return "", fmt.Errorf("invalid ORCID %q", raw)
		}
		total = (total + int(character-'0')) * 2
	}
	checkValue := (12 - total%11) % 11
	checkCharacter := byte('0' + checkValue)
	if checkValue == 10 {
		checkCharacter = 'X'
	}
	if digits[15] != checkCharacter {
		return "", fmt.Errorf("invalid ORCID checksum %q", raw)
	}
	return value, nil
}

func firstText(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return normalizeText(values[0])
}

func normalizeText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
