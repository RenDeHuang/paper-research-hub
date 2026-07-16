package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
)

const OpenAlex = "openalex"

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
	Payload json.RawMessage
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
		Payload: append(json.RawMessage(nil), payload...),
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

type Record struct {
	Source           string
	SourceRecordID   string
	Identity         paper.Identifier
	Identifiers      []Identifier
	Raw              RawRecord
	Evidence         []FieldEvidence
	Title            string
	Abstract         string
	PublishedAt      *time.Time
	CreatedAt        *time.Time
	UpdatedAt        *time.Time
	Authors          []Author
	AuthorsTruncated *bool
	Topics           []Topic
	Keywords         []Keyword
	CitedByCount     *int
	Venue            *Venue
	OpenAccess       OpenAccess
	Licenses         []License
	Retracted        *bool
	CodeURLs         []string
	Scope            ScopeDecision
}

type Author struct {
	OpenAlexID      string
	DisplayName     string
	ORCID           string
	Position        int
	PositionLabel   string
	IsCorresponding bool
	Institutions    []Institution
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
	OpenAlexID  string
	DisplayName string
	Type        string
	ISSNL       string
	ISSN        []string
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
