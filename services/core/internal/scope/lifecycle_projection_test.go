package scope

import (
	"errors"
	"testing"
	"time"
)

func TestLifecycleProjectionRequiresChannelSpecificExactFacts(t *testing.T) {
	t.Parallel()

	assertedAt := time.Date(2026, time.July, 18, 2, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		channel ContentChannel
		fact    LifecycleFact
		want    LifecycleState
	}{
		{
			name:    "journal published",
			channel: ContentChannelJournalPublished,
			fact:    LifecycleFactPublished,
			want:    LifecycleStatePublished,
		},
		{
			name:    "accepted",
			channel: ContentChannelAcceptedEarly,
			fact:    LifecycleFactAccepted,
			want:    LifecycleStateAcceptedEarly,
		},
		{
			name:    "online first",
			channel: ContentChannelAcceptedEarly,
			fact:    LifecycleFactOnlineFirst,
			want:    LifecycleStateAcceptedEarly,
		},
		{
			name:    "preprint posted",
			channel: ContentChannelPreprint,
			fact:    LifecycleFactPosted,
			want:    LifecycleStatePreprintActive,
		},
		{
			name:    "conference published",
			channel: ContentChannelConferenceProceeding,
			fact:    LifecycleFactProceedingPublished,
			want:    LifecycleStateConferencePublished,
		},
	}
	for index, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertion := lifecycleAssertionFixture(index, test.fact, assertedAt)
			projection, err := ProjectLifecycle(
				test.channel,
				[]LifecycleAssertion{assertion},
				"lifecycle-projection/v1",
				assertedAt.Add(time.Minute),
			)
			if err != nil {
				t.Fatalf("ProjectLifecycle() error = %v", err)
			}
			if projection.State != test.want ||
				projection.PolicyVersion != "lifecycle-projection/v1" ||
				projection.SourcePaths[0] != assertion.SourcePath {
				t.Fatalf("projection = %#v", projection)
			}
		})
	}
}

func TestLifecycleProjectionDoesNotRepairOrInferFacts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 18, 2, 5, 0, 0, time.UTC)
	invalid := lifecycleAssertionFixture(1, "online-first", now)
	if _, err := ProjectLifecycle(
		ContentChannelAcceptedEarly,
		[]LifecycleAssertion{invalid},
		"lifecycle-projection/v1",
		now,
	); !errors.Is(err, ErrInvalidLifecycleFact) {
		t.Fatalf("ProjectLifecycle(repaired fact) error = %v", err)
	}

	wrongChannel := lifecycleAssertionFixture(2, LifecycleFactPublished, now)
	if _, err := ProjectLifecycle(
		ContentChannelPreprint,
		[]LifecycleAssertion{wrongChannel},
		"lifecycle-projection/v1",
		now,
	); err == nil {
		t.Fatal("ProjectLifecycle(channel mismatch) error = nil")
	}

	active := lifecycleAssertionFixture(3, LifecycleFactPosted, now)
	withdrawn := lifecycleAssertionFixture(4, LifecycleFactWithdrawn, now.Add(time.Second))
	projection, err := ProjectLifecycle(
		ContentChannelPreprint,
		[]LifecycleAssertion{active, withdrawn},
		"lifecycle-projection/v1",
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("ProjectLifecycle(withdrawn) error = %v", err)
	}
	if projection.State != LifecycleStateWithdrawn {
		t.Fatalf("withdrawn projection = %#v", projection)
	}

	conflicting := lifecycleAssertionFixture(
		5,
		LifecycleFactProceedingPublished,
		now.Add(2*time.Second),
	)
	projection, err = ProjectLifecycle(
		ContentChannelPreprint,
		[]LifecycleAssertion{active, conflicting},
		"lifecycle-projection/v1",
		now.Add(time.Minute),
	)
	if err == nil || projection.State != "" {
		t.Fatalf("ProjectLifecycle(impossible fact) = %#v, %v", projection, err)
	}
}

func lifecycleAssertionFixture(
	index int,
	fact LifecycleFact,
	assertedAt time.Time,
) LifecycleAssertion {
	return LifecycleAssertion{
		ID:                    formatTestUUID(300 + index),
		ProjectionAssertionID: formatTestUUID(400 + index),
		NormalizedAssertionID: formatTestUUID(500 + index),
		SourceRecordID:        formatTestUUID(600 + index),
		WorkID:                "00000000-0000-0000-0000-000000000301",
		Fact:                  fact,
		SourcePath:            "$.lifecycle",
		AssertedAt:            assertedAt,
	}
}
