package contenttruth

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/workfamily"
)

func TestFirstObservedAtUsesExplicitDurableObservationBoundary(t *testing.T) {
	t.Parallel()

	rawEventID := uuid.New()
	jobID := uuid.New()
	runID := uuid.New()
	later := time.Date(2026, time.July, 19, 8, 5, 0, 0, time.UTC)
	earlier := time.Date(2026, time.July, 19, 8, 1, 0, 0, time.UTC)

	observations := []RawObservation{
		mustRawObservation(t, rawEventID, jobID, runID, later, 2, 1),
		mustRawObservation(t, rawEventID, jobID, runID, earlier, 1, 3),
	}
	got, err := FirstObservedAt(observations)
	if err != nil {
		t.Fatalf("FirstObservedAt() error = %v", err)
	}
	if !got.Equal(earlier) {
		t.Fatalf("FirstObservedAt() = %s, want %s", got, earlier)
	}
}

func TestRawObservationRejectsImplicitOrInvalidConnectorCoordinates(t *testing.T) {
	t.Parallel()

	valid := RawObservation{
		RawEventID:     uuid.New(),
		JobID:          uuid.New(),
		ConnectorRunID: uuid.New(),
		ObservedAt:     time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC),
		PageOrdinal:    1,
		RecordOrdinal:  1,
	}
	cases := map[string]RawObservation{
		"zero observed_at": func() RawObservation {
			value := valid
			value.ObservedAt = time.Time{}
			return value
		}(),
		"missing connector run": func() RawObservation {
			value := valid
			value.ConnectorRunID = uuid.Nil
			return value
		}(),
		"zero page ordinal": func() RawObservation {
			value := valid
			value.PageOrdinal = 0
			return value
		}(),
		"zero record ordinal": func() RawObservation {
			value := valid
			value.RecordOrdinal = 0
			return value
		}(),
	}
	for name, observation := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := observation.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestProjectCanonicalChannelEventUsesOnlyExactAssertionsAndVersionedPriority(t *testing.T) {
	t.Parallel()

	workID := uuid.New()
	sourceRecordID := uuid.New()
	sourceRevisionID := uuid.New()
	printDate := time.Date(2026, time.July, 20, 0, 0, 0, 0, time.UTC)
	onlineDate := time.Date(2026, time.July, 19, 0, 0, 0, 0, time.UTC)
	assertedAt := time.Date(2026, time.July, 19, 1, 0, 0, 0, time.UTC)
	decidedAt := time.Date(2026, time.July, 19, 1, 1, 0, 0, time.UTC)

	policy, err := NewChannelEventPolicy(
		"canonical-channel-event/v1",
		map[scope.ContentChannel][]ChannelEventKind{
			scope.ContentChannelJournalPublished: {
				ChannelEventOfficialOnline,
				ChannelEventOfficialPrint,
			},
		},
	)
	if err != nil {
		t.Fatalf("NewChannelEventPolicy() error = %v", err)
	}
	assertions := []ChannelEventAssertion{
		mustChannelEventAssertion(
			t,
			workID,
			sourceRecordID,
			sourceRevisionID,
			scope.ContentChannelJournalPublished,
			ChannelEventOfficialPrint,
			printDate,
			"$.published-print.date-parts",
			assertedAt,
		),
		mustChannelEventAssertion(
			t,
			workID,
			sourceRecordID,
			sourceRevisionID,
			scope.ContentChannelJournalPublished,
			ChannelEventOfficialOnline,
			onlineDate,
			"$.published-online.date-parts",
			assertedAt,
		),
	}

	decision, err := ProjectCanonicalChannelEvent(
		workID,
		scope.ContentChannelJournalPublished,
		assertions,
		policy,
		decidedAt,
	)
	if err != nil {
		t.Fatalf("ProjectCanonicalChannelEvent() error = %v", err)
	}
	if decision.State != AssertionStateKnown {
		t.Fatalf("decision.State = %q, want known", decision.State)
	}
	if decision.Kind != ChannelEventOfficialOnline ||
		!decision.EventAt.Equal(onlineDate) {
		t.Fatalf(
			"canonical event = %q at %s, want official_online at %s",
			decision.Kind,
			decision.EventAt,
			onlineDate,
		)
	}
	if decision.PolicyVersion != policy.Version {
		t.Fatalf(
			"PolicyVersion = %q, want %q",
			decision.PolicyVersion,
			policy.Version,
		)
	}
	if len(decision.Evidence) != 1 ||
		decision.Evidence[0].SourcePath != "$.published-online.date-parts" {
		t.Fatalf("Evidence = %#v, want exact online assertion", decision.Evidence)
	}
}

func TestProjectCanonicalChannelEventRejectsCrossChannelOrConflictingExactDates(t *testing.T) {
	t.Parallel()

	workID := uuid.New()
	sourceRecordID := uuid.New()
	sourceRevisionID := uuid.New()
	assertedAt := time.Date(2026, time.July, 19, 1, 0, 0, 0, time.UTC)
	decidedAt := assertedAt.Add(time.Minute)
	policy, err := NewChannelEventPolicy(
		"canonical-channel-event/v1",
		map[scope.ContentChannel][]ChannelEventKind{
			scope.ContentChannelPreprint: {ChannelEventPreprintPosted},
		},
	)
	if err != nil {
		t.Fatalf("NewChannelEventPolicy() error = %v", err)
	}

	t.Run("cross channel", func(t *testing.T) {
		_, projectionErr := ProjectCanonicalChannelEvent(
			workID,
			scope.ContentChannelPreprint,
			[]ChannelEventAssertion{mustChannelEventAssertion(
				t,
				workID,
				sourceRecordID,
				sourceRevisionID,
				scope.ContentChannelJournalPublished,
				ChannelEventOfficialOnline,
				assertedAt,
				"$.published-online",
				assertedAt,
			)},
			policy,
			decidedAt,
		)
		if projectionErr == nil {
			t.Fatal("ProjectCanonicalChannelEvent() error = nil")
		}
	})

	t.Run("conflicting exact dates", func(t *testing.T) {
		assertions := []ChannelEventAssertion{
			mustChannelEventAssertion(
				t,
				workID,
				sourceRecordID,
				sourceRevisionID,
				scope.ContentChannelPreprint,
				ChannelEventPreprintPosted,
				time.Date(2026, time.July, 18, 0, 0, 0, 0, time.UTC),
				"$.posted",
				assertedAt,
			),
			mustChannelEventAssertion(
				t,
				workID,
				uuid.New(),
				uuid.New(),
				scope.ContentChannelPreprint,
				ChannelEventPreprintPosted,
				time.Date(2026, time.July, 19, 0, 0, 0, 0, time.UTC),
				"$.published",
				assertedAt,
			),
		}
		decision, projectionErr := ProjectCanonicalChannelEvent(
			workID,
			scope.ContentChannelPreprint,
			assertions,
			policy,
			decidedAt,
		)
		if projectionErr != nil {
			t.Fatalf("ProjectCanonicalChannelEvent() error = %v", projectionErr)
		}
		if decision.State != AssertionStateConflict ||
			!decision.EventAt.IsZero() {
			t.Fatalf("decision = %#v, want conflict without guessed date", decision)
		}
	})
}

func TestAssessVersionNumberReturnsNullableValueWithCompleteEvidence(t *testing.T) {
	t.Parallel()

	workID := uuid.New()
	revisionOne := uuid.New()
	revisionTwo := uuid.New()
	policy, err := NewVersionNumberPolicy(
		"version-number/v1",
		[]scope.ContentChannel{scope.ContentChannelPreprint},
	)
	if err != nil {
		t.Fatalf("NewVersionNumberPolicy() error = %v", err)
	}
	assessedAt := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)

	t.Run("known", func(t *testing.T) {
		assertion := VersionNumberAssertion{
			ID:               uuid.New(),
			WorkID:           workID,
			SourceRecordID:   uuid.New(),
			SourceRevisionID: revisionTwo,
			Number:           2,
			RawLabel:         "v2",
			SourcePath:       "$.version",
			AssertedAt:       assessedAt.Add(-time.Minute),
		}
		assessment, assessErr := AssessVersionNumber(
			workID,
			scope.ContentChannelPreprint,
			[]uuid.UUID{revisionOne, revisionTwo},
			[]VersionNumberAssertion{assertion},
			policy,
			assessedAt,
		)
		if assessErr != nil {
			t.Fatalf("AssessVersionNumber() error = %v", assessErr)
		}
		if assessment.Evidence.State != AssertionStateKnown ||
			assessment.Number == nil ||
			*assessment.Number != 2 {
			t.Fatalf("assessment = %#v, want known nullable number 2", assessment)
		}
		if len(assessment.Evidence.Assertions) != 1 ||
			len(assessment.Evidence.CheckedSourceRevisions) != 2 {
			t.Fatalf("evidence = %#v, want assertion and both checked revisions", assessment.Evidence)
		}
	})

	t.Run("missing", func(t *testing.T) {
		assessment, assessErr := AssessVersionNumber(
			workID,
			scope.ContentChannelPreprint,
			[]uuid.UUID{revisionOne, revisionTwo},
			nil,
			policy,
			assessedAt,
		)
		if assessErr != nil {
			t.Fatalf("AssessVersionNumber() error = %v", assessErr)
		}
		if assessment.Evidence.State != AssertionStateMissing ||
			assessment.Number != nil {
			t.Fatalf("assessment = %#v, want missing null number", assessment)
		}
	})

	t.Run("conflict", func(t *testing.T) {
		assertions := []VersionNumberAssertion{
			{
				ID:               uuid.New(),
				WorkID:           workID,
				SourceRecordID:   uuid.New(),
				SourceRevisionID: revisionOne,
				Number:           1,
				RawLabel:         "v1",
				SourcePath:       "$.version",
				AssertedAt:       assessedAt.Add(-time.Minute),
			},
			{
				ID:               uuid.New(),
				WorkID:           workID,
				SourceRecordID:   uuid.New(),
				SourceRevisionID: revisionTwo,
				Number:           2,
				RawLabel:         "v2",
				SourcePath:       "$.version",
				AssertedAt:       assessedAt.Add(-time.Minute),
			},
		}
		assessment, assessErr := AssessVersionNumber(
			workID,
			scope.ContentChannelPreprint,
			[]uuid.UUID{revisionOne, revisionTwo},
			assertions,
			policy,
			assessedAt,
		)
		if assessErr != nil {
			t.Fatalf("AssessVersionNumber() error = %v", assessErr)
		}
		if assessment.Evidence.State != AssertionStateConflict ||
			assessment.Number != nil {
			t.Fatalf("assessment = %#v, want conflict null number", assessment)
		}
	})

	t.Run("not applicable", func(t *testing.T) {
		assessment, assessErr := AssessVersionNumber(
			workID,
			scope.ContentChannelJournalPublished,
			[]uuid.UUID{revisionOne},
			nil,
			policy,
			assessedAt,
		)
		if assessErr != nil {
			t.Fatalf("AssessVersionNumber() error = %v", assessErr)
		}
		if assessment.Evidence.State != AssertionStateNotApplicable ||
			assessment.Number != nil {
			t.Fatalf("assessment = %#v, want not_applicable null number", assessment)
		}
	})
}

func TestProjectVersionKindUsesResolvedChannelAndAcceptedFamilyRelations(t *testing.T) {
	t.Parallel()

	workID := uuid.New()
	olderPreprint := uuid.New()
	assessedAt := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	policy, err := NewVersionKindPolicy("version-kind/v1")
	if err != nil {
		t.Fatalf("NewVersionKindPolicy() error = %v", err)
	}

	projection, err := ProjectVersionKind(
		workID,
		scope.ContentChannelPreprint,
		[]AcceptedFamilyRelation{{
			DecisionID:    uuid.New(),
			SubjectWorkID: workID,
			ObjectWorkID:  olderPreprint,
			Kind:          workfamily.RelationReplaces,
		}},
		policy,
		assessedAt,
	)
	if err != nil {
		t.Fatalf("ProjectVersionKind() error = %v", err)
	}
	if projection.Kind != VersionKindRevisedPreprint {
		t.Fatalf("Kind = %q, want revised_preprint", projection.Kind)
	}
	if len(projection.RelationDecisionIDs) != 1 {
		t.Fatalf("RelationDecisionIDs = %#v, want exact accepted decision", projection.RelationDecisionIDs)
	}
}

func TestProjectVersionKindDoesNotInferFromTitleOrDate(t *testing.T) {
	t.Parallel()

	workID := uuid.New()
	policy, err := NewVersionKindPolicy("version-kind/v1")
	if err != nil {
		t.Fatalf("NewVersionKindPolicy() error = %v", err)
	}
	projection, err := ProjectVersionKind(
		workID,
		scope.ContentChannelPreprint,
		nil,
		policy,
		time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("ProjectVersionKind() error = %v", err)
	}
	if projection.Kind != VersionKindPreprint {
		t.Fatalf("Kind = %q, want preprint without inferred revision", projection.Kind)
	}
}

func mustRawObservation(
	t *testing.T,
	rawEventID uuid.UUID,
	jobID uuid.UUID,
	runID uuid.UUID,
	observedAt time.Time,
	pageOrdinal int,
	recordOrdinal int,
) RawObservation {
	t.Helper()
	observation, err := NewRawObservation(
		rawEventID,
		jobID,
		runID,
		observedAt,
		pageOrdinal,
		recordOrdinal,
	)
	if err != nil {
		t.Fatalf("NewRawObservation() error = %v", err)
	}
	return observation
}

func mustChannelEventAssertion(
	t *testing.T,
	workID uuid.UUID,
	sourceRecordID uuid.UUID,
	sourceRevisionID uuid.UUID,
	channel scope.ContentChannel,
	kind ChannelEventKind,
	eventAt time.Time,
	sourcePath string,
	assertedAt time.Time,
) ChannelEventAssertion {
	t.Helper()
	assertion := ChannelEventAssertion{
		ID:               uuid.New(),
		WorkID:           workID,
		SourceRecordID:   sourceRecordID,
		SourceRevisionID: sourceRevisionID,
		Channel:          channel,
		Kind:             kind,
		EventAt:          eventAt,
		SourcePath:       sourcePath,
		AssertedAt:       assertedAt,
	}
	if err := assertion.Validate(); err != nil {
		t.Fatalf("ChannelEventAssertion.Validate() error = %v", err)
	}
	return assertion
}

func TestFirstObservedAtRequiresAtLeastOneValidObservation(t *testing.T) {
	t.Parallel()

	if _, err := FirstObservedAt(nil); !errors.Is(err, ErrMissingObservation) {
		t.Fatalf("FirstObservedAt(nil) error = %v, want ErrMissingObservation", err)
	}
}
