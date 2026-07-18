package scope

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

var ErrInvalidLifecycleFact = errors.New("invalid lifecycle fact")

type LifecycleFact string

const (
	LifecycleFactPublished           LifecycleFact = "published"
	LifecycleFactAccepted            LifecycleFact = "accepted"
	LifecycleFactPending             LifecycleFact = "pending"
	LifecycleFactAheadOfPrint        LifecycleFact = "ahead_of_print"
	LifecycleFactOnlineFirst         LifecycleFact = "online_first"
	LifecycleFactPosted              LifecycleFact = "posted"
	LifecycleFactWithdrawn           LifecycleFact = "withdrawn"
	LifecycleFactProceedingPublished LifecycleFact = "proceeding_published"
)

func (fact LifecycleFact) Valid() bool {
	switch fact {
	case LifecycleFactPublished,
		LifecycleFactAccepted,
		LifecycleFactPending,
		LifecycleFactAheadOfPrint,
		LifecycleFactOnlineFirst,
		LifecycleFactPosted,
		LifecycleFactWithdrawn,
		LifecycleFactProceedingPublished:
		return true
	default:
		return false
	}
}

type LifecycleState string

const (
	LifecycleStatePublished           LifecycleState = "published"
	LifecycleStateAcceptedEarly       LifecycleState = "accepted_early"
	LifecycleStatePreprintActive      LifecycleState = "preprint_active"
	LifecycleStateWithdrawn           LifecycleState = "withdrawn"
	LifecycleStateConferencePublished LifecycleState = "conference_published"
	LifecycleStateMissing             LifecycleState = "missing"
	LifecycleStateConflict            LifecycleState = "conflict"
)

type LifecycleAssertion struct {
	ID                    string
	ProjectionAssertionID string
	NormalizedAssertionID string
	SourceRecordID        string
	WorkID                string
	Fact                  LifecycleFact
	SourcePath            string
	AssertedAt            time.Time
}

func (assertion LifecycleAssertion) Validate() error {
	for field, value := range map[string]string{
		"ID":                      assertion.ID,
		"projection assertion ID": assertion.ProjectionAssertionID,
		"normalized assertion ID": assertion.NormalizedAssertionID,
		"source record ID":        assertion.SourceRecordID,
		"Work ID":                 assertion.WorkID,
		"source path":             assertion.SourcePath,
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("lifecycle assertion requires exact %s", field)
		}
	}
	if !assertion.Fact.Valid() {
		return fmt.Errorf("%w %q", ErrInvalidLifecycleFact, assertion.Fact)
	}
	if assertion.AssertedAt.IsZero() {
		return errors.New("lifecycle assertion asserted_at is required")
	}
	return nil
}

type LifecycleProjection struct {
	WorkID        string
	Channel       ContentChannel
	State         LifecycleState
	PolicyVersion string
	AssertionIDs  []string
	SourcePaths   []string
	DecidedAt     time.Time
}

func (projection LifecycleProjection) Validate() error {
	if projection.WorkID == "" ||
		projection.WorkID != strings.TrimSpace(projection.WorkID) {
		return errors.New("lifecycle projection requires an exact Work ID")
	}
	if _, err := ParseContentChannel(string(projection.Channel)); err != nil {
		return err
	}
	if projection.PolicyVersion == "" ||
		projection.PolicyVersion != strings.TrimSpace(projection.PolicyVersion) {
		return errors.New("lifecycle projection requires an exact policy version")
	}
	if projection.DecidedAt.IsZero() {
		return errors.New("lifecycle projection decided_at is required")
	}
	switch projection.State {
	case LifecycleStatePublished,
		LifecycleStateAcceptedEarly,
		LifecycleStatePreprintActive,
		LifecycleStateWithdrawn,
		LifecycleStateConferencePublished:
		if len(projection.AssertionIDs) == 0 ||
			len(projection.AssertionIDs) != len(projection.SourcePaths) {
			return errors.New(
				"resolved lifecycle projection requires assertions and source paths",
			)
		}
	case LifecycleStateMissing:
		if len(projection.AssertionIDs) != 0 || len(projection.SourcePaths) != 0 {
			return errors.New("missing lifecycle projection cannot carry assertions")
		}
	case LifecycleStateConflict:
		if len(projection.AssertionIDs) < 2 ||
			len(projection.AssertionIDs) != len(projection.SourcePaths) {
			return errors.New(
				"conflicting lifecycle projection requires assertions and source paths",
			)
		}
	default:
		return fmt.Errorf("invalid lifecycle state %q", projection.State)
	}
	return nil
}

func ProjectLifecycle(
	channel ContentChannel,
	assertions []LifecycleAssertion,
	policyVersion string,
	decidedAt time.Time,
) (LifecycleProjection, error) {
	if len(assertions) == 0 {
		return LifecycleProjection{}, errors.New(
			"ProjectLifecycle requires assertions or an explicit Work ID",
		)
	}
	if _, err := ParseContentChannel(string(channel)); err != nil {
		return LifecycleProjection{}, err
	}
	if policyVersion == "" || policyVersion != strings.TrimSpace(policyVersion) {
		return LifecycleProjection{}, errors.New(
			"lifecycle projection requires an exact policy version",
		)
	}
	if decidedAt.IsZero() {
		return LifecycleProjection{}, errors.New(
			"lifecycle projection decided_at is required",
		)
	}
	ordered := slices.Clone(assertions)
	slices.SortFunc(ordered, func(left, right LifecycleAssertion) int {
		return strings.Compare(left.ID, right.ID)
	})
	projection := LifecycleProjection{
		WorkID:        ordered[0].WorkID,
		Channel:       channel,
		PolicyVersion: policyVersion,
		DecidedAt:     decidedAt.UTC(),
	}
	states := make(map[LifecycleState]struct{})
	for index, assertion := range ordered {
		if err := assertion.Validate(); err != nil {
			return LifecycleProjection{}, fmt.Errorf(
				"lifecycle assertion %d: %w",
				index,
				err,
			)
		}
		if assertion.WorkID != projection.WorkID {
			return LifecycleProjection{}, errors.New(
				"lifecycle assertions must belong to one Work",
			)
		}
		state, err := lifecycleStateForFact(channel, assertion.Fact)
		if err != nil {
			return LifecycleProjection{}, err
		}
		states[state] = struct{}{}
		projection.AssertionIDs = append(projection.AssertionIDs, assertion.ID)
		projection.SourcePaths = append(projection.SourcePaths, assertion.SourcePath)
	}
	if _, withdrawn := states[LifecycleStateWithdrawn]; withdrawn {
		projection.State = LifecycleStateWithdrawn
	} else if len(states) == 1 {
		for state := range states {
			projection.State = state
		}
	} else {
		projection.State = LifecycleStateConflict
	}
	if err := projection.Validate(); err != nil {
		return LifecycleProjection{}, err
	}
	return projection, nil
}

func lifecycleStateForFact(
	channel ContentChannel,
	fact LifecycleFact,
) (LifecycleState, error) {
	switch channel {
	case ContentChannelJournalPublished:
		if fact == LifecycleFactPublished {
			return LifecycleStatePublished, nil
		}
	case ContentChannelAcceptedEarly:
		switch fact {
		case LifecycleFactAccepted,
			LifecycleFactPending,
			LifecycleFactAheadOfPrint,
			LifecycleFactOnlineFirst:
			return LifecycleStateAcceptedEarly, nil
		}
	case ContentChannelPreprint:
		switch fact {
		case LifecycleFactPosted:
			return LifecycleStatePreprintActive, nil
		case LifecycleFactWithdrawn:
			return LifecycleStateWithdrawn, nil
		}
	case ContentChannelConferenceProceeding:
		if fact == LifecycleFactProceedingPublished {
			return LifecycleStateConferencePublished, nil
		}
	}
	return "", fmt.Errorf(
		"lifecycle fact %q is not valid for channel %q",
		fact,
		channel,
	)
}
