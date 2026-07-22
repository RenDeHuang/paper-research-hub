package ingestion

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

var lowerSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type EventKind string

const (
	EventKindUpsert EventKind = "upsert"
	EventKindDelete EventKind = "delete"
)

func (kind EventKind) Valid() bool {
	switch kind {
	case EventKindUpsert, EventKindDelete:
		return true
	default:
		return false
	}
}

func (kind EventKind) String() string {
	return string(kind)
}

// Event is a validated ingestion input. Implementations are intentionally
// closed so every event reaching Service has one of the two explicit shapes.
type Event interface {
	Kind() EventKind
	Validate() error
	cloneEvent() Event
}

type RawObservationBoundary struct {
	ConnectorRunID string
	ObservedAt     time.Time
	PageOrdinal    int
	RecordOrdinal  int
}

func NewRawObservationBoundary(
	connectorRunID string,
	observedAt time.Time,
	pageOrdinal int,
	recordOrdinal int,
) (RawObservationBoundary, error) {
	boundary := RawObservationBoundary{
		ConnectorRunID: strings.TrimSpace(connectorRunID),
		ObservedAt:     observedAt.UTC().Truncate(time.Microsecond),
		PageOrdinal:    pageOrdinal,
		RecordOrdinal:  recordOrdinal,
	}
	if err := boundary.Validate(); err != nil {
		return RawObservationBoundary{}, err
	}
	return boundary, nil
}

func (boundary RawObservationBoundary) Validate() error {
	if _, err := uuid.Parse(boundary.ConnectorRunID); err != nil {
		return errors.New("raw observation connector run ID must be a UUID")
	}
	if boundary.ObservedAt.IsZero() {
		return errors.New("raw observation observed_at is required")
	}
	if boundary.ObservedAt != boundary.ObservedAt.UTC().Truncate(time.Microsecond) {
		return errors.New(
			"raw observation observed_at requires UTC PostgreSQL microsecond precision",
		)
	}
	if boundary.PageOrdinal < 1 {
		return errors.New("raw observation page ordinal must be positive")
	}
	if boundary.RecordOrdinal < 1 {
		return errors.New("raw observation record ordinal must be positive")
	}
	return nil
}

type Envelope struct {
	LogicalSource  string
	EventKey       string
	SourceTime     time.Time
	TieBreakKey    string
	Position       int64
	Record         source.Record
	Raw            source.RawRecord
	RawObservation *RawObservationBoundary
}

func NewEnvelope(
	logicalSource string,
	eventKey string,
	sourceTime time.Time,
	tieBreakKey string,
	position int64,
	record source.Record,
	raw source.RawRecord,
) (Envelope, error) {
	envelope := Envelope{
		LogicalSource: logicalSource,
		EventKey:      strings.TrimSpace(eventKey),
		SourceTime:    sourceTime.UTC(),
		TieBreakKey:   strings.TrimSpace(tieBreakKey),
		Position:      position,
		Record:        cloneSourceRecord(record),
		Raw:           cloneRawRecord(raw),
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func (envelope Envelope) Kind() EventKind {
	return EventKindUpsert
}

func (envelope Envelope) Validate() error {
	if err := validateEventMetadata(
		envelope.LogicalSource,
		envelope.EventKey,
		envelope.SourceTime,
		envelope.TieBreakKey,
		envelope.Position,
	); err != nil {
		return err
	}
	if envelope.Record.Source == "" {
		return errors.New("ingestion record source is required")
	}
	if strings.TrimSpace(envelope.Record.Source) != envelope.Record.Source {
		return errors.New("ingestion record source must be trimmed")
	}
	if envelope.Record.Source != envelope.LogicalSource {
		return fmt.Errorf(
			"record source %q conflicts with logical source %q",
			envelope.Record.Source,
			envelope.LogicalSource,
		)
	}
	if strings.TrimSpace(envelope.Record.SourceRecordID) == "" {
		return errors.New("ingestion record source record ID is required")
	}
	if err := validateRawRecord(envelope.Raw); err != nil {
		return err
	}
	if err := validateRawRecord(envelope.Record.Raw); err != nil {
		return fmt.Errorf("record raw payload: %w", err)
	}
	if envelope.Record.Raw.SHA256 != envelope.Raw.SHA256 ||
		!bytes.Equal(envelope.Record.Raw.Payload, envelope.Raw.Payload) {
		return errors.New("record raw payload conflicts with envelope raw payload")
	}
	if envelope.RawObservation != nil {
		if err := envelope.RawObservation.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (envelope Envelope) WithRawObservation(
	boundary RawObservationBoundary,
) (Envelope, error) {
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	normalized, err := NewRawObservationBoundary(
		boundary.ConnectorRunID,
		boundary.ObservedAt,
		boundary.PageOrdinal,
		boundary.RecordOrdinal,
	)
	if err != nil {
		return Envelope{}, err
	}
	observed := envelope.Clone()
	observed.RawObservation = &normalized
	if err := observed.Validate(); err != nil {
		return Envelope{}, err
	}
	return observed, nil
}

func (envelope Envelope) Clone() Envelope {
	cloned := Envelope{
		LogicalSource: envelope.LogicalSource,
		EventKey:      envelope.EventKey,
		SourceTime:    envelope.SourceTime,
		TieBreakKey:   envelope.TieBreakKey,
		Position:      envelope.Position,
		Record:        cloneSourceRecord(envelope.Record),
		Raw:           cloneRawRecord(envelope.Raw),
	}
	if envelope.RawObservation != nil {
		observation := *envelope.RawObservation
		cloned.RawObservation = &observation
	}
	return cloned
}

func (envelope Envelope) cloneEvent() Event {
	cloned := envelope.Clone()
	return cloned
}

type DeletionEnvelope struct {
	LogicalSource string
	EventKey      string
	SourceTime    time.Time
	TieBreakKey   string
	Position      int64
	Raw           source.RawRecord
}

func NewDeletionEnvelope(
	logicalSource string,
	eventKey string,
	sourceTime time.Time,
	tieBreakKey string,
	position int64,
	raw source.RawRecord,
) (DeletionEnvelope, error) {
	envelope := DeletionEnvelope{
		LogicalSource: logicalSource,
		EventKey:      strings.TrimSpace(eventKey),
		SourceTime:    sourceTime.UTC(),
		TieBreakKey:   strings.TrimSpace(tieBreakKey),
		Position:      position,
		Raw:           cloneRawRecord(raw),
	}
	if err := envelope.Validate(); err != nil {
		return DeletionEnvelope{}, err
	}
	return envelope, nil
}

func (envelope DeletionEnvelope) Kind() EventKind {
	return EventKindDelete
}

func (envelope DeletionEnvelope) Validate() error {
	if err := validateEventMetadata(
		envelope.LogicalSource,
		envelope.EventKey,
		envelope.SourceTime,
		envelope.TieBreakKey,
		envelope.Position,
	); err != nil {
		return err
	}
	return validateRawRecord(envelope.Raw)
}

func (envelope DeletionEnvelope) Clone() DeletionEnvelope {
	return DeletionEnvelope{
		LogicalSource: envelope.LogicalSource,
		EventKey:      envelope.EventKey,
		SourceTime:    envelope.SourceTime,
		TieBreakKey:   envelope.TieBreakKey,
		Position:      envelope.Position,
		Raw:           cloneRawRecord(envelope.Raw),
	}
}

func (envelope DeletionEnvelope) cloneEvent() Event {
	cloned := envelope.Clone()
	return cloned
}

func validateEventMetadata(
	logicalSource string,
	eventKey string,
	sourceTime time.Time,
	tieBreakKey string,
	position int64,
) error {
	if logicalSource == "" {
		return errors.New("ingestion logical source is required")
	}
	if strings.TrimSpace(logicalSource) != logicalSource {
		return errors.New("ingestion logical source must be trimmed")
	}
	if strings.TrimSpace(eventKey) == "" {
		return errors.New("ingestion event key is required")
	}
	if sourceTime.IsZero() {
		return errors.New("ingestion source time is required")
	}
	if strings.TrimSpace(tieBreakKey) == "" {
		return errors.New("ingestion tie-break key is required")
	}
	if position <= 0 {
		return errors.New("ingestion position must be positive")
	}
	return nil
}

func validateRawRecord(raw source.RawRecord) error {
	if len(raw.Payload) == 0 {
		return errors.New("ingestion raw payload is required")
	}
	if !lowerSHA256Pattern.MatchString(raw.SHA256) {
		return errors.New("ingestion raw payload requires a lowercase SHA256")
	}
	return nil
}

func cloneRawRecord(raw source.RawRecord) source.RawRecord {
	return source.RawRecord{
		Payload: slices.Clone(raw.Payload),
		SHA256:  raw.SHA256,
	}
}

func cloneSourceRecord(record source.Record) source.Record {
	cloned := record
	cloned.Raw = cloneRawRecord(record.Raw)
	cloned.Identifiers = slices.Clone(record.Identifiers)
	cloned.RejectedIdentifiers = slices.Clone(record.RejectedIdentifiers)
	cloned.Evidence = slices.Clone(record.Evidence)
	cloned.AbstractSections = slices.Clone(record.AbstractSections)
	cloned.PublicationHistory = slices.Clone(record.PublicationHistory)
	cloned.PublishedAt = clonePointer(record.PublishedAt)
	cloned.PublishedDate = clonePointer(record.PublishedDate)
	cloned.ElectronicPublishedAt = clonePointer(record.ElectronicPublishedAt)
	cloned.ElectronicPublicationDate = clonePointer(record.ElectronicPublicationDate)
	cloned.CreatedAt = clonePointer(record.CreatedAt)
	cloned.CompletedAt = clonePointer(record.CompletedAt)
	cloned.CompletedDate = clonePointer(record.CompletedDate)
	cloned.UpdatedAt = clonePointer(record.UpdatedAt)
	cloned.RevisedAt = clonePointer(record.RevisedAt)
	cloned.RevisionDate = clonePointer(record.RevisionDate)
	cloned.Authors = cloneAuthors(record.Authors)
	cloned.AuthorsTruncated = clonePointer(record.AuthorsTruncated)
	cloned.MeSHHeadings = cloneMeSHHeadings(record.MeSHHeadings)
	cloned.PublicationTypes = slices.Clone(record.PublicationTypes)
	cloned.Relations = slices.Clone(record.Relations)
	cloned.Topics = cloneTopics(record.Topics)
	cloned.Keywords = cloneKeywords(record.Keywords)
	cloned.CitedByCount = clonePointer(record.CitedByCount)
	cloned.Venue = cloneVenue(record.Venue)
	cloned.OpenAccess = source.OpenAccess{
		IsOA:                     clonePointer(record.OpenAccess.IsOA),
		Status:                   record.OpenAccess.Status,
		URL:                      record.OpenAccess.URL,
		AnyRepositoryHasFulltext: clonePointer(record.OpenAccess.AnyRepositoryHasFulltext),
	}
	cloned.Licenses = slices.Clone(record.Licenses)
	cloned.URLCandidates = slices.Clone(record.URLCandidates)
	cloned.Retracted = clonePointer(record.Retracted)
	cloned.CodeURLs = slices.Clone(record.CodeURLs)
	cloned.Scope = source.ScopeDecision{
		Status:   record.Scope.Status,
		Reason:   record.Scope.Reason,
		Evidence: slices.Clone(record.Scope.Evidence),
	}
	return cloned
}

func cloneAuthors(authors []source.Author) []source.Author {
	if authors == nil {
		return nil
	}
	cloned := make([]source.Author, len(authors))
	for index, author := range authors {
		cloned[index] = author
		cloned[index].Affiliations = slices.Clone(author.Affiliations)
		cloned[index].Institutions = slices.Clone(author.Institutions)
	}
	return cloned
}

func cloneMeSHHeadings(headings []source.MeSHHeading) []source.MeSHHeading {
	if headings == nil {
		return nil
	}
	cloned := make([]source.MeSHHeading, len(headings))
	for index, heading := range headings {
		cloned[index] = heading
		cloned[index].Qualifiers = slices.Clone(heading.Qualifiers)
	}
	return cloned
}

func cloneTopics(topics []source.Topic) []source.Topic {
	if topics == nil {
		return nil
	}
	cloned := make([]source.Topic, len(topics))
	for index, topic := range topics {
		cloned[index] = topic
		cloned[index].Score = clonePointer(topic.Score)
	}
	return cloned
}

func cloneKeywords(keywords []source.Keyword) []source.Keyword {
	if keywords == nil {
		return nil
	}
	cloned := make([]source.Keyword, len(keywords))
	for index, keyword := range keywords {
		cloned[index] = keyword
		cloned[index].Score = clonePointer(keyword.Score)
	}
	return cloned
}

func cloneVenue(venue *source.Venue) *source.Venue {
	if venue == nil {
		return nil
	}
	cloned := *venue
	cloned.ISSN = slices.Clone(venue.ISSN)
	cloned.ISSNDetails = slices.Clone(venue.ISSNDetails)
	return &cloned
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
