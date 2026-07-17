package biomed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrPublicEligibilityConflict = errors.New(
	"conflicting immutable biomedical public eligibility",
)

type PostgresPublicEligibilityStore struct {
	pool *pgxpool.Pool
}

var _ PublicEligibilityStore = (*PostgresPublicEligibilityStore)(nil)
var _ PublicEligibilityReader = (*PostgresPublicEligibilityStore)(nil)

func NewPostgresPublicEligibilityStore(
	pool *pgxpool.Pool,
) (*PostgresPublicEligibilityStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &PostgresPublicEligibilityStore{pool: pool}, nil
}

func (store *PostgresPublicEligibilityStore) LoadEligibility(
	ctx context.Context,
	workID string,
	metricYear int,
	subjectVersionKey string,
) (PublicEligibilityLoad, error) {
	if ctx == nil {
		return PublicEligibilityLoad{}, errors.New(
			"public eligibility context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return PublicEligibilityLoad{}, err
	}
	if store == nil || store.pool == nil {
		return PublicEligibilityLoad{}, errors.New(
			"PostgresPublicEligibilityStore is not initialized",
		)
	}
	if strings.TrimSpace(workID) == "" || workID != strings.TrimSpace(workID) {
		return PublicEligibilityLoad{}, errors.New(
			"public eligibility requires an exact Work ID",
		)
	}
	if metricYear < 1900 || metricYear > 3000 {
		return PublicEligibilityLoad{}, fmt.Errorf(
			"public eligibility metric year %d is outside 1900..3000",
			metricYear,
		)
	}
	if strings.TrimSpace(subjectVersionKey) == "" ||
		subjectVersionKey != strings.TrimSpace(subjectVersionKey) {
		return PublicEligibilityLoad{}, errors.New(
			"public eligibility requires an exact Subject version key",
		)
	}

	var subjectVersionID string
	if err := store.pool.QueryRow(ctx, `
		SELECT id::text
		FROM subject_versions
		WHERE version_key = $1
	`, subjectVersionKey).Scan(&subjectVersionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicEligibilityLoad{}, fmt.Errorf(
				"Subject version %q does not exist",
				subjectVersionKey,
			)
		}
		return PublicEligibilityLoad{}, fmt.Errorf(
			"load exact Subject version %q: %w",
			subjectVersionKey,
			err,
		)
	}

	var (
		venueID, venueType               pgtype.Text
		issnL, printISSN, electronicISSN pgtype.Text
	)
	if err := store.pool.QueryRow(ctx, `
		SELECT
			work.venue_id::text,
			venue.venue_type,
			venue.issn_l,
			venue.issn,
			venue.eissn
		FROM works AS work
		LEFT JOIN venues AS venue
		  ON venue.id = work.venue_id
		WHERE work.id = $1
	`, workID).Scan(
		&venueID,
		&venueType,
		&issnL,
		&printISSN,
		&electronicISSN,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicEligibilityLoad{}, fmt.Errorf(
				"Work %q does not exist",
				workID,
			)
		}
		return PublicEligibilityLoad{}, fmt.Errorf(
			"load Work Venue evidence: %w",
			err,
		)
	}

	load := PublicEligibilityLoad{
		WorkID:            workID,
		MetricYear:        metricYear,
		SubjectVersionID:  subjectVersionID,
		SubjectVersionKey: subjectVersionKey,
	}
	if !venueID.Valid {
		load.MissingReason = PublicEligibilityReasonVenueMissing
		return load, nil
	}
	if !venueType.Valid ||
		venueType.String != "journal" ||
		(!issnL.Valid && !printISSN.Valid && !electronicISSN.Valid) {
		load.MissingReason =
			PublicEligibilityReasonVenueNotVerifiedJournal
		return load, nil
	}
	load.Venue = &PublicEligibilityVenueEvidence{
		VenueID: venueID.String,
		ISSNL:   nullablePostgresText(issnL),
		ISSN:    nullablePostgresText(printISSN),
		EISSN:   nullablePostgresText(electronicISSN),
	}

	rows, err := store.pool.Query(ctx, `
		SELECT
			metric.id::text,
			metric.venue_id::text,
			metric.metric_year,
			metric.category
		FROM venue_metric_snapshots AS metric
		WHERE metric.venue_id = $1
		  AND metric.metric_year = $2
		ORDER BY metric.id
	`, venueID.String, metricYear)
	if err != nil {
		return PublicEligibilityLoad{}, fmt.Errorf(
			"query declared-year journal metrics: %w",
			err,
		)
	}
	for rows.Next() {
		var metric PublicEligibilityMetricEvidence
		if err := rows.Scan(
			&metric.VenueMetricSnapshotID,
			&metric.VenueID,
			&metric.MetricYear,
			&metric.JCRCategory,
		); err != nil {
			rows.Close()
			return PublicEligibilityLoad{}, fmt.Errorf(
				"scan declared-year journal metric: %w",
				err,
			)
		}
		load.Metrics = append(load.Metrics, metric)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PublicEligibilityLoad{}, fmt.Errorf(
			"iterate declared-year journal metrics: %w",
			err,
		)
	}
	rows.Close()
	if len(load.Metrics) == 0 {
		load.MissingReason = PublicEligibilityReasonMetricYearMissing
		return load, nil
	}

	rows, err = store.pool.Query(ctx, `
		SELECT
			link.id::text,
			metric.id::text,
			metric.venue_id::text,
			metric.metric_year,
			version.id::text,
			subject.id::text,
			rule.id::text,
			subject.slug,
			link.jcr_category
		FROM journal_subject_metrics AS link
		JOIN venue_metric_snapshots AS metric
		  ON metric.id = link.venue_metric_snapshot_id
		JOIN biomedical_subject_rules AS rule
		  ON rule.id = link.subject_rule_id
		JOIN subjects AS subject
		  ON subject.id = rule.subject_id
		 AND subject.subject_version_id = rule.subject_version_id
		JOIN subject_versions AS version
		  ON version.id = rule.subject_version_id
		WHERE metric.venue_id = $1
		  AND metric.metric_year = $2
		  AND version.id = $3
		  AND version.version_key = $4
		ORDER BY link.id
	`, venueID.String, metricYear, subjectVersionID, subjectVersionKey)
	if err != nil {
		return PublicEligibilityLoad{}, fmt.Errorf(
			"query exact biomedical Subject metric evidence: %w",
			err,
		)
	}
	for rows.Next() {
		var match PublicEligibilitySubjectEvidence
		if err := rows.Scan(
			&match.JournalSubjectMetricID,
			&match.VenueMetricSnapshotID,
			&match.VenueID,
			&match.MetricYear,
			&match.SubjectVersionID,
			&match.SubjectID,
			&match.SubjectRuleID,
			&match.SubjectSlug,
			&match.JCRCategory,
		); err != nil {
			rows.Close()
			return PublicEligibilityLoad{}, fmt.Errorf(
				"scan exact biomedical Subject metric evidence: %w",
				err,
			)
		}
		load.Matches = append(load.Matches, match)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PublicEligibilityLoad{}, fmt.Errorf(
			"iterate exact biomedical Subject metric evidence: %w",
			err,
		)
	}
	rows.Close()
	if err := load.validate(); err != nil {
		return PublicEligibilityLoad{}, fmt.Errorf(
			"validate PostgreSQL public eligibility evidence: %w",
			err,
		)
	}
	return load, nil
}

type postgresPublicEligibilityEvidence struct {
	PolicyVersion     string                             `json:"policy_version"`
	MetricYear        int                                `json:"metric_year"`
	SubjectVersionID  string                             `json:"subject_version_id"`
	SubjectVersionKey string                             `json:"subject_version_key"`
	Reason            PublicEligibilityReason            `json:"reason,omitempty"`
	Venue             *PublicEligibilityVenueEvidence    `json:"venue,omitempty"`
	Metrics           []PublicEligibilityMetricEvidence  `json:"metrics"`
	Matches           []PublicEligibilitySubjectEvidence `json:"matches"`
}

func (store *PostgresPublicEligibilityStore) PersistEligibility(
	ctx context.Context,
	assessment PublicEligibilityAssessment,
) (PublicEligibilityAssessment, error) {
	if ctx == nil {
		return PublicEligibilityAssessment{}, errors.New(
			"public eligibility context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return PublicEligibilityAssessment{}, err
	}
	if store == nil || store.pool == nil {
		return PublicEligibilityAssessment{}, errors.New(
			"PostgresPublicEligibilityStore is not initialized",
		)
	}
	assessment = assessment.Clone()
	if err := assessment.Validate(); err != nil {
		return PublicEligibilityAssessment{}, err
	}
	payload := postgresEligibilityEvidence(assessment)
	rawEvidence, err := json.Marshal(payload)
	if err != nil {
		return PublicEligibilityAssessment{}, fmt.Errorf(
			"encode biomedical public eligibility evidence: %w",
			err,
		)
	}
	venueID := ""
	if assessment.Evidence.Venue != nil {
		venueID = assessment.Evidence.Venue.VenueID
	}
	decisiveLinkID := assessment.DecisiveJournalSubjectMetricID()

	identical, found, err := store.existingEligibilityMatches(
		ctx,
		assessment,
		rawEvidence,
		venueID,
		decisiveLinkID,
	)
	if err != nil {
		return PublicEligibilityAssessment{}, err
	}
	if found {
		if !identical {
			return PublicEligibilityAssessment{}, fmt.Errorf(
				"%w for Work %q policy %q year %d Subject version %q",
				ErrPublicEligibilityConflict,
				assessment.WorkID,
				assessment.PolicyVersion,
				assessment.MetricYear,
				assessment.SubjectVersionID,
			)
		}
		return assessment.Clone(), nil
	}

	command, err := store.pool.Exec(ctx, `
		INSERT INTO biomedical_publication_eligibility_decisions (
			work_id,
			policy_version,
			metric_year,
			subject_version_id,
			decision,
			venue_id,
			journal_subject_metric_id,
			evidence,
			assessed_at
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			$5,
			NULLIF($6, '')::uuid,
			NULLIF($7, '')::uuid,
			$8::jsonb,
			$9
		)
		ON CONFLICT (
			work_id,
			policy_version,
			metric_year,
			subject_version_id
		) DO NOTHING
	`,
		assessment.WorkID,
		assessment.PolicyVersion,
		assessment.MetricYear,
		assessment.SubjectVersionID,
		assessment.Decision,
		venueID,
		decisiveLinkID,
		rawEvidence,
		assessment.AssessedAt,
	)
	if err != nil {
		return PublicEligibilityAssessment{}, fmt.Errorf(
			"persist biomedical public eligibility for Work %q: %w",
			assessment.WorkID,
			err,
		)
	}
	if command.RowsAffected() == 1 {
		return assessment.Clone(), nil
	}
	identical, found, err = store.existingEligibilityMatches(
		ctx,
		assessment,
		rawEvidence,
		venueID,
		decisiveLinkID,
	)
	if err != nil {
		return PublicEligibilityAssessment{}, err
	}
	if found && identical {
		return assessment.Clone(), nil
	}
	return PublicEligibilityAssessment{}, fmt.Errorf(
		"%w for Work %q policy %q year %d Subject version %q",
		ErrPublicEligibilityConflict,
		assessment.WorkID,
		assessment.PolicyVersion,
		assessment.MetricYear,
		assessment.SubjectVersionID,
	)
}

func (store *PostgresPublicEligibilityStore) FindEligibility(
	ctx context.Context,
	workID string,
	policyVersion string,
	metricYear int,
	subjectVersionKey string,
) (PublicEligibilityAssessment, bool, error) {
	if ctx == nil {
		return PublicEligibilityAssessment{}, false, errors.New(
			"public eligibility context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return PublicEligibilityAssessment{}, false, err
	}
	if store == nil || store.pool == nil {
		return PublicEligibilityAssessment{}, false, errors.New(
			"PostgresPublicEligibilityStore is not initialized",
		)
	}
	if err := validatePublicEligibilityIdentity(
		workID,
		policyVersion,
		metricYear,
		subjectVersionKey,
	); err != nil {
		return PublicEligibilityAssessment{}, false, err
	}

	var (
		subjectVersionID string
		decision         PublicEligibilityDecision
		rawEvidence      []byte
		assessedAt       pgtype.Timestamptz
	)
	err := store.pool.QueryRow(ctx, `
		SELECT
			decision.subject_version_id::text,
			decision.decision,
			decision.evidence,
			decision.assessed_at
		FROM biomedical_publication_eligibility_decisions AS decision
		JOIN subject_versions AS version
		  ON version.id = decision.subject_version_id
		WHERE decision.work_id = $1
		  AND decision.policy_version = $2
		  AND decision.metric_year = $3
		  AND version.version_key = $4
	`,
		workID,
		policyVersion,
		metricYear,
		subjectVersionKey,
	).Scan(
		&subjectVersionID,
		&decision,
		&rawEvidence,
		&assessedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		var versionExists bool
		if err := store.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM subject_versions
				WHERE version_key = $1
			)
		`, subjectVersionKey).Scan(&versionExists); err != nil {
			return PublicEligibilityAssessment{}, false, fmt.Errorf(
				"verify exact Subject version for eligibility lookup: %w",
				err,
			)
		}
		if !versionExists {
			return PublicEligibilityAssessment{}, false, fmt.Errorf(
				"Subject version %q does not exist",
				subjectVersionKey,
			)
		}
		return PublicEligibilityAssessment{}, false, nil
	}
	if err != nil {
		return PublicEligibilityAssessment{}, false, fmt.Errorf(
			"find immutable biomedical public eligibility: %w",
			err,
		)
	}
	if !assessedAt.Valid {
		return PublicEligibilityAssessment{}, false, errors.New(
			"stored biomedical public eligibility has no assessed_at",
		)
	}
	var payload postgresPublicEligibilityEvidence
	if err := json.Unmarshal(rawEvidence, &payload); err != nil {
		return PublicEligibilityAssessment{}, false, fmt.Errorf(
			"decode immutable biomedical public eligibility evidence: %w",
			err,
		)
	}
	if payload.PolicyVersion != policyVersion ||
		payload.MetricYear != metricYear ||
		payload.SubjectVersionID != subjectVersionID ||
		payload.SubjectVersionKey != subjectVersionKey {
		return PublicEligibilityAssessment{}, false, errors.New(
			"stored biomedical public eligibility evidence metadata conflicts with its identity",
		)
	}
	assessment := PublicEligibilityAssessment{
		WorkID:            workID,
		PolicyVersion:     policyVersion,
		MetricYear:        metricYear,
		SubjectVersionID:  subjectVersionID,
		SubjectVersionKey: subjectVersionKey,
		Decision:          decision,
		Evidence: PublicEligibilityEvidence{
			Reason:  payload.Reason,
			Venue:   payload.Venue.clone(),
			Metrics: slices.Clone(payload.Metrics),
			Matches: slices.Clone(payload.Matches),
		},
		AssessedAt: assessedAt.Time.UTC(),
	}
	if err := assessment.Validate(); err != nil {
		return PublicEligibilityAssessment{}, false, fmt.Errorf(
			"validate stored biomedical public eligibility: %w",
			err,
		)
	}
	return assessment, true, nil
}

func (store *PostgresPublicEligibilityStore) existingEligibilityMatches(
	ctx context.Context,
	assessment PublicEligibilityAssessment,
	rawEvidence []byte,
	venueID string,
	decisiveLinkID string,
) (bool, bool, error) {
	var identical bool
	err := store.pool.QueryRow(ctx, `
		SELECT
			decision = $5
			AND venue_id IS NOT DISTINCT FROM NULLIF($6, '')::uuid
			AND journal_subject_metric_id
				IS NOT DISTINCT FROM NULLIF($7, '')::uuid
			AND evidence = $8::jsonb
			AND assessed_at = $9
		FROM biomedical_publication_eligibility_decisions
		WHERE work_id = $1
		  AND policy_version = $2
		  AND metric_year = $3
		  AND subject_version_id = $4
	`,
		assessment.WorkID,
		assessment.PolicyVersion,
		assessment.MetricYear,
		assessment.SubjectVersionID,
		assessment.Decision,
		venueID,
		decisiveLinkID,
		rawEvidence,
		assessment.AssessedAt,
	).Scan(&identical)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf(
			"load immutable biomedical public eligibility replay: %w",
			err,
		)
	}
	return identical, true, nil
}

func postgresEligibilityEvidence(
	assessment PublicEligibilityAssessment,
) postgresPublicEligibilityEvidence {
	metrics := slices.Clone(assessment.Evidence.Metrics)
	if metrics == nil {
		metrics = make([]PublicEligibilityMetricEvidence, 0)
	}
	matches := slices.Clone(assessment.Evidence.Matches)
	if matches == nil {
		matches = make([]PublicEligibilitySubjectEvidence, 0)
	}
	return postgresPublicEligibilityEvidence{
		PolicyVersion:     assessment.PolicyVersion,
		MetricYear:        assessment.MetricYear,
		SubjectVersionID:  assessment.SubjectVersionID,
		SubjectVersionKey: assessment.SubjectVersionKey,
		Reason:            assessment.Evidence.Reason,
		Venue:             assessment.Evidence.Venue.clone(),
		Metrics:           metrics,
		Matches:           matches,
	}
}

func nullablePostgresText(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
