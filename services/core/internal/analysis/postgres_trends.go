package analysis

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TrendAnalysisInput struct {
	AsOf                           time.Time
	FormulaVersion                 string
	ModelSelectionRule             TrendModelSelectionRule
	DispersionThreshold            float64
	SubjectVersion                 string
	EligibilityPolicyVersion       string
	JCRMetricYear                  int
	JCRImportReceipt               uuid.UUID
	VenuePolicyName                string
	VenuePolicyVersion             int
	RecentWindowDays               int
	BaselineWindowDays             int
	MinimumPaperCount              int
	MinimumIndependentJournalCount int
	MinimumIndependentTeamCount    int
}

type trendRunInputPayload struct {
	AsOf                           string    `json:"as_of"`
	FormulaVersion                 string    `json:"formula_version"`
	ModelSelectionRule             string    `json:"model_selection_rule"`
	DispersionThreshold            float64   `json:"dispersion_threshold"`
	SubjectVersion                 string    `json:"subject_version"`
	EligibilityPolicyVersion       string    `json:"eligibility_policy_version"`
	JCRMetricYear                  int       `json:"jcr_metric_year"`
	JCRImportReceipt               uuid.UUID `json:"jcr_import_receipt"`
	VenuePolicyName                string    `json:"venue_policy_name"`
	VenuePolicyVersion             int       `json:"venue_policy_version"`
	RecentWindowDays               int       `json:"recent_window_days"`
	BaselineWindowDays             int       `json:"baseline_window_days"`
	MinimumPaperCount              int       `json:"minimum_paper_count"`
	MinimumIndependentJournalCount int       `json:"minimum_independent_journal_count"`
	MinimumIndependentTeamCount    int       `json:"minimum_independent_team_count"`
	CohortRevision                 string    `json:"cohort_revision"`
}

type trendRunOutputPayload struct {
	CohortRevision string `json:"cohort_revision"`
	SnapshotCount  int    `json:"snapshot_count"`
}

type trendCandidate struct {
	EntityType      string
	EntityID        string
	RecentWorkIDs   map[uuid.UUID]struct{}
	BaselineWorkIDs map[uuid.UUID]struct{}
	JournalIDs      map[uuid.UUID]struct{}
	TeamIDs         map[uuid.UUID]struct{}
}

type PublicationTrendWindowBoundaries struct {
	RecentStart   time.Time
	RecentEnd     time.Time
	BaselineStart time.Time
	BaselineEnd   time.Time
}

func PublicationTrendCalendarWindowBoundaries(
	asOf time.Time,
	recentWindowDays int,
	baselineWindowDays int,
) (PublicationTrendWindowBoundaries, error) {
	if asOf.IsZero() {
		return PublicationTrendWindowBoundaries{}, errors.New(
			"publication trend window as_of is required",
		)
	}
	if recentWindowDays < 1 || baselineWindowDays <= recentWindowDays {
		return PublicationTrendWindowBoundaries{}, &InvalidInputError{
			Field:  "window_days",
			Reason: "baseline window must exceed recent window and both must be positive",
		}
	}
	asOf = asOf.UTC()
	calendarDate := time.Date(
		asOf.Year(),
		asOf.Month(),
		asOf.Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	)
	recentStart := calendarDate.AddDate(0, 0, -(recentWindowDays - 1))
	baselineEnd := recentStart
	return PublicationTrendWindowBoundaries{
		RecentStart: recentStart,
		RecentEnd:   asOf,
		BaselineStart: baselineEnd.AddDate(
			0,
			0,
			-(baselineWindowDays - recentWindowDays),
		),
		BaselineEnd: baselineEnd,
	}, nil
}

func (input TrendAnalysisInput) validate() error {
	if input.AsOf.IsZero() {
		return errors.New("publication trends as_of is required")
	}
	if input.FormulaVersion != PublicationTrendFormulaVersion {
		return fmt.Errorf("publication trends formula_version must equal %s", PublicationTrendFormulaVersion)
	}
	switch input.ModelSelectionRule {
	case TrendModelSelectionFixedPoisson,
		TrendModelSelectionFixedNegativeBinomial:
		if input.DispersionThreshold != 0 {
			return &InvalidInputError{Field: "dispersion_threshold", Reason: "must be zero for a fixed model selection"}
		}
	case TrendModelSelectionDispersionThreshold:
		if !positiveFinite(input.DispersionThreshold) {
			return &InvalidInputError{Field: "dispersion_threshold", Reason: "must be positive and finite"}
		}
	default:
		return &InvalidInputError{Field: "model_selection_rule", Reason: "must be one of the declared trend selection rules"}
	}
	if err := validateTrimmed("subject_version", input.SubjectVersion); err != nil {
		return err
	}
	if input.EligibilityPolicyVersion != "biomedical-public-eligibility/v1" {
		return &InvalidInputError{Field: "eligibility_policy_version", Reason: "must equal biomedical-public-eligibility/v1"}
	}
	if input.JCRMetricYear < 1900 || input.JCRMetricYear > 3000 {
		return &InvalidInputError{Field: "jcr_metric_year", Reason: "must be between 1900 and 3000"}
	}
	if input.JCRImportReceipt == uuid.Nil {
		return &InvalidInputError{Field: "jcr_import_receipt", Reason: "is required"}
	}
	if err := validateTrimmed("venue_policy_name", input.VenuePolicyName); err != nil {
		return err
	}
	if input.VenuePolicyVersion < 1 {
		return &InvalidInputError{Field: "venue_policy_version", Reason: "must be positive"}
	}
	if input.RecentWindowDays < 1 || input.BaselineWindowDays <= input.RecentWindowDays {
		return &InvalidInputError{Field: "window_days", Reason: "baseline window must exceed recent window and both must be positive"}
	}
	if input.MinimumPaperCount < 1 || input.MinimumIndependentJournalCount < 1 || input.MinimumIndependentTeamCount < 1 {
		return &InvalidInputError{Field: "minimum thresholds", Reason: "must be positive"}
	}
	return nil
}

func (service *PostgresAnalysisService) AnalyzePublicationTrends(
	ctx context.Context,
	input TrendAnalysisInput,
) (AnalysisRunSummary, error) {
	if ctx == nil {
		return AnalysisRunSummary{}, errors.New("analysis context is required")
	}
	if err := ctx.Err(); err != nil {
		return AnalysisRunSummary{}, err
	}
	if err := service.ensureInitialized(); err != nil {
		return AnalysisRunSummary{}, err
	}
	if err := input.validate(); err != nil {
		return AnalysisRunSummary{}, err
	}

	tx, err := beginSerializableTx(ctx, service.pool)
	if err != nil {
		return AnalysisRunSummary{}, fmt.Errorf("begin publication trends transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	subjectVersionID, err := loadSubjectVersionID(ctx, tx, input.SubjectVersion)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	if err := validateJCRReceiptScope(ctx, tx, input.JCRImportReceipt, input.JCRMetricYear); err != nil {
		return AnalysisRunSummary{}, err
	}
	works, err := loadAcceptedCohortWorks(ctx, tx, subjectVersionID, input.EligibilityPolicyVersion, input.JCRMetricYear)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	if len(works) == 0 {
		return AnalysisRunSummary{}, errors.New("publication trends require at least one accepted Work")
	}
	workIDs := make([]uuid.UUID, 0, len(works))
	for _, work := range works {
		workIDs = append(workIDs, work.WorkID)
	}
	facts, err := loadCohortRevisionFacts(ctx, tx, workIDs, cohortRevisionLoader{
		SubjectVersionID: subjectVersionID,
		MetricYear:       input.JCRMetricYear,
		PolicyVersion:    input.EligibilityPolicyVersion,
	})
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	cohortRevision, err := ComputeCohortRevision(facts)
	if err != nil {
		return AnalysisRunSummary{}, err
	}

	startedAt := service.currentTimeUTC()
	runID, err := insertRunningAnalysisRun(ctx, tx, "publication_trends", input.FormulaVersion, trendRunInputPayload{
		AsOf:                           input.AsOf.UTC().Format(time.RFC3339Nano),
		FormulaVersion:                 input.FormulaVersion,
		ModelSelectionRule:             string(input.ModelSelectionRule),
		DispersionThreshold:            input.DispersionThreshold,
		SubjectVersion:                 input.SubjectVersion,
		EligibilityPolicyVersion:       input.EligibilityPolicyVersion,
		JCRMetricYear:                  input.JCRMetricYear,
		JCRImportReceipt:               input.JCRImportReceipt,
		VenuePolicyName:                input.VenuePolicyName,
		VenuePolicyVersion:             input.VenuePolicyVersion,
		RecentWindowDays:               input.RecentWindowDays,
		BaselineWindowDays:             input.BaselineWindowDays,
		MinimumPaperCount:              input.MinimumPaperCount,
		MinimumIndependentJournalCount: input.MinimumIndependentJournalCount,
		MinimumIndependentTeamCount:    input.MinimumIndependentTeamCount,
		CohortRevision:                 cohortRevision,
	}, startedAt)
	if err != nil {
		return AnalysisRunSummary{}, err
	}

	subjectsByWork, err := loadAcceptedCohortSubjects(ctx, tx, workIDs, subjectVersionID, input.EligibilityPolicyVersion, input.JCRMetricYear)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	meshByWork, err := loadAcceptedCohortMeshDescriptors(ctx, tx, workIDs)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	publicationTypesByWork, err := loadAcceptedCohortPublicationTypes(ctx, tx, workIDs)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	methodsByWork, err := loadAcceptedCohortMethods(ctx, tx, workIDs)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	institutionsByWork, err := loadAcceptedCohortCorrespondingInstitutions(ctx, tx, workIDs)
	if err != nil {
		return AnalysisRunSummary{}, err
	}

	windows, err := PublicationTrendCalendarWindowBoundaries(
		input.AsOf,
		input.RecentWindowDays,
		input.BaselineWindowDays,
	)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	recentStart := windows.RecentStart
	recentEnd := windows.RecentEnd
	baselineStart := windows.BaselineStart
	baselineEnd := windows.BaselineEnd

	candidates := make(map[string]*trendCandidate)
	addCandidate := func(entityType, entityID string, workID uuid.UUID, venueID uuid.UUID, inRecent, inBaseline bool, institutionIDs []uuid.UUID) {
		key := entityType + "|" + entityID
		candidate, exists := candidates[key]
		if !exists {
			candidate = &trendCandidate{
				EntityType:      entityType,
				EntityID:        entityID,
				RecentWorkIDs:   make(map[uuid.UUID]struct{}),
				BaselineWorkIDs: make(map[uuid.UUID]struct{}),
				JournalIDs:      make(map[uuid.UUID]struct{}),
				TeamIDs:         make(map[uuid.UUID]struct{}),
			}
			candidates[key] = candidate
		}
		if inRecent {
			candidate.RecentWorkIDs[workID] = struct{}{}
		}
		if inBaseline {
			candidate.BaselineWorkIDs[workID] = struct{}{}
		}
		if venueID != uuid.Nil {
			candidate.JournalIDs[venueID] = struct{}{}
		}
		for _, institutionID := range institutionIDs {
			if institutionID != uuid.Nil {
				candidate.TeamIDs[institutionID] = struct{}{}
			}
		}
	}

	for _, work := range works {
		if work.PublishedAt.After(input.AsOf) {
			continue
		}
		inRecent := !work.PublishedAt.Before(recentStart) && !work.PublishedAt.After(recentEnd)
		inBaseline := !work.PublishedAt.Before(baselineStart) && work.PublishedAt.Before(baselineEnd)
		if !inRecent && !inBaseline {
			continue
		}
		institutionIDs := institutionsByWork[work.WorkID]
		for _, subject := range subjectsByWork[work.WorkID] {
			addCandidate("subject", subject.SubjectSlug, work.WorkID, work.VenueID, inRecent, inBaseline, institutionIDs)
		}
		for _, descriptor := range meshByWork[work.WorkID] {
			addCandidate("mesh_descriptor", descriptor.DescriptorUI, work.WorkID, work.VenueID, inRecent, inBaseline, institutionIDs)
		}
		for _, publicationType := range publicationTypesByWork[work.WorkID] {
			addCandidate("publication_type", publicationType.PublicationTypeUI, work.WorkID, work.VenueID, inRecent, inBaseline, institutionIDs)
		}
		for _, method := range methodsByWork[work.WorkID] {
			addCandidate("method", method.MethodName, work.WorkID, work.VenueID, inRecent, inBaseline, institutionIDs)
		}
		if work.VenueType == "journal" && work.VenueID != uuid.Nil {
			addCandidate("journal", work.VenueID.String(), work.WorkID, work.VenueID, inRecent, inBaseline, institutionIDs)
		}
	}
	if len(candidates) == 0 {
		return AnalysisRunSummary{}, errors.New("publication trends require at least one analyzable entity")
	}

	inputs := make([]PublicationTrendInput, 0, len(candidates))
	orderedCandidates := make([]*trendCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		orderedCandidates = append(orderedCandidates, candidate)
	}
	sortCandidates(orderedCandidates)
	for _, candidate := range orderedCandidates {
		recentPaperCount := len(candidate.RecentWorkIDs)
		baselinePaperCount := len(candidate.BaselineWorkIDs)
		if recentPaperCount == 0 && baselinePaperCount == 0 {
			continue
		}
		alpha := float64(recentPaperCount+1) / float64(baselinePaperCount+1)
		inputs = append(inputs, PublicationTrendInput{
			ID: candidate.EntityType + ":" + candidate.EntityID,
			Recent: TrendWindow{
				Start:      recentStart,
				End:        recentEnd,
				PaperCount: recentPaperCount,
			},
			Baseline: TrendWindow{
				Start:      baselineStart,
				End:        baselineEnd,
				PaperCount: baselinePaperCount,
			},
			IndependentJournalCount:    len(candidate.JournalIDs),
			IndependentTeamCount:       len(candidate.TeamIDs),
			PredeclaredDispersionAlpha: alpha,
		})
	}
	if len(inputs) == 0 {
		return AnalysisRunSummary{}, errors.New("publication trends require at least one populated candidate")
	}

	policy := PublicationTrendPolicy{
		FormulaVersion:             input.FormulaVersion,
		ConfidenceLevel:            confidenceLevel95,
		MinimumPapersPerWindow:     input.MinimumPaperCount,
		MinimumIndependentJournals: input.MinimumIndependentJournalCount,
		MinimumIndependentTeams:    input.MinimumIndependentTeamCount,
		ModelSelection: TrendModelSelection{
			Rule:                input.ModelSelectionRule,
			DispersionThreshold: input.DispersionThreshold,
		},
	}
	results, err := AnalyzePublicationTrends(inputs, policy)
	if err != nil {
		return AnalysisRunSummary{}, err
	}

	resultByID := make(map[string]PublicationTrend, len(results))
	for _, result := range results {
		resultByID[result.ID] = result
	}
	for _, candidate := range orderedCandidates {
		inputID := candidate.EntityType + ":" + candidate.EntityID
		result, exists := resultByID[inputID]
		if !exists {
			return AnalysisRunSummary{}, fmt.Errorf("publication trends missing result for %s", inputID)
		}
		if err := service.insertTrendSnapshot(
			ctx,
			tx,
			runID,
			input,
			windows,
			candidate,
			result,
			cohortRevision,
			startedAt,
		); err != nil {
			return AnalysisRunSummary{}, err
		}
	}
	if err := completeAnalysisRun(ctx, tx, runID, trendRunOutputPayload{CohortRevision: cohortRevision, SnapshotCount: len(results)}, startedAt); err != nil {
		return AnalysisRunSummary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AnalysisRunSummary{}, fmt.Errorf("commit publication trends run %s: %w", runID, err)
	}
	return AnalysisRunSummary{RunID: runID, SnapshotCount: len(results), SourceRevisions: []string{cohortRevision}}, nil
}

func (service *PostgresAnalysisService) insertTrendSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	runID uuid.UUID,
	input TrendAnalysisInput,
	windows PublicationTrendWindowBoundaries,
	candidate *trendCandidate,
	result PublicationTrend,
	cohortRevision string,
	generatedAt time.Time,
) error {
	for _, boundary := range []struct {
		name string
		got  time.Time
		want time.Time
	}{
		{name: "recent_window_start", got: result.Recent.Start, want: windows.RecentStart},
		{name: "recent_window_end", got: result.Recent.End, want: windows.RecentEnd},
		{name: "baseline_window_start", got: result.Baseline.Start, want: windows.BaselineStart},
		{name: "baseline_window_end", got: result.Baseline.End, want: windows.BaselineEnd},
	} {
		if !boundary.got.Equal(boundary.want) {
			return fmt.Errorf(
				"publication trend result %s %s = %s, want declared window boundary %s",
				result.ID,
				boundary.name,
				boundary.got.UTC().Format(time.RFC3339Nano),
				boundary.want.UTC().Format(time.RFC3339Nano),
			)
		}
	}
	evidence := map[string]any{
		"analysis_run_id":       runID,
		"as_of":                 input.AsOf.UTC().Format(time.RFC3339Nano),
		"recent_window_days":    input.RecentWindowDays,
		"baseline_window_days":  input.BaselineWindowDays,
		"recent_window_start":   result.Recent.Start.UTC().Format(time.RFC3339Nano),
		"recent_window_end":     result.Recent.End.UTC().Format(time.RFC3339Nano),
		"baseline_window_start": result.Baseline.Start.UTC().Format(time.RFC3339Nano),
		"baseline_window_end":   result.Baseline.End.UTC().Format(time.RFC3339Nano),
		"cohort_revision":       cohortRevision,
		"entity_type":           candidate.EntityType,
		"entity_id":             candidate.EntityID,
		"recent_work_count":     len(candidate.RecentWorkIDs),
		"baseline_work_count":   len(candidate.BaselineWorkIDs),
		"independent_journals":  len(candidate.JournalIDs),
		"independent_teams":     len(candidate.TeamIDs),
	}
	payload := map[string]any{
		"cohort_revision": cohortRevision,
		"result":          result,
	}
	rawPayload, err := marshalJSONObject(payload)
	if err != nil {
		return fmt.Errorf("encode publication trend payload: %w", err)
	}
	rawEvidence, err := marshalJSONObject(evidence)
	if err != nil {
		return fmt.Errorf("encode publication trend evidence: %w", err)
	}
	state := string(result.Status)
	if state == "" {
		state = string(AnalysisStatusInsufficientEvidence)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO publication_trend_snapshots (
			analysis_run_id,
			entity_type,
			entity_id,
			state,
			model,
			model_selection_rule,
			dispersion_threshold,
			predeclared_dispersion_alpha,
			recent_window_start,
			recent_window_end,
			recent_paper_count,
			recent_rate_per_day,
			baseline_window_start,
			baseline_window_end,
			baseline_paper_count,
			baseline_rate_per_day,
			independent_journal_count,
			independent_team_count,
			rate_ratio,
			confidence_level,
			confidence_interval_lower,
			confidence_interval_upper,
			p_value,
			adjusted_p_value,
			cohort_revision,
			formula_version,
			payload,
			evidence,
			generated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15, $16,
			$17, $18, $19, $20, $21, $22, $23, $24,
			$25, $26, $27::jsonb, $28::jsonb, $29
		)
	`,
		runID,
		candidate.EntityType,
		candidate.EntityID,
		state,
		string(result.Model),
		string(result.ModelSelection.Rule),
		input.DispersionThreshold,
		result.PredeclaredDispersionAlpha,
		result.Recent.Start,
		result.Recent.End,
		result.Recent.PaperCount,
		result.Recent.RatePerDay,
		result.Baseline.Start,
		result.Baseline.End,
		result.Baseline.PaperCount,
		result.Baseline.RatePerDay,
		result.IndependentJournalCount,
		result.IndependentTeamCount,
		nullableFloat64(result.RateRatio.Value),
		nullableFloat64(result.RateRatio.ConfidenceInterval.Level),
		nullableFloat64(result.RateRatio.ConfidenceInterval.Lower),
		nullableFloat64(result.RateRatio.ConfidenceInterval.Upper),
		nullableFloat64(result.RateRatio.PValue),
		nullableFloat64(result.RateRatio.AdjustedPValue),
		cohortRevision,
		input.FormulaVersion,
		rawPayload,
		rawEvidence,
		generatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert publication trend snapshot %s: %w", candidate.EntityID, err)
	}
	return nil
}

func trendWindowDuration(window TrendWindow) time.Duration {
	return window.End.Sub(window.Start)
}

func sortCandidates(candidates []*trendCandidate) {
	for i := 1; i < len(candidates); i++ {
		j := i
		for j > 0 && compareTrendCandidate(candidates[j-1], candidates[j]) > 0 {
			candidates[j-1], candidates[j] = candidates[j], candidates[j-1]
			j--
		}
	}
}

func compareTrendCandidate(left, right *trendCandidate) int {
	if left.EntityType != right.EntityType {
		if left.EntityType < right.EntityType {
			return -1
		}
		return 1
	}
	if left.EntityID < right.EntityID {
		return -1
	}
	if left.EntityID > right.EntityID {
		return 1
	}
	return 0
}
