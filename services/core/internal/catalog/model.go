package catalog

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrCatalogNotPublished = errors.New("public catalog is not published")
	ErrNotFound            = errors.New("public catalog resource was not found")
	ErrInvalidCursor       = errors.New("public catalog cursor is invalid")
	ErrCursorConflict      = errors.New("public catalog cursor context conflicts with request")
	ErrInvalidQuery        = errors.New("public catalog query is invalid")
	ErrEmptyDomain         = errors.New("public catalog domain has no included visible works")
	ErrCatalogNotReady     = errors.New("public catalog source state is not ready")
)

type PublishInput struct {
	FormulaVersion     string
	GeneratedAt        time.Time
	JCRMetricYear      int
	VenuePolicyName    string
	VenuePolicyVersion int
	SubjectVersion     string
	JCRImportReceipt   uuid.UUID
}

type Generation struct {
	ID             uuid.UUID
	SourceRevision string
	FormulaVersion string
	GeneratedAt    time.Time
	PublishedAt    time.Time
}

type Document struct {
	Generation Generation
	Payload    json.RawMessage
}

type Pagination struct {
	Limit      int
	Total      int64
	NextCursor string
	HasMore    bool
}

type FacetBucket struct {
	Key   string
	Label string
	Count int64
}

type Facets struct {
	PaperTypes []FacetBucket
	Topics     []FacetBucket
	Methods    []FacetBucket
	Statuses   []FacetBucket
	Sources    []FacetBucket
}

type PaperPage struct {
	Generation Generation
	Items      []json.RawMessage
	Pagination Pagination
	Facets     Facets
}

type TaxonomyPage struct {
	Generation Generation
	Items      []json.RawMessage
	Pagination Pagination
}

type TrendPage struct {
	Generation Generation
	WindowDays int
	Items      []json.RawMessage
	Pagination Pagination
}

type OpportunityPage struct {
	Generation Generation
	Items      []json.RawMessage
	Pagination Pagination
}

type PaperSort string

const (
	PaperSortPublishedAtDesc PaperSort = "published_at_desc"
	PaperSortCitationsDesc   PaperSort = "citations_desc"
	PaperSortTrendDesc       PaperSort = "trend_desc"
	PaperSortRelevance       PaperSort = "relevance"
)

type PaperListQuery struct {
	Query         string
	PublishedFrom *time.Time
	PublishedTo   *time.Time
	PaperType     string
	Topic         string
	Method        string
	HasCode       *bool
	HasData       *bool
	HasBenchmark  *bool
	Status        string
	Source        string
	Sort          PaperSort
	Limit         int
	Cursor        string
}

type PageQuery struct {
	Limit  int
	Cursor string
}

type TrendKind string

const (
	TrendKindPapers  TrendKind = "papers"
	TrendKindTopics  TrendKind = "topics"
	TrendKindMethods TrendKind = "methods"
)

type TrendQuery struct {
	WindowDays int
	Limit      int
	Cursor     string
}

type OpportunityQuery struct {
	Status string
	Limit  int
	Cursor string
}
