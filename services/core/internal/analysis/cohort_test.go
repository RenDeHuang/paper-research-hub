package analysis

import (
	"testing"

	"github.com/google/uuid"
)

func TestCohortRevisionIsDeterministicAndSensitiveToBoundEvidence(t *testing.T) {
	t.Parallel()

	firstWork := CohortWorkRevisionFact{
		WorkID:                uuid.MustParse("00000000-0000-0000-0000-000000000101"),
		VenueID:               uuid.MustParse("00000000-0000-0000-0000-000000000201"),
		EligibilityDecisionID: uuid.MustParse("00000000-0000-0000-0000-000000000301"),
		SourceAssertionIDs: []uuid.UUID{
			uuid.MustParse("00000000-0000-0000-0000-000000000402"),
			uuid.MustParse("00000000-0000-0000-0000-000000000401"),
		},
	}
	secondWork := CohortWorkRevisionFact{
		WorkID:                uuid.MustParse("00000000-0000-0000-0000-000000000102"),
		VenueID:               uuid.MustParse("00000000-0000-0000-0000-000000000202"),
		EligibilityDecisionID: uuid.MustParse("00000000-0000-0000-0000-000000000302"),
		SourceAssertionIDs: []uuid.UUID{
			uuid.MustParse("00000000-0000-0000-0000-000000000403"),
		},
	}

	revision, err := ComputeCohortRevision([]CohortWorkRevisionFact{
		firstWork,
		secondWork,
	})
	if err != nil {
		t.Fatalf("ComputeCohortRevision() error = %v", err)
	}
	reordered, err := ComputeCohortRevision([]CohortWorkRevisionFact{
		secondWork,
		{
			WorkID:                firstWork.WorkID,
			VenueID:               firstWork.VenueID,
			EligibilityDecisionID: firstWork.EligibilityDecisionID,
			SourceAssertionIDs: []uuid.UUID{
				firstWork.SourceAssertionIDs[1],
				firstWork.SourceAssertionIDs[0],
			},
		},
	})
	if err != nil {
		t.Fatalf("ComputeCohortRevision(reordered) error = %v", err)
	}
	if revision != reordered || len(revision) != 64 {
		t.Fatalf(
			"deterministic revisions = %q and %q",
			revision,
			reordered,
		)
	}

	changed := firstWork
	changed.EligibilityDecisionID = uuid.MustParse(
		"00000000-0000-0000-0000-000000000399",
	)
	changedRevision, err := ComputeCohortRevision([]CohortWorkRevisionFact{
		changed,
		secondWork,
	})
	if err != nil {
		t.Fatalf("ComputeCohortRevision(changed) error = %v", err)
	}
	if changedRevision == revision {
		t.Fatal("cohort revision ignored an eligibility evidence change")
	}
}

func TestCohortRevisionRejectsIncompleteOrDuplicateFacts(t *testing.T) {
	t.Parallel()

	valid := CohortWorkRevisionFact{
		WorkID:                uuid.MustParse("00000000-0000-0000-0000-000000000101"),
		VenueID:               uuid.MustParse("00000000-0000-0000-0000-000000000201"),
		EligibilityDecisionID: uuid.MustParse("00000000-0000-0000-0000-000000000301"),
		SourceAssertionIDs: []uuid.UUID{
			uuid.MustParse("00000000-0000-0000-0000-000000000401"),
		},
	}
	tests := []struct {
		name  string
		facts []CohortWorkRevisionFact
	}{
		{name: "empty", facts: nil},
		{
			name: "nil work",
			facts: []CohortWorkRevisionFact{{
				VenueID:               valid.VenueID,
				EligibilityDecisionID: valid.EligibilityDecisionID,
				SourceAssertionIDs:    valid.SourceAssertionIDs,
			}},
		},
		{
			name: "missing source assertion",
			facts: []CohortWorkRevisionFact{{
				WorkID:                valid.WorkID,
				VenueID:               valid.VenueID,
				EligibilityDecisionID: valid.EligibilityDecisionID,
			}},
		},
		{
			name:  "duplicate work",
			facts: []CohortWorkRevisionFact{valid, valid},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := ComputeCohortRevision(test.facts); err == nil {
				t.Fatal("ComputeCohortRevision() error = nil")
			}
		})
	}
}
