package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type publishedResearchOpportunity struct {
	ID      uuid.UUID       `json:"id"`
	Status  string          `json:"status"`
	Ordinal int             `json:"ordinal"`
	Payload json.RawMessage `json:"payload"`
}

type persistedPublicationTrendSnapshot struct {
	EntityType              string
	EntityID                string
	State                   string
	Model                   string
	RecentPaperCount        int64
	BaselinePaperCount      int64
	IndependentJournalCount int
	IndependentTeamCount    int
	RateRatio               *string
	ConfidenceIntervalLower *string
	ConfidenceIntervalUpper *string
	PValue                  *string
	AdjustedPValue          *string
	CohortRevision          string
	FormulaVersion          string
	GeneratedAt             time.Time
}

type persistedJournalPatternSnapshot struct {
	PatternKey               string
	JournalID                uuid.UUID
	SubjectID                uuid.UUID
	FeatureType              string
	FeatureValue             string
	State                    string
	Measure                  string
	JournalFeaturePaperCount int64
	FieldBaselinePaperCount  int64
	Coverage                 string
	EffectValue              *string
	ConfidenceIntervalLower  *string
	ConfidenceIntervalUpper  *string
	PValue                   *string
	AdjustedPValue           *string
	CohortRevision           string
	FormulaVersion           string
	GeneratedAt              time.Time
}

type persistedResearchOpportunitySnapshot struct {
	ID                               uuid.UUID
	Rule                             string
	RuleVersion                      string
	EntityType                       string
	EntityID                         string
	State                            string
	SupportingWorkCount              int
	Coverage                         string
	PrimaryMetric                    string
	PrimaryEstimate                  string
	PrimaryConfidenceIntervalLower   string
	PrimaryConfidenceIntervalUpper   string
	SecondaryMetric                  *string
	SecondaryEstimate                *string
	SecondaryConfidenceIntervalLower *string
	SecondaryConfidenceIntervalUpper *string
	Limitations                      []string
	CohortRevision                   string
	GeneratedAt                      time.Time
	SupportingWorkIDs                []uuid.UUID
}

func publishBiomedicalAnalysisSnapshots(
	ctx context.Context,
	tx pgx.Tx,
	runs validatedBiomedicalAnalysisRuns,
	papers []publishedPaper,
	home json.RawMessage,
	subjects []publishedBiomedicalResource,
	journals []publishedBiomedicalResource,
) (
	json.RawMessage,
	[]publishedBiomedicalResource,
	[]publishedBiomedicalResource,
	[]publishedResearchOpportunity,
	error,
) {
	trends, err := loadPersistedPublicationTrends(ctx, tx, runs.Trend)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	patterns, err := loadPersistedJournalPatterns(ctx, tx, runs.Journal)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	opportunitySnapshots, err := loadPersistedResearchOpportunities(
		ctx,
		tx,
		runs.Opportunity,
		papers,
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	opportunities, err := buildPublishedResearchOpportunities(
		runs.Opportunity,
		opportunitySnapshots,
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	home, subjects, journals, err = applyPersistedBiomedicalAnalysis(
		runs,
		trends,
		patterns,
		opportunities,
		home,
		subjects,
		journals,
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return home, subjects, journals, opportunities, nil
}

func loadPersistedPublicationTrends(
	ctx context.Context,
	tx pgx.Tx,
	run persistedBiomedicalAnalysisRun,
) ([]persistedPublicationTrendSnapshot, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			entity_type,
			entity_id,
			state,
			model,
			recent_paper_count,
			baseline_paper_count,
			independent_journal_count,
			independent_team_count,
			rate_ratio::text,
			confidence_interval_lower::text,
			confidence_interval_upper::text,
			p_value::text,
			adjusted_p_value::text,
			cohort_revision,
			formula_version,
			generated_at
		FROM publication_trend_snapshots
		WHERE analysis_run_id = $1
		ORDER BY entity_type, entity_id, id
	`, run.ID)
	if err != nil {
		return nil, fmt.Errorf(
			"query publication trend snapshots for run %s: %w",
			run.ID,
			err,
		)
	}
	defer rows.Close()

	snapshots := make([]persistedPublicationTrendSnapshot, 0)
	for rows.Next() {
		var snapshot persistedPublicationTrendSnapshot
		if err := rows.Scan(
			&snapshot.EntityType,
			&snapshot.EntityID,
			&snapshot.State,
			&snapshot.Model,
			&snapshot.RecentPaperCount,
			&snapshot.BaselinePaperCount,
			&snapshot.IndependentJournalCount,
			&snapshot.IndependentTeamCount,
			&snapshot.RateRatio,
			&snapshot.ConfidenceIntervalLower,
			&snapshot.ConfidenceIntervalUpper,
			&snapshot.PValue,
			&snapshot.AdjustedPValue,
			&snapshot.CohortRevision,
			&snapshot.FormulaVersion,
			&snapshot.GeneratedAt,
		); err != nil {
			return nil, fmt.Errorf(
				"scan publication trend snapshot for run %s: %w",
				run.ID,
				err,
			)
		}
		if err := validatePersistedSnapshotBinding(
			"publication trend",
			run,
			snapshot.CohortRevision,
			snapshot.FormulaVersion,
			snapshot.GeneratedAt,
		); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate publication trend snapshots for run %s: %w",
			run.ID,
			err,
		)
	}
	if len(snapshots) == 0 {
		return nil, fmt.Errorf(
			"%w: publication trend analysis run %s has no materialized snapshots",
			ErrCatalogNotReady,
			run.ID,
		)
	}
	return snapshots, nil
}

func loadPersistedJournalPatterns(
	ctx context.Context,
	tx pgx.Tx,
	run persistedBiomedicalAnalysisRun,
) ([]persistedJournalPatternSnapshot, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			pattern_key,
			journal_id,
			subject_id,
			feature_type,
			feature_value,
			state,
			measure,
			journal_feature_paper_count,
			field_baseline_paper_count,
			coverage::text,
			effect_value::text,
			confidence_interval_lower::text,
			confidence_interval_upper::text,
			p_value::text,
			adjusted_p_value::text,
			cohort_revision,
			formula_version,
			generated_at
		FROM journal_pattern_snapshots
		WHERE analysis_run_id = $1
		ORDER BY journal_id, pattern_key, id
	`, run.ID)
	if err != nil {
		return nil, fmt.Errorf(
			"query journal pattern snapshots for run %s: %w",
			run.ID,
			err,
		)
	}
	defer rows.Close()

	snapshots := make([]persistedJournalPatternSnapshot, 0)
	for rows.Next() {
		var snapshot persistedJournalPatternSnapshot
		if err := rows.Scan(
			&snapshot.PatternKey,
			&snapshot.JournalID,
			&snapshot.SubjectID,
			&snapshot.FeatureType,
			&snapshot.FeatureValue,
			&snapshot.State,
			&snapshot.Measure,
			&snapshot.JournalFeaturePaperCount,
			&snapshot.FieldBaselinePaperCount,
			&snapshot.Coverage,
			&snapshot.EffectValue,
			&snapshot.ConfidenceIntervalLower,
			&snapshot.ConfidenceIntervalUpper,
			&snapshot.PValue,
			&snapshot.AdjustedPValue,
			&snapshot.CohortRevision,
			&snapshot.FormulaVersion,
			&snapshot.GeneratedAt,
		); err != nil {
			return nil, fmt.Errorf(
				"scan journal pattern snapshot for run %s: %w",
				run.ID,
				err,
			)
		}
		if err := validatePersistedSnapshotBinding(
			"journal pattern",
			run,
			snapshot.CohortRevision,
			snapshot.FormulaVersion,
			snapshot.GeneratedAt,
		); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate journal pattern snapshots for run %s: %w",
			run.ID,
			err,
		)
	}
	if len(snapshots) == 0 {
		return nil, fmt.Errorf(
			"%w: journal analysis run %s has no materialized snapshots",
			ErrCatalogNotReady,
			run.ID,
		)
	}
	return snapshots, nil
}

func loadPersistedResearchOpportunities(
	ctx context.Context,
	tx pgx.Tx,
	run persistedBiomedicalAnalysisRun,
	papers []publishedPaper,
) ([]persistedResearchOpportunitySnapshot, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			id,
			rule,
			rule_version,
			entity_type,
			entity_id,
			state,
			supporting_work_count,
			coverage::text,
			primary_metric,
			primary_estimate::text,
			primary_confidence_interval_lower::text,
			primary_confidence_interval_upper::text,
			secondary_metric,
			secondary_estimate::text,
			secondary_confidence_interval_lower::text,
			secondary_confidence_interval_upper::text,
			limitations,
			cohort_revision,
			generated_at
		FROM research_opportunity_snapshots
		WHERE analysis_run_id = $1
		ORDER BY
			CASE state
				WHEN 'triggered' THEN 1
				WHEN 'not_triggered' THEN 2
				ELSE 3
			END,
			rule,
			coverage DESC,
			entity_type,
			entity_id,
			id
	`, run.ID)
	if err != nil {
		return nil, fmt.Errorf(
			"query research opportunity snapshots for run %s: %w",
			run.ID,
			err,
		)
	}
	defer rows.Close()

	snapshots := make([]persistedResearchOpportunitySnapshot, 0)
	byID := make(map[uuid.UUID]int)
	for rows.Next() {
		var snapshot persistedResearchOpportunitySnapshot
		if err := rows.Scan(
			&snapshot.ID,
			&snapshot.Rule,
			&snapshot.RuleVersion,
			&snapshot.EntityType,
			&snapshot.EntityID,
			&snapshot.State,
			&snapshot.SupportingWorkCount,
			&snapshot.Coverage,
			&snapshot.PrimaryMetric,
			&snapshot.PrimaryEstimate,
			&snapshot.PrimaryConfidenceIntervalLower,
			&snapshot.PrimaryConfidenceIntervalUpper,
			&snapshot.SecondaryMetric,
			&snapshot.SecondaryEstimate,
			&snapshot.SecondaryConfidenceIntervalLower,
			&snapshot.SecondaryConfidenceIntervalUpper,
			&snapshot.Limitations,
			&snapshot.CohortRevision,
			&snapshot.GeneratedAt,
		); err != nil {
			return nil, fmt.Errorf(
				"scan research opportunity snapshot for run %s: %w",
				run.ID,
				err,
			)
		}
		if err := validatePersistedSnapshotBinding(
			"research opportunity",
			run,
			snapshot.CohortRevision,
			run.FormulaVersion,
			snapshot.GeneratedAt,
		); err != nil {
			return nil, err
		}
		if snapshot.ID == uuid.Nil || snapshot.RuleVersion == "" {
			return nil, fmt.Errorf(
				"%w: research opportunity run %s contains an invalid snapshot identity",
				ErrCatalogNotReady,
				run.ID,
			)
		}
		byID[snapshot.ID] = len(snapshots)
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate research opportunity snapshots for run %s: %w",
			run.ID,
			err,
		)
	}
	if len(snapshots) == 0 {
		return nil, fmt.Errorf(
			"%w: opportunity analysis run %s has no materialized snapshots",
			ErrCatalogNotReady,
			run.ID,
		)
	}

	supportRows, err := tx.Query(ctx, `
		SELECT
			research_opportunity_snapshot_id,
			work_id,
			ordinal
		FROM research_opportunity_supporting_works
		WHERE analysis_run_id = $1
		ORDER BY research_opportunity_snapshot_id, ordinal
	`, run.ID)
	if err != nil {
		return nil, fmt.Errorf(
			"query research opportunity supporting Works for run %s: %w",
			run.ID,
			err,
		)
	}
	defer supportRows.Close()

	publishedWorkIDs := make(map[uuid.UUID]struct{}, len(papers))
	for _, paper := range papers {
		publishedWorkIDs[paper.ID] = struct{}{}
	}
	for supportRows.Next() {
		var (
			snapshotID uuid.UUID
			workID     uuid.UUID
			ordinal    int
		)
		if err := supportRows.Scan(&snapshotID, &workID, &ordinal); err != nil {
			return nil, fmt.Errorf(
				"scan research opportunity supporting Work for run %s: %w",
				run.ID,
				err,
			)
		}
		index, found := byID[snapshotID]
		if !found || ordinal != len(snapshots[index].SupportingWorkIDs)+1 {
			return nil, fmt.Errorf(
				"%w: research opportunity run %s has invalid supporting Work ordering",
				ErrCatalogNotReady,
				run.ID,
			)
		}
		if _, found := publishedWorkIDs[workID]; !found {
			return nil, fmt.Errorf(
				"%w: research opportunity run %s references Work %s outside the published cohort",
				ErrCatalogNotReady,
				run.ID,
				workID,
			)
		}
		snapshots[index].SupportingWorkIDs = append(
			snapshots[index].SupportingWorkIDs,
			workID,
		)
	}
	if err := supportRows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate research opportunity supporting Works for run %s: %w",
			run.ID,
			err,
		)
	}
	for _, snapshot := range snapshots {
		if snapshot.SupportingWorkCount != len(snapshot.SupportingWorkIDs) ||
			len(snapshot.SupportingWorkIDs) == 0 {
			return nil, fmt.Errorf(
				"%w: research opportunity %s has invalid supporting Work coverage",
				ErrCatalogNotReady,
				snapshot.ID,
			)
		}
	}
	return snapshots, nil
}

func validatePersistedSnapshotBinding(
	kind string,
	run persistedBiomedicalAnalysisRun,
	cohortRevision string,
	formulaVersion string,
	generatedAt time.Time,
) error {
	if cohortRevision != run.CohortRevision {
		return fmt.Errorf(
			"%w: %s snapshot for run %s has a cohort revision outside the validated analysis run",
			ErrCatalogNotReady,
			kind,
			run.ID,
		)
	}
	if formulaVersion != run.FormulaVersion ||
		generatedAt.IsZero() ||
		generatedAt.After(run.GeneratedAt) {
		return fmt.Errorf(
			"%w: %s snapshot for run %s is outside the validated analysis run boundaries",
			ErrCatalogNotReady,
			kind,
			run.ID,
		)
	}
	return nil
}

func buildPublishedResearchOpportunities(
	run persistedBiomedicalAnalysisRun,
	snapshots []persistedResearchOpportunitySnapshot,
) ([]publishedResearchOpportunity, error) {
	opportunities := make([]publishedResearchOpportunity, 0, len(snapshots))
	for index, snapshot := range snapshots {
		status, err := publicOpportunityStatus(snapshot.State)
		if err != nil {
			return nil, err
		}
		coverage, err := parsePersistedNumber(
			snapshot.Coverage,
			"research opportunity coverage",
		)
		if err != nil {
			return nil, err
		}
		primary, err := persistedOpportunityEstimate(
			snapshot.PrimaryMetric,
			snapshot.PrimaryEstimate,
			snapshot.PrimaryConfidenceIntervalLower,
			snapshot.PrimaryConfidenceIntervalUpper,
		)
		if err != nil {
			return nil, err
		}
		estimates := []map[string]any{primary}
		if snapshot.SecondaryMetric != nil {
			if snapshot.SecondaryEstimate == nil ||
				snapshot.SecondaryConfidenceIntervalLower == nil ||
				snapshot.SecondaryConfidenceIntervalUpper == nil {
				return nil, fmt.Errorf(
					"%w: research opportunity %s has incomplete secondary estimate",
					ErrCatalogNotReady,
					snapshot.ID,
				)
			}
			secondary, err := persistedOpportunityEstimate(
				*snapshot.SecondaryMetric,
				*snapshot.SecondaryEstimate,
				*snapshot.SecondaryConfidenceIntervalLower,
				*snapshot.SecondaryConfidenceIntervalUpper,
			)
			if err != nil {
				return nil, err
			}
			estimates = append(estimates, secondary)
		}
		payload, err := marshalCatalogPayload(map[string]any{
			"analysis_run_id":     run.ID,
			"coverage_ratio":      catalogValue{State: "known", Value: coverage},
			"estimates":           estimates,
			"formula_version":     run.FormulaVersion,
			"generated_at":        snapshot.GeneratedAt.UTC().Format(time.RFC3339Nano),
			"id":                  snapshot.ID,
			"limitations":         snapshot.Limitations,
			"status":              status,
			"supporting_work_ids": snapshot.SupportingWorkIDs,
			"target_id":           snapshot.EntityID,
			"target_kind":         snapshot.EntityType,
			"title":               researchOpportunityTitle(snapshot.Rule, snapshot.EntityID),
			"trigger_rule": map[string]any{
				"code":    snapshot.Rule,
				"version": snapshot.RuleVersion,
			},
		})
		if err != nil {
			return nil, err
		}
		opportunities = append(opportunities, publishedResearchOpportunity{
			ID:      snapshot.ID,
			Status:  status,
			Ordinal: index + 1,
			Payload: payload,
		})
	}
	return opportunities, nil
}

func publicOpportunityStatus(state string) (string, error) {
	switch state {
	case "triggered":
		return "worth_pursuing", nil
	case "not_triggered":
		return "not_recommended_now", nil
	case "insufficient_evidence":
		return "insufficient_evidence", nil
	default:
		return "", fmt.Errorf(
			"%w: unsupported persisted research opportunity state %q",
			ErrCatalogNotReady,
			state,
		)
	}
}

func persistedOpportunityEstimate(
	metric string,
	value string,
	lower string,
	upper string,
) (map[string]any, error) {
	estimate, err := parsePersistedNumber(value, "research opportunity estimate")
	if err != nil {
		return nil, err
	}
	lowerBound, err := parsePersistedNumber(
		lower,
		"research opportunity confidence interval lower",
	)
	if err != nil {
		return nil, err
	}
	upperBound, err := parsePersistedNumber(
		upper,
		"research opportunity confidence interval upper",
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"confidence_interval": map[string]any{
			"lower": lowerBound,
			"upper": upperBound,
		},
		"metric": metric,
		"value":  estimate,
	}, nil
}

func researchOpportunityTitle(rule string, entityID string) string {
	titles := map[string]string{
		"rapid_growth_low_rct_share":                 "快速增长但随机对照证据不足",
		"single_center_external_validation_gap":      "单中心研究的外部验证缺口",
		"high_citation_low_open_data":                "高引用但开放数据不足",
		"observational_dominance":                    "观察性研究占主导",
		"emerging_method_low_independent_team_count": "新兴方法的独立团队不足",
	}
	title, found := titles[rule]
	if !found {
		title = rule
	}
	return title + "：" + entityID
}

func applyPersistedBiomedicalAnalysis(
	runs validatedBiomedicalAnalysisRuns,
	trends []persistedPublicationTrendSnapshot,
	patterns []persistedJournalPatternSnapshot,
	opportunities []publishedResearchOpportunity,
	home json.RawMessage,
	subjects []publishedBiomedicalResource,
	journals []publishedBiomedicalResource,
) (
	json.RawMessage,
	[]publishedBiomedicalResource,
	[]publishedBiomedicalResource,
	error,
) {
	subjectReferences, err := publishedSubjectReferences(subjects)
	if err != nil {
		return nil, nil, nil, err
	}
	subjectTrendItems := make([]any, 0)
	subjectTrendBySlug := make(map[string][]any)
	entityTrendItems := make([]any, 0)
	trendMissingSignals := make([]string, 0)
	for _, snapshot := range trends {
		if snapshot.State == "insufficient_evidence" {
			trendMissingSignals = appendUniqueString(
				trendMissingSignals,
				"insufficient_evidence",
			)
			continue
		}
		item, err := publishedTrendItem(snapshot)
		if err != nil {
			return nil, nil, nil, err
		}
		switch snapshot.EntityType {
		case "subject":
			subject, found := subjectReferences[snapshot.EntityID]
			if !found {
				return nil, nil, nil, fmt.Errorf(
					"%w: publication trend Subject %q is outside the published cohort",
					ErrCatalogNotReady,
					snapshot.EntityID,
				)
			}
			item["subject"] = subject
			subjectTrendItems = append(subjectTrendItems, item)
			subjectTrendBySlug[snapshot.EntityID] = append(
				subjectTrendBySlug[snapshot.EntityID],
				item,
			)
		case "method":
			item["entity_type"] = "method"
			item["label"] = snapshot.EntityID
			entityTrendItems = append(entityTrendItems, item)
		case "publication_type":
			item["entity_type"] = "publication_type"
			item["label"] = snapshot.EntityID
			entityTrendItems = append(entityTrendItems, item)
		case "mesh_descriptor":
			item["entity_type"] = "disease"
			item["label"] = snapshot.EntityID
			entityTrendItems = append(entityTrendItems, item)
		}
	}

	homeObject, err := decodeCatalogObject(home, "biomedical Home")
	if err != nil {
		return nil, nil, nil, err
	}
	homeObject["subject_momentum"] = map[string]any{
		"analysis": persistedRunAnalysisMetadata(
			collectionAnalysis(homeObject["subject_momentum"]),
			runs.Trend,
			runs.Trend.RecentWindowDays,
			len(subjectTrendItems),
			trendMissingSignals,
		),
		"items": subjectTrendItems,
	}
	homeObject["entity_momentum"] = map[string]any{
		"analysis": persistedRunAnalysisMetadata(
			collectionAnalysis(homeObject["entity_momentum"]),
			runs.Trend,
			runs.Trend.RecentWindowDays,
			len(entityTrendItems),
			trendMissingSignals,
		),
		"items": entityTrendItems,
	}
	opportunityItems := make([]json.RawMessage, len(opportunities))
	for index := range opportunities {
		opportunityItems[index] = opportunities[index].Payload
	}
	homeObject["research_opportunities"] = map[string]any{
		"analysis": persistedRunAnalysisMetadata(
			collectionAnalysis(homeObject["research_opportunities"]),
			runs.Opportunity,
			0,
			len(opportunities),
			[]string{},
		),
		"items": opportunityItems,
	}
	homeObject["evidence_gaps"] = withoutStrings(
		stringSlice(homeObject["evidence_gaps"]),
		"entity_trend_analysis_not_published",
		"journal_pattern_analysis_not_published",
		"research_opportunity_analysis_not_published",
		"subject_trend_analysis_not_published",
	)
	home, err = marshalCatalogPayload(homeObject)
	if err != nil {
		return nil, nil, nil, err
	}

	for index := range subjects {
		detail, err := decodeCatalogObject(
			subjects[index].DetailPayload,
			"biomedical Subject detail",
		)
		if err != nil {
			return nil, nil, nil, err
		}
		items := subjectTrendBySlug[subjects[index].Slug]
		detail["trend_estimates"] = map[string]any{
			"analysis": persistedRunAnalysisMetadata(
				collectionAnalysis(detail["trend_estimates"]),
				runs.Trend,
				runs.Trend.RecentWindowDays,
				len(items),
				trendMissingSignals,
			),
			"items": items,
		}
		detail["evidence_gaps"] = withoutStrings(
			stringSlice(detail["evidence_gaps"]),
			"subject_trend_analysis_not_published",
		)
		subjects[index].DetailPayload, err = marshalCatalogPayload(detail)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	patternsByJournal := make(map[uuid.UUID][]any)
	patternMissingByJournal := make(map[uuid.UUID][]string)
	publishedJournalIDs := make(map[uuid.UUID]struct{}, len(journals))
	for _, journal := range journals {
		publishedJournalIDs[journal.ID] = struct{}{}
	}
	for _, snapshot := range patterns {
		if _, found := publishedJournalIDs[snapshot.JournalID]; !found {
			return nil, nil, nil, fmt.Errorf(
				"%w: journal pattern %q belongs to Journal %s outside the published cohort",
				ErrCatalogNotReady,
				snapshot.PatternKey,
				snapshot.JournalID,
			)
		}
		if snapshot.State == "insufficient_evidence" {
			patternMissingByJournal[snapshot.JournalID] = appendUniqueString(
				patternMissingByJournal[snapshot.JournalID],
				"insufficient_evidence",
			)
			continue
		}
		item, err := publishedJournalPatternItem(snapshot, runs.Journal)
		if err != nil {
			return nil, nil, nil, err
		}
		patternsByJournal[snapshot.JournalID] = append(
			patternsByJournal[snapshot.JournalID],
			item,
		)
	}
	for index := range journals {
		detail, err := decodeCatalogObject(
			journals[index].DetailPayload,
			"biomedical Journal detail",
		)
		if err != nil {
			return nil, nil, nil, err
		}
		items := patternsByJournal[journals[index].ID]
		detail["editorial_patterns"] = map[string]any{
			"analysis": persistedRunAnalysisMetadata(
				collectionAnalysis(detail["editorial_patterns"]),
				runs.Journal,
				runs.Journal.WindowDays,
				len(items),
				patternMissingByJournal[journals[index].ID],
			),
			"items": items,
		}
		detail["evidence_gaps"] = withoutStrings(
			stringSlice(detail["evidence_gaps"]),
			"editorial_pattern_analysis_not_published",
			"journal_pattern_analysis_not_published",
		)
		journals[index].DetailPayload, err = marshalCatalogPayload(detail)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return home, subjects, journals, nil
}

func publishedTrendItem(
	snapshot persistedPublicationTrendSnapshot,
) (map[string]any, error) {
	if snapshot.State != "sufficient_evidence" ||
		snapshot.RateRatio == nil ||
		snapshot.ConfidenceIntervalLower == nil ||
		snapshot.ConfidenceIntervalUpper == nil ||
		snapshot.PValue == nil ||
		snapshot.AdjustedPValue == nil {
		return nil, fmt.Errorf(
			"%w: publication trend %s:%s lacks a sufficient persisted estimate",
			ErrCatalogNotReady,
			snapshot.EntityType,
			snapshot.EntityID,
		)
	}
	estimate, err := parsePersistedNumber(*snapshot.RateRatio, "publication trend estimate")
	if err != nil {
		return nil, err
	}
	lower, err := parsePersistedNumber(
		*snapshot.ConfidenceIntervalLower,
		"publication trend confidence interval lower",
	)
	if err != nil {
		return nil, err
	}
	upper, err := parsePersistedNumber(
		*snapshot.ConfidenceIntervalUpper,
		"publication trend confidence interval upper",
	)
	if err != nil {
		return nil, err
	}
	pValue, err := parsePersistedNumber(*snapshot.PValue, "publication trend p-value")
	if err != nil {
		return nil, err
	}
	adjustedPValue, err := parsePersistedNumber(
		*snapshot.AdjustedPValue,
		"publication trend adjusted p-value",
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"adjusted_p_value": catalogValue{State: "known", Value: adjustedPValue},
		"baseline_count":   catalogValue{State: "known", Value: snapshot.BaselinePaperCount},
		"confidence_interval": map[string]any{
			"lower": lower,
			"upper": upper,
		},
		"estimate":                  catalogValue{State: "known", Value: estimate},
		"independent_journal_count": catalogValue{State: "known", Value: snapshot.IndependentJournalCount},
		"independent_team_count":    catalogValue{State: "known", Value: snapshot.IndependentTeamCount},
		"model_family":              snapshot.Model,
		"p_value":                   catalogValue{State: "known", Value: pValue},
		"recent_count":              catalogValue{State: "known", Value: snapshot.RecentPaperCount},
	}, nil
}

func publishedJournalPatternItem(
	snapshot persistedJournalPatternSnapshot,
	run persistedBiomedicalAnalysisRun,
) (map[string]any, error) {
	if snapshot.State != "sufficient_evidence" ||
		snapshot.EffectValue == nil ||
		snapshot.ConfidenceIntervalLower == nil ||
		snapshot.ConfidenceIntervalUpper == nil ||
		snapshot.PValue == nil ||
		snapshot.AdjustedPValue == nil {
		return nil, fmt.Errorf(
			"%w: journal pattern %q lacks a sufficient persisted estimate",
			ErrCatalogNotReady,
			snapshot.PatternKey,
		)
	}
	estimate, err := parsePersistedNumber(*snapshot.EffectValue, "journal pattern estimate")
	if err != nil {
		return nil, err
	}
	lower, err := parsePersistedNumber(
		*snapshot.ConfidenceIntervalLower,
		"journal pattern confidence interval lower",
	)
	if err != nil {
		return nil, err
	}
	upper, err := parsePersistedNumber(
		*snapshot.ConfidenceIntervalUpper,
		"journal pattern confidence interval upper",
	)
	if err != nil {
		return nil, err
	}
	pValue, err := parsePersistedNumber(*snapshot.PValue, "journal pattern p-value")
	if err != nil {
		return nil, err
	}
	adjustedPValue, err := parsePersistedNumber(
		*snapshot.AdjustedPValue,
		"journal pattern adjusted p-value",
	)
	if err != nil {
		return nil, err
	}
	coverage, err := parsePersistedNumber(snapshot.Coverage, "journal pattern coverage")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"adjusted_p_value": catalogValue{State: "known", Value: adjustedPValue},
		"baseline":         "subject:" + snapshot.SubjectID.String(),
		"confidence_interval": map[string]any{
			"lower": lower,
			"upper": upper,
		},
		"coverage_ratio":       catalogValue{State: "known", Value: coverage},
		"dimension":            snapshot.FeatureType,
		"estimate":             catalogValue{State: "known", Value: estimate},
		"estimate_kind":        snapshot.Measure,
		"field_baseline_count": catalogValue{State: "known", Value: snapshot.FieldBaselinePaperCount},
		"interpretation_kind":  "editorial_pattern",
		"label":                snapshot.FeatureValue,
		"minimum_support_count": catalogValue{
			State: "known",
			Value: run.MinimumSupportCount,
		},
		"p_value":        catalogValue{State: "known", Value: pValue},
		"support_papers": catalogValue{State: "known", Value: snapshot.JournalFeaturePaperCount},
	}, nil
}

func persistedRunAnalysisMetadata(
	base map[string]any,
	run persistedBiomedicalAnalysisRun,
	windowDays int,
	sampleSize int,
	missingSignals []string,
) map[string]any {
	metadata := make(map[string]any, len(base)+7)
	for key, value := range base {
		metadata[key] = value
	}
	metadata["analysis_run_id"] = run.ID
	metadata["analysis_type"] = run.AnalysisType
	metadata["cohort_revision"] = run.CohortRevision
	metadata["coverage_ratio"] = catalogValue{State: "known", Value: float64(1)}
	metadata["formula_version"] = catalogValue{State: "known", Value: run.FormulaVersion}
	metadata["generated_at"] = catalogValue{
		State: "known",
		Value: run.GeneratedAt.UTC().Format(time.RFC3339Nano),
	}
	metadata["missing_signals"] = missingSignals
	metadata["sample_size"] = catalogValue{State: "known", Value: int64(sampleSize)}
	if windowDays > 0 {
		metadata["window_days"] = catalogValue{
			State: "known",
			Value: windowDays,
		}
	} else {
		metadata["window_days"] = catalogValue{
			State:  "missing",
			Reason: "analysis does not use a publication window",
		}
	}
	if run.RecentWindowDays > 0 {
		metadata["recent_window_days"] = run.RecentWindowDays
	}
	if run.BaselineWindowDays > 0 {
		metadata["baseline_window_days"] = run.BaselineWindowDays
	}
	return metadata
}

func publishedSubjectReferences(
	subjects []publishedBiomedicalResource,
) (map[string]taxonomyReference, error) {
	references := make(map[string]taxonomyReference, len(subjects))
	for _, subject := range subjects {
		var payload struct {
			ID   uuid.UUID `json:"id"`
			Slug string    `json:"slug"`
			Name string    `json:"name"`
		}
		if err := json.Unmarshal(subject.SummaryPayload, &payload); err != nil {
			return nil, fmt.Errorf(
				"decode published Subject %s identity: %w",
				subject.ID,
				err,
			)
		}
		if payload.ID != subject.ID ||
			payload.Slug != subject.Slug ||
			payload.Name == "" {
			return nil, fmt.Errorf(
				"%w: published Subject %s has inconsistent identity",
				ErrCatalogNotReady,
				subject.ID,
			)
		}
		references[payload.Slug] = taxonomyReference{
			ID:   payload.ID,
			Slug: payload.Slug,
			Name: payload.Name,
		}
	}
	return references, nil
}

func decodeCatalogObject(
	payload json.RawMessage,
	name string,
) (map[string]any, error) {
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, fmt.Errorf("decode %s payload: %w", name, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%w: %s payload is not an object", ErrCatalogNotReady, name)
	}
	return object, nil
}

func collectionAnalysis(value any) map[string]any {
	collection, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	analysis, ok := collection["analysis"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return analysis
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return append([]string(nil), strings...)
		}
		return []string{}
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func withoutStrings(values []string, removals ...string) []string {
	remove := make(map[string]struct{}, len(removals))
	for _, value := range removals {
		remove[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, found := remove[value]; !found {
			result = append(result, value)
		}
	}
	return result
}

func appendUniqueString(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func parsePersistedNumber(value string, field string) (float64, error) {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf(
			"%w: %s is not a finite number",
			ErrCatalogNotReady,
			field,
		)
	}
	if parsed != parsed || parsed > 1.7976931348623157e+308 ||
		parsed < -1.7976931348623157e+308 {
		return 0, fmt.Errorf(
			"%w: %s is not a finite number",
			ErrCatalogNotReady,
			field,
		)
	}
	return parsed, nil
}
