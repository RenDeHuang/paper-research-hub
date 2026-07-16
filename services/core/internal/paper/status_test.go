package paper

import "testing"

func TestNewWorkStatusAcceptsOnlyDesignedStatuses(t *testing.T) {
	t.Parallel()

	valid := []WorkStatus{
		WorkStatusActive,
		WorkStatusWithdrawn,
		WorkStatusRetracted,
		WorkStatusRejected,
		WorkStatusSuperseded,
	}
	for _, want := range valid {
		want := want
		t.Run(string(want), func(t *testing.T) {
			t.Parallel()

			got, err := NewWorkStatus(string(want))
			if err != nil {
				t.Fatalf("NewWorkStatus(%q) error = %v", want, err)
			}
			if got != want {
				t.Fatalf("NewWorkStatus(%q) = %q, want %q", want, got, want)
			}
			if !got.Valid() {
				t.Fatalf("WorkStatus.Valid() = false for %q", got)
			}
		})
	}

	for _, raw := range []string{
		"",
		"ACTIVE",
		" active ",
		"corrected",
		"expression_of_concern",
		"desk_rejected",
		"published",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := NewWorkStatus(raw); err == nil {
				t.Fatalf("NewWorkStatus(%q) = %q, want error", raw, got)
			}
		})
	}
}

func TestWorkStatusRankability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status WorkStatus
		want   bool
	}{
		{status: WorkStatusActive, want: true},
		{status: WorkStatusWithdrawn, want: false},
		{status: WorkStatusRetracted, want: false},
		{status: WorkStatusRejected, want: false},
		{status: WorkStatusSuperseded, want: false},
		{status: WorkStatus("unknown"), want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()

			if got := tt.status.Rankable(); got != tt.want {
				t.Fatalf("WorkStatus(%q).Rankable() = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestWorkStatusTransitionsAreMonotonic(t *testing.T) {
	t.Parallel()

	terminal := []WorkStatus{
		WorkStatusWithdrawn,
		WorkStatusRetracted,
		WorkStatusRejected,
		WorkStatusSuperseded,
	}

	if !WorkStatusActive.CanTransitionTo(WorkStatusActive) {
		t.Fatal("active status must allow an idempotent transition")
	}
	for _, next := range terminal {
		if !WorkStatusActive.CanTransitionTo(next) {
			t.Errorf("active status cannot transition to %q", next)
		}
	}

	for _, current := range terminal {
		current := current
		t.Run(string(current), func(t *testing.T) {
			t.Parallel()

			if !current.CanTransitionTo(current) {
				t.Fatalf("%q must allow an idempotent transition", current)
			}
			for _, next := range append([]WorkStatus{WorkStatusActive}, terminal...) {
				if next == current {
					continue
				}
				if current.CanTransitionTo(next) {
					t.Errorf("%q unexpectedly transitions to %q", current, next)
				}
			}
		})
	}

	if WorkStatus("invalid").CanTransitionTo(WorkStatusActive) {
		t.Fatal("invalid current status can transition")
	}
	if WorkStatusActive.CanTransitionTo(WorkStatus("invalid")) {
		t.Fatal("active status can transition to invalid status")
	}
}

func TestWorkStatusTransitionReturnsValidatedNextStatus(t *testing.T) {
	t.Parallel()

	got, err := WorkStatusActive.TransitionTo(WorkStatusRetracted)
	if err != nil {
		t.Fatalf("TransitionTo() error = %v", err)
	}
	if got != WorkStatusRetracted {
		t.Fatalf("TransitionTo() = %q, want %q", got, WorkStatusRetracted)
	}

	if got, err := WorkStatusRetracted.TransitionTo(WorkStatusActive); err == nil {
		t.Fatalf("TransitionTo() = %q, want terminal transition error", got)
	}
}
