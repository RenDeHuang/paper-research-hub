package paper

import "fmt"

type WorkStatus string

const (
	WorkStatusActive     WorkStatus = "active"
	WorkStatusWithdrawn  WorkStatus = "withdrawn"
	WorkStatusRetracted  WorkStatus = "retracted"
	WorkStatusRejected   WorkStatus = "rejected"
	WorkStatusSuperseded WorkStatus = "superseded"
)

func NewWorkStatus(raw string) (WorkStatus, error) {
	status := WorkStatus(raw)
	if !status.Valid() {
		return "", fmt.Errorf("invalid work status %q", raw)
	}
	return status, nil
}

func (status WorkStatus) Valid() bool {
	switch status {
	case WorkStatusActive,
		WorkStatusWithdrawn,
		WorkStatusRetracted,
		WorkStatusRejected,
		WorkStatusSuperseded:
		return true
	default:
		return false
	}
}

func (status WorkStatus) Rankable() bool {
	return status == WorkStatusActive
}

func (status WorkStatus) IsRankable() bool {
	return status.Rankable()
}

func (status WorkStatus) CanTransitionTo(next WorkStatus) bool {
	if !status.Valid() || !next.Valid() {
		return false
	}
	if status == next {
		return true
	}
	return status == WorkStatusActive
}

func (status WorkStatus) TransitionTo(next WorkStatus) (WorkStatus, error) {
	if !status.CanTransitionTo(next) {
		return status, fmt.Errorf("work status cannot transition from %q to %q", status, next)
	}
	return next, nil
}

func (status WorkStatus) String() string {
	return string(status)
}
