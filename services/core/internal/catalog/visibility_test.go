package catalog

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

func TestPubliclyVisibleRequiresAcceptedAdmissionStableIdentityAndOfficialLink(
	t *testing.T,
) {
	t.Parallel()

	input := visibleJournalInput()
	got, err := EvaluateVisibility(input)
	if err != nil {
		t.Fatalf("EvaluateVisibility() error = %v", err)
	}
	if !got.PubliclyVisible || got.AnalysisReady || len(got.Reasons) != 0 {
		t.Fatalf("visibility = %#v, want public fact without cold analysis", got)
	}

	tests := []struct {
		name   string
		mutate func(*VisibilityInput)
		reason VisibilityReason
	}{
		{
			name: "admission rejected",
			mutate: func(value *VisibilityInput) {
				value.Admission.Decision = scope.AdmissionRejected
				value.Admission.Reason =
					scope.AdmissionReasonJournalQ1NotAccepted
			},
			reason: VisibilityReasonAdmissionRejected,
		},
		{
			name: "stable identity missing",
			mutate: func(value *VisibilityInput) {
				value.HasStableIdentity = false
			},
			reason: VisibilityReasonStableIdentityMissing,
		},
		{
			name: "work inactive",
			mutate: func(value *VisibilityInput) {
				value.WorkActive = false
			},
			reason: VisibilityReasonWorkInactive,
		},
		{
			name: "official link missing",
			mutate: func(value *VisibilityInput) {
				value.OfficialLink = nil
			},
			reason: VisibilityReasonOfficialLinkMissing,
		},
		{
			name: "official link expired",
			mutate: func(value *VisibilityInput) {
				value.OfficialLink.ExpiresAt =
					value.EvaluatedAt.Add(-time.Microsecond)
			},
			reason: VisibilityReasonOfficialLinkExpired,
		},
		{
			name: "terminal lifecycle",
			mutate: func(value *VisibilityInput) {
				value.Lifecycle = scope.LifecycleStateWithdrawn
			},
			reason: VisibilityReasonLifecycleIneligible,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			candidate := input
			link := *input.OfficialLink
			candidate.OfficialLink = &link
			test.mutate(&candidate)
			got, err := EvaluateVisibility(candidate)
			if err != nil {
				t.Fatalf("EvaluateVisibility() error = %v", err)
			}
			if got.PubliclyVisible || got.AnalysisReady ||
				!slices.Contains(got.Reasons, test.reason) {
				t.Fatalf(
					"visibility = %#v, want non-public reason %q",
					got,
					test.reason,
				)
			}
		})
	}
}

func TestVisibilityRejectsMalformedAcceptedAdmission(t *testing.T) {
	t.Parallel()

	input := visibleJournalInput()
	input.Admission.Reason = scope.AdmissionReasonJournalQ1NotAccepted
	if _, err := EvaluateVisibility(input); err == nil {
		t.Fatal("EvaluateVisibility() accepted malformed accepted admission")
	}
}

func TestVisibilityTreatsMissingAdmissionAsRejectedState(t *testing.T) {
	t.Parallel()

	input := visibleJournalInput()
	input.Admission = scope.AdmissionDecision{
		WorkID:                 input.WorkID.String(),
		Decision:               scope.AdmissionMissing,
		Reason:                 scope.AdmissionReasonChannelUnresolved,
		AdmissionPolicyVersion: scope.ChannelAdmissionPolicyVersion,
		DomainRegistryVersion:  scope.ResearchDomainRegistryVersion,
		DecidedAt:              input.EvaluatedAt,
	}
	got, err := EvaluateVisibility(input)
	if err != nil {
		t.Fatalf("EvaluateVisibility() error = %v", err)
	}
	if got.PubliclyVisible ||
		!slices.Contains(got.Reasons, VisibilityReasonAdmissionRejected) {
		t.Fatalf(
			"visibility = %#v, want admission_rejected state",
			got,
		)
	}
}

func TestVisibilityOfficialLinkUsesEvaluatedAtHalfOpenInterval(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		verifiedAt  time.Time
		projectedAt time.Time
		expiresAt   time.Time
		wantPublic  bool
		wantReason  VisibilityReason
	}{
		{
			name:        "verified and projected at cutoff are valid",
			verifiedAt:  now,
			projectedAt: now,
			expiresAt:   now.Add(time.Hour),
			wantPublic:  true,
		},
		{
			name:        "expires at cutoff is invalid",
			verifiedAt:  now.Add(-time.Hour),
			projectedAt: now.Add(-time.Hour),
			expiresAt:   now,
			wantReason:  VisibilityReasonOfficialLinkExpired,
		},
		{
			name:        "verified after cutoff is invalid",
			verifiedAt:  now.Add(time.Hour),
			projectedAt: now.Add(time.Hour),
			expiresAt:   now.Add(2 * time.Hour),
			wantReason:  VisibilityReasonOfficialLinkInvalid,
		},
		{
			name:        "projected after cutoff is invalid",
			verifiedAt:  now.Add(-time.Hour),
			projectedAt: now.Add(time.Hour),
			expiresAt:   now.Add(2 * time.Hour),
			wantReason:  VisibilityReasonOfficialLinkInvalid,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := visibleJournalInput()
			input.EvaluatedAt = now
			input.OfficialLink.VerifiedAt = test.verifiedAt
			input.OfficialLink.ProjectedAt = test.projectedAt
			input.OfficialLink.ExpiresAt = test.expiresAt
			got, err := EvaluateVisibility(input)
			if err != nil {
				t.Fatalf("EvaluateVisibility() error = %v", err)
			}
			if got.PubliclyVisible != test.wantPublic {
				t.Fatalf(
					"PubliclyVisible = %v, want %v; visibility = %#v",
					got.PubliclyVisible,
					test.wantPublic,
					got,
				)
			}
			if test.wantReason != "" &&
				!slices.Contains(got.Reasons, test.wantReason) {
				t.Fatalf(
					"visibility reasons = %#v, want %q",
					got.Reasons,
					test.wantReason,
				)
			}
		})
	}
}

func TestAnalysisReadinessDoesNotControlPublicFacts(t *testing.T) {
	t.Parallel()

	input := visibleJournalInput()
	input.AnalysisCutoff = visibilityTimePointer(
		time.Date(2026, time.July, 18, 0, 0, 0, 0, time.UTC),
	)
	input.CanonicalChannelEventAt = visibilityTimePointer(
		time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC),
	)
	input.DomainClassified = true
	input.RequiredTaxonomyClassified = true
	input.AbstractRouteSucceeded = true

	got, err := EvaluateVisibility(input)
	if err != nil {
		t.Fatalf("EvaluateVisibility() error = %v", err)
	}
	if !got.PubliclyVisible || !got.AnalysisReady ||
		got.AnalysisCutoff == nil ||
		!got.AnalysisCutoff.Equal(*input.AnalysisCutoff) {
		t.Fatalf("visibility = %#v, want public and analysis-ready", got)
	}

	for _, test := range []struct {
		name   string
		mutate func(*VisibilityInput)
		reason VisibilityReason
	}{
		{
			name: "domain not classified",
			mutate: func(value *VisibilityInput) {
				value.DomainClassified = false
			},
			reason: VisibilityReasonDomainClassificationMissing,
		},
		{
			name: "taxonomy incomplete",
			mutate: func(value *VisibilityInput) {
				value.RequiredTaxonomyClassified = false
			},
			reason: VisibilityReasonTaxonomyMissing,
		},
		{
			name: "abstract analysis failed",
			mutate: func(value *VisibilityInput) {
				value.AbstractRouteSucceeded = false
			},
			reason: VisibilityReasonAbstractRouteMissing,
		},
		{
			name: "decisive source conflict",
			mutate: func(value *VisibilityInput) {
				value.DecisiveSourceConflict = true
			},
			reason: VisibilityReasonSourceConflict,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			candidate := input
			link := *input.OfficialLink
			candidate.OfficialLink = &link
			test.mutate(&candidate)
			got, err := EvaluateVisibility(candidate)
			if err != nil {
				t.Fatalf("EvaluateVisibility() error = %v", err)
			}
			if !got.PubliclyVisible || got.AnalysisReady ||
				!slices.Contains(got.Reasons, test.reason) {
				t.Fatalf(
					"visibility = %#v, want public-only reason %q",
					got,
					test.reason,
				)
			}
		})
	}
}

func TestAnalysisReadinessRequiresCanonicalChannelEventAtOrBeforeCutoff(
	t *testing.T,
) {
	t.Parallel()

	cutoff := time.Date(2026, time.July, 18, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		eventAt    *time.Time
		wantReady  bool
		wantReason VisibilityReason
	}{
		{
			name:      "event exactly at cutoff",
			eventAt:   visibilityTimePointer(cutoff),
			wantReady: true,
		},
		{
			name:       "event missing",
			wantReason: VisibilityReasonCanonicalChannelEventMissing,
		},
		{
			name: "event after cutoff",
			eventAt: visibilityTimePointer(
				cutoff.Add(time.Nanosecond),
			),
			wantReason: VisibilityReasonCanonicalChannelEventAfterCutoff,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := visibleJournalInput()
			input.AnalysisCutoff = &cutoff
			input.CanonicalChannelEventAt = test.eventAt
			input.DomainClassified = true
			input.RequiredTaxonomyClassified = true
			input.AbstractRouteSucceeded = true
			got, err := EvaluateVisibility(input)
			if err != nil {
				t.Fatalf("EvaluateVisibility() error = %v", err)
			}
			if got.AnalysisReady != test.wantReady {
				t.Fatalf(
					"AnalysisReady = %v, want %v; visibility = %#v",
					got.AnalysisReady,
					test.wantReady,
					got,
				)
			}
			if test.wantReason != "" &&
				!slices.Contains(got.Reasons, test.wantReason) {
				t.Fatalf(
					"visibility reasons = %#v, want %q",
					got.Reasons,
					test.wantReason,
				)
			}
		})
	}
}

func TestChannelSpecificVisibilityRequiresExactLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		channel   scope.ContentChannel
		lifecycle scope.LifecycleState
	}{
		{
			name:      "journal published",
			channel:   scope.ContentChannelJournalPublished,
			lifecycle: scope.LifecycleStatePublished,
		},
		{
			name:      "accepted early",
			channel:   scope.ContentChannelAcceptedEarly,
			lifecycle: scope.LifecycleStateAcceptedEarly,
		},
		{
			name:      "preprint",
			channel:   scope.ContentChannelPreprint,
			lifecycle: scope.LifecycleStatePreprintActive,
		},
		{
			name:      "conference proceeding",
			channel:   scope.ContentChannelConferenceProceeding,
			lifecycle: scope.LifecycleStateConferencePublished,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := visibleJournalInput()
			input.Admission.Channel = test.channel
			switch test.channel {
			case scope.ContentChannelJournalPublished,
				scope.ContentChannelAcceptedEarly:
				input.Admission.JournalPolicyVersion =
					scope.JournalAllQ1PolicyVersion
				input.Admission.ChannelRegistryVersion = ""
			case scope.ContentChannelPreprint:
				input.Admission.JournalPolicyVersion = ""
				input.Admission.ChannelRegistryVersion =
					scope.PreprintRegistryVersion
			case scope.ContentChannelConferenceProceeding:
				input.Admission.JournalPolicyVersion = ""
				input.Admission.ChannelRegistryVersion =
					scope.ConferenceRegistryVersion
			}
			input.Lifecycle = test.lifecycle
			got, err := EvaluateVisibility(input)
			if err != nil {
				t.Fatalf("EvaluateVisibility() error = %v", err)
			}
			if !got.PubliclyVisible {
				t.Fatalf("visibility = %#v, want public", got)
			}

			input.Lifecycle = scope.LifecycleStateConflict
			got, err = EvaluateVisibility(input)
			if err != nil {
				t.Fatalf("EvaluateVisibility(conflict) error = %v", err)
			}
			if got.PubliclyVisible ||
				!slices.Contains(
					got.Reasons,
					VisibilityReasonLifecycleIneligible,
				) {
				t.Fatalf("conflicting lifecycle visibility = %#v", got)
			}
		})
	}
}

func TestPublicVisibilityAcceptsVerifiedHTTPSOfficialLinkWithQueryParameters(
	t *testing.T,
) {
	t.Parallel()

	input := visibleJournalInput()
	input.OfficialLink.URL =
		"https://publisher.example.test/article?download=1"

	got, err := EvaluateVisibility(input)
	if err != nil {
		t.Fatalf("EvaluateVisibility() error = %v", err)
	}
	if !got.PubliclyVisible ||
		slices.Contains(got.Reasons, VisibilityReasonOfficialLinkInvalid) {
		t.Fatalf(
			"visibility = %#v, want verified HTTPS official URL with query parameters to remain public",
			got,
		)
	}
}

func visibleJournalInput() VisibilityInput {
	now := time.Date(
		2026,
		time.July,
		18,
		8,
		0,
		0,
		0,
		time.UTC,
	)
	workID := uuid.MustParse("00000000-0000-0000-0000-000000004001")
	return VisibilityInput{
		WorkID: workID,
		Admission: scope.AdmissionDecision{
			WorkID:                 workID.String(),
			Channel:                scope.ContentChannelJournalPublished,
			Decision:               scope.AdmissionAccepted,
			Reason:                 scope.AdmissionReasonEligible,
			AdmissionPolicyVersion: scope.ChannelAdmissionPolicyVersion,
			DomainRegistryVersion:  scope.ResearchDomainRegistryVersion,
			JournalPolicyVersion:   scope.JournalAllQ1PolicyVersion,
			SourcePaths:            []string{"$.venue_assessment"},
			DecidedAt:              now,
		},
		HasStableIdentity: true,
		WorkActive:        true,
		Lifecycle:         scope.LifecycleStatePublished,
		OfficialLink: &VisibilityOfficialLink{
			VerificationID: uuid.MustParse(
				"00000000-0000-0000-0000-000000004002",
			),
			URL:             "https://doi.org/10.1000/visibility",
			VerifierVersion: "official-url/v1",
			VerifiedAt:      now.Add(-time.Hour),
			ProjectedAt:     now.Add(-time.Hour),
			ExpiresAt:       now.Add(24 * time.Hour),
		},
		EvaluatedAt:   now,
		PolicyVersion: VisibilityPolicyVersion,
	}
}

func visibilityTimePointer(value time.Time) *time.Time {
	return &value
}
