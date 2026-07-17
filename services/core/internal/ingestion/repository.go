package ingestion

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

type EventSequence = iter.Seq2[Event, error]

type ErrIdempotencyConflict struct {
	IdempotencyKey string
	ExistingJobID  string
	Cause          error
}

func (conflict *ErrIdempotencyConflict) Error() string {
	if conflict == nil {
		return "ingestion idempotency conflict"
	}
	message := "ingestion idempotency conflict"
	if strings.TrimSpace(conflict.IdempotencyKey) != "" {
		message += fmt.Sprintf(" for key %q", conflict.IdempotencyKey)
	}
	if strings.TrimSpace(conflict.ExistingJobID) != "" {
		message += fmt.Sprintf(" with existing job %q", conflict.ExistingJobID)
	}
	if conflict.Cause != nil {
		message += ": " + conflict.Cause.Error()
	}
	return message
}

func (conflict *ErrIdempotencyConflict) Unwrap() error {
	if conflict == nil {
		return nil
	}
	return conflict.Cause
}

type CanonicalConflictError struct {
	LogicalSource        string
	EventKey             string
	ExistingCanonicalKey string
	IncomingCanonicalKey string
	Cause                error
}

func (conflict *CanonicalConflictError) Error() string {
	if conflict == nil {
		return "canonical identity conflict"
	}
	message := "canonical identity conflict"
	if strings.TrimSpace(conflict.LogicalSource) != "" ||
		strings.TrimSpace(conflict.EventKey) != "" {
		message += fmt.Sprintf(
			" for %s/%s",
			conflict.LogicalSource,
			conflict.EventKey,
		)
	}
	if strings.TrimSpace(conflict.ExistingCanonicalKey) != "" ||
		strings.TrimSpace(conflict.IncomingCanonicalKey) != "" {
		message += fmt.Sprintf(
			": existing %q, incoming %q",
			conflict.ExistingCanonicalKey,
			conflict.IncomingCanonicalKey,
		)
	}
	if conflict.Cause != nil {
		message += ": " + conflict.Cause.Error()
	}
	return message
}

func (conflict *CanonicalConflictError) Unwrap() error {
	if conflict == nil {
		return nil
	}
	return conflict.Cause
}

type BatchLocker interface {
	Acquire(context.Context, string) (BatchLease, error)
}

type BatchLease interface {
	Release(context.Context) error
}

type JobRepository interface {
	// Start, SetStage, Complete, and Fail persist job lifecycle independently
	// from raw and projection transactions. Summary must read durable state.
	Start(context.Context, Job) (Job, error)
	SetStage(context.Context, Job) error
	Complete(context.Context, Job) error
	Fail(context.Context, Job, error) error
	Summary(context.Context, string) (JobSummary, error)
	RecordSideEffectFailure(context.Context, string, SideEffectFailure) error
}

type RawDisposition string

const (
	RawDispositionInserted RawDisposition = "inserted"
	RawDispositionReused   RawDisposition = "reused"
)

func (disposition RawDisposition) Valid() bool {
	switch disposition {
	case RawDispositionInserted, RawDispositionReused:
		return true
	default:
		return false
	}
}

type PersistedRaw struct {
	ID          string
	JobID       string
	Disposition RawDisposition
	Envelope    Envelope
}

func NewPersistedRaw(
	id string,
	jobID string,
	envelope Envelope,
	disposition RawDisposition,
) (PersistedRaw, error) {
	persisted := PersistedRaw{
		ID:          strings.TrimSpace(id),
		JobID:       strings.TrimSpace(jobID),
		Disposition: disposition,
		Envelope:    envelope.Clone(),
	}
	if err := persisted.Validate(); err != nil {
		return PersistedRaw{}, err
	}
	return persisted, nil
}

func (persisted PersistedRaw) Validate() error {
	if strings.TrimSpace(persisted.ID) == "" {
		return errors.New("persisted raw record requires an ID")
	}
	if strings.TrimSpace(persisted.JobID) == "" {
		return errors.New("persisted raw record requires a job ID")
	}
	if !persisted.Disposition.Valid() {
		return fmt.Errorf("invalid raw persistence disposition %q", persisted.Disposition)
	}
	return persisted.Envelope.Validate()
}

func (persisted PersistedRaw) Clone() PersistedRaw {
	persisted.Envelope = persisted.Envelope.Clone()
	return persisted
}

type PersistedDeletion struct {
	ID          string
	JobID       string
	Disposition RawDisposition
	Envelope    DeletionEnvelope
}

func NewPersistedDeletion(
	id string,
	jobID string,
	envelope DeletionEnvelope,
	disposition RawDisposition,
) (PersistedDeletion, error) {
	persisted := PersistedDeletion{
		ID:          strings.TrimSpace(id),
		JobID:       strings.TrimSpace(jobID),
		Disposition: disposition,
		Envelope:    envelope.Clone(),
	}
	if err := persisted.Validate(); err != nil {
		return PersistedDeletion{}, err
	}
	return persisted, nil
}

func (persisted PersistedDeletion) Validate() error {
	if strings.TrimSpace(persisted.ID) == "" {
		return errors.New("persisted deletion requires an ID")
	}
	if strings.TrimSpace(persisted.JobID) == "" {
		return errors.New("persisted deletion requires a job ID")
	}
	if !persisted.Disposition.Valid() {
		return fmt.Errorf("invalid deletion persistence disposition %q", persisted.Disposition)
	}
	return persisted.Envelope.Validate()
}

func (persisted PersistedDeletion) Clone() PersistedDeletion {
	persisted.Envelope = persisted.Envelope.Clone()
	return persisted
}

type RawRepository interface {
	// Each successful method return confirms one durable append-only raw write.
	PersistRaw(context.Context, string, Envelope) (PersistedRaw, error)
	PersistDeletion(context.Context, string, DeletionEnvelope) (PersistedDeletion, error)
}

type NormalizedRecord struct {
	AssertionID          string
	PayloadSchemaVersion string
	RawID                string
	JobID                string
	LogicalSource        string
	EventKey             string
	SourceTime           time.Time
	TieBreakKey          string
	Position             int64
	Record               source.Record
}

func NewNormalizedRecord(
	raw PersistedRaw,
	record source.Record,
	assertionID string,
	payloadSchemaVersion string,
) (NormalizedRecord, error) {
	normalized := NormalizedRecord{
		AssertionID:          strings.TrimSpace(assertionID),
		PayloadSchemaVersion: strings.TrimSpace(payloadSchemaVersion),
		RawID:                raw.ID,
		JobID:                raw.JobID,
		LogicalSource:        raw.Envelope.LogicalSource,
		EventKey:             raw.Envelope.EventKey,
		SourceTime:           raw.Envelope.SourceTime,
		TieBreakKey:          raw.Envelope.TieBreakKey,
		Position:             raw.Envelope.Position,
		Record:               cloneSourceRecord(record),
	}
	if err := raw.Validate(); err != nil {
		return NormalizedRecord{}, err
	}
	if err := normalized.Validate(); err != nil {
		return NormalizedRecord{}, err
	}
	return normalized, nil
}

func (record NormalizedRecord) Validate() error {
	if strings.TrimSpace(record.AssertionID) == "" {
		return errors.New("normalized record requires an assertion ID")
	}
	if strings.TrimSpace(record.PayloadSchemaVersion) == "" {
		return errors.New("normalized record requires a payload schema version")
	}
	if strings.TrimSpace(record.RawID) == "" {
		return errors.New("normalized record requires a raw ID")
	}
	if strings.TrimSpace(record.JobID) == "" {
		return errors.New("normalized record requires a job ID")
	}
	if err := validateEventMetadata(
		record.LogicalSource,
		record.EventKey,
		record.SourceTime,
		record.TieBreakKey,
		record.Position,
	); err != nil {
		return err
	}
	if strings.TrimSpace(record.Record.Source) != record.LogicalSource {
		return fmt.Errorf(
			"normalized record source %q conflicts with logical source %q",
			record.Record.Source,
			record.LogicalSource,
		)
	}
	if strings.TrimSpace(record.Record.SourceRecordID) == "" {
		return errors.New("normalized record source record ID is required")
	}
	return nil
}

func (record NormalizedRecord) Clone() NormalizedRecord {
	record.Record = cloneSourceRecord(record.Record)
	return record
}

type ScopePolicy interface {
	Version() string
	Evaluate(context.Context, NormalizedRecord) (source.ScopeDecision, error)
}

type ProjectionCandidate struct {
	NormalizedAssertionID string
	PayloadSchemaVersion  string
	RawID                 string
	JobID                 string
	LogicalSource         string
	EventKey              string
	SourceTime            time.Time
	TieBreakKey           string
	Position              int64
	Record                source.Record
	Scope                 source.ScopeDecision
}

func NewProjectionCandidate(
	record NormalizedRecord,
	decision source.ScopeDecision,
) (ProjectionCandidate, error) {
	candidate := ProjectionCandidate{
		NormalizedAssertionID: record.AssertionID,
		PayloadSchemaVersion:  record.PayloadSchemaVersion,
		RawID:                 record.RawID,
		JobID:                 record.JobID,
		LogicalSource:         record.LogicalSource,
		EventKey:              record.EventKey,
		SourceTime:            record.SourceTime,
		TieBreakKey:           record.TieBreakKey,
		Position:              record.Position,
		Record:                cloneSourceRecord(record.Record),
		Scope: source.ScopeDecision{
			Status:   decision.Status,
			Reason:   decision.Reason,
			Evidence: append([]source.FieldEvidence(nil), decision.Evidence...),
		},
	}
	if err := record.Validate(); err != nil {
		return ProjectionCandidate{}, err
	}
	if err := candidate.Validate(); err != nil {
		return ProjectionCandidate{}, err
	}
	return candidate, nil
}

func (candidate ProjectionCandidate) Validate() error {
	if strings.TrimSpace(candidate.NormalizedAssertionID) == "" {
		return errors.New("projection candidate requires a normalized assertion ID")
	}
	if strings.TrimSpace(candidate.PayloadSchemaVersion) == "" {
		return errors.New("projection candidate requires a payload schema version")
	}
	if strings.TrimSpace(candidate.RawID) == "" {
		return errors.New("projection candidate requires a raw ID")
	}
	if strings.TrimSpace(candidate.JobID) == "" {
		return errors.New("projection candidate requires a job ID")
	}
	if err := validateEventMetadata(
		candidate.LogicalSource,
		candidate.EventKey,
		candidate.SourceTime,
		candidate.TieBreakKey,
		candidate.Position,
	); err != nil {
		return err
	}
	if strings.TrimSpace(candidate.Record.Source) != candidate.LogicalSource {
		return fmt.Errorf(
			"projection candidate source %q conflicts with logical source %q",
			candidate.Record.Source,
			candidate.LogicalSource,
		)
	}
	if !hasProjectionIdentityEvidence(candidate.Record) {
		return errors.New("projection candidate requires controlled identity evidence")
	}
	if candidate.Scope.Status != source.ScopeIncluded {
		return fmt.Errorf(
			"projection candidate requires included scope, got %q",
			candidate.Scope.Status,
		)
	}
	if strings.TrimSpace(candidate.Scope.Reason) == "" {
		return errors.New("projection candidate requires a scope reason")
	}
	return nil
}

func hasProjectionIdentityEvidence(record source.Record) bool {
	if record.Identity.Valid() {
		return true
	}
	for _, identifier := range record.Identifiers {
		if strings.TrimSpace(string(identifier.Scheme)) != "" &&
			strings.TrimSpace(identifier.Value) != "" {
			return true
		}
	}
	return false
}

func (candidate ProjectionCandidate) Clone() ProjectionCandidate {
	candidate.Record = cloneSourceRecord(candidate.Record)
	candidate.Scope.Evidence = append(
		[]source.FieldEvidence(nil),
		candidate.Scope.Evidence...,
	)
	return candidate
}

type ProjectionPolicy interface {
	Version() string
	// Prepare may transform projection-owned record fields, but it must preserve
	// the normalized assertion identity and immutable source identity. Project
	// persists the candidate payload as the projection assertion while retaining
	// semantic provenance from the referenced immutable normalized payload.
	Prepare(
		context.Context,
		NormalizedRecord,
		source.ScopeDecision,
	) (ProjectionCandidate, error)
}

type ProjectionStatus string

const (
	ProjectionStatusProjected ProjectionStatus = "projected"
	ProjectionStatusExcluded  ProjectionStatus = "excluded"
	ProjectionStatusDeleted   ProjectionStatus = "deleted"
	ProjectionStatusUnchanged ProjectionStatus = "unchanged"
)

func (status ProjectionStatus) Valid() bool {
	switch status {
	case ProjectionStatusProjected,
		ProjectionStatusExcluded,
		ProjectionStatusDeleted,
		ProjectionStatusUnchanged:
		return true
	default:
		return false
	}
}

type ProjectionResult struct {
	EventKey     string
	Status       ProjectionStatus
	CanonicalKey string
}

func NewProjectionResult(
	eventKey string,
	status ProjectionStatus,
	canonicalKey string,
) (ProjectionResult, error) {
	result := ProjectionResult{
		EventKey:     strings.TrimSpace(eventKey),
		Status:       status,
		CanonicalKey: strings.TrimSpace(canonicalKey),
	}
	if err := result.Validate(); err != nil {
		return ProjectionResult{}, err
	}
	return result, nil
}

func (result ProjectionResult) Validate() error {
	if strings.TrimSpace(result.EventKey) == "" {
		return errors.New("projection result requires an event key")
	}
	if !result.Status.Valid() {
		return fmt.Errorf("invalid projection result status %q", result.Status)
	}
	if result.Status == ProjectionStatusProjected &&
		strings.TrimSpace(result.CanonicalKey) == "" {
		return errors.New("projected result requires a canonical key")
	}
	return nil
}

type ProjectionRepository interface {
	// Each method is its own commit boundary. Policy versions are explicit so
	// reassessment can reuse an immutable raw snapshot without hidden globals.
	Normalize(
		context.Context,
		string,
		PersistedRaw,
		string,
	) (NormalizedRecord, error)
	Exclude(
		context.Context,
		string,
		NormalizedRecord,
		source.ScopeDecision,
		string,
	) (ProjectionResult, error)
	Project(
		context.Context,
		string,
		ProjectionCandidate,
		string,
		string,
	) (ProjectionResult, error)
	ApplyDeletion(
		context.Context,
		string,
		PersistedDeletion,
		string,
		string,
	) (ProjectionResult, error)
}

type CoreResult struct {
	JobID         string
	Kind          EventKind
	LogicalSource string
	EventKey      string
	Projection    ProjectionResult
}

type SideEffectHook interface {
	Name() string
	Run(context.Context, CoreResult) error
}

type SideEffectFailure struct {
	Hook          string
	LogicalSource string
	EventKey      string
	Stage         Stage
	Message       string
}

func (failure SideEffectFailure) Validate() error {
	if strings.TrimSpace(failure.Hook) == "" {
		return errors.New("side-effect failure requires a hook name")
	}
	if strings.TrimSpace(failure.LogicalSource) == "" {
		return errors.New("side-effect failure requires a logical source")
	}
	if strings.TrimSpace(failure.EventKey) == "" {
		return errors.New("side-effect failure requires an event key")
	}
	if !failure.Stage.Valid() {
		return fmt.Errorf("invalid side-effect failure stage %q", failure.Stage)
	}
	if strings.TrimSpace(failure.Message) == "" {
		return errors.New("side-effect failure requires a message")
	}
	return nil
}

type SideEffectError struct {
	Hook     string
	EventKey string
	Cause    error
}

func (sideEffect *SideEffectError) Error() string {
	if sideEffect == nil {
		return "ingestion side effect failed"
	}
	message := fmt.Sprintf(
		"ingestion side effect %q failed for event %q",
		sideEffect.Hook,
		sideEffect.EventKey,
	)
	if sideEffect.Cause != nil {
		message += ": " + sideEffect.Cause.Error()
	}
	return message
}

func (sideEffect *SideEffectError) Unwrap() error {
	if sideEffect == nil {
		return nil
	}
	return sideEffect.Cause
}
