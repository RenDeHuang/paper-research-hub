package venue

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestAssessmentServiceMaterializesAllQ1PolicyForEveryVenue(t *testing.T) {
	t.Parallel()

	assessedAt := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	receiptID := "00000000-0000-0000-0000-000000000501"
	venues := []AssessmentVenue{
		{
			Venue: assessmentJournal(t, "00000000-0000-0000-0000-000000000101", "Q1 Journal"),
			Metrics: []MetricSnapshot{
				mustJCRRegistryV2MetricSnapshot(
					t,
					"00000000-0000-0000-0000-000000000101",
					2025,
					"Oncology",
					"4.5",
					QuartileQ1,
					MetricStatusKnown,
					"authorized-jcr",
				),
			},
		},
		{
			Venue: assessmentJournal(t, "00000000-0000-0000-0000-000000000102", "High JIF Journal"),
			Metrics: []MetricSnapshot{
				mustJCRRegistryV2MetricSnapshot(
					t,
					"00000000-0000-0000-0000-000000000102",
					2025,
					"Immunology",
					"10.0",
					QuartileQ2,
					MetricStatusKnown,
					"authorized-jcr",
				),
			},
		},
		{
			Venue: assessmentJournal(t, "00000000-0000-0000-0000-000000000103", "Rejected Journal"),
			Metrics: []MetricSnapshot{
				mustJCRRegistryV2MetricSnapshot(
					t,
					"00000000-0000-0000-0000-000000000103",
					2025,
					"Neurosciences",
					"9.999",
					QuartileQ2,
					MetricStatusKnown,
					"authorized-jcr",
				),
			},
		},
		{
			Venue: assessmentJournal(t, "00000000-0000-0000-0000-000000000104", "Missing Journal"),
		},
		{
			Venue: assessmentNonJournal(
				t,
				"00000000-0000-0000-0000-000000000105",
				VenueTypePreprint,
				"Preprint Server",
			),
		},
	}
	store := &recordingAssessmentStore{
		load: AssessmentLoad{
			JCRImportReceiptID: receiptID,
			MetricYear:         2025,
			Venues:             venues,
		},
	}
	service, err := NewAssessmentService(store)
	if err != nil {
		t.Fatalf("NewAssessmentService() error = %v", err)
	}

	summary, err := service.Assess(context.Background(), AssessmentInput{
		JCRImportReceiptID: receiptID,
		MetricYear:         2025,
		PolicyVersion:      JournalAllQ1PolicyVersion,
		AssessedAt:         assessedAt,
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
	if summary.Total != 5 ||
		summary.Accepted != 1 ||
		summary.Rejected != 2 ||
		summary.Unknown != 1 ||
		summary.NotApplicable != 1 {
		t.Fatalf("summary = %#v", summary)
	}

	got := store.persisted.Assessments
	if len(got) != 5 {
		t.Fatalf("persisted assessments = %d, want 5", len(got))
	}
	assertAssessmentDecision(
		t,
		got[0],
		PolicyDecisionAccepted,
		[]MatchedRule{MatchedRuleAnyQ1},
		receiptID,
		assessedAt,
	)
	assertAssessmentDecision(
		t,
		got[1],
		PolicyDecisionRejected,
		nil,
		receiptID,
		assessedAt,
	)
	assertAssessmentDecision(
		t,
		got[2],
		PolicyDecisionRejected,
		nil,
		receiptID,
		assessedAt,
	)
	assertAssessmentDecision(
		t,
		got[3],
		PolicyDecisionUnknown,
		nil,
		receiptID,
		assessedAt,
	)
	assertAssessmentDecision(
		t,
		got[4],
		PolicyDecisionNotApplicable,
		nil,
		receiptID,
		assessedAt,
	)
}

func TestAssessmentServiceRejectsPolicyVersionOrReceiptYearMismatchBeforePersist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input AssessmentInput
		load  AssessmentLoad
		want  string
	}{
		{
			name: "blank receipt",
			input: AssessmentInput{
				MetricYear:    2025,
				PolicyVersion: JournalAllQ1PolicyVersion,
				AssessedAt:    time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			},
			want: "JCR import receipt",
		},
		{
			name: "unsupported policy",
			input: AssessmentInput{
				JCRImportReceiptID: "00000000-0000-0000-0000-000000000501",
				MetricYear:         2025,
				PolicyVersion:      "journal-all-q1/v1",
				AssessedAt:         time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			},
			want: "unsupported policy version",
		},
		{
			name: "receipt metric year mismatch",
			input: AssessmentInput{
				JCRImportReceiptID: "00000000-0000-0000-0000-000000000501",
				MetricYear:         2025,
				PolicyVersion:      JournalAllQ1PolicyVersion,
				AssessedAt:         time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			},
			load: AssessmentLoad{
				JCRImportReceiptID: "00000000-0000-0000-0000-000000000501",
				MetricYear:         2024,
			},
			want: "metric year",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := &recordingAssessmentStore{load: test.load}
			service, err := NewAssessmentService(store)
			if err != nil {
				t.Fatalf("NewAssessmentService() error = %v", err)
			}

			_, err = service.Assess(context.Background(), test.input)
			if err == nil || !containsAssessmentError(err, test.want) {
				t.Fatalf("Assess() error = %v, want containing %q", err, test.want)
			}
			if store.persistCalls != 0 {
				t.Fatalf("persist calls = %d, want zero", store.persistCalls)
			}
		})
	}
}

type recordingAssessmentStore struct {
	load         AssessmentLoad
	loadErr      error
	persistErr   error
	loadCalls    int
	persistCalls int
	persisted    AssessmentBatch
}

func (store *recordingAssessmentStore) LoadAssessment(
	_ context.Context,
	receiptID string,
	metricYear int,
) (AssessmentLoad, error) {
	store.loadCalls++
	if store.loadErr != nil {
		return AssessmentLoad{}, store.loadErr
	}
	if store.load.JCRImportReceiptID == "" {
		store.load.JCRImportReceiptID = receiptID
	}
	if store.load.MetricYear == 0 {
		store.load.MetricYear = metricYear
	}
	return store.load, nil
}

func (store *recordingAssessmentStore) PersistAssessments(
	_ context.Context,
	batch AssessmentBatch,
) (AssessmentSummary, error) {
	store.persistCalls++
	store.persisted = batch
	if store.persistErr != nil {
		return AssessmentSummary{}, store.persistErr
	}
	return summarizeAssessments(batch.Assessments), nil
}

func assessmentJournal(t *testing.T, id, title string) Venue {
	t.Helper()
	return assessmentNonJournal(t, id, VenueTypeJournal, title)
}

func assessmentNonJournal(t *testing.T, id string, venueType VenueType, title string) Venue {
	t.Helper()
	alias, err := NewAlias(title, "assessment-test")
	if err != nil {
		t.Fatalf("NewAlias() error = %v", err)
	}
	identifiers := ISSNSet{}
	if venueType == VenueTypeJournal {
		linkingISSN, parseErr := ParseISSN(ISSNRoleLinking, "1234-5679")
		if parseErr != nil {
			t.Fatalf("ParseISSN() error = %v", parseErr)
		}
		identifiers, parseErr = NewISSNSet(linkingISSN)
		if parseErr != nil {
			t.Fatalf("NewISSNSet() error = %v", parseErr)
		}
	}
	item, err := NewVenue(id, venueType, identifiers, []Alias{alias})
	if err != nil {
		t.Fatalf("NewVenue() error = %v", err)
	}
	return item
}

func assertAssessmentDecision(
	t *testing.T,
	assessment Assessment,
	decision PolicyDecision,
	rules []MatchedRule,
	receiptID string,
	assessedAt time.Time,
) {
	t.Helper()
	if assessment.Result.Decision() != decision ||
		!slices.Equal(assessment.Result.MatchedRules(), rules) ||
		assessment.JCRImportReceiptID != receiptID ||
		assessment.Result.PolicyVersion() != JournalAllQ1PolicyVersion ||
		assessment.Result.MetricYear() != 2025 ||
		!assessment.Result.EvaluatedAt().Equal(assessedAt) {
		t.Fatalf("assessment = %#v", assessment)
	}
}

func containsAssessmentError(err error, want string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return len(want) == 0 || containsFold(err.Error(), want)
}

func containsFold(value, fragment string) bool {
	valueRunes := []rune(value)
	fragmentRunes := []rune(fragment)
	for index := range valueRunes {
		if index+len(fragmentRunes) > len(valueRunes) {
			break
		}
		matches := true
		for offset := range fragmentRunes {
			left := valueRunes[index+offset]
			right := fragmentRunes[offset]
			if left >= 'A' && left <= 'Z' {
				left += 'a' - 'A'
			}
			if right >= 'A' && right <= 'Z' {
				right += 'a' - 'A'
			}
			if left != right {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
