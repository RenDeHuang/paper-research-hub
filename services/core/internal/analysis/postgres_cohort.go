package analysis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type cohortWork struct {
	WorkID       uuid.UUID
	VenueID      uuid.UUID
	VenueType    string
	PublishedAt  time.Time
	SubjectIDs   []uuid.UUID
	SubjectSlugs []string
}

type cohortMeshDescriptor struct {
	WorkID        uuid.UUID
	DescriptorID  uuid.UUID
	DescriptorUI  string
	DescriptorLbl string
}

type cohortPublicationType struct {
	WorkID             uuid.UUID
	PublicationTypeID  uuid.UUID
	PublicationTypeUI  string
	PublicationTypeLbl string
}

type cohortMethod struct {
	WorkID     uuid.UUID
	MethodID   uuid.UUID
	MethodName string
}

type cohortCorrespondingInstitution struct {
	WorkID          uuid.UUID
	InstitutionID   uuid.UUID
	InstitutionName string
}

type cohortCitationPercentile struct {
	WorkID     uuid.UUID
	SubjectID  uuid.UUID
	Percentile float64
}

func loadAcceptedCohortWorks(
	ctx context.Context,
	tx pgx.Tx,
	subjectVersionID uuid.UUID,
	eligibilityPolicyVersion string,
	metricYear int,
	jcrImportReceipt uuid.UUID,
	venuePolicyName string,
	venuePolicyRevision int,
) ([]cohortWork, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			work.id,
			work.venue_id,
			venue.venue_type,
			work.published_at
		FROM works AS work
		JOIN biomedical_publication_eligibility_decisions AS eligibility
		  ON eligibility.work_id = work.id
		 AND eligibility.policy_version = $1
		 AND eligibility.metric_year = $2
		 AND eligibility.subject_version_id = $3
		 AND eligibility.decision = 'accepted'
		JOIN venues AS venue
		  ON venue.id = work.venue_id
		 AND eligibility.venue_id = venue.id
		JOIN venue_policy_versions AS policy
		  ON policy.policy_name = $5
		 AND policy.version_number = $6
		JOIN venue_policy_assessments AS assessment
		  ON assessment.venue_id = venue.id
		 AND assessment.policy_version_id = policy.id
		 AND assessment.metric_year = $2
		 AND assessment.decision = 'accepted'
		 AND assessment.evidence ->> 'jcr_import_receipt_id' =
		     ($4::uuid)::text
		WHERE work.status = 'active'
		  AND work.published_at IS NOT NULL
		  AND venue.venue_type = 'journal'
		  AND COALESCE(venue.issn_l, venue.issn, venue.eissn) IS NOT NULL
		  AND EXISTS (
				SELECT 1
				FROM jcr_import_receipt_metrics AS receipt_metric
				JOIN venue_metric_snapshots AS metric
				  ON metric.id = receipt_metric.metric_snapshot_id
				WHERE receipt_metric.import_receipt_id = $4
				  AND metric.venue_id = venue.id
				  AND metric.metric_year = $2
				  AND metric.metric_status = 'known'
				  AND metric.registry_version = 'jcr-registry/v2'
				  AND metric.quartile = 'Q1'
		  )
		  AND EXISTS (
				SELECT 1
				FROM jcr_import_receipt_metrics AS receipt_metric
				JOIN venue_metric_snapshots AS metric
				  ON metric.id = receipt_metric.metric_snapshot_id
				JOIN journal_subject_metrics AS subject_metric
				  ON subject_metric.venue_metric_snapshot_id = metric.id
				JOIN biomedical_subject_rules AS subject_rule
				  ON subject_rule.id = subject_metric.subject_rule_id
				WHERE receipt_metric.import_receipt_id = $4
				  AND subject_metric.id =
				      eligibility.journal_subject_metric_id
				  AND metric.venue_id = venue.id
				  AND metric.metric_year = $2
				  AND subject_rule.subject_version_id = $3
				  AND subject_metric.jcr_category = metric.category
				  AND subject_rule.jcr_category = metric.category
		  )
		ORDER BY work.id
	`,
		eligibilityPolicyVersion,
		metricYear,
		subjectVersionID,
		jcrImportReceipt,
		venuePolicyName,
		venuePolicyRevision,
	)
	if err != nil {
		return nil, fmt.Errorf("query accepted cohort Works: %w", err)
	}
	defer rows.Close()

	works := make([]cohortWork, 0)
	for rows.Next() {
		var work cohortWork
		if err := rows.Scan(&work.WorkID, &work.VenueID, &work.VenueType, &work.PublishedAt); err != nil {
			return nil, fmt.Errorf("scan accepted cohort Work: %w", err)
		}
		works = append(works, work)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accepted cohort Works: %w", err)
	}
	return works, nil
}

func loadAcceptedCohortSubjects(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
	subjectVersionID uuid.UUID,
	policyVersion string,
	metricYear int,
) (map[uuid.UUID][]cohortSubject, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT
			eligibility.work_id,
			subject.id,
			subject.slug
		FROM biomedical_publication_eligibility_decisions AS eligibility
		JOIN journal_subject_metrics AS metric
		  ON metric.id = eligibility.journal_subject_metric_id
		JOIN biomedical_subject_rules AS rule
		  ON rule.id = metric.subject_rule_id
		JOIN subjects AS subject
		  ON subject.id = rule.subject_id
		 AND subject.subject_version_id = rule.subject_version_id
		WHERE eligibility.work_id = ANY($1::uuid[])
		  AND eligibility.policy_version = $2
		  AND eligibility.metric_year = $3
		  AND eligibility.subject_version_id = $4
		  AND eligibility.decision = 'accepted'
		ORDER BY eligibility.work_id, subject.id
	`, workIDs, policyVersion, metricYear, subjectVersionID)
	if err != nil {
		return nil, fmt.Errorf("query accepted cohort subjects: %w", err)
	}
	defer rows.Close()

	result := make(map[uuid.UUID][]cohortSubject, len(workIDs))
	for rows.Next() {
		var row cohortSubject
		if err := rows.Scan(&row.WorkID, &row.SubjectID, &row.SubjectSlug); err != nil {
			return nil, fmt.Errorf("scan accepted cohort subject: %w", err)
		}
		result[row.WorkID] = append(result[row.WorkID], row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accepted cohort subjects: %w", err)
	}
	return result, nil
}

func loadAcceptedCohortMeshDescriptors(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
) (map[uuid.UUID][]cohortMeshDescriptor, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT
			heading.work_id,
			descriptor.id,
			descriptor.descriptor_ui,
			heading.descriptor_label
		FROM work_mesh_headings AS heading
		JOIN mesh_descriptors AS descriptor
		  ON descriptor.id = heading.descriptor_id
		WHERE heading.work_id = ANY($1::uuid[])
		ORDER BY heading.work_id, descriptor.descriptor_ui, heading.descriptor_label
	`, workIDs)
	if err != nil {
		return nil, fmt.Errorf("query accepted cohort MeSH descriptors: %w", err)
	}
	defer rows.Close()

	result := make(map[uuid.UUID][]cohortMeshDescriptor, len(workIDs))
	for rows.Next() {
		var row cohortMeshDescriptor
		if err := rows.Scan(&row.WorkID, &row.DescriptorID, &row.DescriptorUI, &row.DescriptorLbl); err != nil {
			return nil, fmt.Errorf("scan accepted cohort MeSH descriptor: %w", err)
		}
		result[row.WorkID] = append(result[row.WorkID], row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accepted cohort MeSH descriptors: %w", err)
	}
	return result, nil
}

func loadAcceptedCohortPublicationTypes(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
) (map[uuid.UUID][]cohortPublicationType, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT
			assertion.work_id,
			publication_type.id,
			publication_type.publication_type_ui,
			assertion.publication_type_label
		FROM work_publication_types AS assertion
		JOIN publication_types AS publication_type
		  ON publication_type.id = assertion.publication_type_id
		WHERE assertion.work_id = ANY($1::uuid[])
		ORDER BY assertion.work_id, publication_type.publication_type_ui, assertion.publication_type_label
	`, workIDs)
	if err != nil {
		return nil, fmt.Errorf("query accepted cohort publication types: %w", err)
	}
	defer rows.Close()

	result := make(map[uuid.UUID][]cohortPublicationType, len(workIDs))
	for rows.Next() {
		var row cohortPublicationType
		if err := rows.Scan(&row.WorkID, &row.PublicationTypeID, &row.PublicationTypeUI, &row.PublicationTypeLbl); err != nil {
			return nil, fmt.Errorf("scan accepted cohort publication type: %w", err)
		}
		result[row.WorkID] = append(result[row.WorkID], row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accepted cohort publication types: %w", err)
	}
	return result, nil
}

func loadAcceptedCohortMethods(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
) (map[uuid.UUID][]cohortMethod, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT
			assertion.work_id,
			method.id,
			method.name
		FROM work_methods AS assertion
		JOIN methods AS method
		  ON method.id = assertion.method_id
		WHERE assertion.work_id = ANY($1::uuid[])
		ORDER BY assertion.work_id, method.name
	`, workIDs)
	if err != nil {
		return nil, fmt.Errorf("query accepted cohort methods: %w", err)
	}
	defer rows.Close()

	result := make(map[uuid.UUID][]cohortMethod, len(workIDs))
	for rows.Next() {
		var row cohortMethod
		if err := rows.Scan(&row.WorkID, &row.MethodID, &row.MethodName); err != nil {
			return nil, fmt.Errorf("scan accepted cohort method: %w", err)
		}
		result[row.WorkID] = append(result[row.WorkID], row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accepted cohort methods: %w", err)
	}
	return result, nil
}

func loadAcceptedCohortCorrespondingInstitutions(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
) (map[uuid.UUID][]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT
			work_authors.work_id,
			work_authors.institution_id
		FROM work_authors
		WHERE work_authors.work_id = ANY($1::uuid[])
		  AND work_authors.is_corresponding = true
		  AND work_authors.institution_id IS NOT NULL
		ORDER BY work_authors.work_id, work_authors.institution_id
	`, workIDs)
	if err != nil {
		return nil, fmt.Errorf("query accepted cohort corresponding institutions: %w", err)
	}
	defer rows.Close()

	result := make(map[uuid.UUID][]uuid.UUID, len(workIDs))
	for rows.Next() {
		var workID, institutionID uuid.UUID
		if err := rows.Scan(&workID, &institutionID); err != nil {
			return nil, fmt.Errorf("scan accepted cohort corresponding institution: %w", err)
		}
		result[workID] = append(result[workID], institutionID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accepted cohort corresponding institutions: %w", err)
	}
	return result, nil
}

func loadAcceptedCohortCitationPercentiles(
	ctx context.Context,
	tx pgx.Tx,
	runID uuid.UUID,
) ([]cohortCitationPercentile, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			work_id,
			subject_id,
			citation_percentile
		FROM citation_analysis_percentiles
		WHERE analysis_run_id = $1
		  AND percentile_state = 'known'
		ORDER BY work_id, subject_id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("query citation analysis percentiles: %w", err)
	}
	defer rows.Close()

	result := make([]cohortCitationPercentile, 0)
	for rows.Next() {
		var row cohortCitationPercentile
		if err := rows.Scan(&row.WorkID, &row.SubjectID, &row.Percentile); err != nil {
			return nil, fmt.Errorf("scan citation analysis percentile: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate citation analysis percentiles: %w", err)
	}
	return result, nil
}

type cohortSubject struct {
	WorkID      uuid.UUID
	SubjectID   uuid.UUID
	SubjectSlug string
}
