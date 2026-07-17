package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/citation"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ranking"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

func TestPublisherRequiresAcceptedBiomedicalJCRAssessment(t *testing.T) {
	pool := openCatalogTestPool(t)
	input := catalogCurationInput()
	fixtures := []struct {
		name            string
		decision        string
		policyName      string
		policyVersion   int
		metricYear      int
		lifecycle       string
		venueType       string
		withISSN        bool
		withAssessment  bool
		withSubject     bool
		withEligibility bool
	}{
		{
			name:            "accepted",
			decision:        "accepted",
			policyName:      input.VenuePolicyName,
			policyVersion:   input.VenuePolicyVersion,
			metricYear:      input.JCRMetricYear,
			lifecycle:       "active",
			venueType:       "journal",
			withISSN:        true,
			withAssessment:  true,
			withSubject:     true,
			withEligibility: true,
		},
		{
			name:           "missing persisted eligibility",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "rejected",
			decision:       "rejected",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "unknown",
			decision:       "unknown",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:          "missing assessment",
			lifecycle:     "active",
			venueType:     "journal",
			withISSN:      true,
			withSubject:   true,
			metricYear:    input.JCRMetricYear,
			policyName:    input.VenuePolicyName,
			policyVersion: input.VenuePolicyVersion,
		},
		{
			name:           "wrong metric year",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear - 1,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "stale policy",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion + 1,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "missing exact subject",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
		},
		{
			name:           "missing ISSN",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "non journal",
			decision:       "not_applicable",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "preprint",
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "inactive work",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "withdrawn",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
	}

	subjectVersionID, subjectRuleID := insertCatalogSubjectVersion(
		t,
		pool,
		input.SubjectVersion,
	)
	_ = subjectVersionID

	workIDs := make(map[string]uuid.UUID, len(fixtures))
	type preparedFixture struct {
		spec    int
		venueID uuid.UUID
	}
	prepared := make([]preparedFixture, 0, len(fixtures))
	for index, spec := range fixtures {
		fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "pubmed:curation-" + strings.ReplaceAll(spec.name, " ", "-"),
			canonicalKey:      fmt.Sprintf("doi:10.1000/curation-%02d", index),
			title:             "Curation " + spec.name,
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
			noJCRAssessment:   true,
			sourceTime: time.Date(
				2026,
				time.July,
				17,
				10,
				index,
				0,
				0,
				time.UTC,
			),
		})
		workIDs[spec.name] = fixture.workID
		venueID := catalogWorkVenueID(t, pool, fixture.workID)
		if _, err := pool.Exec(context.Background(), `
			UPDATE works
			SET status = $2
			WHERE id = $1
		`, fixture.workID, spec.lifecycle); err != nil {
			t.Fatalf("set %s lifecycle: %v", spec.name, err)
		}
		var issn any
		if spec.withISSN {
			issn = deterministicCatalogISSN(index + 1)
		}
		if _, err := pool.Exec(context.Background(), `
			UPDATE venues
			SET venue_type = $2,
			    issn_l = $3
			WHERE id = $1
		`, venueID, spec.venueType, issn); err != nil {
			t.Fatalf("set %s Venue evidence: %v", spec.name, err)
		}
		prepared = append(prepared, preparedFixture{spec: index, venueID: venueID})
	}
	jcrFixtures := make([]catalogJCRVenueFixture, 0, len(prepared))
	for _, item := range prepared {
		jcrFixtures = append(jcrFixtures, catalogJCRVenueFixture{
			venueID:     item.venueID,
			index:       item.spec,
			withSubject: fixtures[item.spec].withSubject,
		})
	}
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, jcrFixtures)
	for _, item := range prepared {
		spec := fixtures[item.spec]
		if spec.withAssessment {
			insertCatalogVenueAssessment(
				t,
				pool,
				input,
				item.venueID,
				spec.policyName,
				spec.policyVersion,
				spec.metricYear,
				spec.decision,
			)
		}
		if spec.withEligibility {
			assessCatalogBiomedicalEligibility(
				t,
				pool,
				workIDs[spec.name],
				input,
			)
		}
	}
	insertCatalogCitationAnalysisRun(
		t,
		pool,
		input,
		workIDs["accepted"],
	)
	acceptedFixtures := []publisherWorkFixture{{
		workID: workIDs["accepted"],
	}}
	insertCatalogBiomedicalAnalysisRuns(
		t,
		pool,
		input,
		catalogBiomedicalCohortRevisionForFixtures(
			t,
			pool,
			input,
			acceptedFixtures,
		),
		input.TrendAnalysisRunID,
	)

	publisher := mustPublisher(t, pool)
	generation, err := publisher.PublishCurrent(context.Background(), input)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	repository := mustRepository(t, pool)
	page, err := repository.Papers(context.Background(), PaperListQuery{Limit: 50})
	if err != nil {
		t.Fatalf("Papers() error = %v", err)
	}
	assertPaperIDs(t, page.Items, workIDs["accepted"])

	for name, workID := range workIDs {
		if name == "accepted" {
			continue
		}
		if _, err := repository.Paper(context.Background(), workID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Paper(%s) error = %v, want ErrNotFound", name, err)
		}
	}

	var metadata []byte
	if err := pool.QueryRow(context.Background(), `
		SELECT metadata
		FROM public_catalog_generations
		WHERE id = $1
	`, generation.ID).Scan(&metadata); err != nil {
		t.Fatalf("query Catalog generation metadata: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatalf("decode Catalog generation metadata: %v", err)
	}
	for key, want := range map[string]any{
		"jcr_metric_year":             float64(input.JCRMetricYear),
		"venue_policy_name":           input.VenuePolicyName,
		"venue_policy_version":        float64(input.VenuePolicyVersion),
		"eligibility_policy_version":  input.EligibilityPolicyVersion,
		"subject_version":             input.SubjectVersion,
		"jcr_import_receipt":          input.JCRImportReceipt.String(),
		"citation_source":             input.CitationSource,
		"citation_analysis_run_id":    input.CitationAnalysisRunID.String(),
		"trend_analysis_run_id":       input.TrendAnalysisRunID.String(),
		"journal_analysis_run_id":     input.JournalAnalysisRunID.String(),
		"opportunity_analysis_run_id": input.OpportunityAnalysisRunID.String(),
	} {
		if decoded[key] != want {
			t.Fatalf("generation metadata[%q] = %#v, want %#v", key, decoded[key], want)
		}
	}
}

func TestValidatePublishInputRequiresExplicitBiomedicalEligibilityPolicyVersion(
	t *testing.T,
) {
	input := catalogCurationInput()
	input.EligibilityPolicyVersion = ""
	if err := validatePublishInput(input); err == nil ||
		!strings.Contains(err.Error(), "eligibility policy version") {
		t.Fatalf(
			"validatePublishInput(missing eligibility policy) error = %v",
			err,
		)
	}

	input.EligibilityPolicyVersion = "biomedical-public-eligibility/v2"
	if err := validatePublishInput(input); err == nil ||
		!strings.Contains(err.Error(), "unsupported biomedical eligibility policy") {
		t.Fatalf(
			"validatePublishInput(unsupported eligibility policy) error = %v",
			err,
		)
	}
}

func TestValidatePublishInputRequiresExplicitCitationSource(t *testing.T) {
	t.Parallel()

	input := catalogCurationInput()
	input.CitationSource = ""
	if err := validatePublishInput(input); err == nil ||
		!strings.Contains(err.Error(), "citation source") {
		t.Fatalf(
			"validatePublishInput(missing citation source) error = %v",
			err,
		)
	}

	input.CitationSource = " openalex"
	if err := validatePublishInput(input); err == nil ||
		!strings.Contains(err.Error(), "citation source") {
		t.Fatalf(
			"validatePublishInput(untrimmed citation source) error = %v",
			err,
		)
	}
}

func TestValidatePublishInputRequiresExplicitCitationAnalysisRun(t *testing.T) {
	t.Parallel()

	input := catalogCurationInput()
	input.CitationAnalysisRunID = uuid.Nil
	if err := validatePublishInput(input); err == nil ||
		!strings.Contains(err.Error(), "citation analysis run") {
		t.Fatalf(
			"validatePublishInput(missing citation analysis run) error = %v",
			err,
		)
	}
}

func TestValidatePublishInputRequiresEveryBiomedicalAnalysisRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*PublishInput)
		want string
	}{
		{
			name: "trend",
			edit: func(input *PublishInput) {
				input.TrendAnalysisRunID = uuid.Nil
			},
			want: "trend analysis run",
		},
		{
			name: "journal",
			edit: func(input *PublishInput) {
				input.JournalAnalysisRunID = uuid.Nil
			},
			want: "journal analysis run",
		},
		{
			name: "opportunity",
			edit: func(input *PublishInput) {
				input.OpportunityAnalysisRunID = uuid.Nil
			},
			want: "opportunity analysis run",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := catalogCurationInput()
			test.edit(&input)
			if err := validatePublishInput(input); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"validatePublishInput(missing %s) error = %v",
					test.name,
					err,
				)
			}
		})
	}
}

func TestValidateBiomedicalAnalysisRunsRequiresExactCohortAndUpstreams(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	input := catalogCurationInput()
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:validate-biomedical-analysis-runs",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/validate-biomedical-analysis-runs",
		title:             "Validate biomedical analysis runs",
		publishedAt:       input.GeneratedAt.Add(-24 * time.Hour),
		sourceTime:        input.GeneratedAt.Add(-2 * time.Hour),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	cohortRevision := preparePublisherAcceptedCurationWithoutBiomedicalRuns(
		t,
		pool,
		input,
		fixture,
	)
	insertCatalogBiomedicalAnalysisRuns(
		t,
		pool,
		input,
		cohortRevision,
		input.TrendAnalysisRunID,
	)

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin Catalog analysis validation: %v", err)
	}
	defer tx.Rollback(context.Background())

	runs, err := validateBiomedicalAnalysisRuns(
		context.Background(),
		tx,
		input,
		cohortRevision,
	)
	if err != nil {
		t.Fatalf("validateBiomedicalAnalysisRuns() error = %v", err)
	}
	if runs.Trend.ID != input.TrendAnalysisRunID ||
		runs.Journal.ID != input.JournalAnalysisRunID ||
		runs.Opportunity.ID != input.OpportunityAnalysisRunID {
		t.Fatalf("validated biomedical analysis runs = %#v", runs)
	}

	if _, err := validateBiomedicalAnalysisRuns(
		context.Background(),
		tx,
		input,
		strings.Repeat("b", 64),
	); err == nil || !strings.Contains(err.Error(), "cohort revision") {
		t.Fatalf(
			"validateBiomedicalAnalysisRuns(mismatched cohort) error = %v",
			err,
		)
	}
}

func TestPublisherReturnsEmptyDomainWhenNoWorkPassesCurationGate(t *testing.T) {
	pool := openCatalogTestPool(t)
	input := catalogCurationInput()
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:curation-empty",
		canonicalKey:      "doi:10.1000/curation-empty",
		title:             "Rejected biomedical paper",
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE venues SET issn_l = $2 WHERE id = $1
	`, venueID, deterministicCatalogISSN(100)); err != nil {
		t.Fatalf("set rejected Venue ISSN: %v", err)
	}
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, []catalogJCRVenueFixture{{
		venueID:     venueID,
		index:       100,
		withSubject: true,
	}})
	insertCatalogVenueAssessment(
		t,
		pool,
		input,
		venueID,
		input.VenuePolicyName,
		input.VenuePolicyVersion,
		input.JCRMetricYear,
		"rejected",
	)
	insertCatalogCitationAnalysisRun(t, pool, input)

	_, err := mustPublisher(t, pool).PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrEmptyDomain) {
		t.Fatalf("PublishCurrent() error = %v, want ErrEmptyDomain", err)
	}
	assertNoCatalogWrites(t, pool)
}

func catalogCurationInput() PublishInput {
	return catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
}

func catalogCurationInputAt(generatedAt time.Time) PublishInput {
	return PublishInput{
		FormulaVersion:           "public-catalog/biomedical-v1",
		GeneratedAt:              generatedAt,
		JCRMetricYear:            2025,
		VenuePolicyName:          "journal-jif-or-q1",
		VenuePolicyVersion:       1,
		EligibilityPolicyVersion: biomed.BiomedicalPublicEligibilityPolicyVersion,
		SubjectVersion:           "biomedical-jcr-subjects/v1",
		JCRImportReceipt:         uuid.MustParse("00000000-0000-0000-0000-000000000501"),
		CitationSource:           "openalex",
		CitationAnalysisRunID: uuid.MustParse(
			"00000000-0000-0000-0000-000000000701",
		),
		TrendAnalysisRunID: uuid.MustParse(
			"00000000-0000-0000-0000-000000000702",
		),
		JournalAnalysisRunID: uuid.MustParse(
			"00000000-0000-0000-0000-000000000703",
		),
		OpportunityAnalysisRunID: uuid.MustParse(
			"00000000-0000-0000-0000-000000000704",
		),
	}
}

func preparePublisherAcceptedCuration(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	fixtures ...publisherWorkFixture,
) {
	t.Helper()
	preparePublisherAcceptedCurationReferences(t, pool, input, fixtures...)
	analysisWorkIDs := make([]uuid.UUID, 0, len(fixtures))
	for _, fixture := range fixtures {
		analysisWorkIDs = append(analysisWorkIDs, fixture.workID)
	}
	insertCatalogCitationAnalysisRun(t, pool, input, analysisWorkIDs...)
}

func preparePublisherAcceptedCurationReferences(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	fixtures ...publisherWorkFixture,
) {
	t.Helper()
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	jcrFixtures := make([]catalogJCRVenueFixture, 0, len(fixtures))
	venueIDs := make([]uuid.UUID, 0, len(fixtures))
	for index, fixture := range fixtures {
		venueID := catalogWorkVenueID(t, pool, fixture.workID)
		if _, err := pool.Exec(context.Background(), `
			UPDATE venues
			SET venue_type = 'journal',
			    issn_l = $2
			WHERE id = $1
		`, venueID, deterministicCatalogISSN(1000+index)); err != nil {
			t.Fatalf("prepare publisher Venue curation: %v", err)
		}
		venueIDs = append(venueIDs, venueID)
		jcrFixtures = append(jcrFixtures, catalogJCRVenueFixture{
			venueID:     venueID,
			index:       1000 + index,
			withSubject: true,
		})
	}
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, jcrFixtures)
	assessCatalogVenuePolicy(t, pool, input)
	for _, fixture := range fixtures {
		assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	}
	if len(fixtures) > 0 {
		cohortRevision := catalogBiomedicalCohortRevisionForFixtures(
			t,
			pool,
			input,
			fixtures,
		)
		insertCatalogBiomedicalAnalysisRuns(
			t,
			pool,
			input,
			cohortRevision,
			input.TrendAnalysisRunID,
		)
	}
}

func catalogBiomedicalCohortRevisionForFixtures(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	fixtures []publisherWorkFixture,
) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Catalog cohort revision fixture: %v", err)
	}
	defer tx.Rollback(context.Background())

	states, err := loadCurrentSourceStates(ctx, tx)
	if err != nil {
		t.Fatalf("load current source states for cohort revision: %v", err)
	}
	fixtureWorkIDs := make(map[uuid.UUID]struct{}, len(fixtures))
	for _, fixture := range fixtures {
		fixtureWorkIDs[fixture.workID] = struct{}{}
	}
	visible := make(map[uuid.UUID]*workSources, len(fixtures))
	for _, state := range states {
		if state.isDeleted || state.scopeStatus != "included" {
			continue
		}
		if _, found := fixtureWorkIDs[state.workID]; !found {
			continue
		}
		entry := visible[state.workID]
		if entry == nil {
			entry = &workSources{}
			visible[state.workID] = entry
		}
		entry.states = append(entry.states, state)
		entry.sourceRecordIDs = append(entry.sourceRecordIDs, state.sourceRecordID)
	}

	workIDs := make([]uuid.UUID, 0, len(fixtures))
	curations := make([]curationRevisionFact, 0, len(fixtures))
	for _, fixture := range fixtures {
		curation, accepted, err := loadAcceptedCuration(
			ctx,
			tx,
			fixture.workID,
			input,
		)
		if err != nil {
			t.Fatalf("load accepted curation for cohort revision: %v", err)
		}
		if !accepted {
			t.Fatalf(
				"fixture Work %s is not accepted for cohort revision",
				fixture.workID,
			)
		}
		workIDs = append(workIDs, fixture.workID)
		curations = append(curations, curation)
	}
	revision, err := biomedicalCohortRevision(workIDs, visible, curations)
	if err != nil {
		t.Fatalf("compute Catalog fixture cohort revision: %v", err)
	}
	return revision
}

func assessCatalogBiomedicalEligibility(
	t *testing.T,
	pool *pgxpool.Pool,
	workID uuid.UUID,
	input PublishInput,
) {
	t.Helper()
	store, err := biomed.NewPostgresPublicEligibilityStore(pool)
	if err != nil {
		t.Fatalf("create Catalog biomedical eligibility store: %v", err)
	}
	service, err := biomed.NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("create Catalog biomedical eligibility service: %v", err)
	}
	assessment, err := service.Assess(
		context.Background(),
		biomed.PublicEligibilityInput{
			WorkID:            workID.String(),
			PolicyVersion:     biomed.BiomedicalPublicEligibilityPolicyVersion,
			MetricYear:        input.JCRMetricYear,
			SubjectVersionKey: input.SubjectVersion,
			AssessedAt:        input.GeneratedAt.Add(-30 * time.Minute),
		},
	)
	if err != nil {
		t.Fatalf("assess Catalog biomedical eligibility: %v", err)
	}
	if assessment.Decision != biomed.PublicEligibilityDecisionAccepted {
		t.Fatalf(
			"Catalog biomedical eligibility decision = %q, want accepted",
			assessment.Decision,
		)
	}
}

func preparePublisherEmptyCurationReferences(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
) {
	t.Helper()
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, nil)
	insertCatalogCitationAnalysisRun(t, pool, input)
}

func insertCatalogCitationAnalysisRun(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	workIDs ...uuid.UUID,
) {
	t.Helper()
	insertCatalogCitationAnalysisRunWithOptions(
		t,
		pool,
		input,
		catalogCitationAnalysisFixtureOptions{
			runVelocityWindowDays:          30,
			materializedVelocityWindowDays: 30,
			runMinimumCohortSize:           20,
		},
		workIDs...,
	)
}

func insertCatalogBiomedicalAnalysisRuns(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	cohortRevision string,
	opportunityTrendRunID uuid.UUID,
) {
	t.Helper()
	var workID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		SELECT work_id
		FROM biomedical_publication_eligibility_decisions
		WHERE decision = 'accepted'
		  AND policy_version = $1
		  AND metric_year = $2
		ORDER BY work_id
		LIMIT 1
	`, input.EligibilityPolicyVersion, input.JCRMetricYear).Scan(&workID); err != nil {
		t.Fatalf("query biomedical analysis fixture Work: %v", err)
	}
	insertCatalogBiomedicalPublicationFixtures(
		t,
		pool,
		input,
		cohortRevision,
		cohortRevision,
		opportunityTrendRunID,
		publisherWorkFixture{workID: workID},
	)
}

type catalogCitationAnalysisFixtureOptions struct {
	runVelocityWindowDays          int
	materializedVelocityWindowDays int
	runMinimumCohortSize           int
	materializedMinimumCohortSize  *int
}

func insertCatalogCitationAnalysisRunWithOptions(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	options catalogCitationAnalysisFixtureOptions,
	workIDs ...uuid.UUID,
) {
	t.Helper()
	ctx := context.Background()
	asOf := input.GeneratedAt.Add(-2 * time.Minute).UTC()
	completedAt := input.GeneratedAt.Add(-time.Minute).UTC()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Catalog citation analysis fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	rawInput, err := json.Marshal(map[string]any{
		"source":                     input.CitationSource,
		"as_of":                      asOf.Format(time.RFC3339Nano),
		"velocity_window_days":       options.runVelocityWindowDays,
		"minimum_cohort_size":        options.runMinimumCohortSize,
		"formula_version":            citation.CitationIntelligenceFormulaVersion,
		"subject_version":            input.SubjectVersion,
		"eligibility_policy_version": input.EligibilityPolicyVersion,
		"jcr_metric_year":            input.JCRMetricYear,
		"jcr_import_receipt":         input.JCRImportReceipt,
		"venue_policy_name":          input.VenuePolicyName,
		"venue_policy_version":       input.VenuePolicyVersion,
	})
	if err != nil {
		t.Fatalf("encode Catalog citation analysis input: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO analysis_runs (
			id,
			analysis_type,
			model_provider,
			model_name,
			prompt_version,
			status,
			input_payload,
			started_at
		) VALUES (
			$1,
			'citation_intelligence',
			'internal',
			'deterministic',
			$2,
			'running',
			$3::jsonb,
			$4
		)
	`,
		input.CitationAnalysisRunID,
		citation.CitationIntelligenceFormulaVersion,
		rawInput,
		completedAt.Add(-time.Minute),
	); err != nil {
		t.Fatalf("insert Catalog citation analysis run: %v", err)
	}

	for _, workID := range workIDs {
		type storedSnapshot struct {
			id         uuid.UUID
			observedAt time.Time
			count      int64
		}
		rows, err := tx.Query(ctx, `
			SELECT id, observed_at, count
			FROM citation_snapshots
			WHERE work_id = $1
			  AND source = $2
			  AND observed_at <= $3
			ORDER BY observed_at, id
		`, workID, input.CitationSource, asOf)
		if err != nil {
			t.Fatalf(
				"query Catalog citation analysis snapshots for Work %s: %v",
				workID,
				err,
			)
		}
		snapshots := make([]storedSnapshot, 0, 2)
		for rows.Next() {
			var snapshot storedSnapshot
			if err := rows.Scan(
				&snapshot.id,
				&snapshot.observedAt,
				&snapshot.count,
			); err != nil {
				rows.Close()
				t.Fatalf(
					"scan Catalog citation analysis snapshot for Work %s: %v",
					workID,
					err,
				)
			}
			snapshots = append(snapshots, snapshot)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf(
				"iterate Catalog citation analysis snapshots for Work %s: %v",
				workID,
				err,
			)
		}
		rows.Close()

		countState := "known"
		var currentSnapshotValue any
		var countValue any
		var baselineSnapshotValue any
		var citationVelocityValue any
		velocityState := "insufficient_evidence"
		var elapsedHours any
		supportingSnapshotIDs := make([]uuid.UUID, 0, len(snapshots))
		for _, snapshot := range snapshots {
			supportingSnapshotIDs = append(
				supportingSnapshotIDs,
				snapshot.id,
			)
		}
		missing := []string{"baseline_snapshot"}
		if len(snapshots) == 0 {
			countState = "missing"
			countValue = nil
			missing = []string{"baseline_snapshot", "current_snapshot"}
		} else {
			current := snapshots[len(snapshots)-1]
			currentSnapshotValue = current.id
			countValue = current.count
			startBoundary := asOf.Add(
				-time.Duration(options.materializedVelocityWindowDays) *
					24 * time.Hour,
			)
			var baseline *storedSnapshot
			for index := range snapshots {
				if snapshots[index].observedAt.After(startBoundary) {
					break
				}
				candidate := snapshots[index]
				baseline = &candidate
			}
			if baseline != nil && baseline.id != current.id {
				elapsed := current.observedAt.Sub(baseline.observedAt)
				elapsedDecimal := decimal.NewFromInt(elapsed.Nanoseconds())
				dayNanoseconds := decimal.NewFromInt(
					int64((24 * time.Hour).Nanoseconds()),
				)
				velocity := decimal.NewFromInt(
					current.count-baseline.count,
				).Mul(dayNanoseconds).DivRound(
					elapsedDecimal,
					ranking.DivisionScale,
				)
				hours := decimal.NewFromInt(
					elapsed.Milliseconds(),
				).Div(decimal.NewFromInt(int64(time.Hour / time.Millisecond)))
				baselineSnapshotValue = baseline.id
				citationVelocityValue = velocity.String()
				elapsedHours = hours.String()
				velocityState = "known"
				missing = []string{}
			}
		}
		rawEvidence, err := json.Marshal(map[string]any{
			"source":                  input.CitationSource,
			"as_of":                   asOf.Format(time.RFC3339Nano),
			"velocity_window_days":    options.materializedVelocityWindowDays,
			"current_snapshot_id":     currentSnapshotValue,
			"baseline_snapshot_id":    baselineSnapshotValue,
			"elapsed_hours":           elapsedHours,
			"missing_signals":         missing,
			"supporting_snapshot_ids": supportingSnapshotIDs,
			"scope": map[string]any{
				"jcr_metric_year":            input.JCRMetricYear,
				"jcr_import_receipt":         input.JCRImportReceipt,
				"venue_policy_name":          input.VenuePolicyName,
				"venue_policy_version":       input.VenuePolicyVersion,
				"eligibility_policy_version": input.EligibilityPolicyVersion,
				"subject_version":            input.SubjectVersion,
			},
		})
		if err != nil {
			t.Fatalf("encode Catalog citation Work evidence: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO citation_analysis_work_snapshots (
				analysis_run_id,
				work_id,
				source,
				as_of,
				velocity_window_days,
				citation_count_state,
				current_snapshot_id,
				citation_count,
				citation_velocity_state,
				baseline_snapshot_id,
				citation_velocity,
				source_revision,
				formula_version,
				evidence,
				generated_at
			) VALUES (
				$1, $2, $3, $4, $15, $5, $6, $7,
				$8, $9, $10::numeric, $11, $12, $13::jsonb, $14
			)
		`,
			input.CitationAnalysisRunID,
			workID,
			input.CitationSource,
			asOf,
			countState,
			currentSnapshotValue,
			countValue,
			velocityState,
			baselineSnapshotValue,
			citationVelocityValue,
			strings.Repeat("c", 64),
			citation.CitationIntelligenceFormulaVersion,
			rawEvidence,
			completedAt,
			options.materializedVelocityWindowDays,
		); err != nil {
			t.Fatalf(
				"insert Catalog citation Work analysis %s: %v",
				workID,
				err,
			)
		}
	}
	if options.materializedMinimumCohortSize != nil {
		for _, workID := range workIDs {
			insertCatalogCitationPercentileFixture(
				t,
				tx,
				input,
				workID,
				*options.materializedMinimumCohortSize,
				completedAt,
			)
		}
	}
	output, err := json.Marshal(map[string]any{
		"total_works": len(workIDs),
	})
	if err != nil {
		t.Fatalf("encode Catalog citation analysis output: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE analysis_runs
		SET status = 'succeeded',
		    output_payload = $2::jsonb,
		    completed_at = $3
		WHERE id = $1
	`, input.CitationAnalysisRunID, output, completedAt); err != nil {
		t.Fatalf("complete Catalog citation analysis fixture: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit Catalog citation analysis fixture: %v", err)
	}
}

func insertCatalogCitationPercentileFixture(
	t *testing.T,
	tx pgx.Tx,
	input PublishInput,
	workID uuid.UUID,
	minimumCohortSize int,
	generatedAt time.Time,
) {
	t.Helper()
	ctx := context.Background()
	var (
		subjectVersionID  uuid.UUID
		subjectID         uuid.UUID
		publicationTypeID uuid.UUID
		snapshotID        uuid.UUID
		citationCount     int64
		publicationYear   int
	)
	if err := tx.QueryRow(ctx, `
		SELECT
			eligibility.subject_version_id,
			rule.subject_id
		FROM biomedical_publication_eligibility_decisions AS eligibility
		JOIN journal_subject_metrics AS link
		  ON link.id = eligibility.journal_subject_metric_id
		JOIN biomedical_subject_rules AS rule
		  ON rule.id = link.subject_rule_id
		WHERE eligibility.work_id = $1
		  AND eligibility.decision = 'accepted'
	`, workID).Scan(&subjectVersionID, &subjectID); err != nil {
		t.Fatalf("query Catalog citation percentile Subject identity: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT publication_type_id
		FROM work_publication_types
		WHERE work_id = $1
		ORDER BY id
		LIMIT 1
	`, workID).Scan(&publicationTypeID); err != nil {
		t.Fatalf("query Catalog citation percentile Publication Type: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT id, count
		FROM citation_snapshots
		WHERE work_id = $1
		  AND source = $2
		ORDER BY observed_at DESC, id DESC
		LIMIT 1
	`, workID, input.CitationSource).Scan(
		&snapshotID,
		&citationCount,
	); err != nil {
		t.Fatalf("query Catalog citation percentile snapshot: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT EXTRACT(YEAR FROM published_at)::integer
		FROM works
		WHERE id = $1
	`, workID).Scan(&publicationYear); err != nil {
		t.Fatalf("query Catalog citation percentile publication year: %v", err)
	}
	cohortKey := fmt.Sprintf(
		"subject-version:%s:subject:%s:year:%d:publication-type:%s",
		subjectVersionID,
		subjectID,
		publicationYear,
		publicationTypeID,
	)
	rawEvidence, err := json.Marshal(map[string]any{
		"cohort": map[string]any{
			"subject_version_id":  subjectVersionID,
			"subject_id":          subjectID,
			"publication_year":    publicationYear,
			"publication_type_id": publicationTypeID,
		},
		"cohort_key":           cohortKey,
		"minimum_cohort_size":  minimumCohortSize,
		"supporting_work_ids":  []uuid.UUID{workID},
		"citation_snapshot_id": snapshotID,
		"scope": map[string]any{
			"subject_version": input.SubjectVersion,
		},
	})
	if err != nil {
		t.Fatalf("encode Catalog citation percentile evidence: %v", err)
	}
	revision := sha256.Sum256(rawEvidence)
	if _, err := tx.Exec(ctx, `
		INSERT INTO citation_analysis_percentiles (
			analysis_run_id,
			work_id,
			subject_version_id,
			subject_id,
			publication_year,
			publication_type_id,
			source,
			citation_snapshot_id,
			citation_count,
			cohort_key,
			cohort_size,
			minimum_cohort_size,
			percentile_state,
			source_revision,
			formula_version,
			evidence,
			generated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, 1, $11, 'insufficient_evidence', $12, $13,
			$14::jsonb, $15
		)
	`,
		input.CitationAnalysisRunID,
		workID,
		subjectVersionID,
		subjectID,
		publicationYear,
		publicationTypeID,
		input.CitationSource,
		snapshotID,
		citationCount,
		cohortKey,
		minimumCohortSize,
		hex.EncodeToString(revision[:]),
		citation.CitationIntelligenceFormulaVersion,
		rawEvidence,
		generatedAt,
	); err != nil {
		t.Fatalf("insert Catalog citation percentile fixture: %v", err)
	}
}

func insertCatalogSubjectVersion(
	t *testing.T,
	pool *pgxpool.Pool,
	version string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	versionID, rules := insertCatalogSubjectVersionCategories(
		t,
		pool,
		version,
		map[string]string{"oncology": "Oncology"},
	)
	return versionID, rules["Oncology"]
}

func insertCatalogSubjectVersionCategories(
	t *testing.T,
	pool *pgxpool.Pool,
	version string,
	categories map[string]string,
) (uuid.UUID, map[string]uuid.UUID) {
	t.Helper()
	if len(categories) == 0 {
		t.Fatal("Catalog Subject fixture requires at least one category")
	}
	receiptID := uuid.New()
	versionID := uuid.New()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Catalog Subject fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `
		INSERT INTO subject_import_receipts (
			id,
			source,
			registry_version,
			file_sha256,
			subject_count,
			rule_count,
			imported_at
		) VALUES (
			$1,
			'medpaperhub-reviewed-jcr-category-allowlist',
			$2,
			$3,
			$4,
			$4,
			$5
		)
	`,
		receiptID,
		version,
		strings.Repeat("a", 64),
		len(categories),
		time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("insert Catalog Subject receipt: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO subject_versions (
			id,
			subject_import_receipt_id,
			version_key
		) VALUES ($1, $2, $3)
	`, versionID, receiptID, version); err != nil {
		t.Fatalf("insert Catalog Subject version: %v", err)
	}
	rules := make(map[string]uuid.UUID, len(categories))
	slugs := make([]string, 0, len(categories))
	for slug := range categories {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		category := categories[slug]
		subjectID := uuid.New()
		ruleID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO subjects (
				id,
				subject_version_id,
				slug,
				display_label
			) VALUES ($1, $2, $3, $4)
		`, subjectID, versionID, slug, category); err != nil {
			t.Fatalf("insert Catalog Subject %q: %v", category, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO biomedical_subject_rules (
				id,
				subject_version_id,
				subject_id,
				jcr_category
			) VALUES ($1, $2, $3, $4)
		`,
			ruleID,
			versionID,
			subjectID,
			category,
		); err != nil {
			t.Fatalf("insert Catalog Subject rule %q: %v", category, err)
		}
		rules[category] = ruleID
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit Catalog Subject fixture: %v", err)
	}
	return versionID, rules
}

type catalogJCRVenueFixture struct {
	venueID         uuid.UUID
	index           int
	withSubject     bool
	primaryMetric   *catalogJCRMetricFixture
	primaryMetricID *uuid.UUID
	subjectLinkID   *uuid.UUID
	extraMetrics    []catalogJCRMetricFixture
}

type catalogJCRMetricFixture struct {
	category string
	jif      string
	quartile string
}

func insertCatalogJCRBundle(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	subjectRuleID uuid.UUID,
	fixtures []catalogJCRVenueFixture,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Catalog JCR fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	metricRows := len(fixtures)
	for _, fixture := range fixtures {
		metricRows += len(fixture.extraMetrics)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipts (
			id,
			file_sha256,
			source,
			imported_at,
			input_rows,
			inserted_rows,
			unchanged_rows
		) VALUES (
			$1,
			$2,
			'authorized-jcr',
			$3,
			$4,
			$4,
			0
		)
	`,
		input.JCRImportReceipt,
		strings.Repeat("b", 64),
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
		metricRows,
	); err != nil {
		t.Fatalf("insert Catalog JCR receipt: %v", err)
	}
	for _, fixture := range fixtures {
		primaryMetric := catalogJCRMetricFixture{
			category: "Oncology",
			jif:      "12",
			quartile: "Q1",
		}
		if fixture.primaryMetric != nil {
			primaryMetric = *fixture.primaryMetric
		}
		metrics := []catalogJCRMetricFixture{primaryMetric}
		metrics = append(metrics, fixture.extraMetrics...)
		for metricIndex, metric := range metrics {
			metricID := uuid.New()
			if metricIndex == 0 && fixture.primaryMetricID != nil {
				metricID = *fixture.primaryMetricID
			}
			if metric.category == "" ||
				metric.jif == "" ||
				metric.quartile == "" {
				t.Fatal("Catalog JCR metric fixture must be explicit")
			}
			if _, err := tx.Exec(ctx, `
			INSERT INTO venue_metric_snapshots (
				id,
				venue_id,
				metric_year,
				category,
				jif,
				quartile,
				metric_status,
				source_name,
				source_license,
				captured_at,
				jcr_import_receipt_id
			) VALUES (
				$1,
				$2,
				$3,
				$4,
				$5::numeric,
				$6,
				'known',
				'authorized-jcr',
				'institution-authorized',
				$7,
				$8
			)
		`,
				metricID,
				fixture.venueID,
				input.JCRMetricYear,
				metric.category,
				metric.jif,
				metric.quartile,
				input.GeneratedAt.Add(
					time.Duration(fixture.index)*time.Second+
						time.Duration(metricIndex)*time.Millisecond,
				),
				input.JCRImportReceipt,
			); err != nil {
				t.Fatalf("insert Catalog JCR metric: %v", err)
			}
			if _, err := tx.Exec(ctx, `
			INSERT INTO jcr_import_receipt_metrics (
				import_receipt_id,
				metric_snapshot_id
			) VALUES ($1, $2)
		`, input.JCRImportReceipt, metricID); err != nil {
				t.Fatalf("link Catalog JCR receipt metric: %v", err)
			}
			if fixture.withSubject && metricIndex == 0 {
				linkID := uuid.New()
				if fixture.subjectLinkID != nil {
					linkID = *fixture.subjectLinkID
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO journal_subject_metrics (
						id,
						venue_metric_snapshot_id,
						subject_rule_id,
						jcr_category
					) VALUES ($1, $2, $3, $4)
			`, linkID, metricID, subjectRuleID, metric.category); err != nil {
					t.Fatalf("link Catalog exact Subject: %v", err)
				}
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit Catalog JCR fixture: %v", err)
	}
}

func assessCatalogVenuePolicy(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
) {
	t.Helper()
	store, err := venue.NewPostgresAssessmentStore(pool)
	if err != nil {
		t.Fatalf("create Catalog Venue assessment store: %v", err)
	}
	service, err := venue.NewAssessmentService(store)
	if err != nil {
		t.Fatalf("create Catalog Venue assessment service: %v", err)
	}
	summary, err := service.Assess(
		context.Background(),
		venue.AssessmentInput{
			JCRImportReceiptID: input.JCRImportReceipt.String(),
			MetricYear:         input.JCRMetricYear,
			PolicyVersion:      venue.JournalJIFOrQ1PolicyVersion,
			AssessedAt:         input.GeneratedAt.Add(-time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("assess Catalog Venue policy: %v", err)
	}
	if summary.Accepted == 0 {
		t.Fatalf("Catalog Venue assessment summary = %#v, want accepted rows", summary)
	}
}

func insertCatalogVenueAssessment(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	venueID uuid.UUID,
	policyName string,
	policyVersion int,
	metricYear int,
	decision string,
) {
	t.Helper()
	matchedRules := `[]`
	reason := `"no_policy_rule_matched"`
	if decision == "accepted" {
		matchedRules = `["jif_gte_10","jcr_q1"]`
		reason = `null`
	}
	if decision == "unknown" {
		reason = `"missing_or_unknown_metric_evidence"`
	}
	if decision == "not_applicable" {
		reason = `"venue_type_not_journal"`
	}
	evidence := fmt.Sprintf(
		`{
			"jcr_import_receipt_id":%q,
			"policy_version":%q,
			"metric_year":%d,
			"venue_type":%q,
			"reason":%s,
			"categories":[{"category":"Oncology","jif":"12","quartile":"Q1","status":"known","source_name":"authorized-jcr"}]
		}`,
		input.JCRImportReceipt.String(),
		fmt.Sprintf("%s/v%d", policyName, policyVersion),
		metricYear,
		catalogVenueType(t, pool, venueID),
		reason,
	)
	insertCatalogVenueAssessmentPayload(
		t,
		pool,
		input,
		venueID,
		policyName,
		policyVersion,
		metricYear,
		decision,
		matchedRules,
		evidence,
	)
}

func insertCatalogVenueAssessmentPayload(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	venueID uuid.UUID,
	policyName string,
	policyVersion int,
	metricYear int,
	decision string,
	matchedRules string,
	evidence string,
) {
	t.Helper()
	policyID := uuid.New()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO venue_policy_versions (
			id,
			policy_name,
			version_number,
			definition,
			effective_at
		) VALUES ($1, $2, $3, '{"kind":"curation-gate-test"}', $4)
		ON CONFLICT (policy_name, version_number) DO NOTHING
	`,
		policyID,
		policyName,
		policyVersion,
		input.GeneratedAt.Add(-time.Hour),
	); err != nil {
		t.Fatalf("insert Catalog Venue policy: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `
		SELECT id
		FROM venue_policy_versions
		WHERE policy_name = $1
		  AND version_number = $2
	`, policyName, policyVersion).Scan(&policyID); err != nil {
		t.Fatalf("query Catalog Venue policy: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO venue_policy_assessments (
			venue_id,
			policy_version_id,
			metric_year,
			decision,
			matched_rules,
			evidence,
			assessed_at
		) VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7)
	`,
		venueID,
		policyID,
		metricYear,
		decision,
		matchedRules,
		evidence,
		input.GeneratedAt.Add(-time.Hour),
	); err != nil {
		t.Fatalf("insert Catalog Venue assessment: %v", err)
	}
}

func catalogWorkVenueID(
	t *testing.T,
	pool *pgxpool.Pool,
	workID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var venueID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		SELECT venue_id FROM works WHERE id = $1
	`, workID).Scan(&venueID); err != nil {
		t.Fatalf("query Work Venue: %v", err)
	}
	return venueID
}

func catalogVenueType(
	t *testing.T,
	pool *pgxpool.Pool,
	venueID uuid.UUID,
) string {
	t.Helper()
	var venueType string
	if err := pool.QueryRow(context.Background(), `
		SELECT venue_type FROM venues WHERE id = $1
	`, venueID).Scan(&venueType); err != nil {
		t.Fatalf("query Venue type: %v", err)
	}
	return venueType
}

func deterministicCatalogISSN(value int) string {
	digits := fmt.Sprintf("%07d", value%10_000_000)
	sum := 0
	for index := 0; index < 7; index++ {
		sum += int(digits[index]-'0') * (8 - index)
	}
	check := (11 - sum%11) % 11
	checkDigit := byte('0' + check)
	if check == 10 {
		checkDigit = 'X'
	}
	return digits[:4] + "-" + digits[4:] + string(checkDigit)
}
