package workfamily

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	canonicalPolicyVersion                  = "work-family-canonical/v1"
	canonicalPrecedenceSingleton            = 0
	canonicalPrecedenceGenericVersion       = 100
	canonicalPrecedenceLifecycle            = 200
	canonicalPrecedenceVersionOfRecord      = 300
	advisoryLockNamespaceFamily        byte = 1
	advisoryLockNamespaceWork          byte = 2
)

var errWorkFamilyProjectionChanged = errors.New(
	"Work Family projection changed before locks were acquired",
)

type canonicalSelection struct {
	DecisionID uuid.UUID
	FamilyID   uuid.UUID
	WorkID     uuid.UUID
	Basis      string
	Precedence int
	DecidedAt  time.Time
}

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &PostgresStore{pool: pool}, nil
}

func (store *PostgresStore) RecordRelationAssertion(
	ctx context.Context,
	assertion RelationAssertion,
) (RelationAssertion, error) {
	if err := store.validate(ctx); err != nil {
		return RelationAssertion{}, err
	}
	if assertion.ID != uuid.Nil {
		return RelationAssertion{}, errors.New(
			"new relation assertion cannot provide a persisted ID",
		)
	}
	if err := assertion.Validate(); err != nil {
		return RelationAssertion{}, err
	}

	err := store.pool.QueryRow(ctx, `
		INSERT INTO work_relation_assertions (
			subject_work_id,
			object_work_id,
			relation_kind,
			evidence_kind,
			subject_source_record_id,
			subject_source_path,
			object_source_record_id,
			object_source_path,
			identifier_scheme,
			identifier_value,
			asserted_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11
		)
		ON CONFLICT ON CONSTRAINT work_relation_assertions_identity_key
		DO NOTHING
		RETURNING id
	`,
		assertion.SubjectWorkID,
		assertion.ObjectWorkID,
		assertion.RelationKind,
		assertion.EvidenceKind,
		assertion.SubjectSourceRecordID,
		assertion.SubjectSourcePath,
		nullableUUID(assertion.ObjectSourceRecordID),
		nullableText(assertion.ObjectSourcePath),
		nullableText(assertion.IdentifierScheme),
		nullableText(assertion.IdentifierValue),
		assertion.AssertedAt,
	).Scan(&assertion.ID)
	if err == nil {
		return assertion, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RelationAssertion{}, fmt.Errorf(
			"record Work relation assertion: %w",
			err,
		)
	}

	existing, found, err := findRelationAssertionByIdentity(
		ctx,
		store.pool,
		assertion,
	)
	if err != nil {
		return RelationAssertion{}, err
	}
	if !found || !relationAssertionsEqual(existing, assertion) {
		return RelationAssertion{}, ErrAssertionConflict
	}
	return existing, nil
}

func (store *PostgresStore) RecordRelationDecision(
	ctx context.Context,
	decision RelationDecision,
) (RelationDecision, error) {
	if err := store.validate(ctx); err != nil {
		return RelationDecision{}, err
	}
	if decision.ID != uuid.Nil {
		return RelationDecision{}, errors.New(
			"new relation decision cannot provide a persisted ID",
		)
	}
	if err := decision.Validate(); err != nil {
		return RelationDecision{}, err
	}

	for {
		persisted, projectionChanged, err := store.recordRelationDecisionAttempt(
			ctx,
			decision,
		)
		if !projectionChanged {
			return persisted, err
		}
		if err := ctx.Err(); err != nil {
			return RelationDecision{}, err
		}
	}
}

func (store *PostgresStore) recordRelationDecisionAttempt(
	ctx context.Context,
	decision RelationDecision,
) (RelationDecision, bool, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return RelationDecision{}, false, fmt.Errorf(
			"begin Work relation decision transaction: %w",
			err,
		)
	}
	defer tx.Rollback(context.Background())

	preliminaryAssertion, found, err := findRelationAssertionByID(
		ctx,
		tx,
		decision.AssertionID,
	)
	if err != nil {
		return RelationDecision{}, false, err
	}
	if !found {
		return RelationDecision{}, false, errors.New(
			"relation decision assertion does not exist",
		)
	}

	projectionSnapshot, err := loadDecisionProjectionSnapshot(
		ctx,
		tx,
		preliminaryAssertion,
	)
	if err != nil {
		return RelationDecision{}, false, err
	}
	if err := lockDecisionWorks(
		ctx,
		tx,
		projectionSnapshot.WorkIDs,
	); err != nil {
		return RelationDecision{}, false, err
	}

	assertion, found, err := lockRelationAssertionByID(
		ctx,
		tx,
		decision.AssertionID,
	)
	if err != nil {
		return RelationDecision{}, false, err
	}
	if !found {
		return RelationDecision{}, false, errors.New(
			"relation decision assertion no longer exists",
		)
	}
	if assertion.ID != preliminaryAssertion.ID ||
		!relationAssertionsEqual(assertion, preliminaryAssertion) {
		return RelationDecision{}, false, ErrAssertionConflict
	}

	current, currentFound, err := findCurrentRelationDecisionByAssertion(
		ctx,
		tx,
		decision.AssertionID,
	)
	if err != nil {
		return RelationDecision{}, false, err
	}

	existing, found, err := findRelationDecisionByIdentity(
		ctx,
		tx,
		decision,
	)
	if err != nil {
		return RelationDecision{}, false, err
	}
	if found {
		if err := tx.Commit(ctx); err != nil {
			return RelationDecision{}, false, fmt.Errorf(
				"commit replayed Work relation decision: %w",
				err,
			)
		}
		return existing, false, nil
	}

	if currentFound {
		if decision.SupersedesDecisionID != current.ID ||
			decision.DecidedAt.Before(current.DecidedAt) {
			return RelationDecision{}, false, ErrDecisionConflict
		}
	} else if decision.SupersedesDecisionID != uuid.Nil {
		return RelationDecision{}, false, ErrDecisionConflict
	}

	var affectedWorkIDs, lockedFamilyIDs []uuid.UUID
	if decision.Outcome == DecisionAccepted ||
		(currentFound && current.Outcome == DecisionAccepted) {
		affectedWorkIDs, lockedFamilyIDs, err = lockDecisionProjection(
			ctx,
			tx,
			assertion,
			projectionSnapshot,
		)
		if errors.Is(err, errWorkFamilyProjectionChanged) {
			return RelationDecision{}, true, nil
		}
		if err != nil {
			return RelationDecision{}, false, err
		}
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO work_relation_decisions (
			assertion_id,
			supersedes_decision_id,
			outcome,
			reason,
			policy_version,
			decided_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`,
		decision.AssertionID,
		nullableUUID(decision.SupersedesDecisionID),
		decision.Outcome,
		decision.Reason,
		decision.PolicyVersion,
		decision.DecidedAt,
	).Scan(&decision.ID)
	if err != nil {
		return RelationDecision{}, false, fmt.Errorf(
			"record Work relation decision: %w",
			err,
		)
	}

	switch {
	case !currentFound && decision.Outcome == DecisionAccepted:
		if err := mergeActiveFamilies(
			ctx,
			tx,
			assertion,
			decision,
		); err != nil {
			return RelationDecision{}, false, err
		}
	case currentFound &&
		(current.Outcome == DecisionAccepted ||
			decision.Outcome == DecisionAccepted):
		if err := rebuildCurrentAcceptedRelationComponents(
			ctx,
			tx,
			decision,
			affectedWorkIDs,
			lockedFamilyIDs,
		); err != nil {
			return RelationDecision{}, false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return RelationDecision{}, false, fmt.Errorf(
			"commit Work relation decision transaction: %w",
			err,
		)
	}
	return decision, false, nil
}

type decisionProjectionSnapshot struct {
	EndpointFamilies map[uuid.UUID]uuid.UUID
	FamilyIDs        []uuid.UUID
	WorkIDs          []uuid.UUID
}

func loadDecisionProjectionSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	assertion RelationAssertion,
) (decisionProjectionSnapshot, error) {
	endpointWorkIDs := []uuid.UUID{
		assertion.SubjectWorkID,
		assertion.ObjectWorkID,
	}
	endpointFamilies, err := activeFamilyIDsForWorks(
		ctx,
		tx,
		endpointWorkIDs,
	)
	if err != nil {
		return decisionProjectionSnapshot{}, err
	}
	familyIDs := uniqueSortedUUIDs(mapValues(endpointFamilies))
	workIDs, err := activeWorkIDsForFamilies(ctx, tx, familyIDs, false)
	if err != nil {
		return decisionProjectionSnapshot{}, err
	}
	return decisionProjectionSnapshot{
		EndpointFamilies: endpointFamilies,
		FamilyIDs:        familyIDs,
		WorkIDs:          workIDs,
	}, nil
}

func lockDecisionWorks(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
) error {
	workIDs = uniqueSortedUUIDs(workIDs)
	if err := acquireSortedAdvisoryLocks(
		ctx,
		tx,
		advisoryLockNamespaceWork,
		workIDs,
	); err != nil {
		return fmt.Errorf("lock affected Work identities: %w", err)
	}
	for _, workID := range workIDs {
		var lockedWorkID uuid.UUID
		err := tx.QueryRow(ctx, `
			SELECT id
			FROM works
			WHERE id = $1
			FOR KEY SHARE
		`, workID).Scan(&lockedWorkID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrWorkNotFound
		}
		if err != nil {
			return fmt.Errorf("lock affected Work %s: %w", workID, err)
		}
	}
	return nil
}

func lockDecisionProjection(
	ctx context.Context,
	tx pgx.Tx,
	assertion RelationAssertion,
	snapshot decisionProjectionSnapshot,
) ([]uuid.UUID, []uuid.UUID, error) {
	endpointWorkIDs := []uuid.UUID{
		assertion.SubjectWorkID,
		assertion.ObjectWorkID,
	}
	if err := acquireSortedAdvisoryLocks(
		ctx,
		tx,
		advisoryLockNamespaceFamily,
		snapshot.FamilyIDs,
	); err != nil {
		return nil, nil, fmt.Errorf(
			"lock affected Work Families: %w",
			err,
		)
	}

	lockedFamilies, err := activeFamilyIDsForWorks(
		ctx,
		tx,
		endpointWorkIDs,
	)
	if err != nil {
		return nil, nil, err
	}
	if !uuidMapEqual(snapshot.EndpointFamilies, lockedFamilies) {
		return nil, nil, errWorkFamilyProjectionChanged
	}

	affectedWorkIDs, err := activeWorkIDsForFamilies(
		ctx,
		tx,
		snapshot.FamilyIDs,
		true,
	)
	if err != nil {
		return nil, nil, err
	}
	if !uuidSlicesEqual(snapshot.WorkIDs, affectedWorkIDs) {
		return nil, nil, errWorkFamilyProjectionChanged
	}
	return affectedWorkIDs, snapshot.FamilyIDs, nil
}

func activeWorkIDsForFamilies(
	ctx context.Context,
	tx pgx.Tx,
	familyIDs []uuid.UUID,
	lock bool,
) ([]uuid.UUID, error) {
	query := `
		SELECT work_id
		FROM work_family_memberships
		WHERE work_family_id = ANY($1::uuid[])
		  AND ended_at IS NULL
		ORDER BY work_id
	`
	if lock {
		query += " FOR UPDATE"
	}
	rows, err := tx.Query(ctx, query, familyIDs)
	if err != nil {
		return nil, fmt.Errorf(
			"load affected Work Family memberships: %w",
			err,
		)
	}
	defer rows.Close()

	var workIDs []uuid.UUID
	for rows.Next() {
		var workID uuid.UUID
		if err := rows.Scan(&workID); err != nil {
			return nil, fmt.Errorf(
				"scan affected Work Family membership: %w",
				err,
			)
		}
		workIDs = append(workIDs, workID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate affected Work Family memberships: %w",
			err,
		)
	}
	return uniqueSortedUUIDs(workIDs), nil
}

func activeFamilyIDsForWorks(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
) (map[uuid.UUID]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT work_id, work_family_id
		FROM work_family_memberships
		WHERE work_id = ANY($1::uuid[])
		  AND ended_at IS NULL
		ORDER BY work_id
	`, workIDs)
	if err != nil {
		return nil, fmt.Errorf(
			"load active families for decision endpoints: %w",
			err,
		)
	}
	defer rows.Close()

	families := make(map[uuid.UUID]uuid.UUID, len(workIDs))
	for rows.Next() {
		var workID, familyID uuid.UUID
		if err := rows.Scan(&workID, &familyID); err != nil {
			return nil, fmt.Errorf(
				"scan active decision endpoint family: %w",
				err,
			)
		}
		families[workID] = familyID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate active decision endpoint families: %w",
			err,
		)
	}
	if len(families) != len(uniqueSortedUUIDs(workIDs)) {
		return nil, errors.New(
			"relation decision endpoint has no active family membership",
		)
	}
	return families, nil
}

func acquireSortedAdvisoryLocks(
	ctx context.Context,
	tx pgx.Tx,
	namespace byte,
	ids []uuid.UUID,
) error {
	keys := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		key := advisoryEntityLockKey(namespace, id)
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		return keys[left] < keys[right]
	})
	for _, key := range keys {
		if _, err := tx.Exec(
			ctx,
			"SELECT pg_advisory_xact_lock($1)",
			key,
		); err != nil {
			return err
		}
	}
	return nil
}

func advisoryEntityLockKey(namespace byte, id uuid.UUID) int64 {
	input := make([]byte, 1+len(id))
	input[0] = namespace
	copy(input[1:], id[:])
	sum := sha256.Sum256(input)
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func uniqueSortedUUIDs(ids []uuid.UUID) []uuid.UUID {
	unique := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		unique[id] = struct{}{}
	}
	result := make([]uuid.UUID, 0, len(unique))
	for id := range unique {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool {
		return bytes.Compare(result[left][:], result[right][:]) < 0
	})
	return result
}

func mapValues(values map[uuid.UUID]uuid.UUID) []uuid.UUID {
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func uuidMapEqual(
	left map[uuid.UUID]uuid.UUID,
	right map[uuid.UUID]uuid.UUID,
) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func uuidSlicesEqual(left, right []uuid.UUID) bool {
	left = uniqueSortedUUIDs(left)
	right = uniqueSortedUUIDs(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (store *PostgresStore) ActiveMembership(
	ctx context.Context,
	workID uuid.UUID,
) (Membership, bool, error) {
	if err := store.validate(ctx); err != nil {
		return Membership{}, false, err
	}
	if workID == uuid.Nil {
		return Membership{}, false, errors.New(
			"active membership Work ID is required",
		)
	}
	membership, found, err := activeMembership(ctx, store.pool, workID)
	if err != nil {
		return Membership{}, false, fmt.Errorf(
			"load active Work Family membership: %w",
			err,
		)
	}
	return membership, found, nil
}

func (store *PostgresStore) VersionTimeline(
	ctx context.Context,
	workID uuid.UUID,
) (Timeline, bool, error) {
	if err := store.validate(ctx); err != nil {
		return Timeline{}, false, err
	}
	if workID == uuid.Nil {
		return Timeline{}, false, errors.New(
			"version timeline Work ID is required",
		)
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return Timeline{}, false, fmt.Errorf(
			"begin version timeline snapshot: %w",
			err,
		)
	}
	defer tx.Rollback(context.Background())

	membership, found, err := activeMembership(ctx, tx, workID)
	if err != nil {
		return Timeline{}, false, fmt.Errorf(
			"load timeline Work Family membership: %w",
			err,
		)
	}
	if !found {
		if err := tx.Commit(ctx); err != nil {
			return Timeline{}, false, fmt.Errorf(
				"commit empty version timeline snapshot: %w",
				err,
			)
		}
		return Timeline{}, false, nil
	}

	timeline := Timeline{FamilyID: membership.FamilyID}
	if err := tx.QueryRow(ctx, `
		SELECT canonical_work_id
		FROM work_family_canonical_states
		WHERE work_family_id = $1
	`, membership.FamilyID).Scan(&timeline.CanonicalWorkID); err != nil {
		return Timeline{}, false, fmt.Errorf(
			"load current canonical Work for family %s: %w",
			membership.FamilyID,
			err,
		)
	}
	rows, err := tx.Query(ctx, `
		SELECT
			work.id,
			work.canonical_key,
			work.title,
			work.published_at,
			membership.started_at
		FROM work_family_memberships AS membership
		JOIN works AS work
		  ON work.id = membership.work_id
		WHERE membership.work_family_id = $1
		  AND membership.ended_at IS NULL
		ORDER BY
			COALESCE(work.published_at, work.created_at),
			work.created_at,
			work.id
	`, membership.FamilyID)
	if err != nil {
		return Timeline{}, false, fmt.Errorf(
			"query Work Family timeline versions: %w",
			err,
		)
	}
	for rows.Next() {
		var (
			version     Version
			publishedAt pgtype.Timestamptz
		)
		if err := rows.Scan(
			&version.WorkID,
			&version.CanonicalKey,
			&version.Title,
			&publishedAt,
			&version.MembershipStartedAt,
		); err != nil {
			rows.Close()
			return Timeline{}, false, fmt.Errorf(
				"scan Work Family timeline version: %w",
				err,
			)
		}
		version.MembershipStartedAt = normalizeTimestamp(
			version.MembershipStartedAt,
		)
		if publishedAt.Valid {
			value := normalizeTimestamp(publishedAt.Time)
			version.PublishedAt = &value
		}
		timeline.Versions = append(timeline.Versions, version)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Timeline{}, false, fmt.Errorf(
			"iterate Work Family timeline versions: %w",
			err,
		)
	}
	rows.Close()

	relationRows, err := tx.Query(ctx, `
		SELECT
			assertion.id,
			assertion.subject_work_id,
			assertion.object_work_id,
			assertion.relation_kind,
			assertion.evidence_kind,
			assertion.subject_source_record_id,
			assertion.subject_source_path,
			assertion.object_source_record_id,
			assertion.object_source_path,
			assertion.identifier_scheme,
			assertion.identifier_value,
			assertion.asserted_at,
			decision.id,
			decision.supersedes_decision_id,
			decision.outcome,
			decision.reason,
			decision.policy_version,
			decision.decided_at
		FROM work_relation_assertions AS assertion
		JOIN work_relation_decisions AS decision
		  ON decision.assertion_id = assertion.id
		 AND decision.outcome = 'accepted'
		 AND NOT EXISTS (
		     SELECT 1
		     FROM work_relation_decisions AS successor
		     WHERE successor.supersedes_decision_id = decision.id
		 )
		JOIN work_family_memberships AS subject_membership
		  ON subject_membership.work_id = assertion.subject_work_id
		 AND subject_membership.work_family_id = $1
		 AND subject_membership.ended_at IS NULL
		JOIN work_family_memberships AS object_membership
		  ON object_membership.work_id = assertion.object_work_id
		 AND object_membership.work_family_id = $1
		 AND object_membership.ended_at IS NULL
		ORDER BY assertion.asserted_at, assertion.id
	`, membership.FamilyID)
	if err != nil {
		return Timeline{}, false, fmt.Errorf(
			"query Work Family timeline relations: %w",
			err,
		)
	}
	for relationRows.Next() {
		relation, err := scanTimelineRelation(relationRows)
		if err != nil {
			relationRows.Close()
			return Timeline{}, false, err
		}
		timeline.Relations = append(timeline.Relations, relation)
	}
	if err := relationRows.Err(); err != nil {
		relationRows.Close()
		return Timeline{}, false, fmt.Errorf(
			"iterate Work Family timeline relations: %w",
			err,
		)
	}
	relationRows.Close()

	if err := tx.Commit(ctx); err != nil {
		return Timeline{}, false, fmt.Errorf(
			"commit version timeline snapshot: %w",
			err,
		)
	}
	return timeline, true, nil
}

func (store *PostgresStore) validate(ctx context.Context) error {
	if ctx == nil {
		return errors.New("Work Family context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || store.pool == nil {
		return errors.New("PostgresStore is not initialized")
	}
	return nil
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func activeMembership(
	ctx context.Context,
	db queryRower,
	workID uuid.UUID,
) (Membership, bool, error) {
	var (
		membership       Membership
		relationDecision pgtype.UUID
		endedAt          pgtype.Timestamptz
	)
	err := db.QueryRow(ctx, `
		SELECT
			id,
			work_family_id,
			work_id,
			origin,
			relation_decision_id,
			started_at,
			ended_at
		FROM work_family_memberships
		WHERE work_id = $1
		  AND ended_at IS NULL
	`, workID).Scan(
		&membership.ID,
		&membership.FamilyID,
		&membership.WorkID,
		&membership.Origin,
		&relationDecision,
		&membership.StartedAt,
		&endedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Membership{}, false, nil
	}
	if err != nil {
		return Membership{}, false, err
	}
	if relationDecision.Valid {
		membership.RelationDecisionID = relationDecision.Bytes
	}
	membership.StartedAt = normalizeTimestamp(membership.StartedAt)
	if endedAt.Valid {
		value := normalizeTimestamp(endedAt.Time)
		membership.EndedAt = &value
	}
	return membership, true, nil
}

func mergeActiveFamilies(
	ctx context.Context,
	tx pgx.Tx,
	assertion RelationAssertion,
	decision RelationDecision,
) error {
	subjectWorkID := assertion.SubjectWorkID
	objectWorkID := assertion.ObjectWorkID
	canonicalWorkID, precedence := canonicalCandidateForAcceptedRelation(
		assertion,
	)
	var projectedAt time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(
		&projectedAt,
	); err != nil {
		return fmt.Errorf("resolve Work Family projection time: %w", err)
	}
	projectedAt = normalizeTimestamp(projectedAt)

	subjectMembership, found, err := activeMembership(
		ctx,
		tx,
		subjectWorkID,
	)
	if err != nil {
		return fmt.Errorf("load subject active family membership: %w", err)
	}
	if !found {
		return errors.New("subject Work has no active family membership")
	}
	objectMembership, found, err := activeMembership(
		ctx,
		tx,
		objectWorkID,
	)
	if err != nil {
		return fmt.Errorf("load object active family membership: %w", err)
	}
	if !found {
		return errors.New("object Work has no active family membership")
	}
	if subjectMembership.FamilyID == objectMembership.FamilyID {
		current, err := loadCurrentCanonicalSelection(
			ctx,
			tx,
			subjectMembership.FamilyID,
		)
		if err != nil {
			return err
		}
		candidate, err := insertRelationCanonicalCandidate(
			ctx,
			tx,
			subjectMembership.FamilyID,
			canonicalWorkID,
			assertion.RelationKind,
			precedence,
			decision,
		)
		if err != nil {
			return err
		}
		if canonicalSelectionOutranks(candidate, current) {
			return projectCanonicalSelection(
				ctx,
				tx,
				candidate,
				projectedAt,
			)
		}
		return nil
	}

	var (
		subjectFamilyCreatedAt time.Time
		objectFamilyCreatedAt  time.Time
	)
	if err := tx.QueryRow(ctx, `
		SELECT
			subject_family.created_at,
			object_family.created_at
		FROM work_families AS subject_family,
		     work_families AS object_family
		WHERE subject_family.id = $1
		  AND object_family.id = $2
		FOR UPDATE OF subject_family, object_family
	`,
		subjectMembership.FamilyID,
		objectMembership.FamilyID,
	).Scan(
		&subjectFamilyCreatedAt,
		&objectFamilyCreatedAt,
	); err != nil {
		return fmt.Errorf("lock Work Families for merge: %w", err)
	}

	winnerFamilyID := subjectMembership.FamilyID
	loserFamilyID := objectMembership.FamilyID
	if objectFamilyCreatedAt.Before(subjectFamilyCreatedAt) ||
		(objectFamilyCreatedAt.Equal(subjectFamilyCreatedAt) &&
			objectMembership.FamilyID.String() <
				subjectMembership.FamilyID.String()) {
		winnerFamilyID = objectMembership.FamilyID
		loserFamilyID = subjectMembership.FamilyID
	}

	winnerCanonical, err := loadCurrentCanonicalSelection(
		ctx,
		tx,
		winnerFamilyID,
	)
	if err != nil {
		return err
	}
	loserCanonical, err := loadCurrentCanonicalSelection(
		ctx,
		tx,
		loserFamilyID,
	)
	if err != nil {
		return err
	}
	relationCandidate, err := insertRelationCanonicalCandidate(
		ctx,
		tx,
		winnerFamilyID,
		canonicalWorkID,
		assertion.RelationKind,
		precedence,
		decision,
	)
	if err != nil {
		return err
	}

	rows, err := tx.Query(ctx, `
		SELECT work_id
		FROM work_family_memberships
		WHERE work_family_id = $1
		  AND ended_at IS NULL
		ORDER BY work_id
		FOR UPDATE
	`, loserFamilyID)
	if err != nil {
		return fmt.Errorf("lock losing Work Family memberships: %w", err)
	}
	var movedWorkIDs []uuid.UUID
	for rows.Next() {
		var movedWorkID uuid.UUID
		if err := rows.Scan(&movedWorkID); err != nil {
			rows.Close()
			return fmt.Errorf(
				"scan losing Work Family membership: %w",
				err,
			)
		}
		movedWorkIDs = append(movedWorkIDs, movedWorkID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf(
			"iterate losing Work Family memberships: %w",
			err,
		)
	}
	rows.Close()

	if _, err := tx.Exec(ctx, `
		UPDATE work_family_memberships
		SET ended_at = $2
		WHERE work_family_id = $1
		  AND ended_at IS NULL
	`, loserFamilyID, projectedAt); err != nil {
		return fmt.Errorf("close losing Work Family memberships: %w", err)
	}

	for _, movedWorkID := range movedWorkIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_family_memberships (
				work_family_id,
				work_id,
				origin,
				relation_decision_id,
				started_at
			) VALUES ($1, $2, 'relation_decision', $3, $4)
		`,
			winnerFamilyID,
			movedWorkID,
			decision.ID,
			projectedAt,
		); err != nil {
			return fmt.Errorf(
				"create merged Work Family membership for %s: %w",
				movedWorkID,
				err,
			)
		}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM work_family_canonical_states
		WHERE work_family_id = $1
	`, loserFamilyID); err != nil {
		return fmt.Errorf(
			"remove losing Work Family canonical projection: %w",
			err,
		)
	}

	selected := winnerCanonical
	selectedFromLoser := false
	if canonicalSelectionOutranks(loserCanonical, selected) {
		selected = loserCanonical
		selectedFromLoser = true
	}
	if canonicalSelectionOutranks(relationCandidate, selected) {
		selected = relationCandidate
		selectedFromLoser = false
	}
	if selectedFromLoser {
		selected, err = carryCanonicalSelection(
			ctx,
			tx,
			winnerFamilyID,
			selected,
		)
		if err != nil {
			return err
		}
	}
	if selected.DecisionID == winnerCanonical.DecisionID {
		return nil
	}
	return projectCanonicalSelection(ctx, tx, selected, projectedAt)
}

type activeRelationEdge struct {
	AssertionID   uuid.UUID
	SubjectWorkID uuid.UUID
	ObjectWorkID  uuid.UUID
	RelationKind  RelationKind
	DecisionID    uuid.UUID
	DecidedAt     time.Time
}

type relationComponent struct {
	WorkIDs []uuid.UUID
	Edges   []activeRelationEdge
}

func rebuildCurrentAcceptedRelationComponents(
	ctx context.Context,
	tx pgx.Tx,
	triggerDecision RelationDecision,
	seedWorkIDs []uuid.UUID,
	lockedFamilyIDs []uuid.UUID,
) error {
	edges, err := loadActiveAcceptedRelationEdges(ctx, tx, seedWorkIDs)
	if err != nil {
		return err
	}
	affectedWorkIDs := append([]uuid.UUID(nil), seedWorkIDs...)
	for _, edge := range edges {
		affectedWorkIDs = append(
			affectedWorkIDs,
			edge.SubjectWorkID,
			edge.ObjectWorkID,
		)
	}
	affectedWorkIDs = uniqueSortedUUIDs(affectedWorkIDs)

	lockedFamilies := make(map[uuid.UUID]struct{}, len(lockedFamilyIDs))
	for _, familyID := range lockedFamilyIDs {
		lockedFamilies[familyID] = struct{}{}
	}
	rows, err := tx.Query(ctx, `
		SELECT work_id, work_family_id
		FROM work_family_memberships
		WHERE work_id = ANY($1::uuid[])
		  AND ended_at IS NULL
		ORDER BY work_id
		FOR UPDATE
	`, affectedWorkIDs)
	if err != nil {
		return fmt.Errorf(
			"lock active memberships for relation graph rebuild: %w",
			err,
		)
	}
	activeMembershipCount := 0
	for rows.Next() {
		var workID, familyID uuid.UUID
		if err := rows.Scan(&workID, &familyID); err != nil {
			rows.Close()
			return fmt.Errorf(
				"scan relation graph rebuild membership: %w",
				err,
			)
		}
		activeMembershipCount++
		if _, locked := lockedFamilies[familyID]; !locked {
			rows.Close()
			return errors.New(
				"active accepted relation graph crosses an unlocked Work Family",
			)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf(
			"iterate relation graph rebuild memberships: %w",
			err,
		)
	}
	rows.Close()
	if activeMembershipCount != len(affectedWorkIDs) {
		return errors.New(
			"active accepted relation graph contains a Work without active membership",
		)
	}

	components := buildRelationComponents(affectedWorkIDs, edges)
	var projectedAt time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(
		&projectedAt,
	); err != nil {
		return fmt.Errorf("resolve relation graph projection time: %w", err)
	}
	projectedAt = normalizeTimestamp(projectedAt)

	if _, err := tx.Exec(ctx, `
		DELETE FROM work_family_canonical_states
		WHERE work_family_id = ANY($1::uuid[])
	`, lockedFamilyIDs); err != nil {
		return fmt.Errorf(
			"remove prior canonical projections for relation graph rebuild: %w",
			err,
		)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_family_memberships
		SET ended_at = $2
		WHERE work_id = ANY($1::uuid[])
		  AND ended_at IS NULL
	`, affectedWorkIDs, projectedAt); err != nil {
		return fmt.Errorf(
			"close memberships for relation graph rebuild: %w",
			err,
		)
	}

	for _, component := range components {
		var familyID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO work_families (created_at)
			VALUES ($1)
			RETURNING id
		`, projectedAt).Scan(&familyID); err != nil {
			return fmt.Errorf(
				"create rebuilt Work Family component: %w",
				err,
			)
		}
		for _, workID := range component.WorkIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO work_family_memberships (
					work_family_id,
					work_id,
					origin,
					relation_decision_id,
					started_at
				) VALUES ($1, $2, 'relation_decision', $3, $4)
			`,
				familyID,
				workID,
				triggerDecision.ID,
				projectedAt,
			); err != nil {
				return fmt.Errorf(
					"create rebuilt Work Family membership for %s: %w",
					workID,
					err,
				)
			}
		}
		if err := createRebuiltCanonicalState(
			ctx,
			tx,
			familyID,
			component,
			triggerDecision,
			projectedAt,
		); err != nil {
			return err
		}
	}
	return nil
}

func loadActiveAcceptedRelationEdges(
	ctx context.Context,
	tx pgx.Tx,
	seedWorkIDs []uuid.UUID,
) ([]activeRelationEdge, error) {
	workRows, err := tx.Query(ctx, `
		WITH RECURSIVE affected_work(work_id) AS (
			SELECT seed.work_id
			FROM unnest($1::uuid[]) AS seed(work_id)
			UNION
			SELECT neighbor.work_id
			FROM affected_work AS affected
			CROSS JOIN LATERAL (
				SELECT candidate.other_work_id AS work_id
				FROM (
					SELECT
						assertion.id AS assertion_id,
						assertion.object_work_id AS other_work_id
					FROM work_relation_assertions AS assertion
					WHERE assertion.subject_work_id = affected.work_id

					UNION ALL

					SELECT
						assertion.id AS assertion_id,
						assertion.subject_work_id AS other_work_id
					FROM work_relation_assertions AS assertion
					WHERE assertion.object_work_id = affected.work_id
				) AS candidate
				CROSS JOIN LATERAL (
					SELECT decision.outcome
					FROM work_relation_decisions AS decision
					WHERE decision.assertion_id = candidate.assertion_id
					  AND NOT EXISTS (
						  SELECT 1
						  FROM work_relation_decisions AS successor
						  WHERE successor.supersedes_decision_id = decision.id
					  )
					LIMIT 1
				) AS current_decision
				WHERE current_decision.outcome = 'accepted'
			) AS neighbor
		)
		SELECT work_id
		FROM affected_work
		ORDER BY work_id
	`, seedWorkIDs)
	if err != nil {
		return nil, fmt.Errorf(
			"query current accepted relation component Works: %w",
			err,
		)
	}
	var affectedWorkIDs []uuid.UUID
	for workRows.Next() {
		var workID uuid.UUID
		if err := workRows.Scan(&workID); err != nil {
			workRows.Close()
			return nil, fmt.Errorf(
				"scan current accepted relation component Work: %w",
				err,
			)
		}
		affectedWorkIDs = append(affectedWorkIDs, workID)
	}
	if err := workRows.Err(); err != nil {
		workRows.Close()
		return nil, fmt.Errorf(
			"iterate current accepted relation component Works: %w",
			err,
		)
	}
	workRows.Close()

	rows, err := tx.Query(ctx, `
		WITH component_assertions AS MATERIALIZED (
			SELECT
				assertion.id,
				assertion.subject_work_id,
				assertion.object_work_id,
				assertion.relation_kind
			FROM work_relation_assertions AS assertion
			WHERE assertion.subject_work_id = ANY($1::uuid[])
			  AND assertion.object_work_id = ANY($1::uuid[])
		)
		SELECT DISTINCT
			assertion.id AS assertion_id,
			assertion.subject_work_id,
			assertion.object_work_id,
			assertion.relation_kind,
			decision.id AS decision_id,
			decision.decided_at
		FROM component_assertions AS assertion
		CROSS JOIN LATERAL (
			SELECT
				decision.id,
				decision.outcome,
				decision.decided_at
			FROM work_relation_decisions AS decision
			WHERE decision.assertion_id = assertion.id
			  AND NOT EXISTS (
				  SELECT 1
				  FROM work_relation_decisions AS successor
				  WHERE successor.supersedes_decision_id = decision.id
			  )
			LIMIT 1
		) AS decision
		WHERE decision.outcome = 'accepted'
		ORDER BY assertion.id, decision.id
	`, affectedWorkIDs)
	if err != nil {
		return nil, fmt.Errorf(
			"query current accepted relation graph: %w",
			err,
		)
	}
	defer rows.Close()

	var edges []activeRelationEdge
	for rows.Next() {
		var edge activeRelationEdge
		if err := rows.Scan(
			&edge.AssertionID,
			&edge.SubjectWorkID,
			&edge.ObjectWorkID,
			&edge.RelationKind,
			&edge.DecisionID,
			&edge.DecidedAt,
		); err != nil {
			return nil, fmt.Errorf(
				"scan current accepted relation edge: %w",
				err,
			)
		}
		edge.DecidedAt = normalizeTimestamp(edge.DecidedAt)
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate current accepted relation graph: %w",
			err,
		)
	}
	return edges, nil
}

func buildRelationComponents(
	workIDs []uuid.UUID,
	edges []activeRelationEdge,
) []relationComponent {
	adjacent := make(map[uuid.UUID][]uuid.UUID, len(workIDs))
	for _, workID := range workIDs {
		adjacent[workID] = nil
	}
	for _, edge := range edges {
		adjacent[edge.SubjectWorkID] = append(
			adjacent[edge.SubjectWorkID],
			edge.ObjectWorkID,
		)
		adjacent[edge.ObjectWorkID] = append(
			adjacent[edge.ObjectWorkID],
			edge.SubjectWorkID,
		)
	}
	for workID := range adjacent {
		adjacent[workID] = uniqueSortedUUIDs(adjacent[workID])
	}

	visited := make(map[uuid.UUID]bool, len(workIDs))
	var components []relationComponent
	for _, startWorkID := range uniqueSortedUUIDs(workIDs) {
		if visited[startWorkID] {
			continue
		}
		queue := []uuid.UUID{startWorkID}
		visited[startWorkID] = true
		var componentWorkIDs []uuid.UUID
		for len(queue) > 0 {
			workID := queue[0]
			queue = queue[1:]
			componentWorkIDs = append(componentWorkIDs, workID)
			for _, adjacentWorkID := range adjacent[workID] {
				if visited[adjacentWorkID] {
					continue
				}
				visited[adjacentWorkID] = true
				queue = append(queue, adjacentWorkID)
			}
		}
		componentWorkIDs = uniqueSortedUUIDs(componentWorkIDs)
		componentWorkSet := make(map[uuid.UUID]struct{}, len(componentWorkIDs))
		for _, workID := range componentWorkIDs {
			componentWorkSet[workID] = struct{}{}
		}
		component := relationComponent{WorkIDs: componentWorkIDs}
		for _, edge := range edges {
			_, subjectIncluded := componentWorkSet[edge.SubjectWorkID]
			_, objectIncluded := componentWorkSet[edge.ObjectWorkID]
			if subjectIncluded && objectIncluded {
				component.Edges = append(component.Edges, edge)
			}
		}
		components = append(components, component)
	}
	return components
}

func createRebuiltCanonicalState(
	ctx context.Context,
	tx pgx.Tx,
	familyID uuid.UUID,
	component relationComponent,
	triggerDecision RelationDecision,
	projectedAt time.Time,
) error {
	selected := canonicalSelection{
		DecisionID: triggerDecision.ID,
		FamilyID:   familyID,
		WorkID:     component.WorkIDs[0],
		Basis:      "singleton",
		Precedence: canonicalPrecedenceSingleton,
		DecidedAt:  triggerDecision.DecidedAt,
	}
	selectedFromRelation := false
	for _, edge := range component.Edges {
		candidateWorkID, precedence := canonicalCandidateForAcceptedRelation(
			RelationAssertion{
				SubjectWorkID: edge.SubjectWorkID,
				ObjectWorkID:  edge.ObjectWorkID,
				RelationKind:  edge.RelationKind,
			},
		)
		candidate := canonicalSelection{
			DecisionID: edge.DecisionID,
			FamilyID:   familyID,
			WorkID:     candidateWorkID,
			Basis:      string(edge.RelationKind),
			Precedence: precedence,
			DecidedAt:  edge.DecidedAt,
		}
		if !selectedFromRelation ||
			canonicalSelectionOutranks(candidate, selected) {
			selected = candidate
			selectedFromRelation = true
		}
	}

	var canonicalDecisionID uuid.UUID
	if selectedFromRelation {
		if err := tx.QueryRow(ctx, `
			INSERT INTO work_family_canonical_decisions (
				work_family_id,
				canonical_work_id,
				relation_decision_id,
				decision_kind,
				basis,
				precedence,
				reason,
				policy_version,
				decided_at
			) VALUES (
				$1,
				$2,
				$3,
				'accepted_relation',
				$4,
				$5,
				$6,
				$7,
				$8
			)
			RETURNING id
		`,
			familyID,
			selected.WorkID,
			selected.DecisionID,
			selected.Basis,
			selected.Precedence,
			"active accepted relation "+selected.Basis,
			canonicalPolicyVersion,
			selected.DecidedAt,
		).Scan(&canonicalDecisionID); err != nil {
			return fmt.Errorf(
				"record rebuilt relation canonical decision: %w",
				err,
			)
		}
	} else {
		if err := tx.QueryRow(ctx, `
			INSERT INTO work_family_canonical_decisions (
				work_family_id,
				canonical_work_id,
				relation_decision_id,
				decision_kind,
				basis,
				precedence,
				reason,
				policy_version,
				decided_at
			) VALUES (
				$1,
				$2,
				$3,
				'component_rebuild',
				'singleton',
				$4,
				$5,
				$6,
				$7
			)
			RETURNING id
		`,
			familyID,
			selected.WorkID,
			triggerDecision.ID,
			canonicalPrecedenceSingleton,
			"singleton component after relation decision "+
				triggerDecision.ID.String(),
			canonicalPolicyVersion,
			triggerDecision.DecidedAt,
		).Scan(&canonicalDecisionID); err != nil {
			return fmt.Errorf(
				"record rebuilt singleton canonical decision: %w",
				err,
			)
		}
	}
	selected.DecisionID = canonicalDecisionID
	selected.FamilyID = familyID
	return projectCanonicalSelection(ctx, tx, selected, projectedAt)
}

func loadCurrentCanonicalSelection(
	ctx context.Context,
	db queryRower,
	familyID uuid.UUID,
) (canonicalSelection, error) {
	var selection canonicalSelection
	err := db.QueryRow(ctx, `
		SELECT
			decision.id,
			decision.work_family_id,
			decision.canonical_work_id,
			decision.basis,
			decision.precedence,
			decision.decided_at
		FROM work_family_canonical_states AS state
		JOIN work_family_canonical_decisions AS decision
		  ON decision.id = state.canonical_decision_id
		WHERE state.work_family_id = $1
	`, familyID).Scan(
		&selection.DecisionID,
		&selection.FamilyID,
		&selection.WorkID,
		&selection.Basis,
		&selection.Precedence,
		&selection.DecidedAt,
	)
	if err != nil {
		return canonicalSelection{}, fmt.Errorf(
			"load current canonical selection for family %s: %w",
			familyID,
			err,
		)
	}
	selection.DecidedAt = normalizeTimestamp(selection.DecidedAt)
	return selection, nil
}

func insertRelationCanonicalCandidate(
	ctx context.Context,
	tx pgx.Tx,
	familyID uuid.UUID,
	canonicalWorkID uuid.UUID,
	relationKind RelationKind,
	precedence int,
	decision RelationDecision,
) (canonicalSelection, error) {
	selection := canonicalSelection{
		FamilyID:   familyID,
		WorkID:     canonicalWorkID,
		Basis:      string(relationKind),
		Precedence: precedence,
		DecidedAt:  decision.DecidedAt,
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO work_family_canonical_decisions (
			work_family_id,
			canonical_work_id,
			relation_decision_id,
			decision_kind,
			basis,
			precedence,
			reason,
			policy_version,
			decided_at
		) VALUES (
			$1,
			$2,
			$3,
			'accepted_relation',
			$4,
			$5,
			$6,
			$7,
			$8
		)
		RETURNING id
	`,
		familyID,
		canonicalWorkID,
		decision.ID,
		string(relationKind),
		precedence,
		"accepted relation "+string(relationKind),
		canonicalPolicyVersion,
		decision.DecidedAt,
	).Scan(&selection.DecisionID); err != nil {
		return canonicalSelection{}, fmt.Errorf(
			"record relation canonical decision: %w",
			err,
		)
	}
	return selection, nil
}

func carryCanonicalSelection(
	ctx context.Context,
	tx pgx.Tx,
	familyID uuid.UUID,
	source canonicalSelection,
) (canonicalSelection, error) {
	carried := source
	carried.DecisionID = uuid.Nil
	carried.FamilyID = familyID
	if err := tx.QueryRow(ctx, `
		INSERT INTO work_family_canonical_decisions (
			work_family_id,
			canonical_work_id,
			prior_canonical_decision_id,
			decision_kind,
			basis,
			precedence,
			reason,
			policy_version,
			decided_at
		) VALUES (
			$1,
			$2,
			$3,
			'carried_forward',
			$4,
			$5,
			$6,
			$7,
			$8
		)
		RETURNING id
	`,
		familyID,
		source.WorkID,
		source.DecisionID,
		source.Basis,
		source.Precedence,
		"carried forward canonical decision "+source.DecisionID.String(),
		canonicalPolicyVersion,
		source.DecidedAt,
	).Scan(&carried.DecisionID); err != nil {
		return canonicalSelection{}, fmt.Errorf(
			"carry canonical decision into merged family: %w",
			err,
		)
	}
	return carried, nil
}

func projectCanonicalSelection(
	ctx context.Context,
	tx pgx.Tx,
	selection canonicalSelection,
	projectedAt time.Time,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_family_canonical_states (
			work_family_id,
			canonical_work_id,
			canonical_decision_id,
			updated_at
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (work_family_id)
		DO UPDATE SET
			canonical_work_id = EXCLUDED.canonical_work_id,
			canonical_decision_id = EXCLUDED.canonical_decision_id,
			updated_at = EXCLUDED.updated_at
	`,
		selection.FamilyID,
		selection.WorkID,
		selection.DecisionID,
		projectedAt,
	); err != nil {
		return fmt.Errorf("project current canonical Work: %w", err)
	}
	return nil
}

func canonicalCandidateForAcceptedRelation(
	assertion RelationAssertion,
) (uuid.UUID, int) {
	switch assertion.RelationKind {
	case RelationHasPreprint,
		RelationIsPreprintOf:
		if assertion.RelationKind == RelationHasPreprint {
			return assertion.SubjectWorkID, canonicalPrecedenceVersionOfRecord
		}
		return assertion.ObjectWorkID, canonicalPrecedenceVersionOfRecord
	case RelationHasVersion:
		return assertion.SubjectWorkID, canonicalPrecedenceGenericVersion
	case RelationIsVersionOf:
		return assertion.ObjectWorkID, canonicalPrecedenceGenericVersion
	case RelationReplaces:
		return assertion.SubjectWorkID, canonicalPrecedenceLifecycle
	case RelationIsReplacedBy,
		RelationIsCorrection,
		RelationIsRetraction:
		return assertion.ObjectWorkID, canonicalPrecedenceLifecycle
	default:
		panic("validated relation kind has no canonical semantics")
	}
}

func canonicalSelectionOutranks(
	candidate canonicalSelection,
	current canonicalSelection,
) bool {
	if candidate.Precedence != current.Precedence {
		return candidate.Precedence > current.Precedence
	}
	if !candidate.DecidedAt.Equal(current.DecidedAt) {
		return candidate.DecidedAt.After(current.DecidedAt)
	}
	if workOrder := bytes.Compare(
		candidate.WorkID[:],
		current.WorkID[:],
	); workOrder != 0 {
		return workOrder < 0
	}
	return bytes.Compare(
		candidate.DecisionID[:],
		current.DecisionID[:],
	) < 0
}

func findRelationAssertionByIdentity(
	ctx context.Context,
	db queryRower,
	assertion RelationAssertion,
) (RelationAssertion, bool, error) {
	return scanRelationAssertion(db.QueryRow(ctx, `
		SELECT
			id,
			subject_work_id,
			object_work_id,
			relation_kind,
			evidence_kind,
			subject_source_record_id,
			subject_source_path,
			object_source_record_id,
			object_source_path,
			identifier_scheme,
			identifier_value,
			asserted_at
		FROM work_relation_assertions
		WHERE subject_work_id = $1
		  AND object_work_id = $2
		  AND relation_kind = $3
		  AND evidence_kind = $4
		  AND subject_source_record_id = $5
		  AND subject_source_path = $6
		  AND object_source_record_id IS NOT DISTINCT FROM $7
		  AND object_source_path IS NOT DISTINCT FROM $8
		  AND identifier_scheme IS NOT DISTINCT FROM $9
		  AND identifier_value IS NOT DISTINCT FROM $10
	`,
		assertion.SubjectWorkID,
		assertion.ObjectWorkID,
		assertion.RelationKind,
		assertion.EvidenceKind,
		assertion.SubjectSourceRecordID,
		assertion.SubjectSourcePath,
		nullableUUID(assertion.ObjectSourceRecordID),
		nullableText(assertion.ObjectSourcePath),
		nullableText(assertion.IdentifierScheme),
		nullableText(assertion.IdentifierValue),
	))
}

func findRelationAssertionByID(
	ctx context.Context,
	db queryRower,
	assertionID uuid.UUID,
) (RelationAssertion, bool, error) {
	return relationAssertionByID(ctx, db, assertionID, false)
}

func lockRelationAssertionByID(
	ctx context.Context,
	db queryRower,
	assertionID uuid.UUID,
) (RelationAssertion, bool, error) {
	return relationAssertionByID(ctx, db, assertionID, true)
}

func relationAssertionByID(
	ctx context.Context,
	db queryRower,
	assertionID uuid.UUID,
	lock bool,
) (RelationAssertion, bool, error) {
	query := `
		SELECT
			id,
			subject_work_id,
			object_work_id,
			relation_kind,
			evidence_kind,
			subject_source_record_id,
			subject_source_path,
			object_source_record_id,
			object_source_path,
			identifier_scheme,
			identifier_value,
			asserted_at
		FROM work_relation_assertions
		WHERE id = $1
	`
	if lock {
		query += " FOR UPDATE"
	}
	assertion, found, err := scanRelationAssertion(
		db.QueryRow(ctx, query, assertionID),
	)
	if err != nil {
		return RelationAssertion{}, false, fmt.Errorf(
			"load Work relation assertion: %w",
			err,
		)
	}
	return assertion, found, nil
}

type relationRow interface {
	Scan(...any) error
}

func scanRelationAssertion(
	row relationRow,
) (RelationAssertion, bool, error) {
	var (
		assertion          RelationAssertion
		objectSourceRecord pgtype.UUID
		objectSourcePath   pgtype.Text
		identifierScheme   pgtype.Text
		identifierValue    pgtype.Text
	)
	err := row.Scan(
		&assertion.ID,
		&assertion.SubjectWorkID,
		&assertion.ObjectWorkID,
		&assertion.RelationKind,
		&assertion.EvidenceKind,
		&assertion.SubjectSourceRecordID,
		&assertion.SubjectSourcePath,
		&objectSourceRecord,
		&objectSourcePath,
		&identifierScheme,
		&identifierValue,
		&assertion.AssertedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RelationAssertion{}, false, nil
	}
	if err != nil {
		return RelationAssertion{}, false, err
	}
	if objectSourceRecord.Valid {
		assertion.ObjectSourceRecordID = objectSourceRecord.Bytes
	}
	if objectSourcePath.Valid {
		assertion.ObjectSourcePath = objectSourcePath.String
	}
	if identifierScheme.Valid {
		assertion.IdentifierScheme = identifierScheme.String
	}
	if identifierValue.Valid {
		assertion.IdentifierValue = identifierValue.String
	}
	assertion.AssertedAt = normalizeTimestamp(assertion.AssertedAt)
	if err := assertion.Validate(); err != nil {
		return RelationAssertion{}, false, fmt.Errorf(
			"restore Work relation assertion: %w",
			err,
		)
	}
	return assertion, true, nil
}

func findRelationDecisionByIdentity(
	ctx context.Context,
	db queryRower,
	decision RelationDecision,
) (RelationDecision, bool, error) {
	return scanRelationDecision(db.QueryRow(ctx, `
		SELECT
			id,
			assertion_id,
			supersedes_decision_id,
			outcome,
			reason,
			policy_version,
			decided_at
		FROM work_relation_decisions
		WHERE assertion_id = $1
		  AND supersedes_decision_id IS NOT DISTINCT FROM $2
		  AND outcome = $3
		  AND reason = $4
		  AND policy_version = $5
		  AND decided_at = $6
	`,
		decision.AssertionID,
		nullableUUID(decision.SupersedesDecisionID),
		decision.Outcome,
		decision.Reason,
		decision.PolicyVersion,
		decision.DecidedAt,
	))
}

func findCurrentRelationDecisionByAssertion(
	ctx context.Context,
	db queryRower,
	assertionID uuid.UUID,
) (RelationDecision, bool, error) {
	return scanRelationDecision(db.QueryRow(ctx, `
		SELECT
			decision.id,
			decision.assertion_id,
			decision.supersedes_decision_id,
			decision.outcome,
			decision.reason,
			decision.policy_version,
			decision.decided_at
		FROM work_relation_decisions AS decision
		WHERE decision.assertion_id = $1
		  AND NOT EXISTS (
		      SELECT 1
		      FROM work_relation_decisions AS successor
		      WHERE successor.supersedes_decision_id = decision.id
		  )
		FOR UPDATE OF decision
	`, assertionID))
}

func scanRelationDecision(
	row relationRow,
) (RelationDecision, bool, error) {
	var decision RelationDecision
	var supersedesDecision pgtype.UUID
	err := row.Scan(
		&decision.ID,
		&decision.AssertionID,
		&supersedesDecision,
		&decision.Outcome,
		&decision.Reason,
		&decision.PolicyVersion,
		&decision.DecidedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RelationDecision{}, false, nil
	}
	if err != nil {
		return RelationDecision{}, false, fmt.Errorf(
			"load Work relation decision: %w",
			err,
		)
	}
	if supersedesDecision.Valid {
		decision.SupersedesDecisionID = supersedesDecision.Bytes
	}
	decision.DecidedAt = normalizeTimestamp(decision.DecidedAt)
	if err := decision.Validate(); err != nil {
		return RelationDecision{}, false, fmt.Errorf(
			"restore Work relation decision: %w",
			err,
		)
	}
	return decision, true, nil
}

func scanTimelineRelation(row relationRow) (TimelineRelation, error) {
	var (
		relation           TimelineRelation
		objectSourceRecord pgtype.UUID
		objectSourcePath   pgtype.Text
		identifierScheme   pgtype.Text
		identifierValue    pgtype.Text
		supersedesDecision pgtype.UUID
	)
	err := row.Scan(
		&relation.Assertion.ID,
		&relation.Assertion.SubjectWorkID,
		&relation.Assertion.ObjectWorkID,
		&relation.Assertion.RelationKind,
		&relation.Assertion.EvidenceKind,
		&relation.Assertion.SubjectSourceRecordID,
		&relation.Assertion.SubjectSourcePath,
		&objectSourceRecord,
		&objectSourcePath,
		&identifierScheme,
		&identifierValue,
		&relation.Assertion.AssertedAt,
		&relation.Decision.ID,
		&supersedesDecision,
		&relation.Decision.Outcome,
		&relation.Decision.Reason,
		&relation.Decision.PolicyVersion,
		&relation.Decision.DecidedAt,
	)
	if err != nil {
		return TimelineRelation{}, fmt.Errorf(
			"scan Work Family timeline relation: %w",
			err,
		)
	}
	relation.Decision.AssertionID = relation.Assertion.ID
	if supersedesDecision.Valid {
		relation.Decision.SupersedesDecisionID =
			supersedesDecision.Bytes
	}
	if objectSourceRecord.Valid {
		relation.Assertion.ObjectSourceRecordID = objectSourceRecord.Bytes
	}
	if objectSourcePath.Valid {
		relation.Assertion.ObjectSourcePath = objectSourcePath.String
	}
	if identifierScheme.Valid {
		relation.Assertion.IdentifierScheme = identifierScheme.String
	}
	if identifierValue.Valid {
		relation.Assertion.IdentifierValue = identifierValue.String
	}
	relation.Assertion.AssertedAt = normalizeTimestamp(
		relation.Assertion.AssertedAt,
	)
	relation.Decision.DecidedAt = normalizeTimestamp(
		relation.Decision.DecidedAt,
	)
	if err := relation.Assertion.Validate(); err != nil {
		return TimelineRelation{}, fmt.Errorf(
			"restore Work Family timeline assertion: %w",
			err,
		)
	}
	if err := relation.Decision.Validate(); err != nil {
		return TimelineRelation{}, fmt.Errorf(
			"restore Work Family timeline decision: %w",
			err,
		)
	}
	return relation, nil
}

func relationAssertionsEqual(
	left RelationAssertion,
	right RelationAssertion,
) bool {
	return left.SubjectWorkID == right.SubjectWorkID &&
		left.ObjectWorkID == right.ObjectWorkID &&
		left.RelationKind == right.RelationKind &&
		left.EvidenceKind == right.EvidenceKind &&
		left.SubjectSourceRecordID == right.SubjectSourceRecordID &&
		left.SubjectSourcePath == right.SubjectSourcePath &&
		left.ObjectSourceRecordID == right.ObjectSourceRecordID &&
		left.ObjectSourcePath == right.ObjectSourcePath &&
		left.IdentifierScheme == right.IdentifierScheme &&
		left.IdentifierValue == right.IdentifierValue &&
		left.AssertedAt.Equal(right.AssertedAt)
}

func relationDecisionsEqual(
	left RelationDecision,
	right RelationDecision,
) bool {
	return left.AssertionID == right.AssertionID &&
		left.SupersedesDecisionID == right.SupersedesDecisionID &&
		left.Outcome == right.Outcome &&
		left.Reason == right.Reason &&
		left.PolicyVersion == right.PolicyVersion &&
		left.DecidedAt.Equal(right.DecidedAt)
}

func nullableUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
