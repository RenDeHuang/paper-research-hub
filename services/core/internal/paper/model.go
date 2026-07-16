package paper

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Work struct {
	identity    Identifier
	status      WorkStatus
	title       string
	abstract    string
	publishedAt *time.Time
	venueID     string
}

type PersistedWorkState struct {
	Identity    Identifier
	Status      WorkStatus
	Title       string
	Abstract    string
	PublishedAt *time.Time
	VenueID     string
}

func NewWork(identity Identifier, title string) (Work, error) {
	return restoreWork(PersistedWorkState{
		Identity: identity,
		Status:   WorkStatusActive,
		Title:    title,
	})
}

func RestoreWork(state PersistedWorkState) (Work, error) {
	return restoreWork(state)
}

func restoreWork(state PersistedWorkState) (Work, error) {
	if !state.Identity.Valid() {
		return Work{}, errors.New("work requires a valid canonical identifier")
	}

	title := strings.TrimSpace(state.Title)
	if title == "" {
		return Work{}, errors.New("work title is required")
	}

	if !state.Status.Valid() {
		return Work{}, fmt.Errorf("invalid persisted work status %q", state.Status)
	}

	return Work{
		identity:    state.Identity,
		status:      state.Status,
		title:       title,
		abstract:    state.Abstract,
		publishedAt: cloneTime(state.PublishedAt),
		venueID:     state.VenueID,
	}, nil
}

func (work Work) Valid() bool {
	return work.identity.Valid() &&
		strings.TrimSpace(work.title) != "" &&
		work.status.Valid()
}

func (work Work) Identity() Identifier {
	return work.identity
}

func (work Work) CanonicalKey() string {
	return work.identity.CanonicalKey()
}

func (work Work) Status() WorkStatus {
	return work.status
}

func (work Work) Title() string {
	return work.title
}

func (work Work) Abstract() string {
	return work.abstract
}

func (work Work) PublishedAt() *time.Time {
	return cloneTime(work.publishedAt)
}

func (work Work) VenueID() string {
	return work.venueID
}

func (work Work) Rankable() bool {
	return work.Valid() && work.status.Rankable()
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

	status, err := work.status.TransitionTo(next)
	if err != nil {
		return err
	}
	work.status = status
	return nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
