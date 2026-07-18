package scope

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type ChannelDecisionStatus string

const (
	ChannelDecisionResolved ChannelDecisionStatus = "resolved"
	ChannelDecisionMissing  ChannelDecisionStatus = "missing"
	ChannelDecisionConflict ChannelDecisionStatus = "conflict"
)

type ChannelAssertion struct {
	ID                    string
	ProjectionAssertionID string
	NormalizedAssertionID string
	SourceRecordID        string
	WorkID                string
	Channel               ContentChannel
	SourcePath            string
	AssertedAt            time.Time
}

func (assertion ChannelAssertion) Validate() error {
	for field, value := range map[string]string{
		"ID":                      assertion.ID,
		"projection assertion ID": assertion.ProjectionAssertionID,
		"normalized assertion ID": assertion.NormalizedAssertionID,
		"source record ID":        assertion.SourceRecordID,
		"Work ID":                 assertion.WorkID,
		"source path":             assertion.SourcePath,
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("channel assertion requires exact %s", field)
		}
	}
	if _, err := ParseContentChannel(string(assertion.Channel)); err != nil {
		return err
	}
	if assertion.AssertedAt.IsZero() {
		return errors.New("channel assertion asserted_at is required")
	}
	return nil
}

type ChannelDecision struct {
	WorkID        string
	Status        ChannelDecisionStatus
	Channel       ContentChannel
	PolicyVersion string
	AssertionIDs  []string
	SourcePaths   []string
	DecidedAt     time.Time
}

func (decision ChannelDecision) Validate() error {
	if decision.WorkID == "" || decision.WorkID != strings.TrimSpace(decision.WorkID) {
		return errors.New("channel decision requires an exact Work ID")
	}
	if decision.PolicyVersion == "" ||
		decision.PolicyVersion != strings.TrimSpace(decision.PolicyVersion) {
		return errors.New("channel decision requires an exact policy version")
	}
	if decision.DecidedAt.IsZero() {
		return errors.New("channel decision decided_at is required")
	}
	switch decision.Status {
	case ChannelDecisionResolved:
		if _, err := ParseContentChannel(string(decision.Channel)); err != nil {
			return err
		}
		if len(decision.AssertionIDs) == 0 ||
			len(decision.AssertionIDs) != len(decision.SourcePaths) {
			return errors.New(
				"resolved channel decision requires assertion IDs and source paths",
			)
		}
	case ChannelDecisionMissing:
		if decision.Channel != "" ||
			len(decision.AssertionIDs) != 0 ||
			len(decision.SourcePaths) != 0 {
			return errors.New("missing channel decision cannot carry assertions")
		}
	case ChannelDecisionConflict:
		if decision.Channel != "" ||
			len(decision.AssertionIDs) < 2 ||
			len(decision.AssertionIDs) != len(decision.SourcePaths) {
			return errors.New(
				"conflicting channel decision requires conflicting assertions",
			)
		}
	default:
		return fmt.Errorf("invalid channel decision status %q", decision.Status)
	}
	return nil
}

func ProjectChannel(
	assertions []ChannelAssertion,
	policyVersion string,
	decidedAt time.Time,
) (ChannelDecision, error) {
	if len(assertions) == 0 {
		return ChannelDecision{}, errors.New(
			"ProjectChannel requires assertions or an explicit Work ID",
		)
	}
	return ProjectChannelForWork(
		assertions[0].WorkID,
		assertions,
		policyVersion,
		decidedAt,
	)
}

func ProjectChannelForWork(
	workID string,
	assertions []ChannelAssertion,
	policyVersion string,
	decidedAt time.Time,
) (ChannelDecision, error) {
	if workID == "" || workID != strings.TrimSpace(workID) {
		return ChannelDecision{}, errors.New(
			"channel projection requires an exact Work ID",
		)
	}
	if policyVersion == "" || policyVersion != strings.TrimSpace(policyVersion) {
		return ChannelDecision{}, errors.New(
			"channel projection requires an exact policy version",
		)
	}
	if decidedAt.IsZero() {
		return ChannelDecision{}, errors.New("channel projection decided_at is required")
	}
	ordered := slices.Clone(assertions)
	slices.SortFunc(ordered, func(left, right ChannelAssertion) int {
		return strings.Compare(left.ID, right.ID)
	})
	channels := make(map[ContentChannel]struct{})
	decision := ChannelDecision{
		WorkID:        workID,
		PolicyVersion: policyVersion,
		DecidedAt:     decidedAt.UTC(),
	}
	for index, assertion := range ordered {
		if err := assertion.Validate(); err != nil {
			return ChannelDecision{}, fmt.Errorf(
				"channel assertion %d: %w",
				index,
				err,
			)
		}
		if assertion.WorkID != workID {
			return ChannelDecision{}, fmt.Errorf(
				"channel assertion %q belongs to Work %q, not %q",
				assertion.ID,
				assertion.WorkID,
				workID,
			)
		}
		channels[assertion.Channel] = struct{}{}
		decision.AssertionIDs = append(decision.AssertionIDs, assertion.ID)
		decision.SourcePaths = append(decision.SourcePaths, assertion.SourcePath)
	}
	switch len(channels) {
	case 0:
		decision.Status = ChannelDecisionMissing
	case 1:
		decision.Status = ChannelDecisionResolved
		for channel := range channels {
			decision.Channel = channel
		}
	default:
		decision.Status = ChannelDecisionConflict
	}
	if err := decision.Validate(); err != nil {
		return ChannelDecision{}, err
	}
	return decision, nil
}
