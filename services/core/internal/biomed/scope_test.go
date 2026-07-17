package biomed

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPublicEligibilityPolicyAcceptsOnlyExactControlledSubjectMetricEvidence(t *testing.T) {
	t.Parallel()

	policy, err := NewPublicEligibilityPolicy(
		BiomedicalPublicEligibilityPolicyVersion,
	)
	if err != nil {
		t.Fatalf("NewPublicEligibilityPolicy() error = %v", err)
	}
	evaluatedAt := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		load PublicEligibilityLoad
		want PublicEligibilityDecision
	}{
		{
			name: "exact journal Subject metric link is accepted",
			load: controlledEligibilityLoad(
				[]PublicEligibilityMetricEvidence{
					controlledMetric("metric-oncology", "Oncology"),
				},
				[]PublicEligibilitySubjectEvidence{
					controlledSubjectMatch("link-oncology", "metric-oncology", "Oncology"),
				},
			),
			want: PublicEligibilityDecisionAccepted,
		},
		{
			name: "case variant category is rejected",
			load: controlledEligibilityLoad(
				[]PublicEligibilityMetricEvidence{
					controlledMetric("metric-lower", "oncology"),
				},
				nil,
			),
			want: PublicEligibilityDecisionRejected,
		},
		{
			name: "space variant category is rejected",
			load: controlledEligibilityLoad(
				[]PublicEligibilityMetricEvidence{
					controlledMetric("metric-space", "Oncology "),
				},
				nil,
			),
			want: PublicEligibilityDecisionRejected,
		},
		{
			name: "prefix category is rejected",
			load: controlledEligibilityLoad(
				[]PublicEligibilityMetricEvidence{
					controlledMetric("metric-prefix", "Onco"),
				},
				nil,
			),
			want: PublicEligibilityDecisionRejected,
		},
		{
			name: "substring category is rejected",
			load: controlledEligibilityLoad(
				[]PublicEligibilityMetricEvidence{
					controlledMetric("metric-substring", "Clinical Oncology"),
				},
				nil,
			),
			want: PublicEligibilityDecisionRejected,
		},
		{
			name: "missing declared year metric evidence stays missing",
			load: PublicEligibilityLoad{
				WorkID:            eligibilityWorkID,
				MetricYear:        2025,
				SubjectVersionID:  eligibilitySubjectVersionID,
				SubjectVersionKey: eligibilitySubjectVersionKey,
				Venue:             controlledVenue(),
				MissingReason:     PublicEligibilityReasonMetricYearMissing,
			},
			want: PublicEligibilityDecisionMissing,
		},
		{
			name: "unverified Venue stays missing",
			load: PublicEligibilityLoad{
				WorkID:            eligibilityWorkID,
				MetricYear:        2025,
				SubjectVersionID:  eligibilitySubjectVersionID,
				SubjectVersionKey: eligibilitySubjectVersionKey,
				MissingReason:     PublicEligibilityReasonVenueNotVerifiedJournal,
			},
			want: PublicEligibilityDecisionMissing,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assessment, err := policy.Evaluate(test.load, evaluatedAt)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if assessment.Decision != test.want {
				t.Fatalf("decision = %q, want %q", assessment.Decision, test.want)
			}
			if assessment.WorkID != eligibilityWorkID ||
				assessment.PolicyVersion != BiomedicalPublicEligibilityPolicyVersion ||
				assessment.MetricYear != 2025 ||
				assessment.SubjectVersionID != eligibilitySubjectVersionID ||
				assessment.SubjectVersionKey != eligibilitySubjectVersionKey ||
				!assessment.AssessedAt.Equal(evaluatedAt) {
				t.Fatalf("assessment metadata = %#v", assessment)
			}
		})
	}
}

func TestPublicEligibilityPolicyRejectsFabricatedOrMismatchedControlledEvidence(t *testing.T) {
	t.Parallel()

	policy, err := NewPublicEligibilityPolicy(
		BiomedicalPublicEligibilityPolicyVersion,
	)
	if err != nil {
		t.Fatalf("NewPublicEligibilityPolicy() error = %v", err)
	}
	evaluatedAt := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		edit func(*PublicEligibilityLoad)
		want string
	}{
		{
			name: "metric year mismatch",
			edit: func(load *PublicEligibilityLoad) {
				load.Metrics[0].MetricYear = 2024
			},
			want: "metric year",
		},
		{
			name: "matched metric identity mismatch",
			edit: func(load *PublicEligibilityLoad) {
				load.Matches[0].VenueMetricSnapshotID = "different-metric"
			},
			want: "metric",
		},
		{
			name: "matched Subject version mismatch",
			edit: func(load *PublicEligibilityLoad) {
				load.Matches[0].SubjectVersionID = "different-version"
			},
			want: "Subject version",
		},
		{
			name: "matched exact category mismatch",
			edit: func(load *PublicEligibilityLoad) {
				load.Matches[0].JCRCategory = "oncology"
			},
			want: "exact category",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			load := controlledEligibilityLoad(
				[]PublicEligibilityMetricEvidence{
					controlledMetric("metric-oncology", "Oncology"),
				},
				[]PublicEligibilitySubjectEvidence{
					controlledSubjectMatch("link-oncology", "metric-oncology", "Oncology"),
				},
			)
			test.edit(&load)
			_, err := policy.Evaluate(load, evaluatedAt)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Evaluate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestPublicEligibilityServiceKeepsIdentityScopeSeparateAndPersistsDecision(t *testing.T) {
	t.Parallel()

	evaluatedAt := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)
	store := &recordingPublicEligibilityStore{
		load: controlledEligibilityLoad(
			[]PublicEligibilityMetricEvidence{
				controlledMetric("metric-oncology", "Oncology"),
			},
			[]PublicEligibilitySubjectEvidence{
				controlledSubjectMatch("link-oncology", "metric-oncology", "Oncology"),
			},
		),
	}
	service, err := NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}

	assessment, err := service.Assess(context.Background(), PublicEligibilityInput{
		WorkID:            eligibilityWorkID,
		PolicyVersion:     BiomedicalPublicEligibilityPolicyVersion,
		MetricYear:        2025,
		SubjectVersionKey: eligibilitySubjectVersionKey,
		AssessedAt:        evaluatedAt,
	})
	if err != nil {
		t.Fatalf("Assess() error = %v", err)
	}
	if store.loadCalls != 1 || store.persistCalls != 1 {
		t.Fatalf(
			"store calls = load %d persist %d, want one each",
			store.loadCalls,
			store.persistCalls,
		)
	}
	if store.loadedWorkID != eligibilityWorkID ||
		store.loadedMetricYear != 2025 ||
		store.loadedSubjectVersionKey != eligibilitySubjectVersionKey {
		t.Fatalf(
			"load request = %q/%d/%q",
			store.loadedWorkID,
			store.loadedMetricYear,
			store.loadedSubjectVersionKey,
		)
	}
	if assessment.Decision != PublicEligibilityDecisionAccepted ||
		!reflect.DeepEqual(store.persisted, assessment) {
		t.Fatalf("assessment = %#v persisted = %#v", assessment, store.persisted)
	}
}

func TestPublicEligibilityServiceRequiresExplicitVersionYearSubjectAndTime(t *testing.T) {
	t.Parallel()

	valid := PublicEligibilityInput{
		WorkID:            eligibilityWorkID,
		PolicyVersion:     BiomedicalPublicEligibilityPolicyVersion,
		MetricYear:        2025,
		SubjectVersionKey: eligibilitySubjectVersionKey,
		AssessedAt:        time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name string
		edit func(*PublicEligibilityInput)
		want string
	}{
		{
			name: "work",
			edit: func(input *PublicEligibilityInput) {
				input.WorkID = ""
			},
			want: "Work",
		},
		{
			name: "policy version",
			edit: func(input *PublicEligibilityInput) {
				input.PolicyVersion = "biomedical-public-eligibility/v2"
			},
			want: "policy version",
		},
		{
			name: "metric year",
			edit: func(input *PublicEligibilityInput) {
				input.MetricYear = 0
			},
			want: "metric year",
		},
		{
			name: "Subject version",
			edit: func(input *PublicEligibilityInput) {
				input.SubjectVersionKey = ""
			},
			want: "Subject version",
		},
		{
			name: "assessed at",
			edit: func(input *PublicEligibilityInput) {
				input.AssessedAt = time.Time{}
			},
			want: "assessed_at",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := &recordingPublicEligibilityStore{}
			service, err := NewPublicEligibilityService(store)
			if err != nil {
				t.Fatalf("NewPublicEligibilityService() error = %v", err)
			}
			input := valid
			test.edit(&input)
			_, err = service.Assess(context.Background(), input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Assess() error = %v, want containing %q", err, test.want)
			}
			if store.loadCalls != 0 || store.persistCalls != 0 {
				t.Fatalf(
					"invalid input store calls = load %d persist %d",
					store.loadCalls,
					store.persistCalls,
				)
			}
		})
	}
}

const (
	eligibilityWorkID            = "00000000-0000-0000-0000-000000000601"
	eligibilityVenueID           = "00000000-0000-0000-0000-000000000602"
	eligibilitySubjectVersionID  = "00000000-0000-0000-0000-000000000603"
	eligibilitySubjectVersionKey = "biomedical-jcr-subjects/v1"
)

func controlledVenue() *PublicEligibilityVenueEvidence {
	return &PublicEligibilityVenueEvidence{
		VenueID: eligibilityVenueID,
		ISSNL:   "1234-5679",
	}
}

func controlledMetric(id, category string) PublicEligibilityMetricEvidence {
	return PublicEligibilityMetricEvidence{
		VenueMetricSnapshotID: id,
		VenueID:               eligibilityVenueID,
		MetricYear:            2025,
		JCRCategory:           category,
	}
}

func controlledSubjectMatch(
	linkID string,
	metricID string,
	category string,
) PublicEligibilitySubjectEvidence {
	return PublicEligibilitySubjectEvidence{
		JournalSubjectMetricID: linkID,
		VenueMetricSnapshotID:  metricID,
		VenueID:                eligibilityVenueID,
		MetricYear:             2025,
		SubjectVersionID:       eligibilitySubjectVersionID,
		SubjectID:              "00000000-0000-0000-0000-000000000604",
		SubjectRuleID:          "00000000-0000-0000-0000-000000000605",
		SubjectSlug:            "oncology",
		JCRCategory:            category,
	}
}

func controlledEligibilityLoad(
	metrics []PublicEligibilityMetricEvidence,
	matches []PublicEligibilitySubjectEvidence,
) PublicEligibilityLoad {
	return PublicEligibilityLoad{
		WorkID:            eligibilityWorkID,
		MetricYear:        2025,
		SubjectVersionID:  eligibilitySubjectVersionID,
		SubjectVersionKey: eligibilitySubjectVersionKey,
		Venue:             controlledVenue(),
		Metrics:           slices.Clone(metrics),
		Matches:           slices.Clone(matches),
	}
}

type recordingPublicEligibilityStore struct {
	load                    PublicEligibilityLoad
	loadErr                 error
	persistErr              error
	loadCalls               int
	persistCalls            int
	loadedWorkID            string
	loadedMetricYear        int
	loadedSubjectVersionKey string
	persisted               PublicEligibilityAssessment
}

func (store *recordingPublicEligibilityStore) LoadEligibility(
	_ context.Context,
	workID string,
	metricYear int,
	subjectVersionKey string,
) (PublicEligibilityLoad, error) {
	store.loadCalls++
	store.loadedWorkID = workID
	store.loadedMetricYear = metricYear
	store.loadedSubjectVersionKey = subjectVersionKey
	if store.loadErr != nil {
		return PublicEligibilityLoad{}, store.loadErr
	}
	return store.load.Clone(), nil
}

func (store *recordingPublicEligibilityStore) PersistEligibility(
	_ context.Context,
	assessment PublicEligibilityAssessment,
) (PublicEligibilityAssessment, error) {
	store.persistCalls++
	store.persisted = assessment.Clone()
	if store.persistErr != nil {
		return PublicEligibilityAssessment{}, store.persistErr
	}
	return assessment.Clone(), nil
}

func TestNewPublicEligibilityServiceRejectsNilStore(t *testing.T) {
	t.Parallel()

	_, err := NewPublicEligibilityService(nil)
	if err == nil {
		t.Fatal("NewPublicEligibilityService(nil) error = nil")
	}
	var target *recordingPublicEligibilityStore
	_, err = NewPublicEligibilityService(target)
	if err == nil {
		t.Fatal("NewPublicEligibilityService(typed nil) error = nil")
	}
}

func TestPublicEligibilityServicePropagatesStoreErrorsWithoutSecondWrite(t *testing.T) {
	t.Parallel()

	loadErr := errors.New("controlled evidence unavailable")
	store := &recordingPublicEligibilityStore{loadErr: loadErr}
	service, err := NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}
	_, err = service.Assess(context.Background(), PublicEligibilityInput{
		WorkID:            eligibilityWorkID,
		PolicyVersion:     BiomedicalPublicEligibilityPolicyVersion,
		MetricYear:        2025,
		SubjectVersionKey: eligibilitySubjectVersionKey,
		AssessedAt:        time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, loadErr) {
		t.Fatalf("Assess() error = %v, want %v", err, loadErr)
	}
	if store.persistCalls != 0 {
		t.Fatalf("persist calls = %d, want zero", store.persistCalls)
	}
}
