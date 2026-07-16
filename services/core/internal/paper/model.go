package paper

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Work struct {
	Identity     Identifier
	CanonicalKey string
	Status       WorkStatus
	Title        string
	Abstract     string
	PublishedAt  *time.Time
	VenueID      string
}

func NewWork(identity Identifier, title string, statuses ...WorkStatus) (Work, error) {
	if !identity.Valid() {
		return Work{}, errors.New("work requires a valid canonical identifier")
	}

	title = strings.TrimSpace(title)
	if title == "" {
		return Work{}, errors.New("work title is required")
	}

	if len(statuses) > 1 {
		return Work{}, errors.New("work accepts at most one initial status")
	}
	status := WorkStatusActive
	if len(statuses) == 1 {
		status = statuses[0]
	}
	if !status.Valid() {
		return Work{}, fmt.Errorf("invalid initial work status %q", status)
	}

	return Work{
		Identity:     identity,
		CanonicalKey: identity.CanonicalKey(),
		Status:       status,
		Title:        title,
	}, nil
}

func (work Work) Valid() bool {
	return work.Identity.Valid() &&
		work.CanonicalKey == work.Identity.CanonicalKey() &&
		strings.TrimSpace(work.Title) != "" &&
		work.Status.Valid()
}

func (work Work) Rankable() bool {
	return work.Valid() && work.Status.Rankable()
}

func (work Work) CanRank() bool {
	return work.Rankable()
}

func (work *Work) TransitionTo(next WorkStatus) error {
	if work == nil {
		return errors.New("cannot transition a nil work")
	}
	if !work.Valid() {
		return errors.New("cannot transition an invalid work")
	}

	status, err := work.Status.TransitionTo(next)
	if err != nil {
		return err
	}
	work.Status = status
	return nil
}
