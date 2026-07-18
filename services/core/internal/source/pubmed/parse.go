package pubmed

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

var (
	pmidPattern  = regexp.MustCompile(`^[1-9][0-9]*$`)
	pmcidPattern = regexp.MustCompile(`^PMC[1-9][0-9]*$`)
)

type pubmedArticleXML struct {
	MedlineCitation medlineCitationXML `xml:"MedlineCitation"`
	PubmedData      pubmedDataXML      `xml:"PubmedData"`
}

type medlineCitationXML struct {
	PMID                    string                  `xml:"PMID"`
	DateCompleted           dateXML                 `xml:"DateCompleted"`
	DateRevised             dateXML                 `xml:"DateRevised"`
	Article                 articleXML              `xml:"Article"`
	MedlineJournalInfo      medlineJournalInfoXML   `xml:"MedlineJournalInfo"`
	CommentsCorrectionsList []commentsCorrectionXML `xml:"CommentsCorrectionsList>CommentsCorrections"`
	MeshHeadingList         []meshHeadingXML        `xml:"MeshHeadingList>MeshHeading"`
}

type articleXML struct {
	PublicationModel    string              `xml:"PubModel,attr"`
	Journal             journalXML          `xml:"Journal"`
	Title               mixedText           `xml:"ArticleTitle"`
	Abstract            abstractXML         `xml:"Abstract"`
	AuthorList          authorListXML       `xml:"AuthorList"`
	PublicationTypeList []namedElementXML   `xml:"PublicationTypeList>PublicationType"`
	ArticleDates        []articleDateXML    `xml:"ArticleDate"`
	ELocationIDs        []identifierTextXML `xml:"ELocationID"`
}

type journalXML struct {
	ISSNs           []issnXML `xml:"ISSN"`
	Issue           issueXML  `xml:"JournalIssue"`
	Title           string    `xml:"Title"`
	ISOAbbreviation string    `xml:"ISOAbbreviation"`
}

type issnXML struct {
	Type  string `xml:"IssnType,attr"`
	Value string `xml:",chardata"`
}

type issueXML struct {
	PubDate dateXML `xml:"PubDate"`
}

type abstractXML struct {
	Sections             []abstractSectionXML `xml:"AbstractText"`
	CopyrightInformation mixedText            `xml:"CopyrightInformation"`
}

type abstractSectionXML struct {
	Label       string    `xml:"Label,attr"`
	NLMCategory string    `xml:"NlmCategory,attr"`
	Text        mixedText `xml:",any"`
}

func (section *abstractSectionXML) UnmarshalXML(
	decoder *xml.Decoder,
	start xml.StartElement,
) error {
	for _, attribute := range start.Attr {
		switch attribute.Name.Local {
		case "Label":
			section.Label = attribute.Value
		case "NlmCategory":
			section.NLMCategory = attribute.Value
		}
	}
	return decoder.DecodeElement(&section.Text, &start)
}

type authorListXML struct {
	CompleteYN string      `xml:"CompleteYN,attr"`
	Authors    []authorXML `xml:"Author"`
}

type authorXML struct {
	LastName        string               `xml:"LastName"`
	ForeName        string               `xml:"ForeName"`
	Initials        string               `xml:"Initials"`
	CollectiveName  string               `xml:"CollectiveName"`
	Identifiers     []identifierTextXML  `xml:"Identifier"`
	AffiliationInfo []affiliationInfoXML `xml:"AffiliationInfo"`
}

type affiliationInfoXML struct {
	Affiliation mixedText `xml:"Affiliation"`
}

type articleDateXML struct {
	Type string `xml:"DateType,attr"`
	dateXML
}

type dateXML struct {
	Year        string `xml:"Year"`
	Month       string `xml:"Month"`
	Day         string `xml:"Day"`
	Season      string `xml:"Season"`
	MedlineDate string `xml:"MedlineDate"`
}

type medlineJournalInfoXML struct {
	ISSNLinking string `xml:"ISSNLinking"`
}

type commentsCorrectionXML struct {
	Type      string    `xml:"RefType,attr"`
	RefSource mixedText `xml:"RefSource"`
	PMID      string    `xml:"PMID"`
}

type meshHeadingXML struct {
	Descriptor namedElementXML   `xml:"DescriptorName"`
	Qualifiers []namedElementXML `xml:"QualifierName"`
}

type namedElementXML struct {
	UI         string
	MajorTopic string
	Text       string
}

func (element *namedElementXML) UnmarshalXML(
	decoder *xml.Decoder,
	start xml.StartElement,
) error {
	for _, attribute := range start.Attr {
		switch attribute.Name.Local {
		case "UI":
			element.UI = strings.TrimSpace(attribute.Value)
		case "MajorTopicYN":
			element.MajorTopic = strings.TrimSpace(attribute.Value)
		}
	}
	var text mixedText
	if err := decoder.DecodeElement(&text, &start); err != nil {
		return err
	}
	element.Text = normalizeText(string(text))
	return nil
}

type identifierTextXML struct {
	Type   string
	Source string
	Valid  string
	Value  string
}

func (identifier *identifierTextXML) UnmarshalXML(
	decoder *xml.Decoder,
	start xml.StartElement,
) error {
	for _, attribute := range start.Attr {
		switch attribute.Name.Local {
		case "IdType", "EIdType":
			identifier.Type = strings.TrimSpace(attribute.Value)
		case "Source":
			identifier.Source = strings.TrimSpace(attribute.Value)
		case "ValidYN":
			identifier.Valid = strings.TrimSpace(attribute.Value)
		}
	}
	var value mixedText
	if err := decoder.DecodeElement(&value, &start); err != nil {
		return err
	}
	identifier.Value = normalizeText(string(value))
	return nil
}

type pubmedDataXML struct {
	ArticleIDs         []identifierTextXML `xml:"ArticleIdList>ArticleId"`
	Licenses           []licenseXML        `xml:"License"`
	PublicationStatus  string              `xml:"PublicationStatus"`
	PublicationHistory []pubMedPubDateXML  `xml:"History>PubMedPubDate"`
}

type pubMedPubDateXML struct {
	Status string `xml:"PubStatus,attr"`
	dateXML
}

type licenseXML struct {
	ID   string
	Type string
	URL  string
	Text string
}

func (license *licenseXML) UnmarshalXML(
	decoder *xml.Decoder,
	start xml.StartElement,
) error {
	for _, attribute := range start.Attr {
		switch attribute.Name.Local {
		case "LicenseID":
			license.ID = strings.TrimSpace(attribute.Value)
		case "LicenseType":
			license.Type = strings.TrimSpace(attribute.Value)
		case "URL":
			license.URL = strings.TrimSpace(attribute.Value)
		}
	}
	var text mixedText
	if err := decoder.DecodeElement(&text, &start); err != nil {
		return err
	}
	license.Text = normalizeText(string(text))
	return nil
}

type mixedText string

func (text *mixedText) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	var builder strings.Builder
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			builder.Write([]byte(value))
		}
	}
	*text = mixedText(builder.String())
	return nil
}

func Parse(payload []byte) ([]source.Record, error) {
	records := make([]source.Record, 0)
	err := scanElements(bytes.NewReader(payload), "PubmedArticle", func(ordinal int64, raw []byte) error {
		record, err := ParseRecord(raw)
		if err != nil {
			return fmt.Errorf("PubMed record %d: %w", ordinal, err)
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

func ParseRecord(raw []byte) (source.Record, error) {
	rawRecord, err := source.NewRawXMLRecord(raw)
	if err != nil {
		return source.Record{}, fmt.Errorf("parse PubMed record XML: %w", err)
	}

	var article pubmedArticleXML
	if err := xml.Unmarshal(raw, &article); err != nil {
		return source.Record{}, fmt.Errorf("decode PubMed article XML: %w", err)
	}
	pmid, err := requiredPMID(article.MedlineCitation.PMID)
	if err != nil {
		return source.Record{}, err
	}

	dois, pmcids, rejectedIdentifiers, err := parseArticleIdentifiers(pmid, article)
	if err != nil {
		return source.Record{}, err
	}
	var identity paper.Identifier
	if len(dois) > 0 {
		identity, err = paper.CanonicalIdentity(paper.Identifiers{DOI: dois}, "")
		if err != nil {
			return source.Record{}, fmt.Errorf("resolve PubMed PMID %s identity: %w", pmid, err)
		}
	}

	identifiers := []source.Identifier{{Scheme: source.IdentifierPMID, Value: pmid}}
	for _, doi := range dois {
		identifiers = append(identifiers, source.Identifier{Scheme: source.IdentifierDOI, Value: doi})
	}
	for _, pmcid := range pmcids {
		identifiers = append(identifiers, source.Identifier{Scheme: source.IdentifierPMCID, Value: pmcid})
	}

	abstractSections := parseAbstractSections(article.MedlineCitation.Article.Abstract.Sections)
	abstractParts := make([]string, len(abstractSections))
	for index, section := range abstractSections {
		abstractParts[index] = section.Text
	}
	authors, authorsTruncated := parseAuthors(article.MedlineCitation.Article.AuthorList)
	venue, err := parseVenue(
		article.MedlineCitation.Article.Journal,
		article.MedlineCitation.MedlineJournalInfo,
	)
	if err != nil {
		return source.Record{}, err
	}
	meshHeadings := parseMeshHeadings(article.MedlineCitation.MeshHeadingList)
	publicationTypes := parsePublicationTypes(
		article.MedlineCitation.Article.PublicationTypeList,
	)
	relations, relationRetracted, err := parseRelations(
		article.MedlineCitation.CommentsCorrectionsList,
	)
	if err != nil {
		return source.Record{}, err
	}
	retracted := relationRetracted || hasPublicationType(publicationTypes, "Retracted Publication")
	var retractedValue *bool
	if retracted {
		value := true
		retractedValue = &value
	}

	publishedDate, publishedAt, err := parseDate(
		article.MedlineCitation.Article.Journal.Issue.PubDate,
	)
	if err != nil {
		return source.Record{}, fmt.Errorf("parse PubMed journal publication date: %w", err)
	}
	electronicPublicationDate, electronicPublishedAt, err := parseElectronicDate(
		article.MedlineCitation.Article.ArticleDates,
	)
	if err != nil {
		return source.Record{}, err
	}
	completedDate, completedAt, err := parseDate(article.MedlineCitation.DateCompleted)
	if err != nil {
		return source.Record{}, fmt.Errorf("parse PubMed completion date: %w", err)
	}
	revisionDate, revisedAt, err := parseDate(article.MedlineCitation.DateRevised)
	if err != nil {
		return source.Record{}, fmt.Errorf("parse PubMed revision date: %w", err)
	}
	publicationHistory, err := parsePublicationHistory(article.PubmedData.PublicationHistory)
	if err != nil {
		return source.Record{}, err
	}
	licenses := parseLicenses(article.PubmedData.Licenses)

	scope, err := source.NewScopeDecision(
		source.ScopePending,
		source.ScopeReasonAwaitingDeterministicEvaluation,
		nil,
	)
	if err != nil {
		return source.Record{}, err
	}
	record := source.Record{
		Source:                    source.PubMed,
		SourceRecordID:            pmid,
		Identity:                  identity,
		Identifiers:               identifiers,
		RejectedIdentifiers:       rejectedIdentifiers,
		Raw:                       rawRecord,
		Title:                     normalizeText(string(article.MedlineCitation.Article.Title)),
		Abstract:                  strings.Join(abstractParts, "\n\n"),
		AbstractSections:          abstractSections,
		CopyrightInformation:      normalizeText(string(article.MedlineCitation.Article.Abstract.CopyrightInformation)),
		PublicationModel:          strings.TrimSpace(article.MedlineCitation.Article.PublicationModel),
		PublicationStatus:         strings.TrimSpace(article.PubmedData.PublicationStatus),
		PublicationHistory:        publicationHistory,
		PublishedAt:               publishedAt,
		PublishedDate:             publishedDate,
		ElectronicPublishedAt:     electronicPublishedAt,
		ElectronicPublicationDate: electronicPublicationDate,
		CompletedAt:               completedAt,
		CompletedDate:             completedDate,
		UpdatedAt:                 cloneTime(revisedAt),
		RevisedAt:                 revisedAt,
		RevisionDate:              revisionDate,
		Authors:                   authors,
		AuthorsTruncated:          authorsTruncated,
		MeSHHeadings:              meshHeadings,
		PublicationTypes:          publicationTypes,
		Relations:                 relations,
		Venue:                     venue,
		Licenses:                  licenses,
		Retracted:                 retractedValue,
		Scope:                     scope,
	}
	record.Evidence = buildEvidence(record)
	return record, nil
}

func requiredPMID(raw string) (string, error) {
	pmid := strings.TrimSpace(raw)
	if !pmidPattern.MatchString(pmid) {
		return "", fmt.Errorf("invalid required PMID %q", raw)
	}
	return pmid, nil
}

func parseArticleIdentifiers(
	requiredPMID string,
	article pubmedArticleXML,
) ([]string, []string, []source.RejectedIdentifierAssertion, error) {
	dois := make([]string, 0)
	pmcids := make([]string, 0)
	rejected := make([]source.RejectedIdentifierAssertion, 0)
	parseCandidate := func(candidate identifierTextXML) error {
		switch strings.ToLower(candidate.Type) {
		case "pubmed":
			pmid, err := requiredPMIDValue(candidate.Value)
			if err != nil {
				return err
			}
			if pmid != requiredPMID {
				return fmt.Errorf(
					"conflicting PubMed PMID %s and ArticleId %s",
					requiredPMID,
					pmid,
				)
			}
		case "doi":
			identifier, err := paper.NewIdentifier(paper.SchemeDOI, candidate.Value)
			if err != nil {
				return fmt.Errorf("invalid PubMed DOI %q: %w", candidate.Value, err)
			}
			dois = appendUnique(dois, identifier.Value())
		case "pmc", "pmcid":
			pmcid := strings.ToUpper(strings.TrimSpace(candidate.Value))
			if !pmcidPattern.MatchString(pmcid) {
				return fmt.Errorf("invalid PubMed PMCID %q", candidate.Value)
			}
			pmcids = appendUnique(pmcids, pmcid)
		}
		return nil
	}
	for _, candidate := range article.PubmedData.ArticleIDs {
		if err := parseCandidate(candidate); err != nil {
			return nil, nil, nil, err
		}
	}
	for _, candidate := range article.MedlineCitation.Article.ELocationIDs {
		if strings.EqualFold(candidate.Type, "doi") &&
			strings.EqualFold(candidate.Valid, "N") {
			rejected = append(rejected, source.RejectedIdentifierAssertion{
				Scheme:     source.IdentifierDOI,
				Value:      strings.TrimSpace(candidate.Value),
				SourcePath: "/PubmedArticle/MedlineCitation/Article/ELocationID",
				Reason:     source.IdentifierRejectionSourceInvalid,
			})
			continue
		}
		if err := parseCandidate(candidate); err != nil {
			return nil, nil, nil, err
		}
	}
	if len(dois) > 1 {
		return nil, nil, nil, fmt.Errorf(
			"%w: PubMed record has DOI values %s",
			paper.ErrConflictingIdentifiers,
			strings.Join(dois, ", "),
		)
	}
	return dois, pmcids, rejected, nil
}

func requiredPMIDValue(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if !pmidPattern.MatchString(value) {
		return "", fmt.Errorf("invalid PubMed ArticleId PMID %q", raw)
	}
	return value, nil
}

func parseAbstractSections(values []abstractSectionXML) []source.AbstractSection {
	sections := make([]source.AbstractSection, 0, len(values))
	for _, value := range values {
		text := normalizeText(string(value.Text))
		if text == "" {
			continue
		}
		sections = append(sections, source.AbstractSection{
			Label:       strings.TrimSpace(value.Label),
			NLMCategory: strings.TrimSpace(value.NLMCategory),
			Text:        text,
		})
	}
	return sections
}

func parseAuthors(list authorListXML) ([]source.Author, *bool) {
	authors := make([]source.Author, 0, len(list.Authors))
	for index, value := range list.Authors {
		author := source.Author{
			LastName:       normalizeText(value.LastName),
			ForeName:       normalizeText(value.ForeName),
			Initials:       normalizeText(value.Initials),
			CollectiveName: normalizeText(value.CollectiveName),
			Position:       index + 1,
		}
		if author.CollectiveName != "" {
			author.DisplayName = author.CollectiveName
		} else {
			author.DisplayName = normalizeText(author.ForeName + " " + author.LastName)
			if author.DisplayName == "" {
				author.DisplayName = normalizeText(author.Initials + " " + author.LastName)
			}
		}
		for _, identifier := range value.Identifiers {
			if strings.EqualFold(identifier.Source, "ORCID") {
				author.ORCID = normalizeORCID(identifier.Value)
			}
		}
		for _, affiliation := range value.AffiliationInfo {
			name := normalizeText(string(affiliation.Affiliation))
			if name != "" {
				author.Affiliations = append(author.Affiliations, name)
			}
		}
		authors = append(authors, author)
	}

	switch strings.ToUpper(strings.TrimSpace(list.CompleteYN)) {
	case "Y":
		value := false
		return authors, &value
	case "N":
		value := true
		return authors, &value
	default:
		return authors, nil
	}
}

func normalizeORCID(value string) string {
	normalized := strings.TrimSpace(value)
	for _, prefix := range []string{"https://orcid.org/", "http://orcid.org/"} {
		if strings.HasPrefix(strings.ToLower(normalized), prefix) {
			return normalized[len(prefix):]
		}
	}
	return normalized
}

func parseVenue(
	journal journalXML,
	info medlineJournalInfoXML,
) (*source.Venue, error) {
	if normalizeText(journal.Title) == "" &&
		normalizeText(journal.ISOAbbreviation) == "" &&
		len(journal.ISSNs) == 0 &&
		strings.TrimSpace(info.ISSNLinking) == "" {
		return nil, nil
	}
	venue := &source.Venue{
		DisplayName:     normalizeText(journal.Title),
		ISOAbbreviation: normalizeText(journal.ISOAbbreviation),
		Type:            "journal",
	}
	for _, raw := range journal.ISSNs {
		value := strings.ToUpper(strings.TrimSpace(raw.Value))
		if !validISSN(value) {
			return nil, fmt.Errorf("invalid PubMed journal ISSN %q", raw.Value)
		}
		venue.ISSN = appendUnique(venue.ISSN, value)
		venue.ISSNDetails = append(venue.ISSNDetails, source.VenueISSN{
			Value: value,
			Type:  strings.TrimSpace(raw.Type),
		})
	}
	if linking := strings.ToUpper(strings.TrimSpace(info.ISSNLinking)); linking != "" {
		if !validISSN(linking) {
			return nil, fmt.Errorf("invalid PubMed ISSNLinking %q", info.ISSNLinking)
		}
		venue.ISSNL = linking
		venue.ISSN = appendUnique(venue.ISSN, linking)
	}
	return venue, nil
}

func parseMeshHeadings(values []meshHeadingXML) []source.MeSHHeading {
	headings := make([]source.MeSHHeading, 0, len(values))
	for _, value := range values {
		heading := source.MeSHHeading{
			Descriptor: meshTerm(value.Descriptor),
			Qualifiers: make([]source.MeSHTerm, 0, len(value.Qualifiers)),
		}
		for _, qualifier := range value.Qualifiers {
			heading.Qualifiers = append(heading.Qualifiers, meshTerm(qualifier))
		}
		headings = append(headings, heading)
	}
	return headings
}

func meshTerm(value namedElementXML) source.MeSHTerm {
	return source.MeSHTerm{
		UI:         value.UI,
		Name:       value.Text,
		MajorTopic: strings.EqualFold(value.MajorTopic, "Y"),
	}
}

func parsePublicationTypes(values []namedElementXML) []source.PublicationType {
	result := make([]source.PublicationType, 0, len(values))
	for _, value := range values {
		result = append(result, source.PublicationType{
			UI:   value.UI,
			Name: value.Text,
		})
	}
	return result
}

func hasPublicationType(values []source.PublicationType, name string) bool {
	for _, value := range values {
		if strings.EqualFold(value.Name, name) {
			return true
		}
	}
	return false
}

func parseRelations(values []commentsCorrectionXML) ([]source.Relation, bool, error) {
	result := make([]source.Relation, 0, len(values))
	retracted := false
	for _, value := range values {
		var pmid string
		if strings.TrimSpace(value.PMID) != "" {
			var err error
			pmid, err = requiredPMIDValue(value.PMID)
			if err != nil {
				return nil, false, fmt.Errorf("invalid PubMed relation: %w", err)
			}
		}
		relationType := strings.TrimSpace(value.Type)
		result = append(result, source.Relation{
			Type:     relationType,
			TargetID: pmid,
			Note:     normalizeText(string(value.RefSource)),
		})
		if strings.EqualFold(relationType, "RetractionIn") {
			retracted = true
		}
	}
	return result, retracted, nil
}

func parseElectronicDate(
	values []articleDateXML,
) (*source.SourceDate, *time.Time, error) {
	for _, value := range values {
		if !strings.EqualFold(value.Type, "Electronic") {
			continue
		}
		date, parsed, err := parseDate(value.dateXML)
		if err != nil {
			return nil, nil, fmt.Errorf("parse PubMed electronic publication date: %w", err)
		}
		return date, parsed, nil
	}
	return nil, nil, nil
}

func parsePublicationHistory(
	values []pubMedPubDateXML,
) ([]source.PublicationHistoryEntry, error) {
	history := make([]source.PublicationHistoryEntry, 0, len(values))
	for index, value := range values {
		ordinal := index + 1
		status := strings.TrimSpace(value.Status)
		if status == "" {
			return nil, fmt.Errorf(
				"parse PubMed publication history PubMedPubDate[%d]: requires a non-empty PubStatus",
				ordinal,
			)
		}
		date, _, err := parseDate(value.dateXML)
		if err != nil {
			return nil, fmt.Errorf(
				"parse PubMed publication history PubMedPubDate[%d]: %w",
				ordinal,
				err,
			)
		}
		if date == nil {
			return nil, fmt.Errorf(
				"parse PubMed publication history PubMedPubDate[%d]: requires a date",
				ordinal,
			)
		}
		switch date.Precision {
		case source.DatePrecisionYear,
			source.DatePrecisionMonth,
			source.DatePrecisionDay:
		default:
			return nil, fmt.Errorf(
				"parse PubMed publication history PubMedPubDate[%d]: date precision %q is not supported",
				ordinal,
				date.Precision,
			)
		}
		history = append(history, source.PublicationHistoryEntry{
			Status: status,
			Date:   *date,
			SourcePath: fmt.Sprintf(
				"/PubmedArticle/PubmedData/History/PubMedPubDate[%d]",
				ordinal,
			),
			Ordinal: ordinal,
		})
	}
	return history, nil
}

func parseDate(value dateXML) (*source.SourceDate, *time.Time, error) {
	yearText := strings.TrimSpace(value.Year)
	monthText := strings.TrimSpace(value.Month)
	dayText := strings.TrimSpace(value.Day)
	season := normalizeText(value.Season)
	medlineDate := normalizeText(value.MedlineDate)
	if medlineDate != "" {
		if yearText != "" || monthText != "" || dayText != "" || season != "" {
			return nil, nil, errors.New("date cannot combine MedlineDate with structured fields")
		}
		return &source.SourceDate{
			Raw:       medlineDate,
			Precision: source.DatePrecisionText,
		}, nil, nil
	}
	if yearText == "" && monthText == "" && dayText == "" && season == "" {
		return nil, nil, nil
	}
	if yearText == "" {
		return nil, nil, errors.New("structured date requires a year")
	}
	year, err := strconv.Atoi(yearText)
	if err != nil || year <= 0 {
		return nil, nil, fmt.Errorf("invalid year %q", yearText)
	}
	date := &source.SourceDate{
		Year:      year,
		Precision: source.DatePrecisionYear,
	}
	if season != "" {
		if monthText != "" || dayText != "" {
			return nil, nil, errors.New("structured date season cannot combine with month or day")
		}
		date.Season = season
		date.Precision = source.DatePrecisionSeason
		return date, nil, nil
	}
	if monthText == "" {
		if dayText != "" {
			return nil, nil, errors.New("structured date day requires a month")
		}
		return date, nil, nil
	}
	month, err := parseMonth(monthText)
	if err != nil {
		return nil, nil, err
	}
	date.Month = month
	date.Precision = source.DatePrecisionMonth
	if dayText == "" {
		return date, nil, nil
	}
	day, err := strconv.Atoi(dayText)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid day %q", dayText)
	}
	parsed := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if parsed.Year() != year || parsed.Month() != month || parsed.Day() != day {
		return nil, nil, fmt.Errorf("invalid date %s-%s-%s", yearText, monthText, dayText)
	}
	date.Day = day
	date.Precision = source.DatePrecisionDay
	return date, &parsed, nil
}

func parseMonth(value string) (time.Month, error) {
	if month, err := strconv.Atoi(value); err == nil {
		if month >= 1 && month <= 12 {
			return time.Month(month), nil
		}
		return 0, fmt.Errorf("invalid month %q", value)
	}
	for month := time.January; month <= time.December; month++ {
		if strings.EqualFold(value, month.String()) ||
			strings.EqualFold(value, month.String()[:3]) {
			return month, nil
		}
	}
	return 0, fmt.Errorf("invalid month %q", value)
}

func parseLicenses(values []licenseXML) []source.License {
	result := make([]source.License, 0, len(values))
	for _, value := range values {
		if value.Text == "" && value.ID == "" && value.URL == "" {
			continue
		}
		result = append(result, source.License{
			Name:       value.Text,
			ID:         value.ID,
			URL:        value.URL,
			SourcePath: "/PubmedArticle/PubmedData/License",
		})
	}
	return result
}

func buildEvidence(record source.Record) []source.FieldEvidence {
	evidence := []source.FieldEvidence{
		{Field: "identifiers", SourcePath: "/PubmedArticle/MedlineCitation/PMID"},
	}
	appendField := func(field, path string, present bool) {
		if present {
			evidence = append(evidence, source.FieldEvidence{Field: field, SourcePath: path})
		}
	}
	appendField("title", "/PubmedArticle/MedlineCitation/Article/ArticleTitle", record.Title != "")
	appendField("abstract", "/PubmedArticle/MedlineCitation/Article/Abstract", record.Abstract != "")
	appendField("authors", "/PubmedArticle/MedlineCitation/Article/AuthorList", len(record.Authors) > 0)
	appendField("authors_truncated", "/PubmedArticle/MedlineCitation/Article/AuthorList/@CompleteYN", record.AuthorsTruncated != nil)
	appendField("mesh_headings", "/PubmedArticle/MedlineCitation/MeshHeadingList", len(record.MeSHHeadings) > 0)
	appendField("publication_types", "/PubmedArticle/MedlineCitation/Article/PublicationTypeList", len(record.PublicationTypes) > 0)
	appendField("publication_model", "/PubmedArticle/MedlineCitation/Article/@PubModel", record.PublicationModel != "")
	appendField("publication_status", "/PubmedArticle/PubmedData/PublicationStatus", record.PublicationStatus != "")
	appendField("publication_history", "/PubmedArticle/PubmedData/History/PubMedPubDate", len(record.PublicationHistory) > 0)
	appendField("relations", "/PubmedArticle/MedlineCitation/CommentsCorrectionsList", len(record.Relations) > 0)
	appendField("published_at", "/PubmedArticle/MedlineCitation/Article/Journal/JournalIssue/PubDate", record.PublishedAt != nil)
	appendField("electronic_published_at", "/PubmedArticle/MedlineCitation/Article/ArticleDate", record.ElectronicPublishedAt != nil)
	appendField("revised_at", "/PubmedArticle/MedlineCitation/DateRevised", record.RevisedAt != nil)
	appendField("venue", "/PubmedArticle/MedlineCitation/Article/Journal", record.Venue != nil)
	appendField("licenses", "/PubmedArticle/PubmedData/License", len(record.Licenses) > 0)
	appendField("retracted", "/PubmedArticle/MedlineCitation", record.Retracted != nil)
	return evidence
}

func normalizeText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func scanElements(
	reader io.Reader,
	elementName string,
	yield func(ordinal int64, raw []byte) error,
) error {
	capture := newCaptureReader(reader, DefaultMaxBulkRecordBytes)
	decoder := xml.NewDecoder(capture)
	var ordinal int64

	for {
		startOffset := decoder.InputOffset()
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode PubMed XML stream: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != elementName {
			capture.discardBefore(decoder.InputOffset())
			continue
		}
		capture.discardBefore(startOffset)
		if err := capture.beginCapture(startOffset); err != nil {
			return fmt.Errorf("capture PubMed %s element: %w", elementName, err)
		}

		depth := 1
		for depth > 0 {
			token, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode PubMed %s element: %w", elementName, err)
			}
			switch token.(type) {
			case xml.StartElement:
				depth++
			case xml.EndElement:
				depth--
			}
		}
		endOffset := decoder.InputOffset()
		raw, err := capture.slice(startOffset, endOffset)
		if err != nil {
			return err
		}
		capture.endCapture()
		ordinal++
		if err := yield(ordinal, raw); err != nil {
			return err
		}
		capture.discardBefore(endOffset)
	}
}

type captureReader struct {
	reader          io.Reader
	base            int64
	data            []byte
	maxCaptureBytes int64
	captureStart    int64
	capturing       bool
}

func newCaptureReader(reader io.Reader, maxCaptureBytes int64) *captureReader {
	return &captureReader{
		reader:          reader,
		maxCaptureBytes: maxCaptureBytes,
	}
}

func (reader *captureReader) Read(payload []byte) (int, error) {
	const maxReadBytes = 32 << 10
	readLimit := int64(maxReadBytes)
	if reader.maxCaptureBytes < readLimit {
		readLimit = reader.maxCaptureBytes
	}
	if int64(len(payload)) > readLimit {
		payload = payload[:readLimit]
	}
	retained := int64(len(reader.data))
	if reader.capturing {
		retained = reader.base + int64(len(reader.data)) - reader.captureStart
	}
	remaining := reader.maxCaptureBytes - retained
	if remaining <= 0 {
		return 0, ErrBulkRecordTooLarge
	}
	if int64(len(payload)) > remaining {
		payload = payload[:remaining]
	}
	count, err := reader.reader.Read(payload)
	reader.data = append(reader.data, payload[:count]...)
	return count, err
}

func (reader *captureReader) beginCapture(start int64) error {
	if reader.capturing {
		return errors.New("PubMed XML capture is already active")
	}
	if start < reader.base {
		return fmt.Errorf(
			"PubMed XML capture start %d precedes buffered offset %d",
			start,
			reader.base,
		)
	}
	buffered := reader.base + int64(len(reader.data)) - start
	if buffered > reader.maxCaptureBytes {
		return ErrBulkRecordTooLarge
	}
	reader.captureStart = start
	reader.capturing = true
	return nil
}

func (reader *captureReader) endCapture() {
	reader.capturing = false
	reader.captureStart = 0
}

func (reader *captureReader) slice(start, end int64) ([]byte, error) {
	if start < reader.base || end < start || end-reader.base > int64(len(reader.data)) {
		return nil, fmt.Errorf(
			"PubMed XML capture offsets [%d,%d) outside buffered range [%d,%d)",
			start,
			end,
			reader.base,
			reader.base+int64(len(reader.data)),
		)
	}
	return append([]byte(nil), reader.data[start-reader.base:end-reader.base]...), nil
}

func (reader *captureReader) discardBefore(offset int64) {
	if offset <= reader.base {
		return
	}
	count := min(offset-reader.base, int64(len(reader.data)))
	reader.data = append(reader.data[:0], reader.data[count:]...)
	reader.base += count
}
