package workfamily

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
)

type postgresExplainPlanEnvelope struct {
	Plan postgresExplainPlanNode `json:"Plan"`
}

type postgresExplainPlanNode struct {
	NodeType                  string                    `json:"Node Type"`
	RelationName              string                    `json:"Relation Name"`
	ActualRows                float64                   `json:"Actual Rows"`
	ActualLoops               float64                   `json:"Actual Loops"`
	RowsRemovedByFilter       float64                   `json:"Rows Removed by Filter"`
	RowsRemovedByIndexRecheck float64                   `json:"Rows Removed by Index Recheck"`
	Plans                     []postgresExplainPlanNode `json:"Plans"`
}

func postgresExplainRelationRows(
	node postgresExplainPlanNode,
	relationNames map[string]struct{},
) float64 {
	var rows float64
	if _, included := relationNames[node.RelationName]; included {
		rows += (node.ActualRows +
			node.RowsRemovedByFilter +
			node.RowsRemovedByIndexRecheck) *
			node.ActualLoops
	}
	for _, child := range node.Plans {
		rows += postgresExplainRelationRows(child, relationNames)
	}
	return rows
}

func postgresExplainRelationSeqScans(
	node postgresExplainPlanNode,
	relationNames map[string]struct{},
) []string {
	var relationSeqScans []string
	if _, included := relationNames[node.RelationName]; included &&
		node.NodeType == "Seq Scan" &&
		node.ActualLoops > 0 {
		relationSeqScans = append(relationSeqScans, node.RelationName)
	}
	for _, child := range node.Plans {
		relationSeqScans = append(
			relationSeqScans,
			postgresExplainRelationSeqScans(child, relationNames)...,
		)
	}
	return relationSeqScans
}

func TestPostgresExplainRelationRowsCountsRowsDiscardedByFullScan(
	t *testing.T,
) {
	t.Parallel()

	const loops = 2
	node := postgresExplainPlanNode{
		NodeType:                  "Seq Scan",
		RelationName:              "work_relation_assertions",
		ActualRows:                0,
		ActualLoops:               loops,
		RowsRemovedByFilter:       50_000,
		RowsRemovedByIndexRecheck: 7,
	}
	got := postgresExplainRelationRows(
		node,
		map[string]struct{}{
			"work_relation_assertions": {},
			"work_relation_decisions":  {},
		},
	)
	const want = (50_000 + 7) * loops
	if got != want {
		t.Fatalf(
			"full-scan relation rows = %.0f, want %.0f",
			got,
			float64(want),
		)
	}
}

func TestPostgresExplainRelationSeqScansFindTargetTables(t *testing.T) {
	t.Parallel()

	node := postgresExplainPlanNode{
		NodeType:     "Nested Loop",
		RelationName: "",
		Plans: []postgresExplainPlanNode{
			{
				NodeType:     "Seq Scan",
				RelationName: "work_relation_decisions",
				ActualLoops:  1,
			},
			{
				NodeType:     "Seq Scan",
				RelationName: "works",
				ActualLoops:  1,
			},
		},
	}
	got := postgresExplainRelationSeqScans(
		node,
		map[string]struct{}{
			"work_relation_assertions": {},
			"work_relation_decisions":  {},
		},
	)
	if len(got) != 1 || got[0] != "work_relation_decisions" {
		t.Fatalf("target relation Seq Scans = %v, want [work_relation_decisions]", got)
	}
}

func TestPostgresExplainRelationSeqScansIgnoreUnexecutedPlanNodes(
	t *testing.T,
) {
	t.Parallel()

	node := postgresExplainPlanNode{
		NodeType:     "Seq Scan",
		RelationName: "work_relation_decisions",
		ActualLoops:  0,
	}
	got := postgresExplainRelationSeqScans(
		node,
		map[string]struct{}{
			"work_relation_decisions": {},
		},
	)
	if len(got) != 0 {
		t.Fatalf("unexecuted target relation Seq Scans = %v, want none", got)
	}
}

func TestPostgresTriggerCreatesExactlyOneActiveSingletonMembership(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	workID := fixture.insertWork(
		t,
		"openalex:W41001",
		"A singleton Work",
		time.Now().UTC().Add(-time.Hour),
	)

	first, found, err := store.ActiveMembership(t.Context(), workID)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership(first) = (%#v, %t, %v)",
			first,
			found,
			err,
		)
	}
	second, found, err := store.ActiveMembership(t.Context(), workID)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership(replay) = (%#v, %t, %v)",
			second,
			found,
			err,
		)
	}
	if first.ID == uuid.Nil ||
		first != second ||
		first.WorkID != workID ||
		first.FamilyID == uuid.Nil ||
		first.Origin != MembershipOriginSingleton ||
		first.EndedAt != nil {
		t.Fatalf("singleton membership = %#v, replay = %#v", first, second)
	}

	active, found, err := store.ActiveMembership(t.Context(), workID)
	if err != nil {
		t.Fatalf("ActiveMembership() error = %v", err)
	}
	if !found || active != first {
		t.Fatalf("ActiveMembership() = (%#v, %t), want %#v", active, found, first)
	}

	var activeCount int
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM work_family_memberships
		WHERE work_id = $1
		  AND ended_at IS NULL
	`, workID).Scan(&activeCount); err != nil {
		t.Fatalf("count active memberships: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active membership count = %d, want 1", activeCount)
	}

	var competingFamilyID uuid.UUID
	if err := fixture.Pool.QueryRow(t.Context(), `
		INSERT INTO work_families DEFAULT VALUES
		RETURNING id
	`).Scan(&competingFamilyID); err != nil {
		t.Fatalf("insert competing family: %v", err)
	}
	_, err = fixture.Pool.Exec(t.Context(), `
		INSERT INTO work_family_memberships (
			work_family_id,
			work_id,
			origin,
			started_at
		) VALUES ($1, $2, 'singleton', now())
	`, competingFamilyID, workID)
	assertWorkFamilyPostgresCode(t, err, "23505")
}

func TestWorkFamilyMigrationBackfillsExistingWorksWithSingletons(
	t *testing.T,
) {
	databaseURL := newWorkFamilyTestDatabase(t)
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open Work Family migration test pool: %v", err)
	}
	t.Cleanup(pool.Close)

	embedded, err := database.EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	var (
		beforeWorkFamilies []database.Migration
		workFamilies       database.Migration
	)
	for _, migration := range embedded {
		switch {
		case migration.Version < 24:
			beforeWorkFamilies = append(beforeWorkFamilies, migration)
		case migration.Version == 24:
			workFamilies = migration
		}
	}
	if workFamilies.Version != 24 {
		t.Fatal("000024_work_families migration is missing")
	}
	if err := database.UpMigrations(
		t.Context(),
		pool,
		beforeWorkFamilies,
	); err != nil {
		t.Fatalf("apply migrations through 000023: %v", err)
	}

	workIDs := make([]uuid.UUID, 0, 2)
	for index, canonicalKey := range []string{
		"openalex:W41901",
		"openalex:W41902",
	} {
		var workID uuid.UUID
		if err := pool.QueryRow(t.Context(), `
			INSERT INTO works (canonical_key, status, title)
			VALUES ($1, 'active', $2)
			RETURNING id
		`, canonicalKey, "Pre-migration Work "+canonicalKey).Scan(
			&workID,
		); err != nil {
			t.Fatalf("insert pre-migration Work %d: %v", index, err)
		}
		workIDs = append(workIDs, workID)
	}

	if err := database.UpMigrations(
		t.Context(),
		pool,
		[]database.Migration{workFamilies},
	); err != nil {
		t.Fatalf("apply 000024_work_families: %v", err)
	}

	for _, workID := range workIDs {
		var (
			activeCount     int
			canonicalWorkID uuid.UUID
		)
		if err := pool.QueryRow(t.Context(), `
			SELECT count(*)
			FROM work_family_memberships
			WHERE work_id = $1
			  AND ended_at IS NULL
		`, workID).Scan(&activeCount); err != nil {
			t.Fatalf("count backfilled membership for %s: %v", workID, err)
		}
		if activeCount != 1 {
			t.Fatalf(
				"backfilled active membership count for %s = %d, want 1",
				workID,
				activeCount,
			)
		}
		if err := pool.QueryRow(t.Context(), `
			SELECT canonical_state.canonical_work_id
			FROM work_family_memberships AS membership
			JOIN work_family_canonical_states AS canonical_state
			  ON canonical_state.work_family_id = membership.work_family_id
			WHERE membership.work_id = $1
			  AND membership.ended_at IS NULL
		`, workID).Scan(&canonicalWorkID); err != nil {
			t.Fatalf("query backfilled canonical Work for %s: %v", workID, err)
		}
		if canonicalWorkID != workID {
			t.Fatalf(
				"backfilled canonical Work for %s = %s",
				workID,
				canonicalWorkID,
			)
		}
	}
}

func TestPostgresStoreAcceptedExplicitRelationMergesFamiliesAndBuildsTimeline(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}

	preprintPublishedAt := time.Now().UTC().Add(-48 * time.Hour)
	recordPublishedAt := preprintPublishedAt.Add(24 * time.Hour)
	preprintWorkID := fixture.insertWork(
		t,
		"arxiv:2607.04101",
		"Exact title shared by versions",
		preprintPublishedAt,
	)
	recordWorkID := fixture.insertWork(
		t,
		"doi:10.1000/work-family-41001",
		"Exact title shared by versions",
		recordPublishedAt,
	)
	preprintSourceRecordID := fixture.insertSourceRecord(
		t,
		preprintWorkID,
		"crossref",
		"preprint-41001",
	)

	preprintSingleton, _, err := store.ActiveMembership(
		t.Context(),
		preprintWorkID,
	)
	if err != nil {
		t.Fatalf("load preprint singleton: %v", err)
	}
	recordSingleton, _, err := store.ActiveMembership(
		t.Context(),
		recordWorkID,
	)
	if err != nil {
		t.Fatalf("load record singleton: %v", err)
	}
	if preprintSingleton.FamilyID == recordSingleton.FamilyID {
		t.Fatal("identical titles automatically merged before proven relation")
	}

	assertion, err := store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         preprintWorkID,
			ObjectWorkID:          recordWorkID,
			RelationKind:          RelationIsPreprintOf,
			EvidenceKind:          EvidenceExplicitSourceRelation,
			SubjectSourceRecordID: preprintSourceRecordID,
			SubjectSourcePath:     "message.relation.is-preprint-of",
			AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationAssertion() error = %v", err)
	}
	decision, err := store.RecordRelationDecision(
		t.Context(),
		RelationDecision{
			AssertionID:   assertion.ID,
			Outcome:       DecisionAccepted,
			Reason:        "explicit source relation",
			PolicyVersion: "work-family/v1",
			DecidedAt:     time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationDecision() error = %v", err)
	}
	if assertion.ID == uuid.Nil || decision.ID == uuid.Nil {
		t.Fatalf("persisted assertion/decision = %#v / %#v", assertion, decision)
	}

	preprintMembership, found, err := store.ActiveMembership(
		t.Context(),
		preprintWorkID,
	)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership(preprint) = (%#v, %t, %v)",
			preprintMembership,
			found,
			err,
		)
	}
	recordMembership, found, err := store.ActiveMembership(
		t.Context(),
		recordWorkID,
	)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership(record) = (%#v, %t, %v)",
			recordMembership,
			found,
			err,
		)
	}
	if preprintMembership.FamilyID != recordMembership.FamilyID {
		t.Fatalf(
			"active families = preprint %s record %s",
			preprintMembership.FamilyID,
			recordMembership.FamilyID,
		)
	}

	timeline, found, err := store.VersionTimeline(t.Context(), recordWorkID)
	if err != nil {
		t.Fatalf("VersionTimeline() error = %v", err)
	}
	if !found {
		t.Fatal("VersionTimeline() found = false")
	}
	if timeline.FamilyID != recordMembership.FamilyID ||
		timeline.CanonicalWorkID != recordWorkID ||
		len(timeline.Versions) != 2 ||
		timeline.Versions[0].WorkID != preprintWorkID ||
		timeline.Versions[1].WorkID != recordWorkID ||
		len(timeline.Relations) != 1 ||
		timeline.Relations[0].Assertion.ID != assertion.ID ||
		timeline.Relations[0].Decision.ID != decision.ID {
		t.Fatalf("timeline = %#v", timeline)
	}

	var (
		persistedCanonicalWorkID uuid.UUID
		canonicalDecisionID      uuid.UUID
	)
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT canonical_work_id, canonical_decision_id
		FROM work_family_canonical_states
		WHERE work_family_id = $1
	`, timeline.FamilyID).Scan(
		&persistedCanonicalWorkID,
		&canonicalDecisionID,
	); err != nil {
		t.Fatalf("query current canonical Work: %v", err)
	}
	if persistedCanonicalWorkID != recordWorkID ||
		canonicalDecisionID == uuid.Nil {
		t.Fatalf(
			"persisted canonical = Work %s decision %s",
			persistedCanonicalWorkID,
			canonicalDecisionID,
		)
	}

	var canonicalHistoryCount int
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM work_family_canonical_decisions
		WHERE work_family_id = $1
		  AND canonical_work_id = $2
		  AND relation_decision_id = $3
	`, timeline.FamilyID, recordWorkID, decision.ID).Scan(
		&canonicalHistoryCount,
	); err != nil {
		t.Fatalf("query canonical decision history: %v", err)
	}
	if canonicalHistoryCount != 1 {
		t.Fatalf(
			"canonical relation decision history count = %d, want 1",
			canonicalHistoryCount,
		)
	}
}

func TestPostgresStoreHasPreprintMakesVersionOfRecordCanonical(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}

	recordWorkID := fixture.insertWork(
		t,
		"doi:10.1000/work-family-41010",
		"Version of Record",
		time.Now().UTC().Add(-time.Hour),
	)
	preprintWorkID := fixture.insertWork(
		t,
		"arxiv:2607.04110",
		"Preprint version",
		time.Now().UTC().Add(-2*time.Hour),
	)
	recordSourceRecordID := fixture.insertSourceRecord(
		t,
		recordWorkID,
		"crossref",
		"record-has-preprint-41010",
	)

	assertion, err := store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         recordWorkID,
			ObjectWorkID:          preprintWorkID,
			RelationKind:          RelationHasPreprint,
			EvidenceKind:          EvidenceExplicitSourceRelation,
			SubjectSourceRecordID: recordSourceRecordID,
			SubjectSourcePath:     "message.relation.has-preprint",
			AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationAssertion(has_preprint) error = %v", err)
	}
	if _, err := store.RecordRelationDecision(
		t.Context(),
		RelationDecision{
			AssertionID:   assertion.ID,
			Outcome:       DecisionAccepted,
			Reason:        "explicit Version of Record relation",
			PolicyVersion: "work-family/v1",
			DecidedAt:     time.Now().UTC().Truncate(time.Microsecond),
		},
	); err != nil {
		t.Fatalf("RecordRelationDecision(has_preprint) error = %v", err)
	}

	timeline, found, err := store.VersionTimeline(t.Context(), preprintWorkID)
	if err != nil || !found {
		t.Fatalf("VersionTimeline() = (%#v, %t, %v)", timeline, found, err)
	}
	if timeline.CanonicalWorkID != recordWorkID {
		t.Fatalf(
			"canonical Work = %s, want Version of Record %s",
			timeline.CanonicalWorkID,
			recordWorkID,
		)
	}
	if len(timeline.Versions) != 2 {
		t.Fatalf(
			"timeline version count = %d, want preprint and Version of Record",
			len(timeline.Versions),
		)
	}
}

func TestCanonicalPrecedencePreservesVersionOfRecordAcrossLaterGenericMerge(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	workA := fixture.insertWork(
		t,
		"openalex:W41101",
		"Generic earlier version A",
		time.Now().UTC().Add(-72*time.Hour),
	)
	workB := fixture.insertWork(
		t,
		"arxiv:2607.04111",
		"Preprint B",
		time.Now().UTC().Add(-48*time.Hour),
	)
	workC := fixture.insertWork(
		t,
		"doi:10.1000/work-family-41101",
		"Version of Record C",
		time.Now().UTC().Add(-24*time.Hour),
	)
	baseTime := time.Now().UTC().Add(-12 * time.Hour).Truncate(time.Microsecond)

	vorDecision := recordAcceptedRelation(
		t,
		fixture,
		store,
		workB,
		workC,
		RelationIsPreprintOf,
		baseTime,
		"precedence-vor-41101",
	)
	genericDecision := recordAcceptedRelation(
		t,
		fixture,
		store,
		workA,
		workB,
		RelationIsVersionOf,
		baseTime.Add(time.Hour),
		"precedence-generic-41101",
	)

	timeline, found, err := store.VersionTimeline(t.Context(), workA)
	if err != nil || !found {
		t.Fatalf("VersionTimeline() = (%#v, %t, %v)", timeline, found, err)
	}
	if timeline.CanonicalWorkID != workC {
		t.Fatalf(
			"canonical Work after generic merge = %s, want VOR %s",
			timeline.CanonicalWorkID,
			workC,
		)
	}
	if len(timeline.Versions) != 3 {
		t.Fatalf("timeline versions = %d, want 3", len(timeline.Versions))
	}

	var (
		vorBasis          string
		vorPrecedence     int
		genericBasis      string
		genericPrecedence int
	)
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT basis, precedence
		FROM work_family_canonical_decisions
		WHERE relation_decision_id = $1
	`, vorDecision.ID).Scan(&vorBasis, &vorPrecedence); err != nil {
		t.Fatalf("query VOR canonical precedence: %v", err)
	}
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT basis, precedence
		FROM work_family_canonical_decisions
		WHERE relation_decision_id = $1
	`, genericDecision.ID).Scan(
		&genericBasis,
		&genericPrecedence,
	); err != nil {
		t.Fatalf("query generic canonical precedence: %v", err)
	}
	if vorBasis != string(RelationIsPreprintOf) ||
		genericBasis != string(RelationIsVersionOf) ||
		vorPrecedence <= genericPrecedence {
		t.Fatalf(
			"canonical precedence = VOR %q/%d generic %q/%d",
			vorBasis,
			vorPrecedence,
			genericBasis,
			genericPrecedence,
		)
	}
}

func TestCanonicalSamePrecedenceRejectsDelayedOlderDecision(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	workA := fixture.insertWork(
		t,
		"openalex:W41102",
		"Delayed generic A",
		time.Now().UTC().Add(-72*time.Hour),
	)
	workB := fixture.insertWork(
		t,
		"openalex:W41103",
		"Generic B",
		time.Now().UTC().Add(-48*time.Hour),
	)
	workC := fixture.insertWork(
		t,
		"openalex:W41104",
		"Newer generic canonical C",
		time.Now().UTC().Add(-24*time.Hour),
	)
	newerDecisionAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(
		time.Microsecond,
	)

	recordAcceptedRelation(
		t,
		fixture,
		store,
		workB,
		workC,
		RelationIsVersionOf,
		newerDecisionAt,
		"precedence-newer-41102",
	)
	recordAcceptedRelation(
		t,
		fixture,
		store,
		workA,
		workB,
		RelationHasVersion,
		newerDecisionAt.Add(-time.Hour),
		"precedence-delayed-old-41102",
	)

	timeline, found, err := store.VersionTimeline(t.Context(), workA)
	if err != nil || !found {
		t.Fatalf("VersionTimeline() = (%#v, %t, %v)", timeline, found, err)
	}
	if timeline.CanonicalWorkID != workC {
		t.Fatalf(
			"delayed old decision canonical = %s, want newer %s",
			timeline.CanonicalWorkID,
			workC,
		)
	}
}

func TestCanonicalSelectionTieBreakIsOrderIndependent(t *testing.T) {
	decidedAt := time.Date(
		2026,
		time.July,
		19,
		12,
		0,
		0,
		0,
		time.UTC,
	)
	lowerKey := canonicalSelection{
		DecisionID: uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		WorkID:     uuid.MustParse("00000000-0000-0000-0000-000000000010"),
		Precedence: canonicalPrecedenceGenericVersion,
		DecidedAt:  decidedAt,
	}
	higherKey := canonicalSelection{
		DecisionID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkID:     uuid.MustParse("00000000-0000-0000-0000-000000000020"),
		Precedence: canonicalPrecedenceGenericVersion,
		DecidedAt:  decidedAt,
	}

	selectCanonical := func(candidates []canonicalSelection) canonicalSelection {
		selected := candidates[0]
		for _, candidate := range candidates[1:] {
			if canonicalSelectionOutranks(candidate, selected) {
				selected = candidate
			}
		}
		return selected
	}

	for _, candidates := range [][]canonicalSelection{
		{lowerKey, higherKey},
		{higherKey, lowerKey},
	} {
		selected := selectCanonical(candidates)
		if selected.WorkID != lowerKey.WorkID {
			t.Fatalf(
				"selected canonical Work = %s, want stable lower UUID %s",
				selected.WorkID,
				lowerKey.WorkID,
			)
		}
	}
}

func TestCanonicalTieBreakIsIndependentOfRelationArrivalOrder(
	t *testing.T,
) {
	workA := uuid.MustParse("00000000-0000-0000-0000-000000004201")
	workB := uuid.MustParse("00000000-0000-0000-0000-000000004202")
	workC := uuid.MustParse("00000000-0000-0000-0000-000000004203")
	decidedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

	for _, test := range []struct {
		name  string
		order []string
	}{
		{name: "AB then BC", order: []string{"AB", "BC"}},
		{name: "BC then AB", order: []string{"BC", "AB"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := openWorkFamilyPostgresFixture(t)
			store, err := NewPostgresStore(fixture.Pool)
			if err != nil {
				t.Fatalf("NewPostgresStore() error = %v", err)
			}
			for _, work := range []struct {
				id           uuid.UUID
				canonicalKey string
			}{
				{id: workA, canonicalKey: "openalex:W4201"},
				{id: workB, canonicalKey: "doi:10.1000/tie-b"},
				{id: workC, canonicalKey: "doi:10.1000/tie-c"},
			} {
				if _, err := fixture.Pool.Exec(t.Context(), `
					INSERT INTO works (
						id,
						canonical_key,
						status,
						title,
						published_at
					) VALUES ($1, $2, 'active', $2, $3)
				`,
					work.id,
					work.canonicalKey,
					decidedAt.Add(-time.Hour),
				); err != nil {
					t.Fatalf("insert fixed Work %s: %v", work.id, err)
				}
			}

			for _, relation := range test.order {
				switch relation {
				case "AB":
					recordAcceptedRelation(
						t,
						fixture,
						store,
						workA,
						workB,
						RelationIsVersionOf,
						decidedAt,
						"tie-order-ab-"+test.name,
					)
				case "BC":
					recordAcceptedRelation(
						t,
						fixture,
						store,
						workB,
						workC,
						RelationIsVersionOf,
						decidedAt,
						"tie-order-bc-"+test.name,
					)
				default:
					t.Fatalf("unknown relation %q", relation)
				}
			}

			timeline, found, err := store.VersionTimeline(t.Context(), workA)
			if err != nil || !found {
				t.Fatalf(
					"VersionTimeline() = (%#v, %t, %v)",
					timeline,
					found,
					err,
				)
			}
			if timeline.CanonicalWorkID != workB {
				t.Fatalf(
					"canonical Work = %s, want stable lower UUID %s",
					timeline.CanonicalWorkID,
					workB,
				)
			}
		})
	}
}

func TestSupersedingDecisionsRebuildCurrentAcceptedRelationGraph(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	workA := fixture.insertWork(
		t,
		"openalex:W41201",
		"Graph Work A",
		time.Now().UTC().Add(-3*time.Hour),
	)
	workB := fixture.insertWork(
		t,
		"openalex:W41202",
		"Graph Work B",
		time.Now().UTC().Add(-2*time.Hour),
	)
	workC := fixture.insertWork(
		t,
		"openalex:W41203",
		"Graph Work C",
		time.Now().UTC().Add(-time.Hour),
	)
	baseDecisionAt := time.Now().UTC().Add(-30 * time.Minute).Truncate(
		time.Microsecond,
	)
	decisionAB := recordAcceptedRelation(
		t,
		fixture,
		store,
		workA,
		workB,
		RelationIsVersionOf,
		baseDecisionAt,
		"graph-ab-41201",
	)
	recordAcceptedRelation(
		t,
		fixture,
		store,
		workB,
		workC,
		RelationIsVersionOf,
		baseDecisionAt.Add(time.Second),
		"graph-bc-41201",
	)
	decisionAC := recordAcceptedRelation(
		t,
		fixture,
		store,
		workA,
		workC,
		RelationIsVersionOf,
		baseDecisionAt.Add(2*time.Second),
		"graph-ac-41201",
	)

	rejectedAB, err := store.RecordRelationDecision(
		t.Context(),
		RelationDecision{
			AssertionID:          decisionAB.AssertionID,
			SupersedesDecisionID: decisionAB.ID,
			Outcome:              DecisionRejected,
			Reason:               "AB relation revoked",
			PolicyVersion:        "work-family/v2",
			DecidedAt:            baseDecisionAt.Add(3 * time.Second),
		},
	)
	if err != nil {
		t.Fatalf("supersede AB accepted decision: %v", err)
	}
	if rejectedAB.ID == uuid.Nil ||
		rejectedAB.SupersedesDecisionID != decisionAB.ID {
		t.Fatalf("rejected AB decision = %#v", rejectedAB)
	}

	membershipA, _, err := store.ActiveMembership(t.Context(), workA)
	if err != nil {
		t.Fatalf("ActiveMembership(A) after first revoke: %v", err)
	}
	membershipB, _, err := store.ActiveMembership(t.Context(), workB)
	if err != nil {
		t.Fatalf("ActiveMembership(B) after first revoke: %v", err)
	}
	membershipC, _, err := store.ActiveMembership(t.Context(), workC)
	if err != nil {
		t.Fatalf("ActiveMembership(C) after first revoke: %v", err)
	}
	if membershipA.FamilyID != membershipB.FamilyID ||
		membershipB.FamilyID != membershipC.FamilyID {
		t.Fatalf(
			"alternate accepted path split family: A=%s B=%s C=%s",
			membershipA.FamilyID,
			membershipB.FamilyID,
			membershipC.FamilyID,
		)
	}

	rejectedAC, err := store.RecordRelationDecision(
		t.Context(),
		RelationDecision{
			AssertionID:          decisionAC.AssertionID,
			SupersedesDecisionID: decisionAC.ID,
			Outcome:              DecisionRejected,
			Reason:               "AC relation revoked",
			PolicyVersion:        "work-family/v2",
			DecidedAt:            baseDecisionAt.Add(4 * time.Second),
		},
	)
	if err != nil {
		t.Fatalf("supersede AC accepted decision: %v", err)
	}

	membershipA, _, err = store.ActiveMembership(t.Context(), workA)
	if err != nil {
		t.Fatalf("ActiveMembership(A) after split: %v", err)
	}
	membershipB, _, err = store.ActiveMembership(t.Context(), workB)
	if err != nil {
		t.Fatalf("ActiveMembership(B) after split: %v", err)
	}
	membershipC, _, err = store.ActiveMembership(t.Context(), workC)
	if err != nil {
		t.Fatalf("ActiveMembership(C) after split: %v", err)
	}
	if membershipA.FamilyID == membershipB.FamilyID ||
		membershipB.FamilyID != membershipC.FamilyID {
		t.Fatalf(
			"active graph components = A:%s B:%s C:%s, want A separate and B/C together",
			membershipA.FamilyID,
			membershipB.FamilyID,
			membershipC.FamilyID,
		)
	}

	timelineA, found, err := store.VersionTimeline(t.Context(), workA)
	if err != nil || !found {
		t.Fatalf("VersionTimeline(A) = (%#v, %t, %v)", timelineA, found, err)
	}
	if timelineA.CanonicalWorkID != workA ||
		len(timelineA.Versions) != 1 ||
		len(timelineA.Relations) != 0 {
		t.Fatalf("split timeline A = %#v", timelineA)
	}
	timelineB, found, err := store.VersionTimeline(t.Context(), workB)
	if err != nil || !found {
		t.Fatalf("VersionTimeline(B) = (%#v, %t, %v)", timelineB, found, err)
	}
	if len(timelineB.Versions) != 2 || len(timelineB.Relations) != 1 {
		t.Fatalf("split timeline B = %#v", timelineB)
	}

	_, err = store.RecordRelationDecision(
		t.Context(),
		RelationDecision{
			AssertionID:          decisionAC.AssertionID,
			SupersedesDecisionID: decisionAC.ID,
			Outcome:              DecisionAccepted,
			Reason:               "stale conflicting AC decision",
			PolicyVersion:        "work-family/v2",
			DecidedAt:            baseDecisionAt.Add(5 * time.Second),
		},
	)
	if !errors.Is(err, ErrDecisionConflict) {
		t.Fatalf(
			"stale superseding decision error = %v, want ErrDecisionConflict",
			err,
		)
	}

	var currentDecisionCount int
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM work_relation_decisions AS decision
		WHERE decision.assertion_id = $1
		  AND NOT EXISTS (
		      SELECT 1
		      FROM work_relation_decisions AS successor
		      WHERE successor.supersedes_decision_id = decision.id
		  )
	`, decisionAC.AssertionID).Scan(&currentDecisionCount); err != nil {
		t.Fatalf("count current AC decisions: %v", err)
	}
	if currentDecisionCount != 1 {
		t.Fatalf(
			"current AC decision count = %d, want 1",
			currentDecisionCount,
		)
	}
	if rejectedAC.SupersedesDecisionID != decisionAC.ID {
		t.Fatalf("rejected AC decision = %#v", rejectedAC)
	}

	reactivatedAC, err := store.RecordRelationDecision(
		t.Context(),
		RelationDecision{
			AssertionID:          decisionAC.AssertionID,
			SupersedesDecisionID: rejectedAC.ID,
			Outcome:              DecisionAccepted,
			Reason:               "AC relation reactivated",
			PolicyVersion:        "work-family/v3",
			DecidedAt:            baseDecisionAt.Add(6 * time.Second),
		},
	)
	if err != nil {
		t.Fatalf("reactivate AC relation: %v", err)
	}
	if reactivatedAC.SupersedesDecisionID != rejectedAC.ID {
		t.Fatalf("reactivated AC decision = %#v", reactivatedAC)
	}
	membershipA, _, err = store.ActiveMembership(t.Context(), workA)
	if err != nil {
		t.Fatalf("ActiveMembership(A) after reactivation: %v", err)
	}
	membershipB, _, err = store.ActiveMembership(t.Context(), workB)
	if err != nil {
		t.Fatalf("ActiveMembership(B) after reactivation: %v", err)
	}
	membershipC, _, err = store.ActiveMembership(t.Context(), workC)
	if err != nil {
		t.Fatalf("ActiveMembership(C) after reactivation: %v", err)
	}
	if membershipA.FamilyID != membershipB.FamilyID ||
		membershipB.FamilyID != membershipC.FamilyID {
		t.Fatalf(
			"reactivated accepted graph families: A=%s B=%s C=%s",
			membershipA.FamilyID,
			membershipB.FamilyID,
			membershipC.FamilyID,
		)
	}
}

func TestActiveAcceptedRelationTraversalDoesNotScanUnrelatedEdges(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	seedWorkID := fixture.insertWork(
		t,
		"openalex:W41211",
		"Local relation traversal seed",
		time.Now().UTC().Add(-3*time.Hour),
	)
	unrelatedSubjectWorkID := fixture.insertWork(
		t,
		"openalex:W41212",
		"Unrelated relation subject",
		time.Now().UTC().Add(-2*time.Hour),
	)
	unrelatedObjectWorkID := fixture.insertWork(
		t,
		"openalex:W41213",
		"Unrelated relation object",
		time.Now().UTC().Add(-time.Hour),
	)
	sourceRecordID := fixture.insertSourceRecord(
		t,
		unrelatedSubjectWorkID,
		"crossref",
		"relation-plan-unrelated-41212",
	)

	const unrelatedEdgeCount = 100_000
	bulkInsert, err := fixture.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin unrelated relation bulk insert: %v", err)
	}
	defer bulkInsert.Rollback(t.Context())
	for _, triggerName := range []string{
		"work_relation_decisions_chain_guard",
		"work_relation_decisions_exactly_one_current",
	} {
		if _, err := bulkInsert.Exec(
			t.Context(),
			"ALTER TABLE work_relation_decisions DISABLE TRIGGER "+
				triggerName,
		); err != nil {
			t.Fatalf("disable %s: %v", triggerName, err)
		}
	}
	if _, err := bulkInsert.Exec(t.Context(), `
		WITH inserted_assertions AS (
			INSERT INTO work_relation_assertions (
				subject_work_id,
				object_work_id,
				relation_kind,
				evidence_kind,
				subject_source_record_id,
				subject_source_path,
				asserted_at
			)
			SELECT
				$1,
				$2,
				'is_version_of',
				'explicit_source_relation',
				$3,
				'query-plan.unrelated.' || edge_number,
				$4
			FROM generate_series(1, $5) AS edge_number
			RETURNING id
		)
		INSERT INTO work_relation_decisions (
			assertion_id,
			outcome,
			reason,
			policy_version,
			decided_at
		)
		SELECT
			id,
			'accepted',
			'bulk unrelated accepted edge',
			'work-family/query-plan-v1',
			$4
		FROM inserted_assertions
	`,
		unrelatedSubjectWorkID,
		unrelatedObjectWorkID,
		sourceRecordID,
		time.Now().UTC().Truncate(time.Microsecond),
		unrelatedEdgeCount,
	); err != nil {
		t.Fatalf("insert unrelated active accepted edges: %v", err)
	}
	for _, triggerName := range []string{
		"work_relation_decisions_chain_guard",
		"work_relation_decisions_exactly_one_current",
	} {
		if _, err := bulkInsert.Exec(
			t.Context(),
			"ALTER TABLE work_relation_decisions ENABLE TRIGGER "+
				triggerName,
		); err != nil {
			t.Fatalf("enable %s: %v", triggerName, err)
		}
	}
	if err := bulkInsert.Commit(t.Context()); err != nil {
		t.Fatalf("commit unrelated relation bulk insert: %v", err)
	}

	capture := &activeRelationEdgesQueryCapture{}
	tracedConfig := fixture.Pool.Config().Copy()
	tracedConfig.ConnConfig.Tracer = capture
	tracedPool, err := pgxpool.NewWithConfig(t.Context(), tracedConfig)
	if err != nil {
		t.Fatalf("open relation query capture pool: %v", err)
	}
	t.Cleanup(tracedPool.Close)
	tracedTx, err := tracedPool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin relation query capture transaction: %v", err)
	}
	edges, err := loadActiveAcceptedRelationEdges(
		t.Context(),
		tracedTx,
		[]uuid.UUID{seedWorkID},
	)
	if err != nil {
		tracedTx.Rollback(t.Context())
		t.Fatalf("load local active accepted relation edges: %v", err)
	}
	if len(edges) != 0 {
		tracedTx.Rollback(t.Context())
		t.Fatalf("local relation edge count = %d, want 0", len(edges))
	}
	if err := tracedTx.Commit(t.Context()); err != nil {
		t.Fatalf("commit relation query capture transaction: %v", err)
	}

	relationQueries := capture.Queries()
	if len(relationQueries) == 0 {
		t.Fatal("active accepted relation queries were not captured")
	}

	explainTx, err := fixture.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin relation query EXPLAIN transaction: %v", err)
	}
	defer explainTx.Rollback(t.Context())
	if _, err := explainTx.Exec(t.Context(), `
		SET LOCAL jit = off;
		SET LOCAL max_parallel_workers_per_gather = 0;
	`); err != nil {
		t.Fatalf("configure relation query EXPLAIN: %v", err)
	}
	targetRelationNames := map[string]struct{}{
		"work_relation_assertions": {},
		"work_relation_decisions":  {},
	}
	var visitedRelationRows float64
	for queryIndex, relationQuery := range relationQueries {
		var planJSON []byte
		if err := explainTx.QueryRow(
			t.Context(),
			"EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+relationQuery,
			[]uuid.UUID{seedWorkID},
		).Scan(&planJSON); err != nil {
			t.Fatalf(
				"EXPLAIN active accepted relation query %d: %v",
				queryIndex,
				err,
			)
		}
		var plan []postgresExplainPlanEnvelope
		if err := json.Unmarshal(planJSON, &plan); err != nil {
			t.Fatalf(
				"decode active relation query %d plan: %v",
				queryIndex,
				err,
			)
		}
		if len(plan) != 1 {
			t.Fatalf(
				"active relation query %d plan count = %d, want 1",
				queryIndex,
				len(plan),
			)
		}
		if relationSeqScans := postgresExplainRelationSeqScans(
			plan[0].Plan,
			targetRelationNames,
		); len(relationSeqScans) > 0 {
			t.Fatalf(
				"active relation query %d uses Seq Scan on target tables: %v",
				queryIndex,
				relationSeqScans,
			)
		}
		visitedRelationRows += postgresExplainRelationRows(
			plan[0].Plan,
			targetRelationNames,
		)
	}
	if visitedRelationRows > 100 {
		t.Fatalf(
			"active relation traversal visited %.0f assertion/decision rows "+
				"for %d unrelated edges, want at most 100",
			visitedRelationRows,
			unrelatedEdgeCount,
		)
	}
}

func TestUnrelatedRelationDecisionsDoNotUseGlobalAdvisoryLock(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	subjectWorkID := fixture.insertWork(
		t,
		"openalex:W41204",
		"Unrelated lock subject",
		time.Now().UTC().Add(-2*time.Hour),
	)
	objectWorkID := fixture.insertWork(
		t,
		"openalex:W41205",
		"Unrelated lock object",
		time.Now().UTC().Add(-time.Hour),
	)
	sourceRecordID := fixture.insertSourceRecord(
		t,
		subjectWorkID,
		"crossref",
		"unrelated-lock-41204",
	)
	assertion, err := store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         subjectWorkID,
			ObjectWorkID:          objectWorkID,
			RelationKind:          RelationIsVersionOf,
			EvidenceKind:          EvidenceExplicitSourceRelation,
			SubjectSourceRecordID: sourceRecordID,
			SubjectSourcePath:     "message.relation.is-version-of",
			AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationAssertion() error = %v", err)
	}

	blocker, err := fixture.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin legacy global lock blocker: %v", err)
	}
	defer blocker.Rollback(t.Context())
	if _, err := blocker.Exec(
		t.Context(),
		"SELECT pg_advisory_xact_lock($1)",
		int64(0x574f524b46414d),
	); err != nil {
		t.Fatalf("hold legacy global advisory lock: %v", err)
	}

	decisionCtx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if _, err := store.RecordRelationDecision(
		decisionCtx,
		RelationDecision{
			AssertionID:   assertion.ID,
			Outcome:       DecisionRejected,
			Reason:        "unrelated decision must not wait globally",
			PolicyVersion: "work-family/v2",
			DecidedAt:     time.Now().UTC().Truncate(time.Microsecond),
		},
	); err != nil {
		t.Fatalf("unrelated decision blocked by legacy global lock: %v", err)
	}
}

func TestConcurrentOverlappingAcceptedDecisionsAvoidDeadlock(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	workA := fixture.insertWork(
		t,
		"openalex:W41206",
		"Concurrent graph Work A",
		time.Now().UTC().Add(-3*time.Hour),
	)
	workB := fixture.insertWork(
		t,
		"openalex:W41207",
		"Concurrent graph Work B",
		time.Now().UTC().Add(-2*time.Hour),
	)
	workC := fixture.insertWork(
		t,
		"openalex:W41208",
		"Concurrent graph Work C",
		time.Now().UTC().Add(-time.Hour),
	)

	createAssertion := func(
		subjectWorkID uuid.UUID,
		objectWorkID uuid.UUID,
		sourceRecordKey string,
	) RelationAssertion {
		sourceRecordID := fixture.insertSourceRecord(
			t,
			subjectWorkID,
			"crossref",
			sourceRecordKey,
		)
		assertion, err := store.RecordRelationAssertion(
			t.Context(),
			RelationAssertion{
				SubjectWorkID:         subjectWorkID,
				ObjectWorkID:          objectWorkID,
				RelationKind:          RelationIsVersionOf,
				EvidenceKind:          EvidenceExplicitSourceRelation,
				SubjectSourceRecordID: sourceRecordID,
				SubjectSourcePath:     "message.relation.is-version-of",
				AssertedAt: time.Now().UTC().Truncate(
					time.Microsecond,
				),
			},
		)
		if err != nil {
			t.Fatalf("RecordRelationAssertion() error = %v", err)
		}
		return assertion
	}
	assertionAB := createAssertion(workA, workB, "concurrent-ab-41206")
	assertionBC := createAssertion(workB, workC, "concurrent-bc-41206")

	start := make(chan struct{})
	results := make(chan error, 2)
	decidedAt := time.Now().UTC().Truncate(time.Microsecond)
	for _, assertion := range []RelationAssertion{assertionAB, assertionBC} {
		assertion := assertion
		go func() {
			<-start
			_, err := store.RecordRelationDecision(
				t.Context(),
				RelationDecision{
					AssertionID:   assertion.ID,
					Outcome:       DecisionAccepted,
					Reason:        "concurrent overlapping accepted relation",
					PolicyVersion: "work-family/v2",
					DecidedAt:     decidedAt,
				},
			)
			results <- err
		}()
	}
	close(start)

	for resultIndex := 0; resultIndex < 2; resultIndex++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf(
					"concurrent decision %d error = %v",
					resultIndex,
					err,
				)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent overlapping decisions deadlocked")
		}
	}

	membershipA, _, err := store.ActiveMembership(t.Context(), workA)
	if err != nil {
		t.Fatalf("ActiveMembership(A) error = %v", err)
	}
	membershipB, _, err := store.ActiveMembership(t.Context(), workB)
	if err != nil {
		t.Fatalf("ActiveMembership(B) error = %v", err)
	}
	membershipC, _, err := store.ActiveMembership(t.Context(), workC)
	if err != nil {
		t.Fatalf("ActiveMembership(C) error = %v", err)
	}
	if membershipA.FamilyID != membershipB.FamilyID ||
		membershipB.FamilyID != membershipC.FamilyID {
		t.Fatalf(
			"concurrent graph families: A=%s B=%s C=%s",
			membershipA.FamilyID,
			membershipB.FamilyID,
			membershipC.FamilyID,
		)
	}
}

func TestConcurrentSharedWorkAcceptedDecisionsAllEventuallySucceed(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}

	const decisionCount = 6
	sharedWorkID := fixture.insertWork(
		t,
		"openalex:W412060",
		"Hot shared Work",
		time.Now().UTC().Add(-24*time.Hour),
	)
	assertions := make([]RelationAssertion, 0, decisionCount)
	for index := 0; index < decisionCount; index++ {
		leafWorkID := fixture.insertWork(
			t,
			fmt.Sprintf("openalex:W41206%d", index+1),
			fmt.Sprintf("Concurrent leaf Work %d", index+1),
			time.Now().UTC().Add(time.Duration(index)*time.Minute),
		)
		sourceRecordID := fixture.insertSourceRecord(
			t,
			sharedWorkID,
			"crossref",
			fmt.Sprintf("concurrent-shared-%d", index+1),
		)
		assertion, assertionErr := store.RecordRelationAssertion(
			t.Context(),
			RelationAssertion{
				SubjectWorkID:         sharedWorkID,
				ObjectWorkID:          leafWorkID,
				RelationKind:          RelationIsVersionOf,
				EvidenceKind:          EvidenceExplicitSourceRelation,
				SubjectSourceRecordID: sourceRecordID,
				SubjectSourcePath: fmt.Sprintf(
					"message.relation.is-version-of.%d",
					index+1,
				),
				AssertedAt: time.Now().UTC().Truncate(time.Microsecond),
			},
		)
		if assertionErr != nil {
			t.Fatalf(
				"RecordRelationAssertion(%d) error = %v",
				index,
				assertionErr,
			)
		}
		assertions = append(assertions, assertion)
	}

	sharedLockKey := advisoryEntityLockKey(
		advisoryLockNamespaceWork,
		sharedWorkID,
	)
	blocker, err := fixture.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin shared Work advisory lock blocker: %v", err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(
		t.Context(),
		"SELECT pg_advisory_xact_lock($1)",
		sharedLockKey,
	); err != nil {
		t.Fatalf("hold shared Work advisory lock: %v", err)
	}

	capture := &advisoryLockAttemptCapture{
		key:     sharedLockKey,
		reached: make(chan struct{}, decisionCount),
		seen:    make(map[uint32]struct{}, decisionCount),
	}
	tracedConfig := fixture.Pool.Config().Copy()
	tracedConfig.MaxConns = decisionCount
	tracedConfig.ConnConfig.Tracer = capture
	tracedPool, err := pgxpool.NewWithConfig(t.Context(), tracedConfig)
	if err != nil {
		t.Fatalf("open shared Work lock capture pool: %v", err)
	}
	t.Cleanup(tracedPool.Close)
	tracedStore, err := NewPostgresStore(tracedPool)
	if err != nil {
		t.Fatalf("NewPostgresStore(traced) error = %v", err)
	}

	testCtx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, decisionCount)
	decidedAt := time.Now().UTC().Truncate(time.Microsecond)
	for index, assertion := range assertions {
		index := index
		assertion := assertion
		go func() {
			<-start
			_, decisionErr := tracedStore.RecordRelationDecision(
				testCtx,
				RelationDecision{
					AssertionID: assertion.ID,
					Outcome:     DecisionAccepted,
					Reason: fmt.Sprintf(
						"concurrent shared Work accepted relation %d",
						index+1,
					),
					PolicyVersion: "work-family/v2",
					DecidedAt:     decidedAt,
				},
			)
			results <- decisionErr
		}()
	}
	close(start)

	for index := 0; index < decisionCount; index++ {
		select {
		case <-capture.reached:
		case <-testCtx.Done():
			t.Fatalf(
				"only %d/%d decisions reached the shared Work lock: %v",
				index,
				decisionCount,
				testCtx.Err(),
			)
		}
	}
	if err := blocker.Commit(testCtx); err != nil {
		t.Fatalf("release shared Work advisory lock: %v", err)
	}

	for index := 0; index < decisionCount; index++ {
		select {
		case decisionErr := <-results:
			if decisionErr != nil {
				t.Fatalf(
					"concurrent shared Work decision %d error = %v",
					index,
					decisionErr,
				)
			}
		case <-testCtx.Done():
			t.Fatalf(
				"concurrent shared Work decisions did not finish: %v",
				testCtx.Err(),
			)
		}
	}

	var (
		activeFamilyCount int
		decisionRowCount  int
	)
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT count(DISTINCT work_family_id)
		FROM work_family_memberships
		WHERE work_id = $1
		   OR work_id = ANY($2::uuid[])
		  AND ended_at IS NULL
	`, sharedWorkID, []uuid.UUID{
		assertions[0].ObjectWorkID,
		assertions[1].ObjectWorkID,
		assertions[2].ObjectWorkID,
		assertions[3].ObjectWorkID,
		assertions[4].ObjectWorkID,
		assertions[5].ObjectWorkID,
	}).Scan(&activeFamilyCount); err != nil {
		t.Fatalf("count final active families: %v", err)
	}
	if activeFamilyCount != 1 {
		t.Fatalf("final active family count = %d, want 1", activeFamilyCount)
	}
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM work_relation_decisions
		WHERE assertion_id = ANY($1::uuid[])
		  AND outcome = 'accepted'
	`, []uuid.UUID{
		assertions[0].ID,
		assertions[1].ID,
		assertions[2].ID,
		assertions[3].ID,
		assertions[4].ID,
		assertions[5].ID,
	}).Scan(&decisionRowCount); err != nil {
		t.Fatalf("count accepted decision rows: %v", err)
	}
	if decisionRowCount != decisionCount {
		t.Fatalf(
			"accepted decision row count = %d, want %d",
			decisionRowCount,
			decisionCount,
		)
	}
}

func TestRelationDecisionAndRestrictedWorkDeleteUseConsistentLockOrder(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	subjectWorkID := fixture.insertWork(
		t,
		"openalex:W41209",
		"Decision delete lock subject",
		time.Now().UTC().Add(-2*time.Hour),
	)
	objectWorkID := fixture.insertWork(
		t,
		"openalex:W41210",
		"Decision delete lock object",
		time.Now().UTC().Add(-time.Hour),
	)
	sourceRecordID := fixture.insertSourceRecord(
		t,
		subjectWorkID,
		"crossref",
		"decision-delete-lock-41209",
	)
	assertion, err := store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         subjectWorkID,
			ObjectWorkID:          objectWorkID,
			RelationKind:          RelationIsVersionOf,
			EvidenceKind:          EvidenceExplicitSourceRelation,
			SubjectSourceRecordID: sourceRecordID,
			SubjectSourcePath:     "message.relation.is-version-of",
			AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationAssertion() error = %v", err)
	}

	barrier := &relationDecisionAssertionLockBarrier{
		reached: make(chan uint32, 1),
		release: make(chan struct{}),
	}
	t.Cleanup(barrier.Release)
	tracedConfig := fixture.Pool.Config().Copy()
	tracedConfig.ConnConfig.Tracer = barrier
	tracedPool, err := pgxpool.NewWithConfig(t.Context(), tracedConfig)
	if err != nil {
		t.Fatalf("open traced Work Family pool: %v", err)
	}
	t.Cleanup(tracedPool.Close)
	tracedStore, err := NewPostgresStore(tracedPool)
	if err != nil {
		t.Fatalf("NewPostgresStore(traced) error = %v", err)
	}

	testCtx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	decisionResult := make(chan error, 1)
	go func() {
		_, decisionErr := tracedStore.RecordRelationDecision(
			testCtx,
			RelationDecision{
				AssertionID:   assertion.ID,
				Outcome:       DecisionAccepted,
				Reason:        "accepted while Work delete is restricted",
				PolicyVersion: "work-family/v2",
				DecidedAt: time.Now().UTC().Truncate(
					time.Microsecond,
				),
			},
		)
		decisionResult <- decisionErr
	}()

	var decisionBackendPID uint32
	select {
	case decisionBackendPID = <-barrier.reached:
	case <-testCtx.Done():
		t.Fatalf(
			"decision did not lock assertion before timeout: %v",
			testCtx.Err(),
		)
	}

	deleteConnection, err := fixture.Pool.Acquire(testCtx)
	if err != nil {
		t.Fatalf("acquire Work delete connection: %v", err)
	}
	defer deleteConnection.Release()
	var deleteBackendPID int32
	if err := deleteConnection.QueryRow(
		testCtx,
		"SELECT pg_backend_pid()",
	).Scan(&deleteBackendPID); err != nil {
		t.Fatalf("query Work delete backend PID: %v", err)
	}

	deleteResult := make(chan error, 1)
	go func() {
		_, deleteErr := deleteConnection.Exec(testCtx, `
			DELETE FROM works
			WHERE id = $1
		`, objectWorkID)
		deleteResult <- deleteErr
	}()

	for {
		var blockedByDecision bool
		if err := fixture.Pool.QueryRow(testCtx, `
			SELECT $2::integer = ANY(pg_blocking_pids($1::integer))
		`,
			deleteBackendPID,
			int32(decisionBackendPID),
		).Scan(&blockedByDecision); err != nil {
			t.Fatalf("inspect concurrent Work delete blocker: %v", err)
		}
		if blockedByDecision {
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-testCtx.Done():
			t.Fatalf(
				"Work delete was not blocked by decision transaction: %v",
				testCtx.Err(),
			)
		}
	}
	barrier.Release()

	var decisionErr, deleteErr error
	select {
	case decisionErr = <-decisionResult:
	case <-testCtx.Done():
		t.Fatalf("relation decision did not finish: %v", testCtx.Err())
	}
	select {
	case deleteErr = <-deleteResult:
	case <-testCtx.Done():
		t.Fatalf("restricted Work delete did not finish: %v", testCtx.Err())
	}

	postgresCode := func(err error) string {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) {
			return postgresError.Code
		}
		return ""
	}
	decisionCode := postgresCode(decisionErr)
	deleteCode := postgresCode(deleteErr)
	if decisionCode == "40P01" || deleteCode == "40P01" {
		t.Fatalf(
			"decision/delete deadlocked: decision=%v delete=%v",
			decisionErr,
			deleteErr,
		)
	}
	if decisionErr != nil &&
		!errors.Is(decisionErr, ErrWorkNotFound) &&
		!strings.HasPrefix(decisionCode, "23") {
		t.Fatalf(
			"decision error = %v, want success, ErrWorkNotFound, or constraint",
			decisionErr,
		)
	}
	if deleteErr == nil || !strings.HasPrefix(deleteCode, "23") {
		t.Fatalf(
			"restricted Work delete error = %v, want constraint",
			deleteErr,
		)
	}
}

func TestVersionTimelineUsesOneRepeatableReadSnapshot(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	preprintWorkID := fixture.insertWork(
		t,
		"arxiv:2607.04112",
		"Snapshot preprint",
		time.Now().UTC().Add(-48*time.Hour),
	)
	recordWorkID := fixture.insertWork(
		t,
		"doi:10.1000/work-family-41102",
		"Snapshot Version of Record",
		time.Now().UTC().Add(-24*time.Hour),
	)
	recordAcceptedRelation(
		t,
		fixture,
		store,
		preprintWorkID,
		recordWorkID,
		RelationIsPreprintOf,
		time.Now().UTC().Truncate(time.Microsecond),
		"timeline-snapshot-41102",
	)
	targetMembership, found, err := store.ActiveMembership(
		t.Context(),
		recordWorkID,
	)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership(record) = (%#v, %t, %v)",
			targetMembership,
			found,
			err,
		)
	}
	lateWorkID := fixture.insertWork(
		t,
		"openalex:W41105",
		"Late concurrent version",
		time.Now().UTC(),
	)
	lateMembership, found, err := store.ActiveMembership(
		t.Context(),
		lateWorkID,
	)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership(late) = (%#v, %t, %v)",
			lateMembership,
			found,
			err,
		)
	}

	barrier := &timelineQueryBarrier{
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(barrier.Release)
	tracedConfig := fixture.Pool.Config().Copy()
	tracedConfig.ConnConfig.Tracer = barrier
	tracedPool, err := pgxpool.NewWithConfig(t.Context(), tracedConfig)
	if err != nil {
		t.Fatalf("open traced Work Family pool: %v", err)
	}
	t.Cleanup(tracedPool.Close)
	tracedStore, err := NewPostgresStore(tracedPool)
	if err != nil {
		t.Fatalf("NewPostgresStore(traced) error = %v", err)
	}

	type timelineResult struct {
		timeline Timeline
		found    bool
		err      error
	}
	result := make(chan timelineResult, 1)
	go func() {
		timeline, timelineFound, timelineErr := tracedStore.VersionTimeline(
			t.Context(),
			recordWorkID,
		)
		result <- timelineResult{
			timeline: timeline,
			found:    timelineFound,
			err:      timelineErr,
		}
	}()

	select {
	case <-barrier.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("VersionTimeline did not reach versions query")
	}

	writer, err := fixture.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin concurrent timeline writer: %v", err)
	}
	if _, err := writer.Exec(t.Context(), `
		DELETE FROM work_family_canonical_states
		WHERE work_family_id = $1
	`, lateMembership.FamilyID); err != nil {
		writer.Rollback(t.Context())
		t.Fatalf("delete late singleton canonical state: %v", err)
	}
	projectedAt := time.Now().UTC().Add(time.Second)
	if _, err := writer.Exec(t.Context(), `
		UPDATE work_family_memberships
		SET ended_at = $2
		WHERE id = $1
	`, lateMembership.ID, projectedAt); err != nil {
		writer.Rollback(t.Context())
		t.Fatalf("close late singleton membership: %v", err)
	}
	if _, err := writer.Exec(t.Context(), `
		INSERT INTO work_family_memberships (
			work_family_id,
			work_id,
			origin,
			started_at
		) VALUES ($1, $2, 'singleton', $3)
	`, targetMembership.FamilyID, lateWorkID, projectedAt); err != nil {
		writer.Rollback(t.Context())
		t.Fatalf("move late Work into target family: %v", err)
	}
	if err := writer.Commit(t.Context()); err != nil {
		t.Fatalf("commit concurrent timeline writer: %v", err)
	}
	barrier.Release()

	select {
	case got := <-result:
		if got.err != nil || !got.found {
			t.Fatalf(
				"VersionTimeline() = (%#v, %t, %v)",
				got.timeline,
				got.found,
				got.err,
			)
		}
		if len(got.timeline.Versions) != 2 {
			t.Fatalf(
				"repeatable-read timeline versions = %d, want original 2",
				len(got.timeline.Versions),
			)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("VersionTimeline did not finish after releasing query barrier")
	}
}

func TestPostgresStoreAcceptsStableSharedIdentifierAndRejectsSimilarity(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	subjectWorkID := fixture.insertWork(
		t,
		"openalex:W41002",
		"Same title and authors",
		time.Now().UTC().Add(-2*time.Hour),
	)
	objectWorkID := fixture.insertWork(
		t,
		"openalex:W41003",
		"Same title and authors",
		time.Now().UTC().Add(-time.Hour),
	)
	subjectSourceRecordID := fixture.insertSourceRecord(
		t,
		subjectWorkID,
		"publisher",
		"manuscript-subject-41002",
	)
	objectSourceRecordID := fixture.insertSourceRecord(
		t,
		objectWorkID,
		"publisher",
		"manuscript-object-41002",
	)

	_, err = store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         subjectWorkID,
			ObjectWorkID:          objectWorkID,
			RelationKind:          RelationIsVersionOf,
			EvidenceKind:          EvidenceKind("title_author_similarity"),
			SubjectSourceRecordID: subjectSourceRecordID,
			SubjectSourcePath:     "derived.similarity",
			AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if !errors.Is(err, ErrUnsupportedEvidence) {
		t.Fatalf(
			"RecordRelationAssertion(similarity) error = %v, want ErrUnsupportedEvidence",
			err,
		)
	}

	assertion, err := store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         subjectWorkID,
			ObjectWorkID:          objectWorkID,
			RelationKind:          RelationIsVersionOf,
			EvidenceKind:          EvidenceStableSharedIdentifier,
			SubjectSourceRecordID: subjectSourceRecordID,
			SubjectSourcePath:     "record.identifiers.pmid",
			ObjectSourceRecordID:  objectSourceRecordID,
			ObjectSourcePath:      "record.identifiers.pmid",
			IdentifierScheme:      "pmid",
			IdentifierValue:       "41002002",
			AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationAssertion(shared identifier) error = %v", err)
	}
	if _, err := store.RecordRelationDecision(
		t.Context(),
		RelationDecision{
			AssertionID:   assertion.ID,
			Outcome:       DecisionAccepted,
			Reason:        "stable shared manuscript identifier",
			PolicyVersion: "work-family/v1",
			DecidedAt:     time.Now().UTC().Truncate(time.Microsecond),
		},
	); err != nil {
		t.Fatalf("RecordRelationDecision(shared identifier) error = %v", err)
	}

	subjectMembership, _, err := store.ActiveMembership(
		t.Context(),
		subjectWorkID,
	)
	if err != nil {
		t.Fatalf("ActiveMembership(subject) error = %v", err)
	}
	objectMembership, _, err := store.ActiveMembership(
		t.Context(),
		objectWorkID,
	)
	if err != nil {
		t.Fatalf("ActiveMembership(object) error = %v", err)
	}
	if subjectMembership.FamilyID != objectMembership.FamilyID {
		t.Fatalf(
			"stable identifier families = subject %s object %s",
			subjectMembership.FamilyID,
			objectMembership.FamilyID,
		)
	}
}

func TestDatabaseRejectsUnregisteredStableIdentifierSchemes(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	subjectWorkID := fixture.insertWork(
		t,
		"openalex:W41110",
		"Unregistered identifier subject",
		time.Now().UTC().Add(-2*time.Hour),
	)
	objectWorkID := fixture.insertWork(
		t,
		"openalex:W41111",
		"Unregistered identifier object",
		time.Now().UTC().Add(-time.Hour),
	)
	subjectSourceRecordID := fixture.insertSourceRecord(
		t,
		subjectWorkID,
		"publisher",
		"unregistered-identifier-subject-41110",
	)
	objectSourceRecordID := fixture.insertSourceRecord(
		t,
		objectWorkID,
		"publisher",
		"unregistered-identifier-object-41111",
	)

	for _, scheme := range []string{
		"title_author",
		"title",
		"author_similarity",
	} {
		_, err := fixture.Pool.Exec(t.Context(), `
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
				$1,
				$2,
				'is_version_of',
				'stable_shared_identifier',
				$3,
				'record.identifiers',
				$4,
				'record.identifiers',
				$5,
				'derived-similarity-value',
				now()
			)
		`,
			subjectWorkID,
			objectWorkID,
			subjectSourceRecordID,
			objectSourceRecordID,
			scheme,
		)
		assertWorkFamilyPostgresCode(t, err, "23514")
	}
}

func TestPostgresWorkFamilyHistoryIsImmutable(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	subjectWorkID := fixture.insertWork(
		t,
		"openalex:W41004",
		"Immutable relation subject",
		time.Now().UTC().Add(-2*time.Hour),
	)
	objectWorkID := fixture.insertWork(
		t,
		"openalex:W41005",
		"Immutable relation object",
		time.Now().UTC().Add(-time.Hour),
	)
	sourceRecordID := fixture.insertSourceRecord(
		t,
		subjectWorkID,
		"crossref",
		"immutable-relation-41004",
	)
	assertion, err := store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         subjectWorkID,
			ObjectWorkID:          objectWorkID,
			RelationKind:          RelationIsPreprintOf,
			EvidenceKind:          EvidenceExplicitSourceRelation,
			SubjectSourceRecordID: sourceRecordID,
			SubjectSourcePath:     "message.relation.is-preprint-of",
			AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationAssertion() error = %v", err)
	}
	decision, err := store.RecordRelationDecision(
		t.Context(),
		RelationDecision{
			AssertionID:   assertion.ID,
			Outcome:       DecisionAccepted,
			Reason:        "source relation accepted for immutability test",
			PolicyVersion: "work-family/v1",
			DecidedAt:     time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationDecision() error = %v", err)
	}

	_, err = fixture.Pool.Exec(t.Context(), `
		UPDATE work_relation_assertions
		SET subject_source_path = 'mutated.path'
		WHERE id = $1
	`, assertion.ID)
	assertWorkFamilyPostgresCode(t, err, "55000")

	_, err = fixture.Pool.Exec(t.Context(), `
		DELETE FROM work_relation_assertions
		WHERE id = $1
	`, assertion.ID)
	assertWorkFamilyPostgresCode(t, err, "55000")

	_, err = fixture.Pool.Exec(t.Context(), `
		UPDATE work_relation_decisions
		SET reason = 'mutated decision'
		WHERE id = $1
	`, decision.ID)
	assertWorkFamilyPostgresCode(t, err, "55000")

	_, err = fixture.Pool.Exec(t.Context(), `
		DELETE FROM work_relation_decisions
		WHERE id = $1
	`, decision.ID)
	assertWorkFamilyPostgresCode(t, err, "55000")

	var activeMembershipID uuid.UUID
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT id
		FROM work_family_memberships
		WHERE work_id = $1
		  AND ended_at IS NULL
	`, subjectWorkID).Scan(&activeMembershipID); err != nil {
		t.Fatalf("query active membership: %v", err)
	}
	_, err = fixture.Pool.Exec(t.Context(), `
		DELETE FROM work_family_memberships
		WHERE id = $1
	`, activeMembershipID)
	assertWorkFamilyPostgresCode(t, err, "55000")

	var endedMembershipID uuid.UUID
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT id
		FROM work_family_memberships
		WHERE ended_at IS NOT NULL
		ORDER BY ended_at, id
		LIMIT 1
	`).Scan(&endedMembershipID); err != nil {
		t.Fatalf("query ended membership: %v", err)
	}
	_, err = fixture.Pool.Exec(t.Context(), `
		UPDATE work_family_memberships
		SET ended_at = ended_at + interval '1 second'
		WHERE id = $1
	`, endedMembershipID)
	assertWorkFamilyPostgresCode(t, err, "55000")

	var canonicalDecisionID uuid.UUID
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT id
		FROM work_family_canonical_decisions
		WHERE relation_decision_id = $1
	`, decision.ID).Scan(&canonicalDecisionID); err != nil {
		t.Fatalf("query canonical decision: %v", err)
	}
	_, err = fixture.Pool.Exec(t.Context(), `
		UPDATE work_family_canonical_decisions
		SET reason = 'mutated canonical decision'
		WHERE id = $1
	`, canonicalDecisionID)
	assertWorkFamilyPostgresCode(t, err, "55000")

	_, err = fixture.Pool.Exec(t.Context(), `
		DELETE FROM work_family_canonical_decisions
		WHERE id = $1
	`, canonicalDecisionID)
	assertWorkFamilyPostgresCode(t, err, "55000")
}

func TestPostgresMembershipConstraintRequiresOneActiveMembershipAtCommit(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	workID := fixture.insertWork(
		t,
		"openalex:W41006",
		"Membership constraint Work",
		time.Now().UTC().Add(-time.Hour),
	)

	tx, err := fixture.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin membership constraint transaction: %v", err)
	}
	defer tx.Rollback(t.Context())

	if _, err := tx.Exec(t.Context(), `
		UPDATE work_family_memberships
		SET ended_at = now()
		WHERE work_id = $1
		  AND ended_at IS NULL
	`, workID); err != nil {
		t.Fatalf("close only active membership: %v", err)
	}
	err = tx.Commit(t.Context())
	assertWorkFamilyPostgresCode(t, err, "23514")
}

func TestCanonicalConstraintValidatesEachAffectedFamilyOncePerTransaction(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)

	const movedWorkCount = 12
	targetWorkID := fixture.insertWork(
		t,
		"openalex:W410060",
		"Canonical validation target Work",
		time.Now().UTC().Add(-24*time.Hour),
	)
	var targetFamilyID uuid.UUID
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT work_family_id
		FROM work_family_memberships
		WHERE work_id = $1
		  AND ended_at IS NULL
	`, targetWorkID).Scan(&targetFamilyID); err != nil {
		t.Fatalf("load target family: %v", err)
	}

	movedWorkIDs := make([]uuid.UUID, 0, movedWorkCount)
	sourceFamilyIDs := make([]uuid.UUID, 0, movedWorkCount)
	sourceMembershipIDs := make([]uuid.UUID, 0, movedWorkCount)
	for index := 0; index < movedWorkCount; index++ {
		workID := fixture.insertWork(
			t,
			fmt.Sprintf("openalex:W41006%d", index+1),
			fmt.Sprintf("Canonical validation source Work %d", index+1),
			time.Now().UTC().Add(time.Duration(index)*time.Minute),
		)
		var familyID, membershipID uuid.UUID
		if err := fixture.Pool.QueryRow(t.Context(), `
			SELECT work_family_id, id
			FROM work_family_memberships
			WHERE work_id = $1
			  AND ended_at IS NULL
		`, workID).Scan(&familyID, &membershipID); err != nil {
			t.Fatalf("load source family %d: %v", index, err)
		}
		movedWorkIDs = append(movedWorkIDs, workID)
		sourceFamilyIDs = append(sourceFamilyIDs, familyID)
		sourceMembershipIDs = append(sourceMembershipIDs, membershipID)
	}

	connection, err := fixture.Pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("acquire canonical validation connection: %v", err)
	}
	defer connection.Release()

	tx, err := connection.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin canonical validation workload: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), "SET LOCAL track_functions = 'pl'"); err != nil {
		t.Fatalf("enable PL/pgSQL function statistics: %v", err)
	}

	projectedAt := time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	if _, err := tx.Exec(t.Context(), `
		DELETE FROM work_family_canonical_states
		WHERE work_family_id = ANY($1::uuid[])
	`, sourceFamilyIDs); err != nil {
		t.Fatalf("delete source canonical states: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `
		UPDATE work_family_memberships
		SET ended_at = $2
		WHERE id = ANY($1::uuid[])
	`, sourceMembershipIDs, projectedAt); err != nil {
		t.Fatalf("close source memberships: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO work_family_memberships (
			work_family_id,
			work_id,
			origin,
			started_at
		)
		SELECT $1, moved_work_id, 'singleton', $3
		FROM unnest($2::uuid[]) AS moved_work_id
	`, targetFamilyID, movedWorkIDs, projectedAt); err != nil {
		t.Fatalf("insert target memberships: %v", err)
	}
	if _, err := tx.Exec(t.Context(), "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("validate deferred Work Family constraints: %v", err)
	}

	var validationCalls int64
	if err := tx.QueryRow(t.Context(), `
		SELECT calls
		FROM pg_stat_xact_user_functions
		WHERE funcid =
			'validate_active_work_family_canonical(uuid)'::regprocedure
	`).Scan(&validationCalls); err != nil {
		t.Fatalf("query canonical validation calls: %v", err)
	}
	const affectedFamilyCount = movedWorkCount + 1
	if validationCalls != affectedFamilyCount {
		t.Fatalf(
			"canonical validation calls = %d, want %d affected families",
			validationCalls,
			affectedFamilyCount,
		)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit canonical validation workload: %v", err)
	}
}

func TestCanonicalStateDeleteCannotLeaveActiveFamilyAtCommit(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	workID := fixture.insertWork(
		t,
		"openalex:W41112",
		"Canonical state required Work",
		time.Now().UTC().Add(-time.Hour),
	)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	membership, found, err := store.ActiveMembership(t.Context(), workID)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership() = (%#v, %t, %v)",
			membership,
			found,
			err,
		)
	}

	tx, err := fixture.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin canonical state delete transaction: %v", err)
	}
	defer tx.Rollback(t.Context())

	if _, err := tx.Exec(t.Context(), `
		DELETE FROM work_family_canonical_states
		WHERE work_family_id = $1
	`, membership.FamilyID); err != nil {
		t.Fatalf("delete active family canonical state: %v", err)
	}
	err = tx.Commit(t.Context())
	assertWorkFamilyPostgresCode(t, err, "23514")
}

func TestCanonicalValidationQueueRequeuesAfterConstraintsImmediate(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	workID := fixture.insertWork(
		t,
		"openalex:W411120",
		"Immediate constraint canonical Work",
		time.Now().UTC().Add(-time.Hour),
	)
	var familyID uuid.UUID
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT work_family_id
		FROM work_family_memberships
		WHERE work_id = $1
		  AND ended_at IS NULL
	`, workID).Scan(&familyID); err != nil {
		t.Fatalf("load immediate constraint family: %v", err)
	}

	tx, err := fixture.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin immediate canonical constraint transaction: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("set Work Family constraints immediate: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `
		UPDATE work_family_canonical_states
		SET updated_at = updated_at
		WHERE work_family_id = $1
	`, familyID); err != nil {
		t.Fatalf("queue initial valid canonical state: %v", err)
	}
	_, err = tx.Exec(t.Context(), `
		DELETE FROM work_family_canonical_states
		WHERE work_family_id = $1
	`, familyID)
	assertWorkFamilyPostgresCode(t, err, "23514")
}

func TestMembershipMoveCannotLeaveStaleCanonicalAtCommit(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	workID := fixture.insertWork(
		t,
		"openalex:W41106",
		"Stale canonical Work",
		time.Now().UTC().Add(-time.Hour),
	)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	membership, found, err := store.ActiveMembership(t.Context(), workID)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership() = (%#v, %t, %v)",
			membership,
			found,
			err,
		)
	}

	tx, err := fixture.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin stale canonical transaction: %v", err)
	}
	defer tx.Rollback(t.Context())

	var targetFamilyID uuid.UUID
	if err := tx.QueryRow(t.Context(), `
		INSERT INTO work_families DEFAULT VALUES
		RETURNING id
	`).Scan(&targetFamilyID); err != nil {
		t.Fatalf("insert target family: %v", err)
	}
	projectedAt := time.Now().UTC().Add(time.Second)
	if _, err := tx.Exec(t.Context(), `
		UPDATE work_family_memberships
		SET ended_at = $2
		WHERE id = $1
	`, membership.ID, projectedAt); err != nil {
		t.Fatalf("close source membership: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO work_family_memberships (
			work_family_id,
			work_id,
			origin,
			started_at
		) VALUES ($1, $2, 'singleton', $3)
	`, targetFamilyID, workID, projectedAt); err != nil {
		t.Fatalf("insert moved membership: %v", err)
	}
	err = tx.Commit(t.Context())
	assertWorkFamilyPostgresCode(t, err, "23514")
}

func TestSingletonWorkDeletePreservesExistingCascadeSemantics(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	workID := fixture.insertWork(
		t,
		"openalex:W41107",
		"Deletable singleton Work",
		time.Now().UTC().Add(-time.Hour),
	)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	membership, found, err := store.ActiveMembership(t.Context(), workID)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership() = (%#v, %t, %v)",
			membership,
			found,
			err,
		)
	}

	if _, err := fixture.Pool.Exec(t.Context(), `
		DELETE FROM works
		WHERE id = $1
	`, workID); err != nil {
		t.Fatalf("delete singleton Work: %v", err)
	}

	for tableIndex, table := range []string{
		"work_family_memberships",
		"work_family_canonical_states",
		"work_family_canonical_decisions",
		"work_families",
	} {
		var count int
		if err := fixture.Pool.QueryRow(
			t.Context(),
			"SELECT count(*) FROM "+table+" WHERE "+
				map[bool]string{
					true:  "id = $1",
					false: "work_family_id = $1",
				}[table == "work_families"],
			membership.FamilyID,
		).Scan(&count); err != nil {
			t.Fatalf("count cleanup table %d %s: %v", tableIndex, table, err)
		}
		if count != 0 {
			t.Fatalf("%s cleanup count = %d, want 0", table, count)
		}
	}
}

func TestWorkWithAdditionalCanonicalHistoryCannotBeDeleted(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	workID := fixture.insertWork(
		t,
		"openalex:W41114",
		"Additional canonical history Work",
		time.Now().UTC().Add(-time.Hour),
	)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	membership, found, err := store.ActiveMembership(t.Context(), workID)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership() = (%#v, %t, %v)",
			membership,
			found,
			err,
		)
	}

	if _, err := fixture.Pool.Exec(t.Context(), `
		INSERT INTO work_family_canonical_decisions (
			work_family_id,
			canonical_work_id,
			decision_kind,
			basis,
			precedence,
			reason,
			policy_version,
			decided_at
		) VALUES (
			$1,
			$2,
			'singleton',
			'singleton',
			0,
			'additional singleton canonical history',
			'work-family-canonical/v1',
			now()
		)
	`, membership.FamilyID, workID); err != nil {
		t.Fatalf("insert additional singleton canonical decision: %v", err)
	}

	var decisionCount int
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM work_family_canonical_decisions
		WHERE work_family_id = $1
	`, membership.FamilyID).Scan(&decisionCount); err != nil {
		t.Fatalf("count canonical decisions before Work delete: %v", err)
	}
	if decisionCount != 2 {
		t.Fatalf("canonical decision count = %d, want 2", decisionCount)
	}

	_, err = fixture.Pool.Exec(t.Context(), `
		DELETE FROM works
		WHERE id = $1
	`, workID)
	assertWorkFamilyPostgresCode(t, err, "23001")

	var workCount int
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM works
		WHERE id = $1
	`, workID).Scan(&workCount); err != nil {
		t.Fatalf("count Work after restricted delete: %v", err)
	}
	if workCount != 1 {
		t.Fatalf("Work count after restricted delete = %d, want 1", workCount)
	}
	if err := fixture.Pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM work_family_canonical_decisions
		WHERE work_family_id = $1
	`, membership.FamilyID).Scan(&decisionCount); err != nil {
		t.Fatalf("count canonical decisions after restricted delete: %v", err)
	}
	if decisionCount != 2 {
		t.Fatalf(
			"canonical decision count after restricted delete = %d, want 2",
			decisionCount,
		)
	}
}

func TestWorkWithMembershipHistoryCannotBeDeleted(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	workID := fixture.insertWork(
		t,
		"openalex:W41113",
		"Membership history Work",
		time.Now().UTC().Add(-time.Hour),
	)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	firstMembership, found, err := store.ActiveMembership(
		t.Context(),
		workID,
	)
	if err != nil || !found {
		t.Fatalf(
			"ActiveMembership() = (%#v, %t, %v)",
			firstMembership,
			found,
			err,
		)
	}

	tx, err := fixture.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin membership history transaction: %v", err)
	}
	defer tx.Rollback(t.Context())

	startedAt := time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	if _, err := tx.Exec(t.Context(), `
		DELETE FROM work_family_canonical_states
		WHERE work_family_id = $1
	`, firstMembership.FamilyID); err != nil {
		t.Fatalf("delete ended family canonical state: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `
		UPDATE work_family_memberships
		SET ended_at = $2
		WHERE id = $1
	`, firstMembership.ID, startedAt); err != nil {
		t.Fatalf("end first singleton membership: %v", err)
	}

	var secondFamilyID uuid.UUID
	if err := tx.QueryRow(t.Context(), `
		INSERT INTO work_families DEFAULT VALUES
		RETURNING id
	`).Scan(&secondFamilyID); err != nil {
		t.Fatalf("insert second singleton family: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO work_family_memberships (
			work_family_id,
			work_id,
			origin,
			started_at
		) VALUES ($1, $2, 'singleton', $3)
	`, secondFamilyID, workID, startedAt); err != nil {
		t.Fatalf("insert second singleton membership: %v", err)
	}

	var secondCanonicalDecisionID uuid.UUID
	if err := tx.QueryRow(t.Context(), `
		INSERT INTO work_family_canonical_decisions (
			work_family_id,
			canonical_work_id,
			decision_kind,
			basis,
			precedence,
			reason,
			policy_version,
			decided_at
		) VALUES (
			$1,
			$2,
			'singleton',
			'singleton',
			0,
			'second singleton Work',
			'work-family-canonical/v1',
			$3
		)
		RETURNING id
	`, secondFamilyID, workID, startedAt).Scan(
		&secondCanonicalDecisionID,
	); err != nil {
		t.Fatalf("insert second singleton canonical decision: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO work_family_canonical_states (
			work_family_id,
			canonical_work_id,
			canonical_decision_id,
			updated_at
		) VALUES ($1, $2, $3, $4)
	`,
		secondFamilyID,
		workID,
		secondCanonicalDecisionID,
		startedAt,
	); err != nil {
		t.Fatalf("insert second singleton canonical state: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit membership history transaction: %v", err)
	}

	_, err = fixture.Pool.Exec(t.Context(), `
		DELETE FROM works
		WHERE id = $1
	`, workID)
	assertWorkFamilyPostgresCode(t, err, "23001")
}

func TestWorkWithRelationHistoryDeleteUsesExplicitForeignKeyRestriction(
	t *testing.T,
) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	subjectWorkID := fixture.insertWork(
		t,
		"openalex:W41108",
		"Restricted relation subject",
		time.Now().UTC().Add(-2*time.Hour),
	)
	objectWorkID := fixture.insertWork(
		t,
		"openalex:W41109",
		"Restricted relation object",
		time.Now().UTC().Add(-time.Hour),
	)
	sourceRecordID := fixture.insertSourceRecord(
		t,
		subjectWorkID,
		"crossref",
		"delete-restricted-relation-41108",
	)
	if _, err := store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         subjectWorkID,
			ObjectWorkID:          objectWorkID,
			RelationKind:          RelationIsVersionOf,
			EvidenceKind:          EvidenceExplicitSourceRelation,
			SubjectSourceRecordID: sourceRecordID,
			SubjectSourcePath:     "message.relation.is-version-of",
			AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
		},
	); err != nil {
		t.Fatalf("RecordRelationAssertion() error = %v", err)
	}

	_, err = fixture.Pool.Exec(t.Context(), `
		DELETE FROM works
		WHERE id = $1
	`, subjectWorkID)
	assertWorkFamilyPostgresCode(t, err, "23001")
}

func TestNewPostgresStoreRejectsNilPool(t *testing.T) {
	t.Parallel()

	if _, err := NewPostgresStore(nil); err == nil {
		t.Fatal("NewPostgresStore(nil) error = nil")
	}
}

func recordAcceptedRelation(
	t *testing.T,
	fixture workFamilyPostgresFixture,
	store *PostgresStore,
	subjectWorkID uuid.UUID,
	objectWorkID uuid.UUID,
	relationKind RelationKind,
	decidedAt time.Time,
	sourceRecordKey string,
) RelationDecision {
	t.Helper()

	sourceRecordID := fixture.insertSourceRecord(
		t,
		subjectWorkID,
		"crossref",
		sourceRecordKey,
	)
	assertion, err := store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         subjectWorkID,
			ObjectWorkID:          objectWorkID,
			RelationKind:          relationKind,
			EvidenceKind:          EvidenceExplicitSourceRelation,
			SubjectSourceRecordID: sourceRecordID,
			SubjectSourcePath:     "message.relation." + string(relationKind),
			AssertedAt:            decidedAt,
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationAssertion(%s) error = %v", relationKind, err)
	}
	decision, err := store.RecordRelationDecision(
		t.Context(),
		RelationDecision{
			AssertionID:   assertion.ID,
			Outcome:       DecisionAccepted,
			Reason:        "accepted explicit " + string(relationKind),
			PolicyVersion: "work-family/v1",
			DecidedAt:     decidedAt,
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationDecision(%s) error = %v", relationKind, err)
	}
	return decision
}

func TestPostgresStoreRejectsRelationSelfLoop(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	workID := fixture.insertWork(
		t,
		"openalex:W41007",
		"Self-loop Work",
		time.Now().UTC().Add(-time.Hour),
	)
	sourceRecordID := fixture.insertSourceRecord(
		t,
		workID,
		"crossref",
		"self-loop-41007",
	)

	_, err = store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         workID,
			ObjectWorkID:          workID,
			RelationKind:          RelationIsVersionOf,
			EvidenceKind:          EvidenceExplicitSourceRelation,
			SubjectSourceRecordID: sourceRecordID,
			SubjectSourcePath:     "message.relation.is-version-of",
			AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if !errors.Is(err, ErrRelationSelfLoop) {
		t.Fatalf(
			"RecordRelationAssertion(self-loop) error = %v, want ErrRelationSelfLoop",
			err,
		)
	}

	_, err = fixture.Pool.Exec(t.Context(), `
		INSERT INTO work_relation_assertions (
			subject_work_id,
			object_work_id,
			relation_kind,
			evidence_kind,
			subject_source_record_id,
			subject_source_path,
			asserted_at
		) VALUES (
			$1,
			$1,
			'is_version_of',
			'explicit_source_relation',
			$2,
			'message.relation.is-version-of',
			now()
		)
	`, workID, sourceRecordID)
	assertWorkFamilyPostgresCode(t, err, "23514")
}

func TestRejectedDecisionDoesNotMergeFamilies(t *testing.T) {
	fixture := openWorkFamilyPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	subjectWorkID := fixture.insertWork(
		t,
		"openalex:W41008",
		"Rejected relation subject",
		time.Now().UTC().Add(-2*time.Hour),
	)
	objectWorkID := fixture.insertWork(
		t,
		"openalex:W41009",
		"Rejected relation object",
		time.Now().UTC().Add(-time.Hour),
	)
	sourceRecordID := fixture.insertSourceRecord(
		t,
		subjectWorkID,
		"crossref",
		"rejected-relation-41008",
	)
	assertion, err := store.RecordRelationAssertion(
		t.Context(),
		RelationAssertion{
			SubjectWorkID:         subjectWorkID,
			ObjectWorkID:          objectWorkID,
			RelationKind:          RelationIsPreprintOf,
			EvidenceKind:          EvidenceExplicitSourceRelation,
			SubjectSourceRecordID: sourceRecordID,
			SubjectSourcePath:     "message.relation.is-preprint-of",
			AssertedAt:            time.Now().UTC().Truncate(time.Microsecond),
		},
	)
	if err != nil {
		t.Fatalf("RecordRelationAssertion() error = %v", err)
	}
	if _, err := store.RecordRelationDecision(
		t.Context(),
		RelationDecision{
			AssertionID:   assertion.ID,
			Outcome:       DecisionRejected,
			Reason:        "source relation failed policy review",
			PolicyVersion: "work-family/v1",
			DecidedAt:     time.Now().UTC().Truncate(time.Microsecond),
		},
	); err != nil {
		t.Fatalf("RecordRelationDecision(rejected) error = %v", err)
	}

	subjectMembership, _, err := store.ActiveMembership(
		t.Context(),
		subjectWorkID,
	)
	if err != nil {
		t.Fatalf("ActiveMembership(subject) error = %v", err)
	}
	objectMembership, _, err := store.ActiveMembership(
		t.Context(),
		objectWorkID,
	)
	if err != nil {
		t.Fatalf("ActiveMembership(object) error = %v", err)
	}
	if subjectMembership.FamilyID == objectMembership.FamilyID {
		t.Fatal("rejected relation merged Work families")
	}
}
