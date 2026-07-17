package analysis

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

type CohortWorkRevisionFact struct {
	WorkID                uuid.UUID   `json:"work_id"`
	VenueID               uuid.UUID   `json:"venue_id"`
	EligibilityDecisionID uuid.UUID   `json:"eligibility_decision_id"`
	SourceAssertionIDs    []uuid.UUID `json:"source_assertion_ids"`
}

func ComputeCohortRevision(facts []CohortWorkRevisionFact) (string, error) {
	if len(facts) == 0 {
		return "", errors.New("cohort revision requires at least one Work")
	}

	normalized := make([]CohortWorkRevisionFact, len(facts))
	seenWorks := make(map[uuid.UUID]struct{}, len(facts))
	for index, fact := range facts {
		if fact.WorkID == uuid.Nil ||
			fact.VenueID == uuid.Nil ||
			fact.EligibilityDecisionID == uuid.Nil {
			return "", fmt.Errorf(
				"cohort revision Work fact %d has an incomplete identity",
				index,
			)
		}
		if _, exists := seenWorks[fact.WorkID]; exists {
			return "", fmt.Errorf(
				"cohort revision contains duplicate Work %s",
				fact.WorkID,
			)
		}
		seenWorks[fact.WorkID] = struct{}{}
		if len(fact.SourceAssertionIDs) == 0 {
			return "", fmt.Errorf(
				"cohort revision Work %s has no source assertions",
				fact.WorkID,
			)
		}

		sourceIDs := append([]uuid.UUID(nil), fact.SourceAssertionIDs...)
		sort.Slice(sourceIDs, func(left, right int) bool {
			return bytes.Compare(sourceIDs[left][:], sourceIDs[right][:]) < 0
		})
		for sourceIndex, sourceID := range sourceIDs {
			if sourceID == uuid.Nil {
				return "", fmt.Errorf(
					"cohort revision Work %s has a nil source assertion",
					fact.WorkID,
				)
			}
			if sourceIndex > 0 && sourceID == sourceIDs[sourceIndex-1] {
				return "", fmt.Errorf(
					"cohort revision Work %s has duplicate source assertion %s",
					fact.WorkID,
					sourceID,
				)
			}
		}
		normalized[index] = CohortWorkRevisionFact{
			WorkID:                fact.WorkID,
			VenueID:               fact.VenueID,
			EligibilityDecisionID: fact.EligibilityDecisionID,
			SourceAssertionIDs:    sourceIDs,
		}
	}
	sort.Slice(normalized, func(left, right int) bool {
		return bytes.Compare(
			normalized[left].WorkID[:],
			normalized[right].WorkID[:],
		) < 0
	})

	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode cohort revision facts: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}
