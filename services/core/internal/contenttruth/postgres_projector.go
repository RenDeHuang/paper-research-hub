package contenttruth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

var ErrPostgresProjectionConflict = errors.New(
	"persisted PostgreSQL projection conflicts with identical input digest",
)

type PostgresChannelEventDecision struct {
	ID          uuid.UUID
	InputDigest string
	Decision    ChannelEventDecision
}

func ProjectCanonicalChannelEventInTx(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	channel scope.ContentChannel,
	asOf time.Time,
	policy ChannelEventPolicy,
) (PostgresChannelEventDecision, error) {
	if err := validatePostgresProjectorCall(ctx, tx, workID, asOf); err != nil {
		return PostgresChannelEventDecision{}, err
	}
	if _, err := scope.ParseContentChannel(string(channel)); err != nil {
		return PostgresChannelEventDecision{}, err
	}

	asOf = normalizePostgresTimestamp(asOf)
	assertions, err := loadChannelEventAssertions(
		ctx,
		tx,
		workID,
		channel,
		asOf,
	)
	if err != nil {
		return PostgresChannelEventDecision{}, err
	}
	decision, err := ProjectCanonicalChannelEvent(
		workID,
		channel,
		assertions,
		policy,
		asOf,
	)
	if err != nil {
		return PostgresChannelEventDecision{}, err
	}
	inputDigest, err := channelEventInputDigest(
		workID,
		channel,
		asOf,
		policy,
		assertions,
	)
	if err != nil {
		return PostgresChannelEventDecision{}, err
	}
	evidence, err := marshalChannelEventEvidence(decision, assertions)
	if err != nil {
		return PostgresChannelEventDecision{}, err
	}

	persistedID, inserted, err := insertChannelEventDecision(
		ctx,
		tx,
		decision,
		inputDigest,
		evidence,
	)
	if err != nil {
		return PostgresChannelEventDecision{}, err
	}
	if !inserted {
		persistedID, err = verifyExistingChannelEventDecision(
			ctx,
			tx,
			decision,
			inputDigest,
			evidence,
		)
		if err != nil {
			return PostgresChannelEventDecision{}, err
		}
	}
	return PostgresChannelEventDecision{
		ID:          persistedID,
		InputDigest: inputDigest,
		Decision:    decision,
	}, nil
}

func validatePostgresProjectorCall(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	asOf time.Time,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx == nil {
		return errors.New("PostgreSQL transaction is required")
	}
	if workID == uuid.Nil {
		return errors.New("PostgreSQL projector Work ID is required")
	}
	if asOf.IsZero() {
		return errors.New("PostgreSQL projector as_of is required")
	}
	return nil
}

func normalizePostgresTimestamp(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func loadChannelEventAssertions(
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	channel scope.ContentChannel,
	asOf time.Time,
) ([]ChannelEventAssertion, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			id,
			work_id,
			source_record_id,
			normalized_assertion_id,
			channel,
			event_kind,
			event_at,
			source_path,
			asserted_at
		FROM work_channel_event_assertions
		WHERE work_id = $1
		  AND channel = $2
		  AND asserted_at <= $3
		ORDER BY id
	`, workID, channel, asOf)
	if err != nil {
		return nil, fmt.Errorf("load exact channel event assertions: %w", err)
	}
	defer rows.Close()

	var assertions []ChannelEventAssertion
	for rows.Next() {
		var (
			assertion        ChannelEventAssertion
			persistedChannel string
			persistedKind    string
		)
		if err := rows.Scan(
			&assertion.ID,
			&assertion.WorkID,
			&assertion.SourceRecordID,
			&assertion.SourceRevisionID,
			&persistedChannel,
			&persistedKind,
			&assertion.EventAt,
			&assertion.SourcePath,
			&assertion.AssertedAt,
		); err != nil {
			return nil, fmt.Errorf("scan exact channel event assertion: %w", err)
		}
		assertion.Channel = scope.ContentChannel(persistedChannel)
		assertion.Kind = ChannelEventKind(persistedKind)
		assertion.EventAt = normalizePostgresTimestamp(assertion.EventAt)
		assertion.AssertedAt = normalizePostgresTimestamp(assertion.AssertedAt)
		assertions = append(assertions, assertion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate exact channel event assertions: %w", err)
	}
	return assertions, nil
}

type channelEventDigestAssertion struct {
	ID               string `json:"id"`
	SourceRecordID   string `json:"source_record_id"`
	SourceRevisionID string `json:"source_revision_id"`
	Channel          string `json:"channel"`
	Kind             string `json:"kind"`
	EventAt          string `json:"event_at"`
	SourcePath       string `json:"source_path"`
	AssertedAt       string `json:"asserted_at"`
}

type channelEventDigestMaterial struct {
	Schema        string                        `json:"schema"`
	WorkID        string                        `json:"work_id"`
	Channel       string                        `json:"channel"`
	AsOf          string                        `json:"as_of"`
	PolicyVersion string                        `json:"policy_version"`
	Priority      []ChannelEventKind            `json:"priority"`
	Assertions    []channelEventDigestAssertion `json:"assertions"`
}

func channelEventInputDigest(
	workID uuid.UUID,
	channel scope.ContentChannel,
	asOf time.Time,
	policy ChannelEventPolicy,
	assertions []ChannelEventAssertion,
) (string, error) {
	priority, ok := policy.priority[channel]
	if !ok {
		return "", fmt.Errorf(
			"channel event policy %q has no rule for %q",
			policy.Version,
			channel,
		)
	}
	ordered := slices.Clone(assertions)
	slices.SortFunc(ordered, func(left, right ChannelEventAssertion) int {
		return strings.Compare(left.ID.String(), right.ID.String())
	})
	material := channelEventDigestMaterial{
		Schema:        "contenttruth/channel-event-input/v1",
		WorkID:        workID.String(),
		Channel:       string(channel),
		AsOf:          normalizePostgresTimestamp(asOf).Format(time.RFC3339Nano),
		PolicyVersion: policy.Version,
		Priority:      slices.Clone(priority),
		Assertions: make(
			[]channelEventDigestAssertion,
			0,
			len(ordered),
		),
	}
	for _, assertion := range ordered {
		material.Assertions = append(
			material.Assertions,
			channelEventDigestAssertion{
				ID:               assertion.ID.String(),
				SourceRecordID:   assertion.SourceRecordID.String(),
				SourceRevisionID: assertion.SourceRevisionID.String(),
				Channel:          string(assertion.Channel),
				Kind:             string(assertion.Kind),
				EventAt: normalizePostgresTimestamp(
					assertion.EventAt,
				).Format(time.RFC3339Nano),
				SourcePath: assertion.SourcePath,
				AssertedAt: normalizePostgresTimestamp(
					assertion.AssertedAt,
				).Format(time.RFC3339Nano),
			},
		)
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("encode channel event digest input: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type persistedChannelEventAssertion struct {
	AssertionID      uuid.UUID        `json:"assertion_id"`
	SourceRecordID   uuid.UUID        `json:"source_record_id"`
	SourceRevisionID uuid.UUID        `json:"source_revision_id"`
	SourcePath       string           `json:"source_path"`
	EventKind        ChannelEventKind `json:"event_kind"`
	EventAt          time.Time        `json:"event_at"`
}

type persistedChannelEventEvidence struct {
	Assertions []persistedChannelEventAssertion `json:"assertions"`
}

func marshalChannelEventEvidence(
	decision ChannelEventDecision,
	assertions []ChannelEventAssertion,
) ([]byte, error) {
	byID := make(map[uuid.UUID]ChannelEventAssertion, len(assertions))
	for _, assertion := range assertions {
		byID[assertion.ID] = assertion
	}
	document := persistedChannelEventEvidence{
		Assertions: make(
			[]persistedChannelEventAssertion,
			0,
			len(decision.Evidence),
		),
	}
	for _, evidence := range decision.Evidence {
		assertion, ok := byID[evidence.AssertionID]
		if !ok {
			return nil, fmt.Errorf(
				"projected channel event evidence assertion %s is missing",
				evidence.AssertionID,
			)
		}
		document.Assertions = append(
			document.Assertions,
			persistedChannelEventAssertion{
				AssertionID:      evidence.AssertionID,
				SourceRecordID:   evidence.SourceRecordID,
				SourceRevisionID: evidence.SourceRevisionID,
				SourcePath:       evidence.SourcePath,
				EventKind:        assertion.Kind,
				EventAt:          normalizePostgresTimestamp(assertion.EventAt),
			},
		)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode channel event evidence: %w", err)
	}
	return encoded, nil
}

func insertChannelEventDecision(
	ctx context.Context,
	tx pgx.Tx,
	decision ChannelEventDecision,
	inputDigest string,
	evidence []byte,
) (uuid.UUID, bool, error) {
	var eventKind any
	var eventAt any
	if decision.State == AssertionStateKnown {
		eventKind = decision.Kind
		eventAt = normalizePostgresTimestamp(decision.EventAt)
	}

	var persistedID uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO work_channel_event_decisions (
			work_id,
			channel,
			state,
			event_kind,
			event_at,
			policy_version,
			input_digest,
			evidence,
			decided_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9
		)
		ON CONFLICT ON CONSTRAINT
			work_channel_event_decisions_identity_key
		DO NOTHING
		RETURNING id
	`,
		decision.WorkID,
		decision.Channel,
		decision.State,
		eventKind,
		eventAt,
		decision.PolicyVersion,
		inputDigest,
		evidence,
		normalizePostgresTimestamp(decision.DecidedAt),
	).Scan(&persistedID)
	switch {
	case err == nil:
		return persistedID, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, false, nil
	default:
		return uuid.Nil, false, fmt.Errorf(
			"insert canonical channel event decision: %w",
			err,
		)
	}
}

func verifyExistingChannelEventDecision(
	ctx context.Context,
	tx pgx.Tx,
	decision ChannelEventDecision,
	inputDigest string,
	evidence []byte,
) (uuid.UUID, error) {
	var (
		persistedID        uuid.UUID
		persistedState     string
		persistedKind      pgtype.Text
		persistedEventAt   pgtype.Timestamptz
		persistedPolicy    string
		persistedDigest    string
		persistedEvidence  []byte
		persistedDecidedAt time.Time
	)
	err := tx.QueryRow(ctx, `
		SELECT
			id,
			state,
			event_kind,
			event_at,
			policy_version,
			input_digest,
			evidence,
			decided_at
		FROM work_channel_event_decisions
		WHERE work_id = $1
		  AND channel = $2
		  AND policy_version = $3
		  AND input_digest = $4
	`,
		decision.WorkID,
		decision.Channel,
		decision.PolicyVersion,
		inputDigest,
	).Scan(
		&persistedID,
		&persistedState,
		&persistedKind,
		&persistedEventAt,
		&persistedPolicy,
		&persistedDigest,
		&persistedEvidence,
		&persistedDecidedAt,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf(
			"load replayed canonical channel event decision: %w",
			err,
		)
	}

	expectedKindValid := decision.State == AssertionStateKnown
	expectedEventAtValid := decision.State == AssertionStateKnown
	if persistedState != string(decision.State) ||
		persistedKind.Valid != expectedKindValid ||
		(persistedKind.Valid && persistedKind.String != string(decision.Kind)) ||
		persistedEventAt.Valid != expectedEventAtValid ||
		(persistedEventAt.Valid &&
			!persistedEventAt.Time.Equal(decision.EventAt)) ||
		persistedPolicy != decision.PolicyVersion ||
		persistedDigest != inputDigest ||
		!jsonValuesEqual(persistedEvidence, evidence) ||
		!persistedDecidedAt.Equal(decision.DecidedAt) {
		return uuid.Nil, fmt.Errorf(
			"%w: canonical channel event decision %s",
			ErrPostgresProjectionConflict,
			persistedID,
		)
	}
	return persistedID, nil
}

func jsonValuesEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil ||
		json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}
