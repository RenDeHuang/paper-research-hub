package urlverify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

var (
	ErrCurrentProjectionNotFound = errors.New(
		"specified current projection was not found",
	)
	ErrCurrentProjectionNotV4 = errors.New(
		"current projection is not normalized-record/v4",
	)
	ErrInvalidNormalizedURLCandidate = errors.New(
		"invalid normalized-record/v4 URL candidate",
	)
	ErrCandidateNotFound    = errors.New("official URL candidate was not found")
	ErrVerificationNotFound = errors.New(
		"official URL verification was not found",
	)
	ErrVerificationNotVerified = errors.New(
		"official URL verification is not verified",
	)
	ErrVerificationNotYetChecked = errors.New(
		"official URL verification is not active yet",
	)
	ErrVerificationExpired = errors.New(
		"official URL verification is expired",
	)
	ErrVerificationIntegrity = errors.New(
		"official URL verification was not produced by Verify",
	)
	ErrVerificationOlderThanCurrent = errors.New(
		"official URL verification is older than the current projection",
	)
)

type CurrentProjectionRef struct {
	WorkID                string
	NormalizedAssertionID string
}

func (reference CurrentProjectionRef) Validate() error {
	if !exactNonEmpty(reference.WorkID) ||
		!exactNonEmpty(reference.NormalizedAssertionID) {
		return errors.New(
			"current projection reference requires exact Work and normalized assertion IDs",
		)
	}
	return nil
}

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New(
			"official URL PostgreSQL pool is required",
		)
	}
	return &PostgresStore{pool: pool}, nil
}

func (store *PostgresStore) ImportCurrentProjectionCandidates(
	ctx context.Context,
	reference CurrentProjectionRef,
) ([]Candidate, error) {
	if err := validateStoreContext(ctx); err != nil {
		return nil, err
	}
	if err := store.validate(); err != nil {
		return nil, err
	}
	if err := reference.Validate(); err != nil {
		return nil, err
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead,
	})
	if err != nil {
		return nil, fmt.Errorf(
			"begin official URL candidate import: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var (
		projectionAssertionID string
		sourceRecordID        string
		sourceTime            time.Time
		schemaVersion         string
		payload               []byte
	)
	err = tx.QueryRow(ctx, `
		SELECT
			assertion.id::text,
			state.source_record_uuid::text,
			state.source_time,
			normalized.payload_schema_version,
			normalized.normalized_payload
		FROM work_projection_states AS state
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.work_id = state.work_id
		 AND assertion.normalized_assertion_id =
		     state.normalized_assertion_id
		 AND assertion.raw_event_id = state.raw_event_id
		 AND assertion.source_record_uuid =
		     state.source_record_uuid
		 AND assertion.scope_policy_version =
		     state.scope_policy_version
		 AND assertion.projection_policy_version =
		     state.projection_policy_version
		JOIN ingestion_normalized_records AS normalized
		  ON normalized.id = state.normalized_assertion_id
		 AND normalized.raw_event_id = state.raw_event_id
		 AND normalized.source_record_uuid =
		     state.source_record_uuid
		JOIN source_record_works AS source_work
		  ON source_work.source_record_id =
		     state.source_record_uuid
		 AND source_work.work_id = state.work_id
		WHERE state.work_id = $1
		  AND state.normalized_assertion_id = $2
		FOR SHARE OF state
	`, reference.WorkID, reference.NormalizedAssertionID).Scan(
		&projectionAssertionID,
		&sourceRecordID,
		&sourceTime,
		&schemaVersion,
		&payload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCurrentProjectionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf(
			"read exact current URL projection: %w",
			err,
		)
	}
	if schemaVersion != "normalized-record/v4" {
		return nil, fmt.Errorf(
			"%w: got %q",
			ErrCurrentProjectionNotV4,
			schemaVersion,
		)
	}

	candidates, rejections, err := decodeNormalizedURLCandidates(
		payload,
		Candidate{
			WorkID:                reference.WorkID,
			ProjectionAssertionID: projectionAssertionID,
			NormalizedAssertionID: reference.NormalizedAssertionID,
			SourceRecordID:        sourceRecordID,
			AssertedAt:            normalizeTime(sourceTime),
		},
	)
	if err != nil {
		return nil, err
	}
	persisted := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		stored, persistErr := persistCandidate(ctx, tx, candidate)
		if persistErr != nil {
			return nil, persistErr
		}
		persisted = append(persisted, stored)
	}
	for _, rejection := range rejections {
		if err := persistCandidateRejection(
			ctx,
			tx,
			Candidate{
				WorkID:                reference.WorkID,
				ProjectionAssertionID: projectionAssertionID,
				NormalizedAssertionID: reference.NormalizedAssertionID,
				SourceRecordID:        sourceRecordID,
			},
			rejection,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf(
			"commit official URL candidate import: %w",
			err,
		)
	}
	rejectionErrors := make([]error, 0, len(rejections))
	for _, rejection := range rejections {
		rejectionErrors = append(rejectionErrors, fmt.Errorf(
			"%w at index %d: %v",
			ErrInvalidNormalizedURLCandidate,
			rejection.Ordinal,
			rejection.Err,
		))
	}
	return persisted, errors.Join(rejectionErrors...)
}

func (store *PostgresStore) FindCandidate(
	ctx context.Context,
	candidateID string,
) (Candidate, bool, error) {
	if err := validateStoreContext(ctx); err != nil {
		return Candidate{}, false, err
	}
	if err := store.validate(); err != nil {
		return Candidate{}, false, err
	}
	if !exactNonEmpty(candidateID) {
		return Candidate{}, false, errors.New(
			"official URL candidate lookup requires an exact ID",
		)
	}
	candidate, found, err := scanCandidate(store.pool.QueryRow(ctx, `
		SELECT
			id::text,
			work_id::text,
			projection_assertion_id::text,
			normalized_assertion_id::text,
			source_record_id::text,
			source_path,
			content_channel,
			link_role,
			candidate_url,
			parser_version,
			identifier_scheme,
			identifier_value,
			asserted_at
		FROM work_url_candidates
		WHERE id = $1
	`, candidateID))
	if err != nil {
		return Candidate{}, false, fmt.Errorf(
			"find official URL candidate: %w",
			err,
		)
	}
	return candidate, found, nil
}

func (store *PostgresStore) PersistVerification(
	ctx context.Context,
	verification Verification,
) (Verification, error) {
	if err := validateStoreContext(ctx); err != nil {
		return Verification{}, err
	}
	if err := store.validate(); err != nil {
		return Verification{}, err
	}
	if verification.ID != "" {
		return Verification{}, errors.New(
			"new official URL verification cannot provide an ID",
		)
	}
	verification.CheckedAt = normalizeTime(verification.CheckedAt)
	verification.ExpiresAt = normalizeTime(verification.ExpiresAt)
	if err := verification.Validate(); err != nil {
		return Verification{}, err
	}
	if !verificationIntegrityValid(verification) {
		return Verification{}, ErrVerificationIntegrity
	}
	candidate, found, err := store.FindCandidate(
		ctx,
		verification.CandidateID,
	)
	if err != nil {
		return Verification{}, err
	}
	if !found {
		return Verification{}, ErrCandidateNotFound
	}
	if verification.WorkID != candidate.WorkID ||
		verification.Channel != candidate.Channel ||
		verification.LinkRole != candidate.LinkRole ||
		verification.SourceURL != candidate.URL ||
		verification.ExpectedIdentifier != candidate.Identifier {
		return Verification{}, errors.New(
			"official URL verification conflicts with its candidate",
		)
	}
	redirectChain, err := json.Marshal(verification.RedirectChain)
	if err != nil {
		return Verification{}, fmt.Errorf(
			"encode official URL redirect chain: %w",
			err,
		)
	}
	observedIdentifiers, err := json.Marshal(
		verification.ObservedIdentifiers,
	)
	if err != nil {
		return Verification{}, fmt.Errorf(
			"encode observed official URL identifiers: %w",
			err,
		)
	}
	identifierEvidence, err := json.Marshal(
		verification.IdentifierEvidence,
	)
	if err != nil {
		return Verification{}, fmt.Errorf(
			"encode official URL identifier evidence: %w",
			err,
		)
	}
	responseMetadata, err := json.Marshal(
		verification.ResponseMetadata,
	)
	if err != nil {
		return Verification{}, fmt.Errorf(
			"encode official URL response metadata: %w",
			err,
		)
	}
	var matchedScheme, matchedValue any
	if verification.MatchedIdentifier != nil {
		matchedScheme = verification.MatchedIdentifier.Scheme
		matchedValue = verification.MatchedIdentifier.Value
	}
	var failureCode any
	if verification.FailureCode != FailureNone {
		failureCode = string(verification.FailureCode)
	}
	err = store.pool.QueryRow(ctx, `
		SELECT persist_official_url_verification_receipt(
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15, $16,
			$17, $18, $19
		)::text
	`,
		verification.CandidateID,
		verification.SourceURL,
		verification.FinalURL,
		redirectChain,
		verification.HTTPStatus,
		verification.ExpectedIdentifier.Scheme,
		verification.ExpectedIdentifier.Value,
		observedIdentifiers,
		identifierEvidence,
		responseMetadata,
		matchedScheme,
		matchedValue,
		verification.IdentifierMatch,
		verification.State,
		verification.CheckedAt,
		verification.ExpiresAt,
		verification.VerifierVersion,
		verification.PolicyVersion,
		failureCode,
	).Scan(&verification.ID)
	if err != nil {
		return Verification{}, fmt.Errorf(
			"persist immutable official URL verification: %w",
			err,
		)
	}
	verification.integrity = [32]byte{}
	return verification.clone(), nil
}

func (store *PostgresStore) FindVerification(
	ctx context.Context,
	verificationID string,
) (Verification, bool, error) {
	if err := validateStoreContext(ctx); err != nil {
		return Verification{}, false, err
	}
	if err := store.validate(); err != nil {
		return Verification{}, false, err
	}
	if !exactNonEmpty(verificationID) {
		return Verification{}, false, errors.New(
			"official URL verification lookup requires an exact ID",
		)
	}
	verification, found, err := scanVerification(
		store.pool.QueryRow(ctx, verificationSelectSQL+`
			WHERE verification.id = $1
		`, verificationID),
	)
	if err != nil {
		return Verification{}, false, fmt.Errorf(
			"find official URL verification: %w",
			err,
		)
	}
	return verification, found, nil
}

func (store *PostgresStore) ProjectCurrent(
	ctx context.Context,
	verificationID string,
	projectedAt time.Time,
) (OfficialLink, error) {
	if err := validateStoreContext(ctx); err != nil {
		return OfficialLink{}, err
	}
	if err := store.validate(); err != nil {
		return OfficialLink{}, err
	}
	if !exactNonEmpty(verificationID) {
		return OfficialLink{}, errors.New(
			"current official link projection requires an exact verification ID",
		)
	}
	if projectedAt.IsZero() {
		return OfficialLink{}, errors.New(
			"current official link projection time is required",
		)
	}
	projectedAt = normalizeTime(projectedAt)

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OfficialLink{}, fmt.Errorf(
			"begin current official link projection: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var (
		workID    string
		channel   string
		linkRole  string
		finalURL  string
		state     string
		checkedAt time.Time
		expiresAt time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT
			candidate.work_id::text,
			candidate.content_channel,
			candidate.link_role,
			verification.final_url,
			verification.verification_state,
			verification.checked_at,
			verification.expires_at
		FROM work_url_verifications AS verification
		JOIN work_url_candidates AS candidate
		  ON candidate.id = verification.candidate_id
		WHERE verification.id = $1
		FOR SHARE OF verification, candidate
	`, verificationID).Scan(
		&workID,
		&channel,
		&linkRole,
		&finalURL,
		&state,
		&checkedAt,
		&expiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OfficialLink{}, ErrVerificationNotFound
	}
	if err != nil {
		return OfficialLink{}, fmt.Errorf(
			"read verification for current projection: %w",
			err,
		)
	}
	if VerificationState(state) != VerificationStateVerified {
		return OfficialLink{}, ErrVerificationNotVerified
	}
	if projectedAt.Before(checkedAt) {
		return OfficialLink{}, ErrVerificationNotYetChecked
	}
	if !projectedAt.Before(expiresAt) {
		return OfficialLink{}, ErrVerificationExpired
	}
	role, err := ParseLinkRole(linkRole)
	if err != nil || !projectableRole(role) {
		return OfficialLink{}, ErrVerificationNotVerified
	}
	parsedChannel, err := scope.ParseContentChannel(channel)
	if err != nil {
		return OfficialLink{}, err
	}

	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended($1, 0)
		)
	`, workID+":"+linkRole); err != nil {
		return OfficialLink{}, fmt.Errorf(
			"lock current official link projection: %w",
			err,
		)
	}
	if existing, found, findErr := findOfficialLinkByVerification(
		ctx,
		tx,
		verificationID,
	); findErr != nil {
		return OfficialLink{}, findErr
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return OfficialLink{}, fmt.Errorf(
				"commit existing current official link lookup: %w",
				err,
			)
		}
		return existing, nil
	}

	var latestCheckedAt pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `
		SELECT max(verification.checked_at)
		FROM current_work_official_links AS link
		JOIN work_url_verifications AS verification
		  ON verification.id = link.verification_id
		WHERE link.work_id = $1
		  AND link.link_role = $2
	`, workID, linkRole).Scan(&latestCheckedAt); err != nil {
		return OfficialLink{}, fmt.Errorf(
			"read latest projected verification check time: %w",
			err,
		)
	}
	if latestCheckedAt.Valid &&
		checkedAt.Before(normalizeTime(latestCheckedAt.Time)) {
		return OfficialLink{}, ErrVerificationOlderThanCurrent
	}

	var nextVersion int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(projection_version), 0) + 1
		FROM current_work_official_links
		WHERE work_id = $1
		  AND link_role = $2
	`, workID, linkRole).Scan(&nextVersion); err != nil {
		return OfficialLink{}, fmt.Errorf(
			"read next current official link version: %w",
			err,
		)
	}
	link := OfficialLink{
		WorkID:            workID,
		Channel:           parsedChannel,
		LinkRole:          role,
		VerificationID:    verificationID,
		URL:               finalURL,
		ProjectionVersion: nextVersion,
		ProjectedAt:       projectedAt,
		ExpiresAt:         normalizeTime(expiresAt),
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO current_work_official_links (
			work_id,
			content_channel,
			link_role,
			verification_id,
			official_url,
			projection_version,
			projected_at,
			expires_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8
		)
		RETURNING id::text
	`,
		link.WorkID,
		link.Channel,
		link.LinkRole,
		link.VerificationID,
		link.URL,
		link.ProjectionVersion,
		link.ProjectedAt,
		link.ExpiresAt,
	).Scan(&link.ID)
	if err != nil {
		return OfficialLink{}, fmt.Errorf(
			"append current official link projection: %w",
			err,
		)
	}
	if err := link.Validate(); err != nil {
		return OfficialLink{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OfficialLink{}, fmt.Errorf(
			"commit current official link projection: %w",
			err,
		)
	}
	return link, nil
}

func (store *PostgresStore) Current(
	ctx context.Context,
	workID string,
	linkRole LinkRole,
	at time.Time,
) (OfficialLink, bool, error) {
	if err := validateStoreContext(ctx); err != nil {
		return OfficialLink{}, false, err
	}
	if err := store.validate(); err != nil {
		return OfficialLink{}, false, err
	}
	if !exactNonEmpty(workID) {
		return OfficialLink{}, false, errors.New(
			"current official link lookup requires an exact Work ID",
		)
	}
	if _, err := ParseLinkRole(string(linkRole)); err != nil ||
		!projectableRole(linkRole) {
		return OfficialLink{}, false, errors.New(
			"current official link lookup requires a projectable role",
		)
	}
	if at.IsZero() {
		return OfficialLink{}, false, errors.New(
			"current official link lookup time is required",
		)
	}
	link, found, err := scanOfficialLink(store.pool.QueryRow(ctx, `
		SELECT
			id::text,
			work_id::text,
			content_channel,
			link_role,
			verification_id::text,
			official_url,
			projection_version,
			projected_at,
			expires_at
		FROM current_work_official_links
		WHERE work_id = $1
		  AND link_role = $2
		  AND projected_at <= $3
		  AND expires_at > $3
		ORDER BY projection_version DESC, id DESC
		LIMIT 1
	`, workID, linkRole, normalizeTime(at)))
	if err != nil {
		return OfficialLink{}, false, fmt.Errorf(
			"find current official link: %w",
			err,
		)
	}
	if !found {
		return OfficialLink{}, false, nil
	}
	return link, true, nil
}

func (store *PostgresStore) validate() error {
	if store == nil || store.pool == nil {
		return errors.New("official URL PostgreSQL store is nil")
	}
	return nil
}

type normalizedURLCandidateWire struct {
	URL              string `json:"url"`
	SourcePath       string `json:"source_path"`
	ContentChannel   string `json:"content_channel"`
	LinkRole         string `json:"link_role"`
	ParserVersion    string `json:"parser_version"`
	IdentifierScheme string `json:"identifier_scheme"`
	IdentifierValue  string `json:"identifier_value"`
}

const (
	candidateRejectionInvalidJSONShape          = "invalid_json_shape"
	candidateRejectionParserVersionMismatch     = "parser_version_mismatch"
	candidateRejectionInvalidContentChannel     = "invalid_content_channel"
	candidateRejectionInvalidLinkRole           = "invalid_link_role"
	candidateRejectionInvalidIdentifier         = "invalid_identifier"
	candidateRejectionInvalidCandidate          = "invalid_candidate"
	candidateRejectionConflictingExactDuplicate = "conflicting_duplicate"
)

type normalizedURLCandidateRejection struct {
	Ordinal   int
	ErrorCode string
	Err       error
}

func decodeNormalizedURLCandidates(
	payload []byte,
	base Candidate,
) ([]Candidate, []normalizedURLCandidateRejection, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, nil, fmt.Errorf(
			"%w: decode normalized payload: %v",
			ErrInvalidNormalizedURLCandidate,
			err,
		)
	}
	var parserVersion string
	rawParserVersion, exists := object["parser_version"]
	if !exists ||
		json.Unmarshal(rawParserVersion, &parserVersion) != nil ||
		!exactNonEmpty(parserVersion) {
		return nil, nil, fmt.Errorf(
			"%w: normalized payload parser_version is required",
			ErrInvalidNormalizedURLCandidate,
		)
	}
	rawCandidates, exists := object["url_candidates"]
	if !exists {
		return nil, nil, fmt.Errorf(
			"%w: normalized payload url_candidates array is required",
			ErrInvalidNormalizedURLCandidate,
		)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(rawCandidates, &entries); err != nil ||
		entries == nil {
		return nil, nil, fmt.Errorf(
			"%w: url_candidates must be an array",
			ErrInvalidNormalizedURLCandidate,
		)
	}

	result := make([]Candidate, 0, len(entries))
	seen := make(map[string]Candidate, len(entries))
	rejections := make(
		[]normalizedURLCandidateRejection,
		0,
	)
	for index, entry := range entries {
		candidate, errorCode, err := decodeNormalizedURLCandidateEntry(
			entry,
			parserVersion,
			base,
		)
		if err != nil {
			rejections = append(
				rejections,
				normalizedURLCandidateRejection{
					Ordinal:   index,
					ErrorCode: errorCode,
					Err:       err,
				},
			)
			continue
		}
		identity := candidate.SourcePath + "\x00" + candidate.URL
		if existing, duplicate := seen[identity]; duplicate {
			if !candidateEquivalent(existing, candidate) {
				rejections = append(
					rejections,
					normalizedURLCandidateRejection{
						Ordinal:   index,
						ErrorCode: candidateRejectionConflictingExactDuplicate,
						Err: errors.New(
							"conflicting duplicate exact path and URL",
						),
					},
				)
			}
			continue
		}
		seen[identity] = candidate
		result = append(result, candidate)
	}
	return result, rejections, nil
}

func decodeNormalizedURLCandidateEntry(
	entry json.RawMessage,
	parserVersion string,
	base Candidate,
) (Candidate, string, error) {
	var wire normalizedURLCandidateWire
	decoder := json.NewDecoder(bytes.NewReader(entry))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Candidate{}, candidateRejectionInvalidJSONShape, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Candidate{}, candidateRejectionInvalidJSONShape, err
	}
	if wire.ParserVersion != parserVersion {
		return Candidate{}, candidateRejectionParserVersionMismatch, errors.New(
			"candidate parser_version conflicts with normalized payload",
		)
	}
	channel, err := scope.ParseContentChannel(wire.ContentChannel)
	if err != nil {
		return Candidate{}, candidateRejectionInvalidContentChannel, err
	}
	role, err := ParseLinkRole(wire.LinkRole)
	if err != nil {
		return Candidate{}, candidateRejectionInvalidLinkRole, err
	}
	identifier, err := NormalizeStableIdentifier(StableIdentifier{
		Scheme: wire.IdentifierScheme,
		Value:  wire.IdentifierValue,
	})
	if err != nil {
		return Candidate{}, candidateRejectionInvalidIdentifier, err
	}
	candidate := base
	candidate.SourcePath = wire.SourcePath
	candidate.Channel = channel
	candidate.LinkRole = role
	candidate.URL = wire.URL
	candidate.ParserVersion = wire.ParserVersion
	candidate.Identifier = identifier
	if err := candidate.Validate(); err != nil {
		return Candidate{}, candidateRejectionInvalidCandidate, err
	}
	return candidate, "", nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}

func persistCandidateRejection(
	ctx context.Context,
	tx pgx.Tx,
	base Candidate,
	rejection normalizedURLCandidateRejection,
) error {
	detailRunes := []rune(rejection.Err.Error())
	if len(detailRunes) > 2000 {
		detailRunes = detailRunes[:2000]
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO work_url_candidate_import_rejections (
			work_id,
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			candidate_ordinal,
			candidate_payload_hash,
			error_code,
			error_detail
		)
		SELECT
			$1,
			$2,
			$3,
			$4,
			$5,
			encode(
				sha256(
					convert_to(
						jsonb_array_element(
							normalized.normalized_payload->'url_candidates',
							$5
						)::text,
						'UTF8'
					)
				),
				'hex'
			),
			$6,
			$7
		FROM ingestion_normalized_records AS normalized
		WHERE normalized.id = $3
		ON CONFLICT ON CONSTRAINT
			work_url_candidate_rejections_exact_item_key
		DO NOTHING
	`,
		base.WorkID,
		base.ProjectionAssertionID,
		base.NormalizedAssertionID,
		base.SourceRecordID,
		rejection.Ordinal,
		rejection.ErrorCode,
		string(detailRunes),
	)
	if err != nil {
		return fmt.Errorf(
			"persist immutable official URL candidate rejection at index %d: %w",
			rejection.Ordinal,
			err,
		)
	}
	return nil
}

func persistCandidate(
	ctx context.Context,
	tx pgx.Tx,
	candidate Candidate,
) (Candidate, error) {
	if candidate.ID != "" {
		return Candidate{}, errors.New(
			"new official URL candidate cannot provide an ID",
		)
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO work_url_candidates (
			work_id,
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			source_path,
			content_channel,
			link_role,
			candidate_url,
			parser_version,
			identifier_scheme,
			identifier_value,
			asserted_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12
		)
		ON CONFLICT ON CONSTRAINT
			work_url_candidates_exact_revision_path_url_key
		DO NOTHING
		RETURNING id::text
	`,
		candidate.WorkID,
		candidate.ProjectionAssertionID,
		candidate.NormalizedAssertionID,
		candidate.SourceRecordID,
		candidate.SourcePath,
		candidate.Channel,
		candidate.LinkRole,
		candidate.URL,
		candidate.ParserVersion,
		candidate.Identifier.Scheme,
		candidate.Identifier.Value,
		candidate.AssertedAt,
	).Scan(&candidate.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, found, findErr := scanCandidate(tx.QueryRow(ctx, `
			SELECT
				id::text,
				work_id::text,
				projection_assertion_id::text,
				normalized_assertion_id::text,
				source_record_id::text,
				source_path,
				content_channel,
				link_role,
				candidate_url,
				parser_version,
				identifier_scheme,
				identifier_value,
				asserted_at
			FROM work_url_candidates
			WHERE normalized_assertion_id = $1
			  AND source_path = $2
			  AND candidate_url = $3
		`,
			candidate.NormalizedAssertionID,
			candidate.SourcePath,
			candidate.URL,
		))
		if findErr != nil {
			return Candidate{}, fmt.Errorf(
				"read existing exact official URL candidate: %w",
				findErr,
			)
		}
		if !found || !candidateEquivalent(existing, candidate) {
			return Candidate{}, errors.New(
				"conflicting official URL candidate for exact revision, path, and URL",
			)
		}
		return existing, nil
	}
	if err != nil {
		return Candidate{}, fmt.Errorf(
			"persist exact official URL candidate: %w",
			err,
		)
	}
	return candidate, nil
}

func candidateEquivalent(left Candidate, right Candidate) bool {
	left.ID = ""
	right.ID = ""
	return left == right
}

type rowScanner interface {
	Scan(...any) error
}

func scanCandidate(row rowScanner) (Candidate, bool, error) {
	var (
		candidate Candidate
		channel   string
		linkRole  string
	)
	err := row.Scan(
		&candidate.ID,
		&candidate.WorkID,
		&candidate.ProjectionAssertionID,
		&candidate.NormalizedAssertionID,
		&candidate.SourceRecordID,
		&candidate.SourcePath,
		&channel,
		&linkRole,
		&candidate.URL,
		&candidate.ParserVersion,
		&candidate.Identifier.Scheme,
		&candidate.Identifier.Value,
		&candidate.AssertedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Candidate{}, false, nil
	}
	if err != nil {
		return Candidate{}, false, err
	}
	parsedChannel, err := scope.ParseContentChannel(channel)
	if err != nil {
		return Candidate{}, false, err
	}
	parsedRole, err := ParseLinkRole(linkRole)
	if err != nil {
		return Candidate{}, false, err
	}
	candidate.Channel = parsedChannel
	candidate.LinkRole = parsedRole
	candidate.AssertedAt = normalizeTime(candidate.AssertedAt)
	if err := candidate.Validate(); err != nil {
		return Candidate{}, false, err
	}
	return candidate, true, nil
}

const verificationSelectSQL = `
	SELECT
		verification.id::text,
		verification.candidate_id::text,
		candidate.work_id::text,
		candidate.content_channel,
		candidate.link_role,
		verification.source_url,
		verification.final_url,
		verification.redirect_chain,
		verification.http_status,
		verification.expected_identifier_scheme,
		verification.expected_identifier_value,
		verification.observed_identifiers,
		verification.identifier_evidence,
		verification.response_metadata,
		verification.matched_identifier_scheme,
		verification.matched_identifier_value,
		verification.identifier_match,
		verification.verification_state,
		verification.checked_at,
		verification.expires_at,
		verification.verifier_version,
		verification.policy_version,
		verification.failure_code
	FROM work_url_verifications AS verification
	JOIN work_url_candidates AS candidate
	  ON candidate.id = verification.candidate_id
`

func scanVerification(row rowScanner) (Verification, bool, error) {
	var (
		verification        Verification
		channel             string
		linkRole            string
		redirectChain       []byte
		observedIdentifiers []byte
		identifierEvidence  []byte
		responseMetadata    []byte
		matchedScheme       pgtype.Text
		matchedValue        pgtype.Text
		failureCode         pgtype.Text
	)
	err := row.Scan(
		&verification.ID,
		&verification.CandidateID,
		&verification.WorkID,
		&channel,
		&linkRole,
		&verification.SourceURL,
		&verification.FinalURL,
		&redirectChain,
		&verification.HTTPStatus,
		&verification.ExpectedIdentifier.Scheme,
		&verification.ExpectedIdentifier.Value,
		&observedIdentifiers,
		&identifierEvidence,
		&responseMetadata,
		&matchedScheme,
		&matchedValue,
		&verification.IdentifierMatch,
		&verification.State,
		&verification.CheckedAt,
		&verification.ExpiresAt,
		&verification.VerifierVersion,
		&verification.PolicyVersion,
		&failureCode,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Verification{}, false, nil
	}
	if err != nil {
		return Verification{}, false, err
	}
	parsedChannel, err := scope.ParseContentChannel(channel)
	if err != nil {
		return Verification{}, false, err
	}
	parsedRole, err := ParseLinkRole(linkRole)
	if err != nil {
		return Verification{}, false, err
	}
	verification.Channel = parsedChannel
	verification.LinkRole = parsedRole
	if err := json.Unmarshal(
		redirectChain,
		&verification.RedirectChain,
	); err != nil {
		return Verification{}, false, fmt.Errorf(
			"decode persisted redirect chain: %w",
			err,
		)
	}
	if err := json.Unmarshal(
		observedIdentifiers,
		&verification.ObservedIdentifiers,
	); err != nil {
		return Verification{}, false, fmt.Errorf(
			"decode persisted observed identifiers: %w",
			err,
		)
	}
	if err := json.Unmarshal(
		identifierEvidence,
		&verification.IdentifierEvidence,
	); err != nil {
		return Verification{}, false, fmt.Errorf(
			"decode persisted official URL identifier evidence: %w",
			err,
		)
	}
	if err := json.Unmarshal(
		responseMetadata,
		&verification.ResponseMetadata,
	); err != nil {
		return Verification{}, false, fmt.Errorf(
			"decode persisted official URL response metadata: %w",
			err,
		)
	}
	if matchedScheme.Valid != matchedValue.Valid {
		return Verification{}, false, errors.New(
			"persisted matched identifier pair is inconsistent",
		)
	}
	if matchedScheme.Valid {
		verification.MatchedIdentifier = &StableIdentifier{
			Scheme: matchedScheme.String,
			Value:  matchedValue.String,
		}
	}
	if failureCode.Valid {
		verification.FailureCode = FailureCode(failureCode.String)
	}
	verification.CheckedAt = normalizeTime(verification.CheckedAt)
	verification.ExpiresAt = normalizeTime(verification.ExpiresAt)
	if err := verification.Validate(); err != nil {
		return Verification{}, false, err
	}
	return verification.clone(), true, nil
}

func findOfficialLinkByVerification(
	ctx context.Context,
	tx pgx.Tx,
	verificationID string,
) (OfficialLink, bool, error) {
	link, found, err := scanOfficialLink(tx.QueryRow(ctx, `
		SELECT
			id::text,
			work_id::text,
			content_channel,
			link_role,
			verification_id::text,
			official_url,
			projection_version,
			projected_at,
			expires_at
		FROM current_work_official_links
		WHERE verification_id = $1
	`, verificationID))
	if err != nil {
		return OfficialLink{}, false, fmt.Errorf(
			"find projected official link by verification: %w",
			err,
		)
	}
	return link, found, nil
}

func scanOfficialLink(row rowScanner) (OfficialLink, bool, error) {
	var (
		link     OfficialLink
		channel  string
		linkRole string
	)
	err := row.Scan(
		&link.ID,
		&link.WorkID,
		&channel,
		&linkRole,
		&link.VerificationID,
		&link.URL,
		&link.ProjectionVersion,
		&link.ProjectedAt,
		&link.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OfficialLink{}, false, nil
	}
	if err != nil {
		return OfficialLink{}, false, err
	}
	parsedChannel, err := scope.ParseContentChannel(channel)
	if err != nil {
		return OfficialLink{}, false, err
	}
	parsedRole, err := ParseLinkRole(linkRole)
	if err != nil {
		return OfficialLink{}, false, err
	}
	link.Channel = parsedChannel
	link.LinkRole = parsedRole
	link.ProjectedAt = normalizeTime(link.ProjectedAt)
	link.ExpiresAt = normalizeTime(link.ExpiresAt)
	if err := link.Validate(); err != nil {
		return OfficialLink{}, false, err
	}
	return link, true, nil
}

func validateStoreContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("official URL store context is required")
	}
	return ctx.Err()
}
