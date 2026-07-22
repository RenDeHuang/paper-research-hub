package urlverify

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrSelectionLimit = errors.New(
	"official URL selection limit must be between 1 and 1000",
)

type CandidateSelection struct {
	PolicyVersion string
	At            time.Time
	Limit         int
}

func (selection CandidateSelection) Validate() error {
	if !exactNonEmpty(selection.PolicyVersion) {
		return errors.New(
			"official URL candidate selection requires an exact policy version",
		)
	}
	if selection.At.IsZero() {
		return errors.New(
			"official URL candidate selection time is required",
		)
	}
	return validateSelectionLimit(selection.Limit)
}

func validateSelectionLimit(limit int) error {
	if limit < 1 || limit > 1000 {
		return ErrSelectionLimit
	}
	return nil
}

func (store *PostgresStore) ListCurrentProjectionRefs(
	ctx context.Context,
	limit int,
) ([]CurrentProjectionRef, error) {
	if err := validateStoreContext(ctx); err != nil {
		return nil, err
	}
	if err := store.validate(); err != nil {
		return nil, err
	}
	if err := validateSelectionLimit(limit); err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `
		SELECT
			state.work_id::text,
			state.normalized_assertion_id::text
		FROM work_projection_states AS state
		JOIN ingestion_normalized_records AS normalized
		  ON normalized.id = state.normalized_assertion_id
		 AND normalized.raw_event_id = state.raw_event_id
		 AND normalized.source_record_uuid =
		     state.source_record_uuid
		WHERE normalized.payload_schema_version =
		      'normalized-record/v4'
		  AND jsonb_typeof(
		      normalized.normalized_payload->'url_candidates'
		  ) = 'array'
		  AND jsonb_array_length(
		      normalized.normalized_payload->'url_candidates'
		  ) > 0
		  AND EXISTS (
		      SELECT 1
		      FROM jsonb_array_elements(
		          normalized.normalized_payload->'url_candidates'
		      ) WITH ORDINALITY AS source_candidate(value, ordinal)
		      WHERE NOT EXISTS (
		          SELECT 1
		          FROM work_url_candidates AS imported_candidate
		          WHERE imported_candidate.normalized_assertion_id =
		                state.normalized_assertion_id
		            AND imported_candidate.source_path =
		                source_candidate.value->>'source_path'
		            AND imported_candidate.candidate_url =
		                source_candidate.value->>'url'
		      )
		        AND NOT EXISTS (
		          SELECT 1
		          FROM work_url_candidate_import_rejections
		              AS rejected_candidate
		          WHERE rejected_candidate.normalized_assertion_id =
		                state.normalized_assertion_id
		            AND rejected_candidate.candidate_ordinal =
		                source_candidate.ordinal - 1
		            AND rejected_candidate.candidate_payload_hash =
		                encode(
		                    sha256(
		                        convert_to(
		                            source_candidate.value::text,
		                            'UTF8'
		                        )
		                    ),
		                    'hex'
		                )
		      )
		  )
		ORDER BY
			state.source_time,
			state.work_id,
			state.normalized_assertion_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf(
			"list current normalized URL projection references: %w",
			err,
		)
	}
	defer rows.Close()

	references := make([]CurrentProjectionRef, 0)
	for rows.Next() {
		var reference CurrentProjectionRef
		if err := rows.Scan(
			&reference.WorkID,
			&reference.NormalizedAssertionID,
		); err != nil {
			return nil, fmt.Errorf(
				"scan current normalized URL projection reference: %w",
				err,
			)
		}
		if err := reference.Validate(); err != nil {
			return nil, err
		}
		references = append(references, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate current normalized URL projection references: %w",
			err,
		)
	}
	return references, nil
}

func (store *PostgresStore) ListVerificationCandidates(
	ctx context.Context,
	selection CandidateSelection,
) ([]Candidate, error) {
	if err := validateStoreContext(ctx); err != nil {
		return nil, err
	}
	if err := store.validate(); err != nil {
		return nil, err
	}
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	at := normalizeTime(selection.At)
	rows, err := store.pool.Query(ctx, `
		SELECT
			candidate.id::text,
			candidate.work_id::text,
			candidate.projection_assertion_id::text,
			candidate.normalized_assertion_id::text,
			candidate.source_record_id::text,
			candidate.source_path,
			candidate.content_channel,
			candidate.link_role,
			candidate.candidate_url,
			candidate.parser_version,
			candidate.identifier_scheme,
			candidate.identifier_value,
			candidate.asserted_at
		FROM work_url_candidates AS candidate
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.id = candidate.projection_assertion_id
		 AND assertion.work_id = candidate.work_id
		 AND assertion.normalized_assertion_id =
		     candidate.normalized_assertion_id
		 AND assertion.source_record_uuid =
		     candidate.source_record_id
		JOIN work_projection_states AS state
		  ON state.work_id = candidate.work_id
		 AND state.normalized_assertion_id =
		     candidate.normalized_assertion_id
		 AND state.raw_event_id = assertion.raw_event_id
		 AND state.source_record_uuid =
		     assertion.source_record_uuid
		 AND state.scope_policy_version =
		     assertion.scope_policy_version
		 AND state.projection_policy_version =
		     assertion.projection_policy_version
		WHERE candidate.link_role IN (
			'official_article',
			'doi_url',
			'official_preprint',
			'official_proceeding'
		)
		  AND NOT EXISTS (
			SELECT 1
			FROM current_work_official_links AS link
			JOIN work_url_verifications AS verification
			  ON verification.id = link.verification_id
			WHERE link.work_id = candidate.work_id
			  AND link.link_role = candidate.link_role
			  AND link.projected_at <= $1
			  AND link.expires_at > $1
			  AND verification.verification_state = 'verified'
			  AND verification.policy_version = $2
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM work_url_verifications AS latest
			WHERE latest.id = (
				SELECT history.id
				FROM work_url_verifications AS history
				WHERE history.candidate_id = candidate.id
				  AND history.checked_at <= $1
				ORDER BY history.checked_at DESC, history.id DESC
				LIMIT 1
			)
			  AND latest.verification_state = 'failed'
			  AND latest.expires_at > $1
		  )
		ORDER BY
			candidate.asserted_at,
			candidate.work_id,
			candidate.link_role,
			candidate.source_path,
			candidate.candidate_url,
			candidate.id
		LIMIT $3
	`, at, selection.PolicyVersion, selection.Limit)
	if err != nil {
		return nil, fmt.Errorf(
			"list official URL verification candidates: %w",
			err,
		)
	}
	defer rows.Close()

	candidates := make([]Candidate, 0)
	for rows.Next() {
		candidate, found, scanErr := scanCandidate(rows)
		if scanErr != nil {
			return nil, fmt.Errorf(
				"scan official URL verification candidate: %w",
				scanErr,
			)
		}
		if !found {
			return nil, errors.New(
				"official URL verification candidate row disappeared",
			)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate official URL verification candidates: %w",
			err,
		)
	}
	return candidates, nil
}
