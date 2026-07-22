package contenttruth

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestProjectCanonicalChannelEventInTxReplaysCanonicalSortedOutputAndHonorsCallerTransaction(
	t *testing.T,
) {
	pool := openMigratedContentTruthTestPool(t)
	ctx := contentTruthTestContext(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin channel event projector transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	base := time.Date(2026, time.July, 18, 0, 0, 0, 123000, time.UTC)
	workID := insertContentTruthWork(t, ctx, tx, base)
	onlineOne := insertContentTruthRevision(t, ctx, tx, workID, base.Add(time.Hour))
	onlineTwo := insertContentTruthRevision(t, ctx, tx, workID, base.Add(2*time.Hour))
	printRevision := insertContentTruthRevision(t, ctx, tx, workID, base.Add(3*time.Hour))
	futureRevision := insertContentTruthRevision(t, ctx, tx, workID, base.Add(4*time.Hour))

	onlineAt := time.Date(
		2026,
		time.July,
		19,
		9,
		30,
		0,
		456000,
		time.FixedZone("UTC+8", 8*60*60),
	)
	asOf := time.Date(
		2026,
		time.July,
		19,
		12,
		0,
		0,
		789000,
		time.FixedZone("UTC+8", 8*60*60),
	)
	assertionIDs := []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
	}
	insertChannelEventAssertion(
		t,
		ctx,
		tx,
		assertionIDs[0],
		onlineTwo,
		workID,
		scope.ContentChannelJournalPublished,
		ChannelEventOfficialOnline,
		onlineAt,
		"$.published-online.second",
		asOf.Add(-time.Minute),
	)
	insertChannelEventAssertion(
		t,
		ctx,
		tx,
		assertionIDs[1],
		onlineOne,
		workID,
		scope.ContentChannelJournalPublished,
		ChannelEventOfficialOnline,
		onlineAt.UTC(),
		"$.published-online.first",
		asOf.Add(-2*time.Minute),
	)
	insertChannelEventAssertion(
		t,
		ctx,
		tx,
		uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		printRevision,
		workID,
		scope.ContentChannelJournalPublished,
		ChannelEventOfficialPrint,
		onlineAt.Add(24*time.Hour),
		"$.published-print",
		asOf.Add(-3*time.Minute),
	)
	insertChannelEventAssertion(
		t,
		ctx,
		tx,
		uuid.MustParse("00000000-0000-0000-0000-000000000004"),
		futureRevision,
		workID,
		scope.ContentChannelJournalPublished,
		ChannelEventOfficialOnline,
		onlineAt.Add(48*time.Hour),
		"$.future-published-online",
		asOf.Add(time.Microsecond),
	)

	policy, err := NewChannelEventPolicy(
		"canonical-channel-event/postgres-v1",
		map[scope.ContentChannel][]ChannelEventKind{
			scope.ContentChannelJournalPublished: {
				ChannelEventOfficialOnline,
				ChannelEventOfficialPrint,
			},
		},
	)
	if err != nil {
		t.Fatalf("NewChannelEventPolicy() error = %v", err)
	}

	first, err := ProjectCanonicalChannelEventInTx(
		ctx,
		tx,
		workID,
		scope.ContentChannelJournalPublished,
		asOf,
		policy,
	)
	if err != nil {
		t.Fatalf("ProjectCanonicalChannelEventInTx() error = %v", err)
	}
	if first.ID == uuid.Nil {
		t.Fatal("persisted channel event decision ID is nil")
	}
	if !sha256Hex.MatchString(first.InputDigest) {
		t.Fatalf("InputDigest = %q, want lowercase SHA-256", first.InputDigest)
	}
	if first.Decision.State != AssertionStateKnown ||
		first.Decision.Kind != ChannelEventOfficialOnline ||
		!first.Decision.EventAt.Equal(onlineAt) {
		t.Fatalf(
			"Decision = %#v, want exact known official_online event",
			first.Decision,
		)
	}
	if first.Decision.EventAt.Location() != time.UTC ||
		first.Decision.DecidedAt.Location() != time.UTC ||
		!first.Decision.DecidedAt.Equal(asOf) {
		t.Fatalf(
			"Decision timestamps = event %s decided %s, want UTC exact instants",
			first.Decision.EventAt,
			first.Decision.DecidedAt,
		)
	}
	wantAssertionIDs := slices.Clone(assertionIDs)
	slices.SortFunc(wantAssertionIDs, func(left, right uuid.UUID) int {
		return strings.Compare(left.String(), right.String())
	})
	gotAssertionIDs := make([]uuid.UUID, 0, len(first.Decision.Evidence))
	for _, evidence := range first.Decision.Evidence {
		gotAssertionIDs = append(gotAssertionIDs, evidence.AssertionID)
	}
	if !slices.Equal(gotAssertionIDs, wantAssertionIDs) {
		t.Fatalf(
			"evidence assertion order = %v, want canonical %v",
			gotAssertionIDs,
			wantAssertionIDs,
		)
	}

	replayed, err := ProjectCanonicalChannelEventInTx(
		ctx,
		tx,
		workID,
		scope.ContentChannelJournalPublished,
		asOf.UTC(),
		policy,
	)
	if err != nil {
		t.Fatalf("replayed ProjectCanonicalChannelEventInTx() error = %v", err)
	}
	if replayed.ID != first.ID ||
		replayed.InputDigest != first.InputDigest ||
		!replayed.Decision.EventAt.Equal(first.Decision.EventAt) {
		t.Fatalf("replayed decision = %#v, want strict reuse of %#v", replayed, first)
	}

	var rows int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM work_channel_event_decisions
		WHERE work_id = $1
		  AND channel = $2
		  AND policy_version = $3
	`,
		workID,
		scope.ContentChannelJournalPublished,
		policy.Version,
	).Scan(&rows); err != nil {
		t.Fatalf("count projected channel event decisions: %v", err)
	}
	if rows != 1 {
		t.Fatalf("channel event decision count = %d, want 1 after replay", rows)
	}

	var rawEvidence []byte
	if err := tx.QueryRow(ctx, `
		SELECT evidence
		FROM work_channel_event_decisions
		WHERE id = $1
	`, first.ID).Scan(&rawEvidence); err != nil {
		t.Fatalf("load persisted channel event evidence: %v", err)
	}
	var persisted struct {
		Assertions []struct {
			AssertionID uuid.UUID `json:"assertion_id"`
			EventAt     time.Time `json:"event_at"`
		} `json:"assertions"`
	}
	if err := json.Unmarshal(rawEvidence, &persisted); err != nil {
		t.Fatalf("decode persisted channel event evidence: %v", err)
	}
	if len(persisted.Assertions) != 2 ||
		persisted.Assertions[0].AssertionID != wantAssertionIDs[0] ||
		persisted.Assertions[1].AssertionID != wantAssertionIDs[1] {
		t.Fatalf("persisted evidence = %s, want canonical assertion order", rawEvidence)
	}
	for _, assertion := range persisted.Assertions {
		if assertion.EventAt.Location() != time.UTC ||
			!assertion.EventAt.Equal(onlineAt) {
			t.Fatalf(
				"persisted evidence event_at = %s, want exact UTC %s",
				assertion.EventAt,
				onlineAt.UTC(),
			)
		}
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback caller-owned channel event transaction: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM work_channel_event_decisions
		WHERE id = $1
	`, first.ID).Scan(&rows); err != nil {
		t.Fatalf("count rolled back channel event decisions: %v", err)
	}
	if rows != 0 {
		t.Fatalf(
			"rolled back channel event decision count = %d, want caller transaction ownership",
			rows,
		)
	}
}

func TestProjectCanonicalChannelEventInTxPersistsMissingAndConflictWithoutDates(
	t *testing.T,
) {
	pool := openMigratedContentTruthTestPool(t)
	ctx := contentTruthTestContext(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin missing/conflict channel event transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	asOf := time.Date(2026, time.July, 19, 4, 0, 0, 0, time.UTC)
	policy, err := NewChannelEventPolicy(
		"canonical-channel-event/postgres-v1",
		map[scope.ContentChannel][]ChannelEventKind{
			scope.ContentChannelPreprint: {ChannelEventPreprintPosted},
		},
	)
	if err != nil {
		t.Fatalf("NewChannelEventPolicy() error = %v", err)
	}

	missingWorkID := insertContentTruthWork(
		t,
		ctx,
		tx,
		asOf.Add(-24*time.Hour),
	)
	missing, err := ProjectCanonicalChannelEventInTx(
		ctx,
		tx,
		missingWorkID,
		scope.ContentChannelPreprint,
		asOf,
		policy,
	)
	if err != nil {
		t.Fatalf("project missing channel event: %v", err)
	}
	if missing.Decision.State != AssertionStateMissing ||
		!missing.Decision.EventAt.IsZero() {
		t.Fatalf("missing Decision = %#v, want missing without guessed date", missing.Decision)
	}

	conflictWorkID := insertContentTruthWork(
		t,
		ctx,
		tx,
		asOf.Add(-24*time.Hour),
	)
	firstRevision := insertContentTruthRevision(
		t,
		ctx,
		tx,
		conflictWorkID,
		asOf.Add(-3*time.Hour),
	)
	secondRevision := insertContentTruthRevision(
		t,
		ctx,
		tx,
		conflictWorkID,
		asOf.Add(-2*time.Hour),
	)
	insertChannelEventAssertion(
		t,
		ctx,
		tx,
		uuid.New(),
		firstRevision,
		conflictWorkID,
		scope.ContentChannelPreprint,
		ChannelEventPreprintPosted,
		asOf.Add(-48*time.Hour),
		"$.version-one.posted",
		asOf.Add(-time.Hour),
	)
	insertChannelEventAssertion(
		t,
		ctx,
		tx,
		uuid.New(),
		secondRevision,
		conflictWorkID,
		scope.ContentChannelPreprint,
		ChannelEventPreprintPosted,
		asOf.Add(-24*time.Hour),
		"$.version-two.posted",
		asOf.Add(-30*time.Minute),
	)
	conflict, err := ProjectCanonicalChannelEventInTx(
		ctx,
		tx,
		conflictWorkID,
		scope.ContentChannelPreprint,
		asOf,
		policy,
	)
	if err != nil {
		t.Fatalf("project conflicting channel event: %v", err)
	}
	if conflict.Decision.State != AssertionStateConflict ||
		!conflict.Decision.EventAt.IsZero() {
		t.Fatalf(
			"conflict Decision = %#v, want conflict without guessed date",
			conflict.Decision,
		)
	}

	for name, result := range map[string]PostgresChannelEventDecision{
		"missing":  missing,
		"conflict": conflict,
	} {
		t.Run(name, func(t *testing.T) {
			var eventKind pgtype.Text
			var eventAt pgtype.Timestamptz
			if err := tx.QueryRow(ctx, `
				SELECT event_kind, event_at
				FROM work_channel_event_decisions
				WHERE id = $1
			`, result.ID).Scan(&eventKind, &eventAt); err != nil {
				t.Fatalf("load %s persisted nullable event: %v", name, err)
			}
			if eventKind.Valid || eventAt.Valid {
				t.Fatalf(
					"%s persisted event = kind %#v at %#v, want SQL NULL/NULL",
					name,
					eventKind,
					eventAt,
				)
			}
		})
	}
}

func TestProjectCanonicalChannelEventInTxRejectsConflictingOutputForSameDigest(
	t *testing.T,
) {
	pool := openMigratedContentTruthTestPool(t)
	ctx := contentTruthTestContext(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin channel digest conflict transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	asOf := time.Date(2026, time.July, 19, 4, 0, 0, 0, time.UTC)
	workID := insertContentTruthWork(t, ctx, tx, asOf.Add(-24*time.Hour))
	revision := insertContentTruthRevision(
		t,
		ctx,
		tx,
		workID,
		asOf.Add(-2*time.Hour),
	)
	insertChannelEventAssertion(
		t,
		ctx,
		tx,
		uuid.New(),
		revision,
		workID,
		scope.ContentChannelPreprint,
		ChannelEventPreprintPosted,
		asOf.Add(-24*time.Hour),
		"$.posted",
		asOf.Add(-time.Hour),
	)
	policy, err := NewChannelEventPolicy(
		"canonical-channel-event/postgres-v1",
		map[scope.ContentChannel][]ChannelEventKind{
			scope.ContentChannelPreprint: {ChannelEventPreprintPosted},
		},
	)
	if err != nil {
		t.Fatalf("NewChannelEventPolicy() error = %v", err)
	}

	first, err := ProjectCanonicalChannelEventInTx(
		ctx,
		tx,
		workID,
		scope.ContentChannelPreprint,
		asOf,
		policy,
	)
	if err != nil {
		t.Fatalf("initial ProjectCanonicalChannelEventInTx() error = %v", err)
	}
	if _, err := tx.Exec(ctx, `
		ALTER TABLE work_channel_event_decisions
		DISABLE TRIGGER work_channel_event_decisions_immutable
	`); err != nil {
		t.Fatalf("disable channel event immutable trigger: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_channel_event_decisions
		SET decided_at = decided_at + interval '1 second'
		WHERE id = $1
	`, first.ID); err != nil {
		t.Fatalf("corrupt persisted channel event output: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		ALTER TABLE work_channel_event_decisions
		ENABLE TRIGGER work_channel_event_decisions_immutable
	`); err != nil {
		t.Fatalf("re-enable channel event immutable trigger: %v", err)
	}

	_, err = ProjectCanonicalChannelEventInTx(
		ctx,
		tx,
		workID,
		scope.ContentChannelPreprint,
		asOf,
		policy,
	)
	if !errors.Is(err, ErrPostgresProjectionConflict) {
		t.Fatalf(
			"replayed ProjectCanonicalChannelEventInTx() error = %v, want ErrPostgresProjectionConflict",
			err,
		)
	}
}

func insertChannelEventAssertion(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	assertionID uuid.UUID,
	revision contentTruthRevisionFixture,
	workID uuid.UUID,
	channel scope.ContentChannel,
	kind ChannelEventKind,
	eventAt time.Time,
	sourcePath string,
	assertedAt time.Time,
) {
	t.Helper()

	if _, err := tx.Exec(ctx, `
		INSERT INTO work_channel_event_assertions (
			id,
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			work_id,
			channel,
			event_kind,
			event_at,
			source_path,
			asserted_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		)
	`,
		assertionID,
		revision.ProjectionAssertionID,
		revision.NormalizedAssertionID,
		revision.SourceRecordID,
		workID,
		channel,
		kind,
		eventAt,
		sourcePath,
		assertedAt,
	); err != nil {
		t.Fatalf("insert channel event assertion: %v", err)
	}
}
