package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/citation"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ranking"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
)

var (
	errInvalidCitationMomentumObservation = errors.New(
		"invalid citation momentum observation",
	)
	errDuplicateCitationMomentumWork = errors.New(
		"duplicate citation momentum Work",
	)
)

type citationMomentumObservation struct {
	WorkID        uuid.UUID
	CanonicalKey  string
	CitationCount int64
	Velocity      decimal.Decimal
}

type citationMomentumRank struct {
	WorkID uuid.UUID
	Rank   int
	Score  decimal.Decimal
}

type citationTrendEvidence struct {
	State          string
	AnalysisRunID  uuid.UUID
	Source         string
	AsOf           time.Time
	GeneratedAt    time.Time
	FormulaVersion string
	SourceRevision string
	WindowDays     int
	Velocity       decimal.Decimal
	Delta          int64
}

type publishedTrend struct {
	Kind       TrendKind
	WindowDays int
	Rank       int
	SubjectID  uuid.UUID
	Score      string
	Payload    json.RawMessage
}

func rankPositiveCitationMomentum(
	observations []citationMomentumObservation,
) ([]citationMomentumRank, error) {
	seen := make(map[uuid.UUID]struct{}, len(observations))
	positive := make([]citationMomentumObservation, 0, len(observations))
	for index, observation := range observations {
		if observation.WorkID == uuid.Nil ||
			observation.CanonicalKey == "" ||
			observation.CanonicalKey != strings.TrimSpace(observation.CanonicalKey) ||
			observation.CitationCount < 0 {
			return nil, fmt.Errorf(
				"%w at index %d",
				errInvalidCitationMomentumObservation,
				index,
			)
		}
		if _, exists := seen[observation.WorkID]; exists {
			return nil, fmt.Errorf(
				"%w: %s",
				errDuplicateCitationMomentumWork,
				observation.WorkID,
			)
		}
		seen[observation.WorkID] = struct{}{}
		if observation.Velocity.IsPositive() {
			positive = append(positive, observation)
		}
	}
	sort.Slice(positive, func(left, right int) bool {
		if !positive[left].Velocity.Equal(positive[right].Velocity) {
			return positive[left].Velocity.GreaterThan(positive[right].Velocity)
		}
		if positive[left].CitationCount != positive[right].CitationCount {
			return positive[left].CitationCount > positive[right].CitationCount
		}
		if positive[left].CanonicalKey != positive[right].CanonicalKey {
			return positive[left].CanonicalKey < positive[right].CanonicalKey
		}
		return positive[left].WorkID.String() < positive[right].WorkID.String()
	})

	results := make([]citationMomentumRank, len(positive))
	cohortSize := int64(len(positive))
	for start := 0; start < len(positive); {
		end := start + 1
		for end < len(positive) &&
			positive[end].Velocity.Equal(positive[start].Velocity) {
			end++
		}
		less := int64(len(positive) - end)
		equal := int64(end - start)
		midrank := decimal.NewFromInt(less).
			Add(
				decimal.NewFromInt(equal + 1).
					Div(decimal.NewFromInt(2)),
			)
		score := midrank.DivRound(
			decimal.NewFromInt(cohortSize),
			ranking.DivisionScale,
		)
		for index := start; index < end; index++ {
			results[index] = citationMomentumRank{
				WorkID: positive[index].WorkID,
				Rank:   index + 1,
				Score:  score,
			}
		}
		start = end
	}
	return results, nil
}

func loadCitationTrendEvidence(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	run citationAnalysisRun,
) (citationTrendEvidence, error) {
	var (
		asOf, generatedAt                     time.Time
		windowDays                            int
		countState, velocityState             string
		currentSnapshotID, baselineSnapshotID pgtype.UUID
		countValue                            pgtype.Int8
		velocityValue                         pgtype.Text
		sourceRevision, formulaVersion        string
	)
	if err := tx.QueryRow(ctx, `
		SELECT
			as_of,
			velocity_window_days,
			citation_count_state,
			current_snapshot_id,
			citation_count,
			citation_velocity_state,
			baseline_snapshot_id,
			citation_velocity::text,
			source_revision,
			formula_version,
			generated_at
		FROM citation_analysis_work_snapshots
		WHERE analysis_run_id = $1
		  AND work_id = $2
		  AND source = $3
	`, run.ID, workID, run.Source).Scan(
		&asOf,
		&windowDays,
		&countState,
		&currentSnapshotID,
		&countValue,
		&velocityState,
		&baselineSnapshotID,
		&velocityValue,
		&sourceRevision,
		&formulaVersion,
		&generatedAt,
	); err != nil {
		return citationTrendEvidence{}, fmt.Errorf(
			"query Work %s citation trend evidence from run %s: %w",
			workID,
			run.ID,
			err,
		)
	}
	evidence := citationTrendEvidence{
		State:          velocityState,
		AnalysisRunID:  run.ID,
		Source:         run.Source,
		AsOf:           asOf.UTC(),
		GeneratedAt:    generatedAt.UTC(),
		FormulaVersion: formulaVersion,
		SourceRevision: sourceRevision,
		WindowDays:     windowDays,
	}
	if !asOf.Equal(run.AsOf) ||
		!generatedAt.Equal(run.GeneratedAt) ||
		formulaVersion != run.FormulaVersion ||
		windowDays != run.VelocityWindowDays {
		return citationTrendEvidence{}, fmt.Errorf(
			"%w: Work %s citation trend metadata conflicts with analysis run %s",
			ErrCatalogNotReady,
			workID,
			run.ID,
		)
	}
	if velocityState == "insufficient_evidence" {
		if velocityValue.Valid {
			return citationTrendEvidence{}, fmt.Errorf(
				"%w: Work %s insufficient citation velocity has a value",
				ErrCatalogNotReady,
				workID,
			)
		}
		return evidence, nil
	}
	if velocityState != "known" ||
		countState != "known" ||
		!currentSnapshotID.Valid ||
		!baselineSnapshotID.Valid ||
		!countValue.Valid ||
		!velocityValue.Valid {
		return citationTrendEvidence{}, fmt.Errorf(
			"%w: Work %s known citation velocity is structurally incomplete",
			ErrCatalogNotReady,
			workID,
		)
	}
	storedVelocity, err := decimal.NewFromString(velocityValue.String)
	if err != nil {
		return citationTrendEvidence{}, fmt.Errorf(
			"%w: Work %s has invalid stored citation velocity",
			ErrCatalogNotReady,
			workID,
		)
	}

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
	`, workID, run.Source, run.AsOf)
	if err != nil {
		return citationTrendEvidence{}, fmt.Errorf(
			"query Work %s citation snapshots for trend verification: %w",
			workID,
			err,
		)
	}
	defer rows.Close()
	stored := make([]storedSnapshot, 0, 4)
	analysisSnapshots := make([]citation.Snapshot, 0, 4)
	for rows.Next() {
		var snapshot storedSnapshot
		if err := rows.Scan(
			&snapshot.id,
			&snapshot.observedAt,
			&snapshot.count,
		); err != nil {
			return citationTrendEvidence{}, fmt.Errorf(
				"scan Work %s citation snapshot for trend verification: %w",
				workID,
				err,
			)
		}
		stored = append(stored, snapshot)
		analysisSnapshots = append(analysisSnapshots, citation.Snapshot{
			WorkID:     workID,
			Source:     run.Source,
			ObservedAt: snapshot.observedAt,
			Count:      snapshot.count,
		})
	}
	if err := rows.Err(); err != nil {
		return citationTrendEvidence{}, fmt.Errorf(
			"iterate Work %s citation snapshots for trend verification: %w",
			workID,
			err,
		)
	}
	result, err := citation.AnalyzeCitationVelocity(
		citation.CitationVelocityInput{
			Source:    run.Source,
			AsOf:      run.AsOf,
			Window:    time.Duration(windowDays) * 24 * time.Hour,
			Snapshots: analysisSnapshots,
		},
	)
	if err != nil {
		return citationTrendEvidence{}, fmt.Errorf(
			"%w: recompute Work %s citation velocity: %v",
			ErrCatalogNotReady,
			workID,
			err,
		)
	}
	var (
		expectedCurrentID  uuid.UUID
		expectedBaselineID uuid.UUID
	)
	for _, snapshot := range stored {
		if snapshot.observedAt.Equal(result.End.ObservedAt) {
			expectedCurrentID = snapshot.id
		}
		if snapshot.observedAt.Equal(result.Start.ObservedAt) {
			expectedBaselineID = snapshot.id
		}
	}
	if expectedCurrentID == uuid.Nil ||
		expectedBaselineID == uuid.Nil ||
		expectedCurrentID != uuid.UUID(currentSnapshotID.Bytes) ||
		expectedBaselineID != uuid.UUID(baselineSnapshotID.Bytes) ||
		result.End.Count != countValue.Int64 ||
		!result.Velocity.Equal(storedVelocity) {
		return citationTrendEvidence{}, fmt.Errorf(
			"%w: Work %s citation trend evidence is not reproducible from source snapshots",
			ErrCatalogNotReady,
			workID,
		)
	}
	evidence.Velocity = result.Velocity
	evidence.Delta = result.End.Count - result.Start.Count
	return evidence, nil
}

func buildCitationMomentum(
	run citationAnalysisRun,
	papers []publishedPaper,
) ([]publishedPaper, []publishedTrend, map[string]any, error) {
	updated := append([]publishedPaper(nil), papers...)
	observations := make([]citationMomentumObservation, 0, len(updated))
	indexByWork := make(map[uuid.UUID]int, len(updated))
	knownVelocityCount := 0
	windowDays := 0
	for index := range updated {
		paper := &updated[index]
		indexByWork[paper.ID] = index
		if paper.CitationTrend.AnalysisRunID != run.ID ||
			paper.CitationTrend.Source != run.Source ||
			!paper.CitationTrend.AsOf.Equal(run.AsOf) ||
			!paper.CitationTrend.GeneratedAt.Equal(run.GeneratedAt) ||
			paper.CitationTrend.FormulaVersion != run.FormulaVersion ||
			paper.CitationTrend.WindowDays < 1 {
			return nil, nil, nil, fmt.Errorf(
				"%w: paper %s citation trend evidence conflicts with the bound analysis run",
				ErrCatalogNotReady,
				paper.ID,
			)
		}
		if windowDays == 0 {
			windowDays = paper.CitationTrend.WindowDays
		} else if windowDays != paper.CitationTrend.WindowDays {
			return nil, nil, nil, fmt.Errorf(
				"%w: bound citation analysis run has mixed velocity windows",
				ErrCatalogNotReady,
			)
		}
		switch paper.CitationTrend.State {
		case "known":
			if paper.CitationCountValue == nil {
				return nil, nil, nil, fmt.Errorf(
					"%w: paper %s has known citation velocity without a citation count",
					ErrCatalogNotReady,
					paper.ID,
				)
			}
			knownVelocityCount++
			observations = append(observations, citationMomentumObservation{
				WorkID:        paper.ID,
				CanonicalKey:  paper.CanonicalKey,
				CitationCount: *paper.CitationCountValue,
				Velocity:      paper.CitationTrend.Velocity,
			})
		case "insufficient_evidence":
		default:
			return nil, nil, nil, fmt.Errorf(
				"%w: paper %s has invalid citation trend state %q",
				ErrCatalogNotReady,
				paper.ID,
				paper.CitationTrend.State,
			)
		}
	}
	ranks, err := rankPositiveCitationMomentum(observations)
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"%w: rank citation momentum: %v",
			ErrCatalogNotReady,
			err,
		)
	}
	coverage := ratioCatalogValue(knownVelocityCount, len(updated))
	missingSignals := []string{}
	if knownVelocityCount != len(updated) {
		missingSignals = []string{"citation_velocity_incomplete_coverage"}
	}
	analysis := map[string]any{
		"coverage_ratio": coverage,
		"formula_version": catalogValue{
			State: "known",
			Value: run.FormulaVersion,
		},
		"generated_at": catalogValue{
			State: "known",
			Value: run.GeneratedAt.UTC().Format(time.RFC3339Nano),
		},
		"missing_signals": missingSignals,
		"sample_size": catalogValue{
			State: "known",
			Value: int64(knownVelocityCount),
		},
		"sources": catalogValue{
			State: "known",
			Value: []string{run.Source},
		},
		"window_days": catalogValue{
			State: "known",
			Value: windowDays,
		},
	}
	homeItems := make([]map[string]any, 0, len(ranks))
	trends := make([]publishedTrend, 0, len(ranks))
	for _, ranked := range ranks {
		index, found := indexByWork[ranked.WorkID]
		if !found {
			return nil, nil, nil, fmt.Errorf(
				"%w: citation momentum references unpublished Work %s",
				ErrCatalogNotReady,
				ranked.WorkID,
			)
		}
		paper := &updated[index]
		if paper.CitationTrend.State != "known" ||
			paper.CitationTrend.AnalysisRunID != run.ID ||
			paper.CitationTrend.Source != run.Source {
			return nil, nil, nil, fmt.Errorf(
				"%w: citation momentum Work %s is outside the bound analysis run",
				ErrCatalogNotReady,
				paper.ID,
			)
		}
		scoreNumber := json.Number(ranked.Score.String())
		paper.TrendScoreState = "known"
		scoreText := ranked.Score.String()
		paper.TrendScoreValue = &scoreText
		if err := setPaperTrendScore(paper, scoreNumber); err != nil {
			return nil, nil, nil, err
		}
		homeItems = append(homeItems, map[string]any{
			"citation_delta": catalogValue{
				State: "known",
				Value: paper.CitationTrend.Delta,
			},
			"citations_per_day": catalogValue{
				State: "known",
				Value: json.Number(paper.CitationTrend.Velocity.String()),
			},
			"cohort_percentile": catalogValue{
				State: "known",
				Value: scoreNumber,
			},
			"paper": paper.SummaryPayload,
		})
		trendPayload, err := marshalCatalogPayload(map[string]any{
			"rank":       ranked.Rank,
			"score":      scoreNumber,
			"subject_id": paper.ID,
			"paper":      paper.SummaryPayload,
			"ranking": map[string]any{
				"formula_version": run.FormulaVersion,
				"window_days":     paper.CitationTrend.WindowDays,
				"generated_at": run.GeneratedAt.UTC().
					Format(time.RFC3339Nano),
				"coverage":        coverage,
				"missing_signals": missingSignals,
			},
		})
		if err != nil {
			return nil, nil, nil, err
		}
		trends = append(trends, publishedTrend{
			Kind:       TrendKindPapers,
			WindowDays: paper.CitationTrend.WindowDays,
			Rank:       ranked.Rank,
			SubjectID:  paper.ID,
			Score:      ranked.Score.String(),
			Payload:    trendPayload,
		})
	}
	return updated, trends, map[string]any{
		"analysis": analysis,
		"items":    homeItems,
	}, nil
}

func setPaperTrendScore(
	paper *publishedPaper,
	score json.Number,
) error {
	if paper == nil {
		return fmt.Errorf("%w: cannot update a nil paper", ErrCatalogNotReady)
	}
	for _, target := range []*json.RawMessage{
		&paper.SummaryPayload,
		&paper.DetailPayload,
	} {
		var payload map[string]any
		if err := json.Unmarshal(*target, &payload); err != nil {
			return fmt.Errorf(
				"%w: decode paper %s payload before applying trend score: %v",
				ErrCatalogNotReady,
				paper.ID,
				err,
			)
		}
		payload["trend_score"] = catalogValue{
			State: "known",
			Value: score,
		}
		encoded, err := marshalCatalogPayload(payload)
		if err != nil {
			return err
		}
		*target = encoded
	}
	return nil
}
