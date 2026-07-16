package paper

import (
	"testing"
	"time"
)

func TestNewWorkCreatesOnlyActiveWork(t *testing.T) {
	t.Parallel()

	identifier, err := NewIdentifier(SchemeDOI, "https://doi.org/10.1000/ABC")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}

	work, err := NewWork(identifier, "  Planning Agents with Tool Use  ")
	if err != nil {
		t.Fatalf("NewWork() error = %v", err)
	}
	if work.Identity() != identifier {
		t.Fatalf("Work.Identity() = %#v, want %#v", work.Identity(), identifier)
	}
	if work.CanonicalKey() != "doi:10.1000/abc" {
		t.Fatalf("Work.CanonicalKey() = %q, want %q", work.CanonicalKey(), "doi:10.1000/abc")
	}
	if work.Title() != "Planning Agents with Tool Use" {
		t.Fatalf("Work.Title() = %q, want trimmed title", work.Title())
	}
	if work.Status() != WorkStatusActive {
		t.Fatalf("Work.Status() = %q, want %q", work.Status(), WorkStatusActive)
	}
	if work.Abstract() != "" || work.PublishedAt() != nil || work.VenueID() != "" {
		t.Fatal("NewWork() unexpectedly populated persisted metadata")
	}
	if !work.Rankable() {
		t.Fatal("new active work is not rankable")
	}
}

func TestNewWorkRejectsInvalidIdentityOrTitle(t *testing.T) {
	t.Parallel()

	validIdentifier, err := NewIdentifier(SchemeArXiv, "2401.01234")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}

	tests := []struct {
		name       string
		identifier Identifier
		title      string
	}{
		{name: "zero identity", identifier: Identifier{}, title: "Valid title"},
		{name: "empty title", identifier: validIdentifier, title: ""},
		{name: "blank title", identifier: validIdentifier, title: " \t\n "},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := NewWork(tt.identifier, tt.title); err == nil {
				t.Fatalf("NewWork(%#v, %q) = %#v, want error", tt.identifier, tt.title, got)
			}
		})
	}
}

func TestRestoreWorkLoadsValidatedPersistedState(t *testing.T) {
	t.Parallel()

	identifier, err := NewIdentifier(SchemeOpenReview, "Forum_AbC123")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	publishedAt := time.Date(2026, time.July, 16, 8, 30, 0, 0, time.UTC)

	for _, status := range []WorkStatus{
		WorkStatusActive,
		WorkStatusWithdrawn,
		WorkStatusRetracted,
		WorkStatusRejected,
		WorkStatusSuperseded,
	} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()

			work, err := RestoreWork(PersistedWorkState{
				Identity:    identifier,
				Status:      status,
				Title:       " Persisted paper ",
				Abstract:    "Abstract",
				PublishedAt: &publishedAt,
				VenueID:     "venue-1",
			})
			if err != nil {
				t.Fatalf("RestoreWork() error = %v", err)
			}
			if work.Status() != status {
				t.Fatalf("Work.Status() = %q, want %q", work.Status(), status)
			}
			if work.Title() != "Persisted paper" {
				t.Fatalf("Work.Title() = %q, want trimmed title", work.Title())
			}
			if work.Abstract() != "Abstract" || work.VenueID() != "venue-1" {
				t.Fatal("RestoreWork() did not preserve persisted metadata")
			}
			gotPublishedAt := work.PublishedAt()
			if gotPublishedAt == nil || !gotPublishedAt.Equal(publishedAt) {
				t.Fatalf("Work.PublishedAt() = %v, want %v", gotPublishedAt, publishedAt)
			}
			if got := work.Rankable(); got != (status == WorkStatusActive) {
				t.Fatalf("Work.Rankable() = %v for status %q", got, status)
			}
		})
	}
}

func TestRestoreWorkRejectsInvalidPersistedState(t *testing.T) {
	t.Parallel()

	identifier, err := NewIdentifier(SchemeOpenAlex, "W123")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}

	tests := []struct {
		name  string
		state PersistedWorkState
	}{
		{
			name: "zero identity",
			state: PersistedWorkState{
				Title:  "Valid title",
				Status: WorkStatusActive,
			},
		},
		{
			name: "blank title",
			state: PersistedWorkState{
				Identity: identifier,
				Title:    " \t ",
				Status:   WorkStatusActive,
			},
		},
		{
			name: "invalid status",
			state: PersistedWorkState{
				Identity: identifier,
				Title:    "Valid title",
				Status:   WorkStatus("invented"),
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := RestoreWork(tt.state); err == nil {
				t.Fatalf("RestoreWork(%#v) = %#v, want error", tt.state, got)
			}
		})
	}
}

func TestWorkTransitionUpdatesStatusMonotonically(t *testing.T) {
	t.Parallel()

	identifier, err := NewIdentifier(SchemeOpenAlex, "W123")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	work, err := NewWork(identifier, "A paper")
	if err != nil {
		t.Fatalf("NewWork() error = %v", err)
	}

	if err := work.TransitionTo(WorkStatusWithdrawn); err != nil {
		t.Fatalf("Work.TransitionTo(withdrawn) error = %v", err)
	}
	if work.Status() != WorkStatusWithdrawn {
		t.Fatalf("Work.Status() = %q, want %q", work.Status(), WorkStatusWithdrawn)
	}
	if work.Rankable() {
		t.Fatal("withdrawn work is rankable")
	}

	if err := work.TransitionTo(WorkStatusActive); err == nil {
		t.Fatal("Work.TransitionTo(active) reactivated a withdrawn work")
	}
	if work.Status() != WorkStatusWithdrawn {
		t.Fatalf("failed transition mutated Work.Status() to %q", work.Status())
	}
}
