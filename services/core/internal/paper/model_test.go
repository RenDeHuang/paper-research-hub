package paper

import (
	"testing"
	"time"
)

func TestNewWorkRequiresCanonicalIdentityAndTitle(t *testing.T) {
	t.Parallel()

	identifier, err := NewIdentifier(SchemeDOI, "https://doi.org/10.1000/ABC")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}

	work, err := NewWork(identifier, "  Planning Agents with Tool Use  ")
	if err != nil {
		t.Fatalf("NewWork() error = %v", err)
	}
	if work.Identity != identifier {
		t.Fatalf("Work.Identity = %#v, want %#v", work.Identity, identifier)
	}
	if work.CanonicalKey != "doi:10.1000/abc" {
		t.Fatalf("Work.CanonicalKey = %q, want %q", work.CanonicalKey, "doi:10.1000/abc")
	}
	if work.Title != "Planning Agents with Tool Use" {
		t.Fatalf("Work.Title = %q, want trimmed title", work.Title)
	}
	if work.Status != WorkStatusActive {
		t.Fatalf("Work.Status = %q, want %q", work.Status, WorkStatusActive)
	}
	if !work.Rankable() {
		t.Fatal("new active work is not rankable")
	}

	publishedAt := time.Date(2026, time.July, 16, 8, 30, 0, 0, time.UTC)
	work.Abstract = "Abstract"
	work.PublishedAt = &publishedAt
	work.VenueID = "venue-1"
	if work.Abstract != "Abstract" || work.PublishedAt != &publishedAt || work.VenueID != "venue-1" {
		t.Fatal("Work does not preserve designed model fields")
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
		{
			name:       "manually invalid identity",
			identifier: Identifier{Scheme: SchemeDOI, Value: "not-a-doi"},
			title:      "Valid title",
		},
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

func TestNewWorkAcceptsValidatedPersistedStatus(t *testing.T) {
	t.Parallel()

	identifier, err := NewIdentifier(SchemeOpenReview, "Forum_AbC123")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}

	work, err := NewWork(identifier, "Rejected submission", WorkStatusRejected)
	if err != nil {
		t.Fatalf("NewWork() error = %v", err)
	}
	if work.Status != WorkStatusRejected {
		t.Fatalf("Work.Status = %q, want %q", work.Status, WorkStatusRejected)
	}
	if work.Rankable() {
		t.Fatal("rejected work is rankable")
	}

	if got, err := NewWork(identifier, "Invalid status", WorkStatus("invented")); err == nil {
		t.Fatalf("NewWork() = %#v, want invalid status error", got)
	}
	if got, err := NewWork(
		identifier,
		"Too many statuses",
		WorkStatusActive,
		WorkStatusWithdrawn,
	); err == nil {
		t.Fatalf("NewWork() = %#v, want too many statuses error", got)
	}
}

func TestWorkTransitionUpdatesStatusWithoutAllowingReactivation(t *testing.T) {
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
	if work.Status != WorkStatusWithdrawn {
		t.Fatalf("Work.Status = %q, want %q", work.Status, WorkStatusWithdrawn)
	}
	if work.Rankable() {
		t.Fatal("withdrawn work is rankable")
	}

	if err := work.TransitionTo(WorkStatusActive); err == nil {
		t.Fatal("Work.TransitionTo(active) reactivated a withdrawn work")
	}
	if work.Status != WorkStatusWithdrawn {
		t.Fatalf("failed transition mutated Work.Status to %q", work.Status)
	}
}
