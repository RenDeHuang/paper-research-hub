package scope

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestChannelProjectionUsesOnlyExactImmutableAssertions(t *testing.T) {
	t.Parallel()

	assertedAt := time.Date(2026, time.July, 18, 1, 2, 3, 0, time.UTC)
	decidedAt := assertedAt.Add(time.Minute)
	assertion := ChannelAssertion{
		ID:                    "00000000-0000-0000-0000-000000000101",
		ProjectionAssertionID: "00000000-0000-0000-0000-000000000102",
		NormalizedAssertionID: "00000000-0000-0000-0000-000000000103",
		SourceRecordID:        "00000000-0000-0000-0000-000000000104",
		WorkID:                "00000000-0000-0000-0000-000000000105",
		Channel:               ContentChannelPreprint,
		SourcePath:            "$.server",
		AssertedAt:            assertedAt,
	}
	decision, err := ProjectChannel(
		[]ChannelAssertion{assertion},
		"channel-projection/v1",
		decidedAt,
	)
	if err != nil {
		t.Fatalf("ProjectChannel() error = %v", err)
	}
	if decision.Status != ChannelDecisionResolved ||
		decision.WorkID != assertion.WorkID ||
		decision.Channel != ContentChannelPreprint ||
		decision.PolicyVersion != "channel-projection/v1" ||
		!reflect.DeepEqual(decision.AssertionIDs, []string{assertion.ID}) ||
		!reflect.DeepEqual(decision.SourcePaths, []string{"$.server"}) ||
		!decision.DecidedAt.Equal(decidedAt) {
		t.Fatalf("decision = %#v", decision)
	}

	assertion.Channel = "Preprint"
	if _, err := ProjectChannel(
		[]ChannelAssertion{assertion},
		"channel-projection/v1",
		decidedAt,
	); !errors.Is(err, ErrInvalidContentChannel) {
		t.Fatalf("ProjectChannel(normalized channel) error = %v", err)
	}
}

func TestChannelProjectionReturnsMissingOrConflictWithoutHeuristics(t *testing.T) {
	t.Parallel()

	decidedAt := time.Date(2026, time.July, 18, 1, 3, 0, 0, time.UTC)
	missing, err := ProjectChannelForWork(
		"00000000-0000-0000-0000-000000000201",
		nil,
		"channel-projection/v1",
		decidedAt,
	)
	if err != nil {
		t.Fatalf("ProjectChannelForWork(missing) error = %v", err)
	}
	if missing.Status != ChannelDecisionMissing || missing.Channel != "" {
		t.Fatalf("missing decision = %#v", missing)
	}

	base := ChannelAssertion{
		ID:                    "00000000-0000-0000-0000-000000000202",
		ProjectionAssertionID: "00000000-0000-0000-0000-000000000203",
		NormalizedAssertionID: "00000000-0000-0000-0000-000000000204",
		SourceRecordID:        "00000000-0000-0000-0000-000000000205",
		WorkID:                missing.WorkID,
		Channel:               ContentChannelJournalPublished,
		SourcePath:            "$.publication_status",
		AssertedAt:            decidedAt.Add(-time.Minute),
	}
	conflicting := base
	conflicting.ID = "00000000-0000-0000-0000-000000000206"
	conflicting.Channel = ContentChannelAcceptedEarly
	conflicting.SourcePath = "$.status"

	conflict, err := ProjectChannel(
		[]ChannelAssertion{conflicting, base},
		"channel-projection/v1",
		decidedAt,
	)
	if err != nil {
		t.Fatalf("ProjectChannel(conflict) error = %v", err)
	}
	if conflict.Status != ChannelDecisionConflict ||
		conflict.Channel != "" ||
		!reflect.DeepEqual(conflict.AssertionIDs, []string{base.ID, conflicting.ID}) ||
		!reflect.DeepEqual(
			conflict.SourcePaths,
			[]string{"$.publication_status", "$.status"},
		) {
		t.Fatalf("conflict decision = %#v", conflict)
	}
}
