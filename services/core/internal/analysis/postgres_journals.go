package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type JournalPatternAnalysisInput struct {
	AsOf                      time.Time
	FormulaVersion            string
	SubjectVersion            string
	EligibilityPolicyVersion  string
	JCRMetricYear             int
	JCRImportReceipt          uuid.UUID
	VenuePolicyName           string
	VenuePolicyVersion        int
	WindowDays                int
	MinimumSupportCount       int
	MinimumFieldBaselineCount int
	MinimumCoverage           float64
}

type journalRunInputPayload struct {
	AsOf                      string    `json:"as_of"`
	FormulaVersion            string    `json:"formula_version"`
	SubjectVersion            string    `json:"subject_version"`
	EligibilityPolicyVersion  string    `json:"eligibility_policy_version"`
	JCRMetricYear             int       `json:"jcr_metric_year"`
	JCRImportReceipt          uuid.UUID `json:"jcr_import_receipt"`
	VenuePolicyName           string    `json:"venue_policy_name"`
	VenuePolicyVersion        int       `json:"venue_policy_version"`
	WindowDays                int       `json:"window_days"`
	MinimumSupportCount       int       `json:"minimum_support_count"`
	MinimumFieldBaselineCount int       `json:"minimum_field_baseline_count"`
	CohortRevision            string    `json:"cohort_revision"`
}

type journalRunOutputPayload struct {
	CohortRevision string `json:"cohort_revision"`
	SnapshotCount  int    `json:"snapshot_count"`
}

type journalCandidate struct {
	PatternKey   string
	JournalID    uuid.UUID
	JournalLabel string
	SubjectID    uuid.UUID
	SubjectSlug  string
	FeatureType  string
	FeatureValue string
	Input        JournalPatternInput
	WorkIDs      map[uuid.UUID]struct{}
	FieldWorkIDs map[uuid.UUID]struct{}
}

func availableJournalFeatureTypes(
	mesh []cohortMeshDescriptor,
	publicationTypes []cohortPublicationType,
	methods []cohortMethod,
) []string {
	featureTypes := make([]string, 0, 3)
	if len(mesh) > 0 {
		featureTypes = append(featureTypes, "mesh")
	}
	if len(publicationTypes) > 0 {
		featureTypes = append(featureTypes, "publication_type")
	}
	if len(methods) > 0 {
		featureTypes = append(featureTypes, "method")
	}
	return featureTypes
}

func journalSubjectKey(journalID, subjectID uuid.UUID) string {
	return journalID.String() + "|" + subjectID.String()
}

func journalSubjectFeatureTypeKey(
	journalID, subjectID uuid.UUID,
	featureType string,
) string {
	return journalSubjectKey(journalID, subjectID) + "|" + featureType
}

func journalPatternInputCoverage(
	input JournalPatternInput,
) (Coverage, error) {
	return calculateCoverage(
		input.CoveredPaperCount,
		input.EligiblePaperCount,
	)
}

type journalPatternPopulation struct {
	JournalPaperCount       int
	FieldFeaturePaperCount  int
	FieldBaselinePaperCount int
	Coverage                Coverage
}

func journalPatternPopulationCounts(
	eligibleJournalPaperCount int,
	coveredJournalPaperCount int,
	journalFeaturePaperCount int,
	coveredFieldPaperCount int,
	fieldFeaturePaperCount int,
) (journalPatternPopulation, error) {
	coverage, err := calculateCoverage(
		coveredJournalPaperCount,
		eligibleJournalPaperCount,
	)
	if err != nil {
		return journalPatternPopulation{}, err
	}
	if journalFeaturePaperCount < 0 ||
		journalFeaturePaperCount > coveredJournalPaperCount {
		return journalPatternPopulation{}, &InvalidInputError{
			Field:  "journal_feature_paper_count",
			Reason: "must be covered by the journal classification population",
		}
	}
	if coveredFieldPaperCount < coveredJournalPaperCount {
		return journalPatternPopulation{}, &InvalidInputError{
			Field:  "field_baseline_paper_count",
			Reason: "cannot be smaller than the journal population it contains",
		}
	}
	if fieldFeaturePaperCount < journalFeaturePaperCount {
		return journalPatternPopulation{}, &InvalidInputError{
			Field:  "field_feature_paper_count",
			Reason: "cannot be smaller than the journal feature population it contains",
		}
	}
	comparisonPaperCount := coveredFieldPaperCount -
		coveredJournalPaperCount
	comparisonFeaturePaperCount := fieldFeaturePaperCount -
		journalFeaturePaperCount
	if comparisonFeaturePaperCount > comparisonPaperCount {
		return journalPatternPopulation{}, &InvalidInputError{
			Field:  "field_feature_paper_count",
			Reason: "cannot exceed the disjoint field comparison population",
		}
	}
	if comparisonPaperCount == 0 {
		_, insufficient := newInsufficientEvidence(
			"journal_editorial_pattern",
			[]string{"independent_field_baseline"},
		)
		return journalPatternPopulation{}, insufficient
	}
	return journalPatternPopulation{
		JournalPaperCount:       coveredJournalPaperCount,
		FieldFeaturePaperCount:  comparisonFeaturePaperCount,
		FieldBaselinePaperCount: comparisonPaperCount,
		Coverage:                coverage,
	}, nil
}

func (input JournalPatternAnalysisInput) validate() error {
	if input.AsOf.IsZero() {
		return errors.New("journal patterns as_of is required")
	}
	if input.FormulaVersion != JournalPatternFormulaVersion {
		return fmt.Errorf("journal patterns formula_version must equal %s", JournalPatternFormulaVersion)
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
	if input.WindowDays < 1 || input.WindowDays > 3650 {
		return &InvalidInputError{Field: "window_days", Reason: "must be between 1 and 3650"}
	}
	if input.MinimumSupportCount < 1 || input.MinimumFieldBaselineCount < input.MinimumSupportCount || input.MinimumCoverage < 0 || input.MinimumCoverage > 1 || !nonNegativeFinite(input.MinimumCoverage) {
		return &InvalidInputError{Field: "journal_pattern_policy", Reason: "has invalid thresholds"}
	}
	return nil
}

func (service *PostgresAnalysisService) AnalyzeJournalPatterns(
	ctx context.Context,
	input JournalPatternAnalysisInput,
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
		return AnalysisRunSummary{}, fmt.Errorf("begin journal patterns transaction: %w", err)
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
		return AnalysisRunSummary{}, errors.New("journal patterns require at least one accepted Work")
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
	runID, err := insertRunningAnalysisRun(ctx, tx, "journal_editorial_patterns", input.FormulaVersion, journalRunInputPayload{
		AsOf:                      input.AsOf.UTC().Format(time.RFC3339Nano),
		FormulaVersion:            input.FormulaVersion,
		SubjectVersion:            input.SubjectVersion,
		EligibilityPolicyVersion:  input.EligibilityPolicyVersion,
		JCRMetricYear:             input.JCRMetricYear,
		JCRImportReceipt:          input.JCRImportReceipt,
		VenuePolicyName:           input.VenuePolicyName,
		VenuePolicyVersion:        input.VenuePolicyVersion,
		WindowDays:                input.WindowDays,
		MinimumSupportCount:       input.MinimumSupportCount,
		MinimumFieldBaselineCount: input.MinimumFieldBaselineCount,
		CohortRevision:            cohortRevision,
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

	journalSubjectWorks := make(map[string]map[uuid.UUID]struct{})
	journalFeatureTypeCoveredWorks := make(
		map[string]map[uuid.UUID]struct{},
	)
	fieldFeatureTypeCoveredWorks := make(
		map[string]map[uuid.UUID]struct{},
	)
	journalFeatureWorks := make(map[string]map[uuid.UUID]struct{})
	featureFieldWorks := make(map[string]map[uuid.UUID]struct{})
	candidates := make(map[string]*journalCandidate)

	addWork := func(set map[uuid.UUID]struct{}, workID uuid.UUID) map[uuid.UUID]struct{} {
		if set == nil {
			set = make(map[uuid.UUID]struct{})
		}
		set[workID] = struct{}{}
		return set
	}
	addFeature := func(journalID uuid.UUID, subject cohortSubject, featureType, featureValue string, workID uuid.UUID) {
		if journalID == uuid.Nil || subject.SubjectID == uuid.Nil {
			return
		}
		patternKey := fmt.Sprintf("journal:%s|subject:%s|%s:%s", journalID, subject.SubjectSlug, featureType, featureValue)
		candidate, exists := candidates[patternKey]
		if !exists {
			candidate = &journalCandidate{
				PatternKey:   patternKey,
				JournalID:    journalID,
				JournalLabel: journalID.String(),
				SubjectID:    subject.SubjectID,
				SubjectSlug:  subject.SubjectSlug,
				FeatureType:  featureType,
				FeatureValue: featureValue,
				WorkIDs:      make(map[uuid.UUID]struct{}),
				FieldWorkIDs: make(map[uuid.UUID]struct{}),
			}
			candidates[patternKey] = candidate
		}
		candidate.WorkIDs = addWork(candidate.WorkIDs, workID)
		candidate.FieldWorkIDs = addWork(candidate.FieldWorkIDs, workID)
		journalFeatureWorks[patternKey] = addWork(journalFeatureWorks[patternKey], workID)
		featureFieldWorks[subject.SubjectID.String()+"|"+featureType+"|"+featureValue] = addWork(featureFieldWorks[subject.SubjectID.String()+"|"+featureType+"|"+featureValue], workID)
	}

	for _, work := range works {
		windowStart := input.AsOf.Add(
			-time.Duration(input.WindowDays) * 24 * time.Hour,
		)
		if work.PublishedAt.Before(windowStart) ||
			work.PublishedAt.After(input.AsOf) {
			continue
		}
		if work.VenueType != "journal" || work.VenueID == uuid.Nil {
			continue
		}
		if len(subjectsByWork[work.WorkID]) == 0 {
			continue
		}
		for _, subject := range subjectsByWork[work.WorkID] {
			journalSubject := journalSubjectKey(
				work.VenueID,
				subject.SubjectID,
			)
			journalSubjectWorks[journalSubject] = addWork(
				journalSubjectWorks[journalSubject],
				work.WorkID,
			)
			for _, featureType := range availableJournalFeatureTypes(
				meshByWork[work.WorkID],
				publicationTypesByWork[work.WorkID],
				methodsByWork[work.WorkID],
			) {
				coverageKey := journalSubjectFeatureTypeKey(
					work.VenueID,
					subject.SubjectID,
					featureType,
				)
				journalFeatureTypeCoveredWorks[coverageKey] = addWork(
					journalFeatureTypeCoveredWorks[coverageKey],
					work.WorkID,
				)
				fieldCoverageKey := subject.SubjectID.String() +
					"|" + featureType
				fieldFeatureTypeCoveredWorks[fieldCoverageKey] = addWork(
					fieldFeatureTypeCoveredWorks[fieldCoverageKey],
					work.WorkID,
				)
			}
			for _, descriptor := range meshByWork[work.WorkID] {
				addFeature(work.VenueID, subject, "mesh", descriptor.DescriptorLbl, work.WorkID)
			}
			for _, publicationType := range publicationTypesByWork[work.WorkID] {
				addFeature(work.VenueID, subject, "publication_type", publicationType.PublicationTypeLbl, work.WorkID)
			}
			for _, method := range methodsByWork[work.WorkID] {
				addFeature(work.VenueID, subject, "method", method.MethodName, work.WorkID)
			}
		}
	}
	if len(candidates) == 0 {
		return AnalysisRunSummary{}, errors.New("journal patterns require at least one analyzable journal pattern")
	}

	inputs := make([]JournalPatternInput, 0, len(candidates))
	ordered := make([]*journalCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].JournalID != ordered[right].JournalID {
			return ordered[left].JournalID.String() < ordered[right].JournalID.String()
		}
		if ordered[left].SubjectSlug != ordered[right].SubjectSlug {
			return ordered[left].SubjectSlug < ordered[right].SubjectSlug
		}
		if ordered[left].FeatureType != ordered[right].FeatureType {
			return ordered[left].FeatureType < ordered[right].FeatureType
		}
		return ordered[left].FeatureValue < ordered[right].FeatureValue
	})
	populated := make([]*journalCandidate, 0, len(ordered))
	for _, candidate := range ordered {
		eligibleJournalPaperCount := len(
			journalSubjectWorks[journalSubjectKey(
				candidate.JournalID,
				candidate.SubjectID,
			)],
		)
		journalFeaturePaperCount := len(journalFeatureWorks[candidate.PatternKey])
		fieldFeaturePaperCount := len(featureFieldWorks[candidate.SubjectID.String()+"|"+candidate.FeatureType+"|"+candidate.FeatureValue])
		coveredPaperCount := len(
			journalFeatureTypeCoveredWorks[journalSubjectFeatureTypeKey(
				candidate.JournalID,
				candidate.SubjectID,
				candidate.FeatureType,
			)],
		)
		fieldCoveredPaperCount := len(
			fieldFeatureTypeCoveredWorks[candidate.SubjectID.String()+"|"+candidate.FeatureType],
		)
		if eligibleJournalPaperCount == 0 {
			continue
		}
		population, err := journalPatternPopulationCounts(
			eligibleJournalPaperCount,
			coveredPaperCount,
			journalFeaturePaperCount,
			fieldCoveredPaperCount,
			fieldFeaturePaperCount,
		)
		if err != nil {
			var insufficient *InsufficientEvidenceError
			if errors.As(err, &insufficient) {
				continue
			}
			return AnalysisRunSummary{}, fmt.Errorf(
				"prepare journal pattern population %s: %w",
				candidate.PatternKey,
				err,
			)
		}
		candidate.Input = JournalPatternInput{
			ID:                       candidate.PatternKey,
			JournalID:                candidate.JournalLabel,
			FieldID:                  candidate.SubjectSlug,
			FeatureType:              candidate.FeatureType,
			FeatureValue:             candidate.FeatureValue,
			Measure:                  JournalPatternOddsRatio,
			JournalFeaturePaperCount: journalFeaturePaperCount,
			JournalPaperCount:        population.JournalPaperCount,
			FieldFeaturePaperCount:   population.FieldFeaturePaperCount,
			FieldBaselinePaperCount:  population.FieldBaselinePaperCount,
			CoveredPaperCount:        coveredPaperCount,
			EligiblePaperCount:       eligibleJournalPaperCount,
		}
		inputs = append(inputs, candidate.Input)
		populated = append(populated, candidate)
	}
	if len(inputs) == 0 {
		return AnalysisRunSummary{}, errors.New("journal patterns require at least one populated candidate")
	}
	ordered = populated

	policy := JournalPatternPolicy{
		FormulaVersion:            input.FormulaVersion,
		ConfidenceLevel:           confidenceLevel95,
		MinimumSupportPaperCount:  input.MinimumSupportCount,
		MinimumFieldBaselineCount: input.MinimumFieldBaselineCount,
		MinimumCoverage:           input.MinimumCoverage,
	}
	results, err := AnalyzeJournalPatterns(inputs, policy)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	resultByID := make(map[string]JournalPattern, len(results))
	for _, result := range results {
		resultByID[result.ID] = result
	}

	snapshotCount := 0
	for _, candidate := range ordered {
		result, exists := resultByID[candidate.PatternKey]
		state := string(AnalysisStatusInsufficientEvidence)
		if exists {
			state = string(AnalysisStatusSufficientEvidence)
		} else {
			insufficientResult, insufficientErr := AnalyzeJournalPattern(candidate.Input, policy)
			if insufficientErr != nil {
				var typed *InsufficientEvidenceError
				if !errors.As(insufficientErr, &typed) {
					return AnalysisRunSummary{}, insufficientErr
				}
				if typed.Status == AnalysisStatusInsufficientEvidence {
					result = insufficientResult
				}
			}
		}
		if err := service.insertJournalSnapshot(ctx, tx, runID, subjectVersionID, input, candidate, result, state, cohortRevision, startedAt); err != nil {
			return AnalysisRunSummary{}, err
		}
		snapshotCount++
	}
	if err := completeAnalysisRun(ctx, tx, runID, journalRunOutputPayload{CohortRevision: cohortRevision, SnapshotCount: snapshotCount}, startedAt); err != nil {
		return AnalysisRunSummary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AnalysisRunSummary{}, fmt.Errorf("commit journal patterns run %s: %w", runID, err)
	}
	return AnalysisRunSummary{RunID: runID, SnapshotCount: snapshotCount, SourceRevisions: []string{cohortRevision}}, nil
}

func (service *PostgresAnalysisService) insertJournalSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	runID uuid.UUID,
	subjectVersionID uuid.UUID,
	input JournalPatternAnalysisInput,
	candidate *journalCandidate,
	result JournalPattern,
	state string,
	cohortRevision string,
	generatedAt time.Time,
) error {
	evidence := map[string]any{
		"cohort_revision":    cohortRevision,
		"pattern_key":        candidate.PatternKey,
		"journal_id":         candidate.JournalID,
		"subject_id":         candidate.SubjectID,
		"feature_type":       candidate.FeatureType,
		"feature_value":      candidate.FeatureValue,
		"journal_work_count": len(candidate.WorkIDs),
		"field_work_count":   len(candidate.FieldWorkIDs),
	}
	payload := map[string]any{
		"cohort_revision": cohortRevision,
		"pattern_key":     candidate.PatternKey,
		"state":           state,
	}
	if state == string(AnalysisStatusSufficientEvidence) {
		payload["result"] = result
	}
	rawPayload, err := marshalJSONObject(payload)
	if err != nil {
		return fmt.Errorf("encode journal pattern payload: %w", err)
	}
	rawEvidence, err := marshalJSONObject(evidence)
	if err != nil {
		return fmt.Errorf("encode journal pattern evidence: %w", err)
	}
	var effectValue, ciLower, ciUpper, pValue, adjustedPValue *float64
	if state == string(AnalysisStatusSufficientEvidence) {
		effectValue = &result.Effect.Value
		ciLower = &result.Effect.ConfidenceInterval.Lower
		ciUpper = &result.Effect.ConfidenceInterval.Upper
		pValue = &result.Effect.PValue
		adjustedPValue = &result.Effect.AdjustedPValue
	}
	journalExposure := (*float64)(nil)
	fieldExposure := (*float64)(nil)
	confidenceLevel := (*float64)(nil)
	if state == string(AnalysisStatusSufficientEvidence) {
		confidenceLevel = &result.Effect.ConfidenceInterval.Level
	}
	coverage, err := journalPatternInputCoverage(candidate.Input)
	if err != nil {
		return fmt.Errorf(
			"calculate journal pattern coverage %s: %w",
			candidate.PatternKey,
			err,
		)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO journal_pattern_snapshots (
			analysis_run_id,
			pattern_key,
			journal_id,
			subject_version_id,
			subject_id,
			pattern_kind,
			feature_type,
			feature_value,
			state,
			measure,
			journal_feature_paper_count,
			journal_paper_count,
			field_feature_paper_count,
			field_baseline_paper_count,
			journal_exposure,
			field_baseline_exposure,
			covered_paper_count,
			eligible_paper_count,
			coverage,
			effect_value,
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
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19,
			$20, $21, $22, $23, $24, $25, $26, $27,
			$28::jsonb, $29::jsonb, $30
		)
	`,
		runID,
		candidate.PatternKey,
		candidate.JournalID,
		subjectVersionID,
		candidate.SubjectID,
		string(EditorialPatternKind),
		candidate.FeatureType,
		candidate.FeatureValue,
		state,
		string(candidate.Input.Measure),
		candidate.Input.JournalFeaturePaperCount,
		candidate.Input.JournalPaperCount,
		candidate.Input.FieldFeaturePaperCount,
		candidate.Input.FieldBaselinePaperCount,
		journalExposure,
		fieldExposure,
		candidate.Input.CoveredPaperCount,
		candidate.Input.EligiblePaperCount,
		coverage.Proportion,
		effectValue,
		confidenceLevel,
		ciLower,
		ciUpper,
		pValue,
		adjustedPValue,
		cohortRevision,
		input.FormulaVersion,
		rawPayload,
		rawEvidence,
		generatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert journal pattern snapshot %s: %w", candidate.PatternKey, err)
	}
	return nil
}

func stringsJoin(parts []string, separator string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for index := 1; index < len(parts); index++ {
		result += separator + parts[index]
	}
	return result
}

func isObservationalPublicationType(label string) bool {
	normalized := strings.ToLower(strings.TrimSpace(label))
	return strings.Contains(normalized, "observational") || strings.Contains(normalized, "cohort") || strings.Contains(normalized, "case control") || strings.Contains(normalized, "case-control")
}

func isRCTPublicationType(label string) bool {
	normalized := strings.ToLower(strings.TrimSpace(label))
	return strings.Contains(normalized, "random") || strings.Contains(normalized, "clinical trial")
}
