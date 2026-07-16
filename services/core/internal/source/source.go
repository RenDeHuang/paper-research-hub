package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
)

const (
	OpenAlex = "openalex"
	PubMed   = "pubmed"
	Crossref = "crossref"
)

type ClientSequence = iter.Seq2[Record, error]

type Client interface {
	Fetch(context.Context, Query) ClientSequence
}

type Query struct {
	Search     string
	Filter     string
	MaxResults int
}

type IdentifierScheme string

const (
	IdentifierDOI      IdentifierScheme = "doi"
	IdentifierArXiv    IdentifierScheme = "arxiv"
	IdentifierOpenAlex IdentifierScheme = "openalex"
	IdentifierPMID     IdentifierScheme = "pmid"
	IdentifierPMCID    IdentifierScheme = "pmcid"
)

type Identifier struct {
	Scheme IdentifierScheme
	Value  string
}

type RawRecord struct {
	Payload []byte
	SHA256  string
}

func NewRawRecord(payload []byte) (RawRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()

	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return RawRecord{}, fmt.Errorf("decode source record JSON: %w", err)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return RawRecord{}, errors.New("source record JSON must be an object")
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return RawRecord{}, errors.New("source record JSON must contain exactly one object")
		}
		return RawRecord{}, fmt.Errorf("decode trailing source record JSON: %w", err)
	}

	canonical, err := json.Marshal(decoded)
	if err != nil {
		return RawRecord{}, fmt.Errorf("canonicalize source record JSON: %w", err)
	}
	digest := sha256.Sum256(canonical)

	return RawRecord{
		Payload: append([]byte(nil), payload...),
		SHA256:  hex.EncodeToString(digest[:]),
	}, nil
}

func NewRawXMLRecord(payload []byte) (RawRecord, error) {
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	depth := 0
	roots := 0

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return RawRecord{}, fmt.Errorf("decode source record XML: %w", err)
		}

		switch value := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots > 1 {
					return RawRecord{}, errors.New("source record XML must contain exactly one element")
				}
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				return RawRecord{}, errors.New("source record XML cannot contain text outside its root element")
			}
		}
	}
	if roots != 1 || depth != 0 {
		return RawRecord{}, errors.New("source record XML must contain exactly one complete element")
	}

	digest := sha256.Sum256(payload)
	return RawRecord{
		Payload: append([]byte(nil), payload...),
		SHA256:  hex.EncodeToString(digest[:]),
	}, nil
}

type ScopeStatus string

const (
	ScopePending  ScopeStatus = "pending"
	ScopeIncluded ScopeStatus = "included"
	ScopeExcluded ScopeStatus = "excluded"

	ScopeReasonAwaitingDeterministicEvaluation = "awaiting_deterministic_scope_evaluation"
)

type ScopeDecision struct {
	Status   ScopeStatus
	Reason   string
	Evidence []FieldEvidence
}

func NewScopeDecision(
	status ScopeStatus,
	reason string,
	evidence []FieldEvidence,
) (ScopeDecision, error) {
	switch status {
	case ScopePending, ScopeIncluded, ScopeExcluded:
	default:
		return ScopeDecision{}, fmt.Errorf("invalid scope status %q", status)
	}

	normalizedReason := strings.TrimSpace(reason)
	if normalizedReason == "" {
		return ScopeDecision{}, errors.New("scope decision reason is required")
	}

	return ScopeDecision{
		Status:   status,
		Reason:   normalizedReason,
		Evidence: append([]FieldEvidence(nil), evidence...),
	}, nil
}

type FieldEvidence struct {
	Field      string
	SourcePath string
}

type DatePrecision string

const (
	DatePrecisionYear   DatePrecision = "year"
	DatePrecisionMonth  DatePrecision = "month"
	DatePrecisionDay    DatePrecision = "day"
	DatePrecisionSeason DatePrecision = "season"
	DatePrecisionText   DatePrecision = "text"
)

type SourceDate struct {
	Year      int
	Month     time.Month
	Day       int
	Season    string
	Raw       string
	Precision DatePrecision
}

const IdentifierRejectionSourceInvalid = "source_marked_invalid"

type RejectedIdentifierAssertion struct {
	Scheme     IdentifierScheme
	Value      string
	SourcePath string
	Reason     string
}

type Record struct {
	Source                    string
	SourceRecordID            string
	Identity                  paper.Identifier
	Identifiers               []Identifier
	RejectedIdentifiers       []RejectedIdentifierAssertion
	Raw                       RawRecord
	Evidence                  []FieldEvidence
	Title                     string
	Abstract                  string
	Publisher                 string
	AbstractSections          []AbstractSection
	CopyrightInformation      string
	PublishedAt               *time.Time
	PublishedDate             *SourceDate
	ElectronicPublishedAt     *time.Time
	ElectronicPublicationDate *SourceDate
	CreatedAt                 *time.Time
	CompletedAt               *time.Time
	CompletedDate             *SourceDate
	UpdatedAt                 *time.Time
	RevisedAt                 *time.Time
	RevisionDate              *SourceDate
	Authors                   []Author
	AuthorsTruncated          *bool
	MeSHHeadings              []MeSHHeading
	PublicationTypes          []PublicationType
	Relations                 []Relation
	Topics                    []Topic
	Keywords                  []Keyword
	CitedByCount              *int
	Venue                     *Venue
	OpenAccess                OpenAccess
	Licenses                  []License
	Retracted                 *bool
	CodeURLs                  []string
	Scope                     ScopeDecision
}

type Author struct {
	OpenAlexID      string
	DisplayName     string
	LastName        string
	ForeName        string
	Initials        string
	CollectiveName  string
	ORCID           string
	Position        int
	PositionLabel   string
	IsCorresponding bool
	Affiliations    []string
	Institutions    []Institution
}

type AbstractSection struct {
	Label       string
	NLMCategory string
	Text        string
}

type MeSHTerm struct {
	UI         string
	Name       string
	MajorTopic bool
}

type MeSHHeading struct {
	Descriptor MeSHTerm
	Qualifiers []MeSHTerm
}

type PublicationType struct {
	UI   string
	Name string
}

type Relation struct {
	Type     string
	TargetID string
	Note     string
}

type Institution struct {
	OpenAlexID  string
	DisplayName string
	ROR         string
	CountryCode string
	Type        string
}

type TopicLevel struct {
	ID          string
	DisplayName string
}

type Topic struct {
	OpenAlexID  string
	DisplayName string
	Score       *float64
	Subfield    TopicLevel
	Field       TopicLevel
	Domain      TopicLevel
}

type Keyword struct {
	OpenAlexID  string
	DisplayName string
	Score       *float64
}

type Venue struct {
	OpenAlexID      string
	DisplayName     string
	ISOAbbreviation string
	Type            string
	ISSNL           string
	ISSN            []string
	ISSNDetails     []VenueISSN
}

type VenueISSN struct {
	Value string
	Type  string
}

type OpenAccess struct {
	IsOA                     *bool
	Status                   string
	URL                      string
	AnyRepositoryHasFulltext *bool
}

type License struct {
	Name       string
	ID         string
	URL        string
	SourcePath string
}
