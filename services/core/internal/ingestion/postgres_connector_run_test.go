package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPostgresConnectorRunAdvancesWatermarkOnlyAfterSucceededBatch(t *testing.T) {
	pool := openIngestionTestPool(t)
	store, err := NewPostgresConnectorRunStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresConnectorRunStore() error = %v", err)
	}
	ctx := context.Background()
	from := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
	until := from.Add(24 * time.Hour)

	first := mustStartConnectorRun(t, store, "created", from, until, "failure")
	if err := store.RecordPage(ctx, first, mustConnectorPage(t, first.ID, 1)); err != nil {
		t.Fatalf("RecordPage() error = %v", err)
	}
	failed, err := first.Fail("parse", "invalid_crossref_page")
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if err := store.Fail(ctx, failed); err != nil {
		t.Fatalf("store.Fail() error = %v", err)
	}
	watermark, version, err := store.Watermark(ctx, "crossref", "created")
	if err != nil {
		t.Fatalf("Watermark() error = %v", err)
	}
	if !watermark.Equal(from) || version != 0 {
		t.Fatalf("failed run advanced watermark to %s/v%d", watermark, version)
	}

	replay := mustStartConnectorRun(t, store, "created", from, until, "success")
	if err := store.RecordPage(ctx, replay, mustConnectorPage(t, replay.ID, 1)); err != nil {
		t.Fatalf("RecordPage(replay) error = %v", err)
	}
	succeeded, err := replay.Succeed()
	if err != nil {
		t.Fatalf("Succeed() error = %v", err)
	}
	if err := store.Succeed(ctx, succeeded); err != nil {
		t.Fatalf("store.Succeed() error = %v", err)
	}
	watermark, version, err = store.Watermark(ctx, "crossref", "created")
	if err != nil {
		t.Fatalf("Watermark(after success) error = %v", err)
	}
	if !watermark.Equal(until) || version != 1 {
		t.Fatalf("watermark = %s/v%d, want %s/v1", watermark, version, until)
	}
}

func TestPostgresConnectorRunUsesCompareAndSwapForWatermark(t *testing.T) {
	pool := openIngestionTestPool(t)
	store, err := NewPostgresConnectorRunStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresConnectorRunStore() error = %v", err)
	}
	ctx := context.Background()
	from := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
	until := from.Add(24 * time.Hour)
	run := mustStartConnectorRun(t, store, "updated", from, until, "cas")
	if err := store.RecordPage(ctx, run, mustConnectorPage(t, run.ID, 1)); err != nil {
		t.Fatalf("RecordPage() error = %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE connector_watermarks
		SET watermark_value = $3, version = version + 1
		WHERE source = $1 AND stream = $2
	`, "crossref", "updated", until.Add(time.Hour)); err != nil {
		t.Fatalf("mutate watermark fixture: %v", err)
	}
	succeeded, err := run.Succeed()
	if err != nil {
		t.Fatalf("Succeed() error = %v", err)
	}
	err = store.Succeed(ctx, succeeded)
	if err == nil || !errors.Is(err, ErrWatermarkConflict) {
		t.Fatalf("store.Succeed() error = %v, want ErrWatermarkConflict", err)
	}
}

func TestPostgresConnectorRunPageReceiptsAreImmutableAndIdempotent(t *testing.T) {
	pool := openIngestionTestPool(t)
	store, err := NewPostgresConnectorRunStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresConnectorRunStore() error = %v", err)
	}
	ctx := context.Background()
	from := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
	run := mustStartConnectorRun(
		t,
		store,
		"created",
		from,
		from.Add(24*time.Hour),
		"page",
	)
	page := mustConnectorPage(t, run.ID, 1)
	if err := store.RecordPage(ctx, run, page); err != nil {
		t.Fatalf("RecordPage() error = %v", err)
	}
	if err := store.RecordPage(ctx, run, page); err != nil {
		t.Fatalf("RecordPage(idempotent) error = %v", err)
	}
	conflicting := page
	conflicting.ContentSHA256 = strings.Repeat("b", 64)
	if err := store.RecordPage(ctx, run, conflicting); err == nil {
		t.Fatal("RecordPage() accepted conflicting immutable page receipt")
	}
}

func TestPostgresConnectorRunRequiresAContinuousTerminalPageChain(t *testing.T) {
	pool := openIngestionTestPool(t)
	store, err := NewPostgresConnectorRunStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresConnectorRunStore() error = %v", err)
	}
	ctx := context.Background()
	from := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
	run := mustStartConnectorRun(
		t,
		store,
		"created",
		from,
		from.Add(24*time.Hour),
		"continuous-chain",
	)

	pageTwo := mustConnectorPageWithCursors(
		t,
		run.ID,
		2,
		"next opaque \t",
		"",
	)
	if err := store.RecordPage(ctx, run, pageTwo); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "ordinal") {
		t.Fatalf("RecordPage(page 2 first) error = %v", err)
	}
	pageOne := mustConnectorPageWithCursors(
		t,
		run.ID,
		1,
		"*",
		"next opaque \t",
	)
	if err := store.RecordPage(ctx, run, pageOne); err != nil {
		t.Fatalf("RecordPage(page 1) error = %v", err)
	}
	succeeded, err := run.Succeed()
	if err != nil {
		t.Fatalf("Succeed() error = %v", err)
	}
	if err := store.Succeed(ctx, succeeded); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "terminal") {
		t.Fatalf("store.Succeed(nonterminal) error = %v", err)
	}
	gap := mustConnectorPageWithCursors(
		t,
		run.ID,
		2,
		"different cursor",
		"",
	)
	if err := store.RecordPage(ctx, run, gap); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "cursor") {
		t.Fatalf("RecordPage(cursor gap) error = %v", err)
	}
	if err := store.RecordPage(ctx, run, pageTwo); err != nil {
		t.Fatalf("RecordPage(page 2) error = %v", err)
	}
	pageThree := mustConnectorPageWithCursors(
		t,
		run.ID,
		3,
		"after-terminal",
		"",
	)
	if err := store.RecordPage(ctx, run, pageThree); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "terminal") {
		t.Fatalf("RecordPage(after terminal) error = %v", err)
	}
	if err := store.Succeed(ctx, succeeded); err != nil {
		t.Fatalf("store.Succeed(terminal) error = %v", err)
	}
}

func TestPostgresConnectorRunRejectsForgedClaimAndConcurrentSameStream(t *testing.T) {
	pool := openIngestionTestPool(t)
	store, err := NewPostgresConnectorRunStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresConnectorRunStore() error = %v", err)
	}
	ctx := context.Background()
	from := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
	run := mustStartConnectorRun(
		t,
		store,
		"updated",
		from,
		from.Add(24*time.Hour),
		"active-one",
	)
	claim, err := NewConnectorClaim(
		"crossref",
		"updated",
		WatermarkTimestamp,
		from,
		from.Add(24*time.Hour),
		"crossref-updated:active-two",
	)
	if err != nil {
		t.Fatalf("NewConnectorClaim() error = %v", err)
	}
	if _, err := store.Start(ctx, claim); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "active") {
		t.Fatalf("Start(concurrent same stream) error = %v", err)
	}

	forged := run
	forged.Claim.Until = forged.Claim.Until.Add(24 * time.Hour)
	page := mustConnectorPage(t, forged.ID, 1)
	if err := store.RecordPage(ctx, forged, page); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "persisted") {
		t.Fatalf("RecordPage(forged claim) error = %v", err)
	}
}

func mustStartConnectorRun(
	t *testing.T,
	store *PostgresConnectorRunStore,
	stream string,
	from time.Time,
	until time.Time,
	suffix string,
) ConnectorRun {
	t.Helper()

	claim, err := NewConnectorClaim(
		"crossref",
		stream,
		WatermarkTimestamp,
		from,
		until,
		"crossref-"+stream+":"+suffix,
	)
	if err != nil {
		t.Fatalf("NewConnectorClaim() error = %v", err)
	}
	run, err := store.Start(context.Background(), claim)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return run
}

func mustConnectorPage(
	t *testing.T,
	runID string,
	ordinal int,
) ConnectorPageReceipt {
	t.Helper()

	return mustConnectorPageWithCursors(t, runID, ordinal, "*", "")
}

func mustConnectorPageWithCursors(
	t *testing.T,
	runID string,
	ordinal int,
	cursorIn string,
	cursorOut string,
) ConnectorPageReceipt {
	t.Helper()

	page, err := NewConnectorPageReceipt(
		runID,
		ordinal,
		cursorIn,
		cursorOut,
		strings.Repeat("a", 64),
		10,
	)
	if err != nil {
		t.Fatalf("NewConnectorPageReceipt() error = %v", err)
	}
	return page
}
