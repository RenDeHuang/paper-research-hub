package biomed

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublicEligibilityBatchServiceAssessesEveryWorkInEnumeratedOrder(t *testing.T) {
	t.Parallel()

	workIDs := []string{
		"00000000-0000-0000-0000-000000000611",
		"00000000-0000-0000-0000-000000000612",
		"00000000-0000-0000-0000-000000000613",
	}
	source := &recordingPublicEligibilityWorkEnumerator{workIDs: workIDs}
	store := &batchPublicEligibilityStore{
		loads: map[string]PublicEligibilityLoad{
			workIDs[0]: batchEligibilityLoad(
				workIDs[0],
				PublicEligibilityDecisionAccepted,
			),
			workIDs[1]: batchEligibilityLoad(
				workIDs[1],
				PublicEligibilityDecisionRejected,
			),
			workIDs[2]: batchEligibilityLoad(
				workIDs[2],
				PublicEligibilityDecisionMissing,
			),
		},
	}
	eligibility, err := NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}
	service, err := NewPublicEligibilityBatchService(source, eligibility)
	if err != nil {
		t.Fatalf("NewPublicEligibilityBatchService() error = %v", err)
	}
	assessedAt := time.Date(
		2026,
		time.July,
		17,
		15,
		30,
		0,
		123456789,
		time.FixedZone("UTC+8", 8*60*60),
	)

	summary, err := service.AssessAll(
		context.Background(),
		PublicEligibilityBatchInput{
			PolicyVersion:     BiomedicalPublicEligibilityPolicyVersion,
			MetricYear:        2025,
			SubjectVersionKey: eligibilitySubjectVersionKey,
			AssessedAt:        assessedAt,
		},
	)
	if err != nil {
		t.Fatalf("AssessAll() error = %v", err)
	}
	if summary != (PublicEligibilityBatchSummary{
		Total:    3,
		Accepted: 1,
		Rejected: 1,
		Missing:  1,
	}) {
		t.Fatalf("AssessAll() summary = %#v", summary)
	}
	if source.calls != 1 {
		t.Fatalf("WorkIDs() calls = %d, want 1", source.calls)
	}
	if !slices.Equal(store.loadedWorkIDs, workIDs) {
		t.Fatalf(
			"loaded Work IDs = %v, want enumerated order %v",
			store.loadedWorkIDs,
			workIDs,
		)
	}
	if len(store.loadCalls) != len(workIDs) {
		t.Fatalf("load calls = %d, want %d", len(store.loadCalls), len(workIDs))
	}
	for index, call := range store.loadCalls {
		if call.workID != workIDs[index] ||
			call.metricYear != 2025 ||
			call.subjectVersionKey != eligibilitySubjectVersionKey {
			t.Fatalf("load call %d = %#v", index, call)
		}
	}
	if len(store.persisted) != len(workIDs) {
		t.Fatalf(
			"persisted assessments = %d, want %d",
			len(store.persisted),
			len(workIDs),
		)
	}
	for index, assessment := range store.persisted {
		if assessment.WorkID != workIDs[index] ||
			assessment.PolicyVersion != BiomedicalPublicEligibilityPolicyVersion ||
			assessment.MetricYear != 2025 ||
			assessment.SubjectVersionKey != eligibilitySubjectVersionKey ||
			!assessment.AssessedAt.Equal(assessedAt.UTC()) {
			t.Fatalf("persisted assessment %d = %#v", index, assessment)
		}
	}
}

func TestPublicEligibilityBatchServiceReturnsZeroSummaryForNoWorks(t *testing.T) {
	t.Parallel()

	source := &recordingPublicEligibilityWorkEnumerator{}
	store := &batchPublicEligibilityStore{}
	eligibility, err := NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}
	service, err := NewPublicEligibilityBatchService(source, eligibility)
	if err != nil {
		t.Fatalf("NewPublicEligibilityBatchService() error = %v", err)
	}

	summary, err := service.AssessAll(
		context.Background(),
		validPublicEligibilityBatchInput(),
	)
	if err != nil {
		t.Fatalf("AssessAll() error = %v", err)
	}
	if summary != (PublicEligibilityBatchSummary{}) {
		t.Fatalf("AssessAll() summary = %#v, want all zero", summary)
	}
	if source.calls != 1 || len(store.loadCalls) != 0 || len(store.persisted) != 0 {
		t.Fatalf(
			"empty batch calls = source %d load %d persist %d",
			source.calls,
			len(store.loadCalls),
			len(store.persisted),
		)
	}
}

func TestPublicEligibilityBatchServiceStopsAndReturnsAnyWorkAssessmentError(t *testing.T) {
	t.Parallel()

	workIDs := []string{
		"00000000-0000-0000-0000-000000000621",
		"00000000-0000-0000-0000-000000000622",
		"00000000-0000-0000-0000-000000000623",
	}
	assessmentErr := errors.New("exact Subject evidence unavailable")
	source := &recordingPublicEligibilityWorkEnumerator{workIDs: workIDs}
	store := &batchPublicEligibilityStore{
		loads: map[string]PublicEligibilityLoad{
			workIDs[0]: batchEligibilityLoad(
				workIDs[0],
				PublicEligibilityDecisionAccepted,
			),
			workIDs[2]: batchEligibilityLoad(
				workIDs[2],
				PublicEligibilityDecisionAccepted,
			),
		},
		loadErrors: map[string]error{
			workIDs[1]: assessmentErr,
		},
	}
	eligibility, err := NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}
	service, err := NewPublicEligibilityBatchService(source, eligibility)
	if err != nil {
		t.Fatalf("NewPublicEligibilityBatchService() error = %v", err)
	}

	_, err = service.AssessAll(
		context.Background(),
		validPublicEligibilityBatchInput(),
	)
	if !errors.Is(err, assessmentErr) ||
		!strings.Contains(err.Error(), workIDs[1]) {
		t.Fatalf(
			"AssessAll() error = %v, want Work %q wrapping %v",
			err,
			workIDs[1],
			assessmentErr,
		)
	}
	if !slices.Equal(store.loadedWorkIDs, workIDs[:2]) {
		t.Fatalf(
			"loaded Work IDs = %v, want stop after %v",
			store.loadedWorkIDs,
			workIDs[:2],
		)
	}
	if len(store.persisted) != 1 ||
		store.persisted[0].WorkID != workIDs[0] {
		t.Fatalf("persisted assessments = %#v, want only first Work", store.persisted)
	}
}

func TestPublicEligibilityBatchServiceValidatesExplicitScopeBeforeEnumeration(
	t *testing.T,
) {
	t.Parallel()

	valid := validPublicEligibilityBatchInput()
	tests := []struct {
		name  string
		input PublicEligibilityBatchInput
		want  string
	}{
		{
			name: "unsupported policy version",
			input: func() PublicEligibilityBatchInput {
				input := valid
				input.PolicyVersion = "biomedical-public-eligibility/v2"
				return input
			}(),
			want: "policy version",
		},
		{
			name: "metric year below range",
			input: func() PublicEligibilityBatchInput {
				input := valid
				input.MetricYear = 1899
				return input
			}(),
			want: "1900..3000",
		},
		{
			name: "metric year above range",
			input: func() PublicEligibilityBatchInput {
				input := valid
				input.MetricYear = 3001
				return input
			}(),
			want: "1900..3000",
		},
		{
			name: "blank Subject version",
			input: func() PublicEligibilityBatchInput {
				input := valid
				input.SubjectVersionKey = ""
				return input
			}(),
			want: "Subject version",
		},
		{
			name: "untrimmed Subject version",
			input: func() PublicEligibilityBatchInput {
				input := valid
				input.SubjectVersionKey = " " + eligibilitySubjectVersionKey
				return input
			}(),
			want: "trimmed",
		},
		{
			name: "zero assessed at",
			input: func() PublicEligibilityBatchInput {
				input := valid
				input.AssessedAt = time.Time{}
				return input
			}(),
			want: "assessed_at",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := &recordingPublicEligibilityWorkEnumerator{
				workIDs: []string{"00000000-0000-0000-0000-000000000631"},
			}
			store := &batchPublicEligibilityStore{}
			eligibility, err := NewPublicEligibilityService(store)
			if err != nil {
				t.Fatalf("NewPublicEligibilityService() error = %v", err)
			}
			service, err := NewPublicEligibilityBatchService(source, eligibility)
			if err != nil {
				t.Fatalf("NewPublicEligibilityBatchService() error = %v", err)
			}

			_, err = service.AssessAll(context.Background(), test.input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"AssessAll() error = %v, want containing %q",
					err,
					test.want,
				)
			}
			if source.calls != 0 ||
				len(store.loadCalls) != 0 ||
				len(store.persisted) != 0 {
				t.Fatalf(
					"invalid input reached enumeration or assessment: source %d load %d persist %d",
					source.calls,
					len(store.loadCalls),
					len(store.persisted),
				)
			}
		})
	}
}

func TestPostgresPublicEligibilityWorkEnumeratorOrdersEveryWorkByID(t *testing.T) {
	pool := openBiomedScopeTestPool(t)
	ctx := biomedScopeTestContext(t)
	inserted := []string{
		"30000000-0000-0000-0000-000000000001",
		"10000000-0000-0000-0000-000000000001",
		"20000000-0000-0000-0000-000000000001",
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO works (
			id,
			canonical_key,
			status,
			title
		) VALUES
			($1, 'openalex:W900003', 'active', 'Third batch Work'),
			($2, 'openalex:W900001', 'active', 'First batch Work'),
			($3, 'openalex:W900002', 'active', 'Second batch Work')
	`, inserted[0], inserted[1], inserted[2]); err != nil {
		t.Fatalf("insert batch Works: %v", err)
	}
	source, err := NewPostgresPublicEligibilityWorkEnumerator(pool)
	if err != nil {
		t.Fatalf("NewPostgresPublicEligibilityWorkEnumerator() error = %v", err)
	}

	var got []string
	for workID, enumerateErr := range source.WorkIDs(ctx) {
		if enumerateErr != nil {
			t.Fatalf("WorkIDs() error = %v", enumerateErr)
		}
		got = append(got, workID)
	}
	want := []string{inserted[1], inserted[2], inserted[0]}
	if !slices.Equal(got, want) {
		t.Fatalf("WorkIDs() = %v, want stable ORDER BY id %v", got, want)
	}
}

func TestPostgresPublicEligibilityBatchSupportsSingleConnectionPool(t *testing.T) {
	setupPool := openBiomedScopeTestPool(t)
	fixture := insertBiomedScopeFixture(t, setupPool, "", false)
	poolConfig, err := pgxpool.ParseConfig(setupPool.Config().ConnString())
	if err != nil {
		t.Fatalf("parse single-connection pool config: %v", err)
	}
	poolConfig.MinConns = 0
	poolConfig.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("open single-connection pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping single-connection pool: %v", err)
	}

	source, err := NewPostgresPublicEligibilityWorkEnumerator(pool)
	if err != nil {
		t.Fatalf("NewPostgresPublicEligibilityWorkEnumerator() error = %v", err)
	}
	store, err := NewPostgresPublicEligibilityStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresPublicEligibilityStore() error = %v", err)
	}
	eligibility, err := NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}
	service, err := NewPublicEligibilityBatchService(source, eligibility)
	if err != nil {
		t.Fatalf("NewPublicEligibilityBatchService() error = %v", err)
	}

	summary, err := service.AssessAll(ctx, validPublicEligibilityBatchInput())
	if err != nil {
		t.Fatalf("AssessAll() error = %v", err)
	}
	if summary != (PublicEligibilityBatchSummary{
		Total:   1,
		Missing: 1,
	}) {
		t.Fatalf(
			"AssessAll() summary = %#v for Work %q",
			summary,
			fixture.workID,
		)
	}
}

func validPublicEligibilityBatchInput() PublicEligibilityBatchInput {
	return PublicEligibilityBatchInput{
		PolicyVersion:     BiomedicalPublicEligibilityPolicyVersion,
		MetricYear:        2025,
		SubjectVersionKey: eligibilitySubjectVersionKey,
		AssessedAt: time.Date(
			2026,
			time.July,
			17,
			15,
			30,
			0,
			123456789,
			time.UTC,
		),
	}
}

func batchEligibilityLoad(
	workID string,
	decision PublicEligibilityDecision,
) PublicEligibilityLoad {
	switch decision {
	case PublicEligibilityDecisionAccepted:
		load := controlledEligibilityLoad(
			[]PublicEligibilityMetricEvidence{
				controlledMetric(
					"00000000-0000-0000-0000-000000000701",
					"Oncology",
				),
			},
			[]PublicEligibilitySubjectEvidence{
				controlledSubjectMatch(
					"00000000-0000-0000-0000-000000000702",
					"00000000-0000-0000-0000-000000000701",
					"Oncology",
				),
			},
		)
		load.WorkID = workID
		return load
	case PublicEligibilityDecisionRejected:
		load := controlledEligibilityLoad(
			[]PublicEligibilityMetricEvidence{
				controlledMetric(
					"00000000-0000-0000-0000-000000000703",
					"Materials Science",
				),
			},
			nil,
		)
		load.WorkID = workID
		return load
	case PublicEligibilityDecisionMissing:
		return PublicEligibilityLoad{
			WorkID:            workID,
			MetricYear:        2025,
			SubjectVersionID:  eligibilitySubjectVersionID,
			SubjectVersionKey: eligibilitySubjectVersionKey,
			MissingReason:     PublicEligibilityReasonVenueMissing,
		}
	default:
		panic("unsupported test decision")
	}
}

type recordingPublicEligibilityWorkEnumerator struct {
	workIDs []string
	err     error
	calls   int
}

func (source *recordingPublicEligibilityWorkEnumerator) WorkIDs(
	_ context.Context,
) iter.Seq2[string, error] {
	source.calls++
	return func(yield func(string, error) bool) {
		for _, workID := range source.workIDs {
			if !yield(workID, nil) {
				return
			}
		}
		if source.err != nil {
			yield("", source.err)
		}
	}
}

type batchEligibilityLoadCall struct {
	workID            string
	metricYear        int
	subjectVersionKey string
}

type batchPublicEligibilityStore struct {
	loads         map[string]PublicEligibilityLoad
	loadErrors    map[string]error
	loadedWorkIDs []string
	loadCalls     []batchEligibilityLoadCall
	persisted     []PublicEligibilityAssessment
}

func (store *batchPublicEligibilityStore) LoadEligibility(
	_ context.Context,
	workID string,
	metricYear int,
	subjectVersionKey string,
) (PublicEligibilityLoad, error) {
	store.loadedWorkIDs = append(store.loadedWorkIDs, workID)
	store.loadCalls = append(store.loadCalls, batchEligibilityLoadCall{
		workID:            workID,
		metricYear:        metricYear,
		subjectVersionKey: subjectVersionKey,
	})
	if err := store.loadErrors[workID]; err != nil {
		return PublicEligibilityLoad{}, err
	}
	return store.loads[workID].Clone(), nil
}

func (store *batchPublicEligibilityStore) PersistEligibility(
	_ context.Context,
	assessment PublicEligibilityAssessment,
) (PublicEligibilityAssessment, error) {
	store.persisted = append(store.persisted, assessment.Clone())
	return assessment.Clone(), nil
}
