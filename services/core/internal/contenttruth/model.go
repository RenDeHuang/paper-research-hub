package contenttruth

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/workfamily"
)

var ErrMissingObservation = errors.New("at least one durable raw observation is required")

type AssertionState string

const (
	AssertionStateKnown         AssertionState = "known"
	AssertionStateMissing       AssertionState = "missing"
	AssertionStateConflict      AssertionState = "conflict"
	AssertionStateNotApplicable AssertionState = "not_applicable"
)

type RawObservation struct {
	RawEventID     uuid.UUID
	JobID          uuid.UUID
	ConnectorRunID uuid.UUID
	ObservedAt     time.Time
	PageOrdinal    int
	RecordOrdinal  int
}

func NewRawObservation(
	rawEventID uuid.UUID,
	jobID uuid.UUID,
	connectorRunID uuid.UUID,
	observedAt time.Time,
	pageOrdinal int,
	recordOrdinal int,
) (RawObservation, error) {
	observation := RawObservation{
		RawEventID:     rawEventID,
		JobID:          jobID,
		ConnectorRunID: connectorRunID,
		ObservedAt:     observedAt.UTC(),
		PageOrdinal:    pageOrdinal,
		RecordOrdinal:  recordOrdinal,
	}
	if err := observation.Validate(); err != nil {
		return RawObservation{}, err
	}
	return observation, nil
}

func (observation RawObservation) Validate() error {
	if observation.RawEventID == uuid.Nil {
		return errors.New("raw observation raw event ID is required")
	}
	if observation.JobID == uuid.Nil {
		return errors.New("raw observation ingestion job ID is required")
	}
	if observation.ConnectorRunID == uuid.Nil {
		return errors.New("raw observation connector run ID is required")
	}
	if observation.ObservedAt.IsZero() {
		return errors.New("raw observation observed_at is required")
	}
	if observation.PageOrdinal < 1 {
		return errors.New("raw observation page ordinal must be positive")
	}
	if observation.RecordOrdinal < 1 {
		return errors.New("raw observation record ordinal must be positive")
	}
	return nil
}

func FirstObservedAt(observations []RawObservation) (time.Time, error) {
	if len(observations) == 0 {
		return time.Time{}, ErrMissingObservation
	}
	var earliest time.Time
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return time.Time{}, fmt.Errorf("raw observation %d: %w", index, err)
		}
		if earliest.IsZero() || observation.ObservedAt.Before(earliest) {
			earliest = observation.ObservedAt
		}
	}
	return earliest.UTC(), nil
}

type ChannelEventKind string

const (
	ChannelEventOfficialOnline      ChannelEventKind = "official_online"
	ChannelEventOfficialPrint       ChannelEventKind = "official_print"
	ChannelEventAccepted            ChannelEventKind = "accepted"
	ChannelEventAheadOfPrint        ChannelEventKind = "ahead_of_print"
	ChannelEventOnlineFirst         ChannelEventKind = "online_first"
	ChannelEventPreprintPosted      ChannelEventKind = "preprint_posted"
	ChannelEventProceedingPublished ChannelEventKind = "proceeding_published"
)

func (kind ChannelEventKind) validFor(channel scope.ContentChannel) bool {
	switch channel {
	case scope.ContentChannelJournalPublished:
		return kind == ChannelEventOfficialOnline ||
			kind == ChannelEventOfficialPrint
	case scope.ContentChannelAcceptedEarly:
		return kind == ChannelEventAccepted ||
			kind == ChannelEventAheadOfPrint ||
			kind == ChannelEventOnlineFirst
	case scope.ContentChannelPreprint:
		return kind == ChannelEventPreprintPosted
	case scope.ContentChannelConferenceProceeding:
		return kind == ChannelEventProceedingPublished
	default:
		return false
	}
}

type ChannelEventAssertion struct {
	ID               uuid.UUID
	WorkID           uuid.UUID
	SourceRecordID   uuid.UUID
	SourceRevisionID uuid.UUID
	Channel          scope.ContentChannel
	Kind             ChannelEventKind
	EventAt          time.Time
	SourcePath       string
	AssertedAt       time.Time
}

func (assertion ChannelEventAssertion) Validate() error {
	if assertion.ID == uuid.Nil {
		return errors.New("channel event assertion ID is required")
	}
	if assertion.WorkID == uuid.Nil {
		return errors.New("channel event assertion Work ID is required")
	}
	if assertion.SourceRecordID == uuid.Nil {
		return errors.New("channel event assertion source record ID is required")
	}
	if assertion.SourceRevisionID == uuid.Nil {
		return errors.New("channel event assertion source revision ID is required")
	}
	if _, err := scope.ParseContentChannel(string(assertion.Channel)); err != nil {
		return err
	}
	if !assertion.Kind.validFor(assertion.Channel) {
		return fmt.Errorf(
			"channel event kind %q is not exact evidence for channel %q",
			assertion.Kind,
			assertion.Channel,
		)
	}
	if assertion.EventAt.IsZero() {
		return errors.New("channel event assertion event_at is required")
	}
	if err := validateExactText(assertion.SourcePath, "channel event source path"); err != nil {
		return err
	}
	if assertion.AssertedAt.IsZero() {
		return errors.New("channel event assertion asserted_at is required")
	}
	return nil
}

type ChannelEventPolicy struct {
	Version  string
	priority map[scope.ContentChannel][]ChannelEventKind
}

func NewChannelEventPolicy(
	version string,
	priority map[scope.ContentChannel][]ChannelEventKind,
) (ChannelEventPolicy, error) {
	if err := validateExactText(version, "channel event policy version"); err != nil {
		return ChannelEventPolicy{}, err
	}
	if len(priority) == 0 {
		return ChannelEventPolicy{}, errors.New(
			"channel event policy requires explicit channel priorities",
		)
	}
	policy := ChannelEventPolicy{
		Version:  version,
		priority: make(map[scope.ContentChannel][]ChannelEventKind, len(priority)),
	}
	for channel, kinds := range priority {
		if _, err := scope.ParseContentChannel(string(channel)); err != nil {
			return ChannelEventPolicy{}, err
		}
		if len(kinds) == 0 {
			return ChannelEventPolicy{}, fmt.Errorf(
				"channel event policy channel %q requires a priority",
				channel,
			)
		}
		seen := make(map[ChannelEventKind]struct{}, len(kinds))
		for _, kind := range kinds {
			if !kind.validFor(channel) {
				return ChannelEventPolicy{}, fmt.Errorf(
					"channel event policy kind %q is invalid for %q",
					kind,
					channel,
				)
			}
			if _, duplicate := seen[kind]; duplicate {
				return ChannelEventPolicy{}, fmt.Errorf(
					"channel event policy repeats kind %q for %q",
					kind,
					channel,
				)
			}
			seen[kind] = struct{}{}
		}
		policy.priority[channel] = slices.Clone(kinds)
	}
	return policy, nil
}

type ChannelEventEvidence struct {
	AssertionID      uuid.UUID
	SourceRecordID   uuid.UUID
	SourceRevisionID uuid.UUID
	SourcePath       string
}

type ChannelEventDecision struct {
	WorkID        uuid.UUID
	Channel       scope.ContentChannel
	State         AssertionState
	Kind          ChannelEventKind
	EventAt       time.Time
	PolicyVersion string
	Evidence      []ChannelEventEvidence
	DecidedAt     time.Time
}

func ProjectCanonicalChannelEvent(
	workID uuid.UUID,
	channel scope.ContentChannel,
	assertions []ChannelEventAssertion,
	policy ChannelEventPolicy,
	decidedAt time.Time,
) (ChannelEventDecision, error) {
	if workID == uuid.Nil {
		return ChannelEventDecision{}, errors.New(
			"canonical channel event requires a Work ID",
		)
	}
	if _, err := scope.ParseContentChannel(string(channel)); err != nil {
		return ChannelEventDecision{}, err
	}
	if decidedAt.IsZero() {
		return ChannelEventDecision{}, errors.New(
			"canonical channel event decided_at is required",
		)
	}
	priorities, ok := policy.priority[channel]
	if !ok {
		return ChannelEventDecision{}, fmt.Errorf(
			"channel event policy %q has no rule for %q",
			policy.Version,
			channel,
		)
	}
	byKind := make(map[ChannelEventKind][]ChannelEventAssertion)
	for index, assertion := range assertions {
		if err := assertion.Validate(); err != nil {
			return ChannelEventDecision{}, fmt.Errorf(
				"channel event assertion %d: %w",
				index,
				err,
			)
		}
		if assertion.WorkID != workID {
			return ChannelEventDecision{}, errors.New(
				"channel event assertions must belong to the projected Work",
			)
		}
		if assertion.Channel != channel {
			return ChannelEventDecision{}, errors.New(
				"channel event assertion conflicts with the resolved content channel",
			)
		}
		byKind[assertion.Kind] = append(byKind[assertion.Kind], assertion)
	}
	decision := ChannelEventDecision{
		WorkID:        workID,
		Channel:       channel,
		State:         AssertionStateMissing,
		PolicyVersion: policy.Version,
		DecidedAt:     decidedAt.UTC(),
	}
	var selected []ChannelEventAssertion
	for _, kind := range priorities {
		if len(byKind[kind]) > 0 {
			decision.Kind = kind
			selected = byKind[kind]
			break
		}
	}
	if len(selected) == 0 {
		return decision, nil
	}
	slices.SortFunc(selected, func(left, right ChannelEventAssertion) int {
		return strings.Compare(left.ID.String(), right.ID.String())
	})
	dates := make(map[string]struct{})
	for _, assertion := range selected {
		dates[assertion.EventAt.UTC().Format(time.RFC3339Nano)] = struct{}{}
		decision.Evidence = append(decision.Evidence, ChannelEventEvidence{
			AssertionID:      assertion.ID,
			SourceRecordID:   assertion.SourceRecordID,
			SourceRevisionID: assertion.SourceRevisionID,
			SourcePath:       assertion.SourcePath,
		})
	}
	if len(dates) != 1 {
		decision.State = AssertionStateConflict
		decision.EventAt = time.Time{}
		return decision, nil
	}
	decision.State = AssertionStateKnown
	decision.EventAt = selected[0].EventAt.UTC()
	return decision, nil
}

type VersionNumberAssertion struct {
	ID               uuid.UUID
	WorkID           uuid.UUID
	SourceRecordID   uuid.UUID
	SourceRevisionID uuid.UUID
	Number           int
	RawLabel         string
	SourcePath       string
	AssertedAt       time.Time
}

func (assertion VersionNumberAssertion) Validate() error {
	if assertion.ID == uuid.Nil {
		return errors.New("version number assertion ID is required")
	}
	if assertion.WorkID == uuid.Nil {
		return errors.New("version number assertion Work ID is required")
	}
	if assertion.SourceRecordID == uuid.Nil {
		return errors.New("version number assertion source record ID is required")
	}
	if assertion.SourceRevisionID == uuid.Nil {
		return errors.New("version number assertion source revision ID is required")
	}
	if assertion.Number < 1 {
		return errors.New("version number assertion must be positive")
	}
	if err := validateExactText(assertion.RawLabel, "version number raw label"); err != nil {
		return err
	}
	if err := validateExactText(assertion.SourcePath, "version number source path"); err != nil {
		return err
	}
	if assertion.AssertedAt.IsZero() {
		return errors.New("version number assertion asserted_at is required")
	}
	return nil
}

type VersionNumberPolicy struct {
	Version            string
	applicableChannels map[scope.ContentChannel]struct{}
}

func NewVersionNumberPolicy(
	version string,
	applicableChannels []scope.ContentChannel,
) (VersionNumberPolicy, error) {
	if err := validateExactText(version, "version number policy version"); err != nil {
		return VersionNumberPolicy{}, err
	}
	if len(applicableChannels) == 0 {
		return VersionNumberPolicy{}, errors.New(
			"version number policy requires applicable channels",
		)
	}
	policy := VersionNumberPolicy{
		Version:            version,
		applicableChannels: make(map[scope.ContentChannel]struct{}),
	}
	for _, channel := range applicableChannels {
		if _, err := scope.ParseContentChannel(string(channel)); err != nil {
			return VersionNumberPolicy{}, err
		}
		if _, duplicate := policy.applicableChannels[channel]; duplicate {
			return VersionNumberPolicy{}, fmt.Errorf(
				"version number policy repeats channel %q",
				channel,
			)
		}
		policy.applicableChannels[channel] = struct{}{}
	}
	return policy, nil
}

type VersionNumberAssertionEvidence struct {
	AssertionID      uuid.UUID
	SourceRecordID   uuid.UUID
	SourceRevisionID uuid.UUID
	Number           int
	RawLabel         string
	SourcePath       string
}

type VersionNumberEvidence struct {
	State                  AssertionState
	Assertions             []VersionNumberAssertionEvidence
	CheckedSourceRevisions []uuid.UUID
}

type VersionNumberAssessment struct {
	WorkID        uuid.UUID
	Channel       scope.ContentChannel
	Number        *int
	Evidence      VersionNumberEvidence
	PolicyVersion string
	AssessedAt    time.Time
}

func AssessVersionNumber(
	workID uuid.UUID,
	channel scope.ContentChannel,
	checkedSourceRevisions []uuid.UUID,
	assertions []VersionNumberAssertion,
	policy VersionNumberPolicy,
	assessedAt time.Time,
) (VersionNumberAssessment, error) {
	if workID == uuid.Nil {
		return VersionNumberAssessment{}, errors.New(
			"version number assessment Work ID is required",
		)
	}
	if _, err := scope.ParseContentChannel(string(channel)); err != nil {
		return VersionNumberAssessment{}, err
	}
	if assessedAt.IsZero() {
		return VersionNumberAssessment{}, errors.New(
			"version number assessment assessed_at is required",
		)
	}
	if len(checkedSourceRevisions) == 0 {
		return VersionNumberAssessment{}, errors.New(
			"version number assessment requires checked source revisions",
		)
	}
	checked := make(map[uuid.UUID]struct{}, len(checkedSourceRevisions))
	for _, revisionID := range checkedSourceRevisions {
		if revisionID == uuid.Nil {
			return VersionNumberAssessment{}, errors.New(
				"checked source revision ID is required",
			)
		}
		if _, duplicate := checked[revisionID]; duplicate {
			return VersionNumberAssessment{}, errors.New(
				"checked source revisions must be unique",
			)
		}
		checked[revisionID] = struct{}{}
	}
	sortedRevisions := slices.Clone(checkedSourceRevisions)
	slices.SortFunc(sortedRevisions, func(left, right uuid.UUID) int {
		return strings.Compare(left.String(), right.String())
	})
	assessment := VersionNumberAssessment{
		WorkID:        workID,
		Channel:       channel,
		PolicyVersion: policy.Version,
		AssessedAt:    assessedAt.UTC(),
		Evidence: VersionNumberEvidence{
			CheckedSourceRevisions: sortedRevisions,
		},
	}
	if _, applicable := policy.applicableChannels[channel]; !applicable {
		if len(assertions) != 0 {
			return VersionNumberAssessment{}, errors.New(
				"not-applicable version number assessment cannot carry assertions",
			)
		}
		assessment.Evidence.State = AssertionStateNotApplicable
		return assessment, nil
	}

	ordered := slices.Clone(assertions)
	slices.SortFunc(ordered, func(left, right VersionNumberAssertion) int {
		return strings.Compare(left.ID.String(), right.ID.String())
	})
	values := make(map[int]struct{})
	for index, assertion := range ordered {
		if err := assertion.Validate(); err != nil {
			return VersionNumberAssessment{}, fmt.Errorf(
				"version number assertion %d: %w",
				index,
				err,
			)
		}
		if assertion.WorkID != workID {
			return VersionNumberAssessment{}, errors.New(
				"version number assertions must belong to the assessed Work",
			)
		}
		if _, wasChecked := checked[assertion.SourceRevisionID]; !wasChecked {
			return VersionNumberAssessment{}, errors.New(
				"version number assertion source revision was not checked",
			)
		}
		values[assertion.Number] = struct{}{}
		assessment.Evidence.Assertions = append(
			assessment.Evidence.Assertions,
			VersionNumberAssertionEvidence{
				AssertionID:      assertion.ID,
				SourceRecordID:   assertion.SourceRecordID,
				SourceRevisionID: assertion.SourceRevisionID,
				Number:           assertion.Number,
				RawLabel:         assertion.RawLabel,
				SourcePath:       assertion.SourcePath,
			},
		)
	}
	switch len(values) {
	case 0:
		assessment.Evidence.State = AssertionStateMissing
	case 1:
		assessment.Evidence.State = AssertionStateKnown
		for number := range values {
			value := number
			assessment.Number = &value
		}
	default:
		assessment.Evidence.State = AssertionStateConflict
	}
	return assessment, nil
}

type VersionKind string

const (
	VersionKindPreprint           VersionKind = "preprint"
	VersionKindRevisedPreprint    VersionKind = "revised_preprint"
	VersionKindAcceptedManuscript VersionKind = "accepted_manuscript"
	VersionKindVersionOfRecord    VersionKind = "version_of_record"
	VersionKindConferencePaper    VersionKind = "conference_paper"
	VersionKindCorrection         VersionKind = "correction"
	VersionKindRetraction         VersionKind = "retraction"
)

func (kind VersionKind) valid() bool {
	switch kind {
	case VersionKindPreprint,
		VersionKindRevisedPreprint,
		VersionKindAcceptedManuscript,
		VersionKindVersionOfRecord,
		VersionKindConferencePaper,
		VersionKindCorrection,
		VersionKindRetraction:
		return true
	default:
		return false
	}
}

type AcceptedFamilyRelation struct {
	DecisionID    uuid.UUID
	SubjectWorkID uuid.UUID
	ObjectWorkID  uuid.UUID
	Kind          workfamily.RelationKind
}

func (relation AcceptedFamilyRelation) Validate() error {
	if relation.DecisionID == uuid.Nil {
		return errors.New("accepted family relation decision ID is required")
	}
	if relation.SubjectWorkID == uuid.Nil || relation.ObjectWorkID == uuid.Nil {
		return errors.New("accepted family relation endpoints are required")
	}
	if relation.SubjectWorkID == relation.ObjectWorkID {
		return errors.New("accepted family relation cannot be a self-loop")
	}
	if !supportedRelationKind(relation.Kind) {
		return fmt.Errorf("unsupported accepted family relation kind %q", relation.Kind)
	}
	return nil
}

type VersionKindPolicy struct {
	Version string
}

func NewVersionKindPolicy(version string) (VersionKindPolicy, error) {
	if err := validateExactText(version, "version kind policy version"); err != nil {
		return VersionKindPolicy{}, err
	}
	return VersionKindPolicy{Version: version}, nil
}

type VersionKindProjection struct {
	WorkID              uuid.UUID
	Channel             scope.ContentChannel
	Kind                VersionKind
	PolicyVersion       string
	RelationDecisionIDs []uuid.UUID
	AssessedAt          time.Time
}

func ProjectVersionKind(
	workID uuid.UUID,
	channel scope.ContentChannel,
	relations []AcceptedFamilyRelation,
	policy VersionKindPolicy,
	assessedAt time.Time,
) (VersionKindProjection, error) {
	if workID == uuid.Nil {
		return VersionKindProjection{}, errors.New(
			"version kind projection Work ID is required",
		)
	}
	if _, err := scope.ParseContentChannel(string(channel)); err != nil {
		return VersionKindProjection{}, err
	}
	if assessedAt.IsZero() {
		return VersionKindProjection{}, errors.New(
			"version kind projection assessed_at is required",
		)
	}
	projection := VersionKindProjection{
		WorkID:        workID,
		Channel:       channel,
		PolicyVersion: policy.Version,
		AssessedAt:    assessedAt.UTC(),
	}
	switch channel {
	case scope.ContentChannelJournalPublished:
		projection.Kind = VersionKindVersionOfRecord
	case scope.ContentChannelAcceptedEarly:
		projection.Kind = VersionKindAcceptedManuscript
	case scope.ContentChannelPreprint:
		projection.Kind = VersionKindPreprint
	case scope.ContentChannelConferenceProceeding:
		projection.Kind = VersionKindConferencePaper
	default:
		return VersionKindProjection{}, fmt.Errorf(
			"unsupported content channel %q",
			channel,
		)
	}

	ordered := slices.Clone(relations)
	slices.SortFunc(ordered, func(left, right AcceptedFamilyRelation) int {
		return strings.Compare(left.DecisionID.String(), right.DecisionID.String())
	})
	var correction, retraction, revision bool
	for index, relation := range ordered {
		if err := relation.Validate(); err != nil {
			return VersionKindProjection{}, fmt.Errorf(
				"accepted family relation %d: %w",
				index,
				err,
			)
		}
		if relation.SubjectWorkID != workID && relation.ObjectWorkID != workID {
			return VersionKindProjection{}, errors.New(
				"accepted family relation does not involve the projected Work",
			)
		}
		relevant := false
		switch {
		case relation.SubjectWorkID == workID &&
			relation.Kind == workfamily.RelationIsCorrection:
			correction = true
			relevant = true
		case relation.SubjectWorkID == workID &&
			relation.Kind == workfamily.RelationIsRetraction:
			retraction = true
			relevant = true
		case channel == scope.ContentChannelPreprint &&
			relation.SubjectWorkID == workID &&
			relation.Kind == workfamily.RelationReplaces:
			revision = true
			relevant = true
		}
		if relevant {
			projection.RelationDecisionIDs = append(
				projection.RelationDecisionIDs,
				relation.DecisionID,
			)
		}
	}
	if correction && retraction {
		return VersionKindProjection{}, errors.New(
			"accepted family relations conflict between correction and retraction",
		)
	}
	switch {
	case retraction:
		projection.Kind = VersionKindRetraction
	case correction:
		projection.Kind = VersionKindCorrection
	case revision:
		projection.Kind = VersionKindRevisedPreprint
	}
	return projection, nil
}

func supportedRelationKind(kind workfamily.RelationKind) bool {
	switch kind {
	case workfamily.RelationIsPreprintOf,
		workfamily.RelationHasPreprint,
		workfamily.RelationIsVersionOf,
		workfamily.RelationHasVersion,
		workfamily.RelationReplaces,
		workfamily.RelationIsReplacedBy,
		workfamily.RelationIsCorrection,
		workfamily.RelationIsRetraction:
		return true
	default:
		return false
	}
}

func validateExactText(value string, label string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be exact and non-empty", label)
	}
	return nil
}
