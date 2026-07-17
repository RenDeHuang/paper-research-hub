package venue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresAssessmentStore struct {
	pool *pgxpool.Pool
}

var _ AssessmentStore = (*PostgresAssessmentStore)(nil)

func NewPostgresAssessmentStore(
	pool *pgxpool.Pool,
) (*PostgresAssessmentStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &PostgresAssessmentStore{pool: pool}, nil
}

func (store *PostgresAssessmentStore) LoadAssessment(
	ctx context.Context,
	receiptID string,
	metricYear int,
) (AssessmentLoad, error) {
	if err := ctx.Err(); err != nil {
		return AssessmentLoad{}, err
	}
	if store == nil || store.pool == nil {
		return AssessmentLoad{}, errors.New("PostgresAssessmentStore is not initialized")
	}
	receiptID = strings.TrimSpace(receiptID)
	if receiptID == "" {
		return AssessmentLoad{}, errors.New("JCR import receipt is required")
	}

	var receiptExists bool
	if err := store.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM jcr_import_receipts
			WHERE id = $1
		)
	`, receiptID).Scan(&receiptExists); err != nil {
		return AssessmentLoad{}, fmt.Errorf("find JCR import receipt: %w", err)
	}
	if !receiptExists {
		return AssessmentLoad{}, fmt.Errorf(
			"JCR import receipt %q does not exist",
			receiptID,
		)
	}

	yearRows, err := store.pool.Query(ctx, `
		SELECT DISTINCT metric.metric_year
		FROM jcr_import_receipt_metrics AS receipt_metric
		JOIN venue_metric_snapshots AS metric
		  ON metric.id = receipt_metric.metric_snapshot_id
		WHERE receipt_metric.import_receipt_id = $1
		ORDER BY metric.metric_year
	`, receiptID)
	if err != nil {
		return AssessmentLoad{}, fmt.Errorf("query JCR receipt metric years: %w", err)
	}
	var metricYears []int
	for yearRows.Next() {
		var year int
		if err := yearRows.Scan(&year); err != nil {
			yearRows.Close()
			return AssessmentLoad{}, fmt.Errorf("scan JCR receipt metric year: %w", err)
		}
		metricYears = append(metricYears, year)
	}
	if err := yearRows.Err(); err != nil {
		yearRows.Close()
		return AssessmentLoad{}, fmt.Errorf("iterate JCR receipt metric years: %w", err)
	}
	yearRows.Close()
	if len(metricYears) != 1 || metricYears[0] != metricYear {
		return AssessmentLoad{}, fmt.Errorf(
			"JCR import receipt %q metric year evidence %v does not match requested metric year %d",
			receiptID,
			metricYears,
			metricYear,
		)
	}

	rows, err := store.pool.Query(ctx, `
		SELECT
			venue.id::text,
			venue.venue_type,
			venue.display_title,
			venue.issn_l,
			venue.issn,
			venue.eissn,
			metric.category,
			metric.jif::text,
			metric.quartile,
			metric.metric_status,
			metric.source_name
		FROM venues AS venue
		LEFT JOIN (
			SELECT metric.*
			FROM jcr_import_receipt_metrics AS receipt_metric
			JOIN venue_metric_snapshots AS metric
			  ON metric.id = receipt_metric.metric_snapshot_id
			WHERE receipt_metric.import_receipt_id = $1
			  AND metric.metric_year = $2
		) AS metric
		  ON metric.venue_id = venue.id
		ORDER BY venue.id, metric.category
	`, receiptID, metricYear)
	if err != nil {
		return AssessmentLoad{}, fmt.Errorf("query Venue assessment inputs: %w", err)
	}
	defer rows.Close()

	venues := make([]AssessmentVenue, 0)
	venueIndexes := make(map[string]int)
	for rows.Next() {
		var (
			venueID, rawVenueType, displayTitle string
			issnL, printISSN, electronicISSN    pgtype.Text
			category, jif, quartile             pgtype.Text
			rawStatus, sourceName               pgtype.Text
		)
		if err := rows.Scan(
			&venueID,
			&rawVenueType,
			&displayTitle,
			&issnL,
			&printISSN,
			&electronicISSN,
			&category,
			&jif,
			&quartile,
			&rawStatus,
			&sourceName,
		); err != nil {
			return AssessmentLoad{}, fmt.Errorf("scan Venue assessment input: %w", err)
		}
		index, found := venueIndexes[venueID]
		if !found {
			identifiers, restoreErr := postgresVenueISSNs(
				issnL,
				printISSN,
				electronicISSN,
			)
			if restoreErr != nil {
				return AssessmentLoad{}, fmt.Errorf(
					"restore Venue %q ISSNs: %w",
					venueID,
					restoreErr,
				)
			}
			alias, restoreErr := NewAlias(displayTitle, "venue_registry")
			if restoreErr != nil {
				return AssessmentLoad{}, fmt.Errorf(
					"restore Venue %q display title: %w",
					venueID,
					restoreErr,
				)
			}
			item, restoreErr := NewVenue(
				venueID,
				VenueType(rawVenueType),
				identifiers,
				[]Alias{alias},
			)
			if restoreErr != nil {
				return AssessmentLoad{}, fmt.Errorf(
					"restore Venue %q: %w",
					venueID,
					restoreErr,
				)
			}
			index = len(venues)
			venueIndexes[venueID] = index
			venues = append(venues, AssessmentVenue{Venue: item})
		}
		if !category.Valid {
			continue
		}
		var decimal *Decimal
		if jif.Valid {
			parsed, parseErr := ParseDecimal(jif.String)
			if parseErr != nil {
				return AssessmentLoad{}, fmt.Errorf(
					"restore Venue %q JIF: %w",
					venueID,
					parseErr,
				)
			}
			decimal = &parsed
		}
		snapshot, restoreErr := NewMetricSnapshot(
			venueID,
			metricYear,
			category.String,
			decimal,
			Quartile(quartile.String),
			MetricStatus(rawStatus.String),
			sourceName.String,
		)
		if restoreErr != nil {
			return AssessmentLoad{}, fmt.Errorf(
				"restore Venue %q metric category %q: %w",
				venueID,
				category.String,
				restoreErr,
			)
		}
		venues[index].Metrics = append(venues[index].Metrics, snapshot)
	}
	if err := rows.Err(); err != nil {
		return AssessmentLoad{}, fmt.Errorf("iterate Venue assessment inputs: %w", err)
	}
	return AssessmentLoad{
		JCRImportReceiptID: receiptID,
		MetricYear:         metricYear,
		Venues:             venues,
	}, nil
}

func (store *PostgresAssessmentStore) PersistAssessments(
	ctx context.Context,
	batch AssessmentBatch,
) (AssessmentSummary, error) {
	if err := ctx.Err(); err != nil {
		return AssessmentSummary{}, err
	}
	if store == nil || store.pool == nil {
		return AssessmentSummary{}, errors.New("PostgresAssessmentStore is not initialized")
	}
	if batch.PolicyVersion != JournalJIFOrQ1PolicyVersion {
		return AssessmentSummary{}, fmt.Errorf(
			"unsupported policy version %q",
			batch.PolicyVersion,
		)
	}
	if batch.JCRImportReceiptID == "" || batch.AssessedAt.IsZero() {
		return AssessmentSummary{}, errors.New(
			"assessment batch requires a JCR receipt and assessed_at",
		)
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.Serializable,
	})
	if err != nil {
		return AssessmentSummary{}, fmt.Errorf(
			"begin serializable Venue assessment transaction: %w",
			err,
		)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	policyID, err := ensureJournalPolicyVersion(ctx, tx, batch.AssessedAt)
	if err != nil {
		return AssessmentSummary{}, err
	}
	assessments := slices.Clone(batch.Assessments)
	slices.SortFunc(assessments, func(left, right Assessment) int {
		return strings.Compare(left.VenueID(), right.VenueID())
	})
	for _, assessment := range assessments {
		if assessment.JCRImportReceiptID != batch.JCRImportReceiptID ||
			assessment.Result.PolicyVersion() != batch.PolicyVersion ||
			assessment.Result.MetricYear() != batch.MetricYear ||
			!assessment.Result.EvaluatedAt().Equal(batch.AssessedAt.UTC()) {
			return AssessmentSummary{}, fmt.Errorf(
				"Venue %q assessment metadata does not match batch",
				assessment.VenueID(),
			)
		}
		matchedRules := assessment.Result.MatchedRules()
		if matchedRules == nil {
			matchedRules = make([]MatchedRule, 0)
		}
		rawRules, err := json.Marshal(matchedRules)
		if err != nil {
			return AssessmentSummary{}, fmt.Errorf(
				"encode Venue %q matched rules: %w",
				assessment.VenueID(),
				err,
			)
		}
		rawEvidence, err := json.Marshal(
			postgresAssessmentEvidence(assessment),
		)
		if err != nil {
			return AssessmentSummary{}, fmt.Errorf(
				"encode Venue %q assessment evidence: %w",
				assessment.VenueID(),
				err,
			)
		}
		if err := persistPostgresAssessment(
			ctx,
			tx,
			policyID,
			assessment,
			rawRules,
			rawEvidence,
		); err != nil {
			return AssessmentSummary{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return AssessmentSummary{}, fmt.Errorf(
			"commit Venue assessments: %w",
			err,
		)
	}
	return summarizeAssessments(assessments), nil
}

type postgresAssessmentCategoryEvidence struct {
	Category   string  `json:"category"`
	JIF        *string `json:"jif"`
	Quartile   string  `json:"quartile,omitempty"`
	Status     string  `json:"status"`
	SourceName string  `json:"source_name"`
}

type postgresAssessmentEvidencePayload struct {
	JCRImportReceiptID string                               `json:"jcr_import_receipt_id"`
	PolicyVersion      string                               `json:"policy_version"`
	MetricYear         int                                  `json:"metric_year"`
	VenueType          string                               `json:"venue_type"`
	Reason             string                               `json:"reason,omitempty"`
	Categories         []postgresAssessmentCategoryEvidence `json:"categories"`
}

func postgresAssessmentEvidence(
	assessment Assessment,
) postgresAssessmentEvidencePayload {
	categories := make([]postgresAssessmentCategoryEvidence, 0)
	for _, evidence := range assessment.Result.CategoryEvidence() {
		category := postgresAssessmentCategoryEvidence{
			Category:   evidence.Category(),
			Quartile:   string(evidence.Quartile()),
			Status:     string(evidence.Status()),
			SourceName: evidence.Source(),
		}
		if evidence.HasJIF() {
			value := evidence.JIF().String()
			category.JIF = &value
		}
		categories = append(categories, category)
	}
	payload := postgresAssessmentEvidencePayload{
		JCRImportReceiptID: assessment.JCRImportReceiptID,
		PolicyVersion:      assessment.Result.PolicyVersion(),
		MetricYear:         assessment.Result.MetricYear(),
		VenueType:          string(assessment.Venue.Type()),
		Categories:         categories,
	}
	switch assessment.Result.Decision() {
	case PolicyDecisionNotApplicable:
		payload.Reason = "venue_type_not_journal"
	case PolicyDecisionUnknown:
		payload.Reason = "missing_or_unknown_metric_evidence"
	case PolicyDecisionRejected:
		payload.Reason = "no_policy_rule_matched"
	}
	return payload
}

func ensureJournalPolicyVersion(
	ctx context.Context,
	tx pgx.Tx,
	effectiveAt time.Time,
) (string, error) {
	const definition = `{
		"logic":"OR",
		"accept":["jcr_q1","jif_gte_10"],
		"policy_version":"journal-jif-or-q1/v1"
	}`
	if _, err := tx.Exec(ctx, `
		INSERT INTO venue_policy_versions (
			policy_name,
			version_number,
			definition,
			effective_at
		) VALUES (
			'journal-jif-or-q1',
			1,
			$1::jsonb,
			$2
		)
		ON CONFLICT (policy_name, version_number) DO NOTHING
	`, definition, effectiveAt.UTC()); err != nil {
		return "", fmt.Errorf("persist journal policy version: %w", err)
	}
	var policyID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text
		FROM venue_policy_versions
		WHERE policy_name = 'journal-jif-or-q1'
		  AND version_number = 1
		  AND definition = $1::jsonb
	`, definition).Scan(&policyID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New(
				"stored journal policy version conflicts with journal-jif-or-q1/v1",
			)
		}
		return "", fmt.Errorf("load journal policy version: %w", err)
	}
	return policyID, nil
}

func persistPostgresAssessment(
	ctx context.Context,
	tx pgx.Tx,
	policyID string,
	assessment Assessment,
	rawRules []byte,
	rawEvidence []byte,
) error {
	commandTag, err := tx.Exec(ctx, `
		INSERT INTO venue_policy_assessments (
			venue_id,
			policy_version_id,
			metric_year,
			decision,
			matched_rules,
			evidence,
			assessed_at
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			$5::jsonb,
			$6::jsonb,
			$7
		)
		ON CONFLICT (venue_id, policy_version_id, metric_year) DO NOTHING
	`,
		assessment.VenueID(),
		policyID,
		assessment.Result.MetricYear(),
		assessment.Result.Decision(),
		rawRules,
		rawEvidence,
		assessment.Result.EvaluatedAt(),
	)
	if err != nil {
		return fmt.Errorf(
			"persist Venue %q assessment: %w",
			assessment.VenueID(),
			err,
		)
	}
	if commandTag.RowsAffected() == 1 {
		return nil
	}
	var identical bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM venue_policy_assessments
			WHERE venue_id = $1
			  AND policy_version_id = $2
			  AND metric_year = $3
			  AND decision = $4
			  AND matched_rules = $5::jsonb
			  AND evidence = $6::jsonb
			  AND assessed_at = $7
		)
	`,
		assessment.VenueID(),
		policyID,
		assessment.Result.MetricYear(),
		assessment.Result.Decision(),
		rawRules,
		rawEvidence,
		assessment.Result.EvaluatedAt(),
	).Scan(&identical); err != nil {
		return fmt.Errorf(
			"verify Venue %q assessment replay: %w",
			assessment.VenueID(),
			err,
		)
	}
	if !identical {
		return fmt.Errorf(
			"Venue %q already has a conflicting immutable assessment",
			assessment.VenueID(),
		)
	}
	return nil
}
