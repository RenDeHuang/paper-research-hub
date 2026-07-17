package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	postgresAnalysisModelProvider = "internal"
	postgresAnalysisModelName     = "deterministic"
)

var (
	errAnalysisServiceNotInitialized = errors.New(
		"Postgres analysis service is not initialized",
	)
	errAnalysisReferenceConflict = errors.New(
		"analysis references are inconsistent",
	)
)

type PostgresAnalysisService struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

type AnalysisRunSummary struct {
	RunID           uuid.UUID
	SnapshotCount   int
	SourceRevisions []string
}

func NewPostgresAnalysisService(
	pool *pgxpool.Pool,
	now func() time.Time,
) (*PostgresAnalysisService, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	if now == nil {
		return nil, errors.New("analysis clock is required")
	}
	return &PostgresAnalysisService{pool: pool, now: now}, nil
}

type analysisRunInputEnvelope struct {
	AnalysisType string         `json:"analysis_type"`
	Scope        map[string]any `json:"scope"`
}

type analysisRunOutputEnvelope struct {
	AnalysisType    string   `json:"analysis_type"`
	SnapshotCount   int      `json:"snapshot_count"`
	SourceRevisions []string `json:"source_revisions"`
}

func beginSerializableTx(
	ctx context.Context,
	pool *pgxpool.Pool,
) (pgx.Tx, error) {
	return pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.Serializable,
		AccessMode: pgx.ReadWrite,
	})
}

func (service *PostgresAnalysisService) ensureInitialized() error {
	if service == nil || service.pool == nil || service.now == nil {
		return errAnalysisServiceNotInitialized
	}
	return nil
}

func (service *PostgresAnalysisService) currentTimeUTC() time.Time {
	return service.now().UTC()
}

func validateTrimmedUUIDList(field string, values []uuid.UUID) error {
	if len(values) == 0 {
		return &InvalidInputError{
			Field:  field,
			Reason: "must contain at least one work",
		}
	}
	for _, value := range values {
		if value == uuid.Nil {
			return &InvalidInputError{
				Field:  field,
				Reason: "cannot contain nil UUID",
			}
		}
	}
	return nil
}

func normalizeUUIDs(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			continue
		}
		seen[value] = struct{}{}
	}
	result := make([]uuid.UUID, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		return strings.Compare(result[left].String(), result[right].String()) < 0
	})
	return result
}

func marshalJSONObject(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func nullableFloat64(value float64) *float64 {
	return &value
}

func proportionEstimate(
	measure string,
	numerator, denominator int,
) (StatisticalEstimate, error) {
	if denominator <= 0 {
		return StatisticalEstimate{}, &InvalidInputError{
			Field:  measure + "_denominator",
			Reason: "must be positive",
		}
	}
	if numerator < 0 || numerator > denominator {
		return StatisticalEstimate{}, &InvalidInputError{
			Field:  measure + "_numerator",
			Reason: "must be between zero and the denominator",
		}
	}
	value := float64(numerator) / float64(denominator)
	standardError := math.Sqrt(value * (1 - value) / float64(denominator))
	lower := value - normalQuantile95*standardError
	upper := value + normalQuantile95*standardError
	if lower < 0 {
		lower = 0
	}
	if upper > 1 {
		upper = 1
	}
	return StatisticalEstimate{
		Measure: measure,
		Value:   value,
		ConfidenceInterval: ConfidenceInterval{
			Level: confidenceLevel95,
			Lower: lower,
			Upper: upper,
		},
		PValue:         1,
		AdjustedPValue: 1,
	}, nil
}

func loadSubjectVersionID(
	ctx context.Context,
	tx pgx.Tx,
	subjectVersion string,
) (uuid.UUID, error) {
	var subjectVersionID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id
		FROM subject_versions
		WHERE version_key = $1
	`, subjectVersion).Scan(&subjectVersionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, fmt.Errorf(
				"Subject version %q does not exist",
				subjectVersion,
			)
		}
		return uuid.Nil, fmt.Errorf("load Subject version: %w", err)
	}
	return subjectVersionID, nil
}

func validateJCRReceiptScope(
	ctx context.Context,
	tx pgx.Tx,
	receiptID uuid.UUID,
	metricYear int,
) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT metric.metric_year
		FROM jcr_import_receipt_metrics AS receipt_metric
		JOIN venue_metric_snapshots AS metric
		  ON metric.id = receipt_metric.metric_snapshot_id
		WHERE receipt_metric.import_receipt_id = $1
		ORDER BY metric.metric_year
	`, receiptID)
	if err != nil {
		return fmt.Errorf("query JCR receipt metric years: %w", err)
	}
	defer rows.Close()

	years := make([]int, 0, 1)
	for rows.Next() {
		var year int
		if err := rows.Scan(&year); err != nil {
			return fmt.Errorf("scan JCR receipt metric year: %w", err)
		}
		years = append(years, year)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate JCR receipt metric years: %w", err)
	}
	if len(years) != 1 || years[0] != metricYear {
		return fmt.Errorf(
			"JCR receipt %s has metric years %v, expected exactly %d",
			receiptID,
			years,
			metricYear,
		)
	}
	return nil
}

type cohortRevisionLoader struct {
	SubjectVersionID uuid.UUID
	MetricYear       int
	PolicyVersion    string
}

func loadCohortRevisionFacts(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
	scope cohortRevisionLoader,
) ([]CohortWorkRevisionFact, error) {
	workIDs = normalizeUUIDs(workIDs)
	if len(workIDs) == 0 {
		return nil, errors.New("cohort revision requires at least one Work")
	}

	type workFact struct {
		workID                uuid.UUID
		venueID               uuid.UUID
		eligibilityDecisionID uuid.UUID
	}
	rows, err := tx.Query(ctx, `
		SELECT
			work.id,
			work.venue_id,
			eligibility.id
		FROM works AS work
		JOIN biomedical_publication_eligibility_decisions AS eligibility
		  ON eligibility.work_id = work.id
		 AND eligibility.policy_version = $2
		 AND eligibility.metric_year = $3
		 AND eligibility.subject_version_id = $4
		 AND eligibility.decision = 'accepted'
		WHERE work.id = ANY($1::uuid[])
		ORDER BY work.id
	`, workIDs, scope.PolicyVersion, scope.MetricYear, scope.SubjectVersionID)
	if err != nil {
		return nil, fmt.Errorf("load cohort Work facts: %w", err)
	}
	defer rows.Close()

	facts := make(map[uuid.UUID]workFact, len(workIDs))
	for rows.Next() {
		var fact workFact
		if err := rows.Scan(&fact.workID, &fact.venueID, &fact.eligibilityDecisionID); err != nil {
			return nil, fmt.Errorf("scan cohort Work fact: %w", err)
		}
		facts[fact.workID] = fact
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cohort Work facts: %w", err)
	}
	if len(facts) != len(workIDs) {
		return nil, fmt.Errorf(
			"cohort revision Work set is incomplete: got %d of %d works",
			len(facts),
			len(workIDs),
		)
	}

	assertionRows, err := tx.Query(ctx, `
		SELECT work_id, source_assertion_id
		FROM (
			SELECT work_id, id AS source_assertion_id
			FROM work_mesh_headings
			WHERE work_id = ANY($1::uuid[])
			UNION ALL
			SELECT work_id, id
			FROM work_mesh_qualifiers
			WHERE work_id = ANY($1::uuid[])
			UNION ALL
			SELECT work_id, id
			FROM work_publication_types
			WHERE work_id = ANY($1::uuid[])
			UNION ALL
			SELECT work_id, id
			FROM work_methods
			WHERE work_id = ANY($1::uuid[])
		) AS assertions
		ORDER BY work_id, source_assertion_id
	`, workIDs)
	if err != nil {
		return nil, fmt.Errorf("load cohort source assertions: %w", err)
	}
	defer assertionRows.Close()

	assertions := make(map[uuid.UUID][]uuid.UUID, len(workIDs))
	for assertionRows.Next() {
		var workID, sourceAssertionID uuid.UUID
		if err := assertionRows.Scan(&workID, &sourceAssertionID); err != nil {
			return nil, fmt.Errorf("scan cohort source assertion: %w", err)
		}
		assertions[workID] = append(assertions[workID], sourceAssertionID)
	}
	if err := assertionRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cohort source assertions: %w", err)
	}

	result := make([]CohortWorkRevisionFact, 0, len(workIDs))
	for _, workID := range workIDs {
		fact := facts[workID]
		sourceAssertionIDs := normalizeUUIDs(assertions[workID])
		if len(sourceAssertionIDs) == 0 {
			return nil, fmt.Errorf("cohort revision Work %s has no source assertions", workID)
		}
		result = append(result, CohortWorkRevisionFact{
			WorkID:                fact.workID,
			VenueID:               fact.venueID,
			EligibilityDecisionID: fact.eligibilityDecisionID,
			SourceAssertionIDs:    sourceAssertionIDs,
		})
	}
	return result, nil
}

func computeCohortRevisionFromWorks(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
	scope cohortRevisionLoader,
) (string, error) {
	facts, err := loadCohortRevisionFacts(ctx, tx, workIDs, scope)
	if err != nil {
		return "", err
	}
	return ComputeCohortRevision(facts)
}

func insertRunningAnalysisRun(
	ctx context.Context,
	tx pgx.Tx,
	analysisType string,
	promptVersion string,
	inputPayload any,
	startedAt time.Time,
) (uuid.UUID, error) {
	rawInput, err := marshalJSONObject(inputPayload)
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode analysis input: %w", err)
	}
	var runID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO analysis_runs (
			analysis_type,
			model_provider,
			model_name,
			prompt_version,
			status,
			input_payload,
			started_at
		) VALUES (
			$1, $2, $3, $4, 'running', $5::jsonb, $6
		)
		RETURNING id
	`,
		analysisType,
		postgresAnalysisModelProvider,
		postgresAnalysisModelName,
		promptVersion,
		rawInput,
		startedAt,
	).Scan(&runID); err != nil {
		return uuid.Nil, fmt.Errorf("create running analysis run: %w", err)
	}
	return runID, nil
}

func completeAnalysisRun(
	ctx context.Context,
	tx pgx.Tx,
	runID uuid.UUID,
	outputPayload any,
	completedAt time.Time,
) error {
	rawOutput, err := marshalJSONObject(outputPayload)
	if err != nil {
		return fmt.Errorf("encode analysis output: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE analysis_runs
		SET status = 'succeeded',
		    output_payload = $2::jsonb,
		    completed_at = $3
		WHERE id = $1
		  AND status = 'running'
	`, runID, rawOutput, completedAt); err != nil {
		return fmt.Errorf("complete analysis run %s: %w", runID, err)
	}
	return nil
}

func loadAnalysisRun(
	ctx context.Context,
	tx pgx.Tx,
	runID uuid.UUID,
) (analysisRunInputEnvelope, analysisRunOutputEnvelope, string, string, error) {
	var (
		inputRaw             []byte
		outputRaw            []byte
		analysisType, status string
	)
	if err := tx.QueryRow(ctx, `
		SELECT analysis_type, status, input_payload, output_payload
		FROM analysis_runs
		WHERE id = $1
	`, runID).Scan(&analysisType, &status, &inputRaw, &outputRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return analysisRunInputEnvelope{}, analysisRunOutputEnvelope{}, "", "", fmt.Errorf(
				"analysis run %s does not exist",
				runID,
			)
		}
		return analysisRunInputEnvelope{}, analysisRunOutputEnvelope{}, "", "", fmt.Errorf(
			"load analysis run %s: %w",
			runID,
			err,
		)
	}

	var inputEnvelope analysisRunInputEnvelope
	if len(inputRaw) != 0 {
		if err := json.Unmarshal(inputRaw, &inputEnvelope); err != nil {
			return analysisRunInputEnvelope{}, analysisRunOutputEnvelope{}, "", "", fmt.Errorf(
				"decode analysis run %s input: %w",
				runID,
				err,
			)
		}
	}
	var outputEnvelope analysisRunOutputEnvelope
	if len(outputRaw) != 0 {
		if err := json.Unmarshal(outputRaw, &outputEnvelope); err != nil {
			return analysisRunInputEnvelope{}, analysisRunOutputEnvelope{}, "", "", fmt.Errorf(
				"decode analysis run %s output: %w",
				runID,
				err,
			)
		}
	}
	return inputEnvelope, outputEnvelope, analysisType, status, nil
}

func validateExactAnalysisRun(
	inputEnvelope analysisRunInputEnvelope,
	outputEnvelope analysisRunOutputEnvelope,
	analysisType string,
	expectedScope map[string]any,
) error {
	if inputEnvelope.AnalysisType != analysisType {
		return fmt.Errorf(
			"%w: expected analysis_type %q, got %q",
			errAnalysisReferenceConflict,
			analysisType,
			inputEnvelope.AnalysisType,
		)
	}
	if outputEnvelope.AnalysisType != analysisType {
		return fmt.Errorf(
			"%w: expected output analysis_type %q, got %q",
			errAnalysisReferenceConflict,
			analysisType,
			outputEnvelope.AnalysisType,
		)
	}
	for key, expected := range expectedScope {
		if actual, found := inputEnvelope.Scope[key]; !found || actual != expected {
			return fmt.Errorf(
				"%w: analysis run scope %q mismatch: got %#v want %#v",
				errAnalysisReferenceConflict,
				key,
				actual,
				expected,
			)
		}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func nullableUUID(value uuid.UUID) *uuid.UUID {
	if value == uuid.Nil {
		return nil
	}
	return &value
}

func nullableText(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func nullablePgText(value string) pgtype.Text {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: trimmed, Valid: true}
}
