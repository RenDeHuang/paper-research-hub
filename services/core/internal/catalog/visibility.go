package catalog

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

const VisibilityPolicyVersion = "catalog-visibility/v1"

type VisibilityReason string

const (
	VisibilityReasonAdmissionRejected                VisibilityReason = "admission_rejected"
	VisibilityReasonStableIdentityMissing            VisibilityReason = "stable_identity_missing"
	VisibilityReasonWorkInactive                     VisibilityReason = "work_inactive"
	VisibilityReasonLifecycleIneligible              VisibilityReason = "lifecycle_ineligible"
	VisibilityReasonOfficialLinkMissing              VisibilityReason = "official_link_missing"
	VisibilityReasonOfficialLinkInvalid              VisibilityReason = "official_link_invalid"
	VisibilityReasonOfficialLinkExpired              VisibilityReason = "official_link_expired"
	VisibilityReasonCanonicalChannelEventMissing     VisibilityReason = "canonical_channel_event_missing"
	VisibilityReasonCanonicalChannelEventAfterCutoff VisibilityReason = "canonical_channel_event_after_cutoff"
	VisibilityReasonDomainClassificationMissing      VisibilityReason = "domain_classification_missing"
	VisibilityReasonTaxonomyMissing                  VisibilityReason = "required_taxonomy_missing"
	VisibilityReasonAbstractRouteMissing             VisibilityReason = "abstract_route_missing"
	VisibilityReasonSourceConflict                   VisibilityReason = "decisive_source_conflict"
)

type VisibilityOfficialLink struct {
	VerificationID  uuid.UUID
	URL             string
	VerifierVersion string
	VerifiedAt      time.Time
	ProjectedAt     time.Time
	ExpiresAt       time.Time
}

type VisibilityInput struct {
	WorkID                     uuid.UUID
	Admission                  scope.AdmissionDecision
	HasStableIdentity          bool
	WorkActive                 bool
	Lifecycle                  scope.LifecycleState
	OfficialLink               *VisibilityOfficialLink
	AnalysisCutoff             *time.Time
	CanonicalChannelEventAt    *time.Time
	DomainClassified           bool
	RequiredTaxonomyClassified bool
	AbstractRouteSucceeded     bool
	DecisiveSourceConflict     bool
	EvaluatedAt                time.Time
	PolicyVersion              string
}

type VisibilityState struct {
	WorkID          uuid.UUID
	PubliclyVisible bool
	AnalysisReady   bool
	AnalysisCutoff  *time.Time
	Reasons         []VisibilityReason
	PolicyVersion   string
	EvaluatedAt     time.Time
}

func EvaluateVisibility(input VisibilityInput) (VisibilityState, error) {
	if err := input.validate(); err != nil {
		return VisibilityState{}, err
	}
	state := VisibilityState{
		WorkID:        input.WorkID,
		PolicyVersion: VisibilityPolicyVersion,
		EvaluatedAt:   input.EvaluatedAt.UTC(),
	}

	if input.Admission.Decision != scope.AdmissionAccepted {
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonAdmissionRejected,
		)
	}
	if !input.HasStableIdentity {
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonStableIdentityMissing,
		)
	}
	if !input.WorkActive {
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonWorkInactive,
		)
	}
	if input.Admission.Decision == scope.AdmissionAccepted &&
		!eligibleLifecycle(input.Admission.Channel, input.Lifecycle) {
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonLifecycleIneligible,
		)
	}
	switch officialLinkState(input.OfficialLink, input.EvaluatedAt) {
	case officialLinkMissing:
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonOfficialLinkMissing,
		)
	case officialLinkInvalid:
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonOfficialLinkInvalid,
		)
	case officialLinkExpired:
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonOfficialLinkExpired,
		)
	}
	state.PubliclyVisible = len(state.Reasons) == 0
	if !state.PubliclyVisible || input.AnalysisCutoff == nil {
		return state, nil
	}

	cutoff := input.AnalysisCutoff.UTC()
	state.AnalysisCutoff = &cutoff
	if input.CanonicalChannelEventAt == nil {
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonCanonicalChannelEventMissing,
		)
	} else if input.CanonicalChannelEventAt.After(cutoff) {
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonCanonicalChannelEventAfterCutoff,
		)
	}
	if !input.DomainClassified {
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonDomainClassificationMissing,
		)
	}
	if !input.RequiredTaxonomyClassified {
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonTaxonomyMissing,
		)
	}
	if !input.AbstractRouteSucceeded {
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonAbstractRouteMissing,
		)
	}
	if input.DecisiveSourceConflict {
		state.Reasons = append(
			state.Reasons,
			VisibilityReasonSourceConflict,
		)
	}
	state.AnalysisReady = len(state.Reasons) == 0
	return state, nil
}

func (input VisibilityInput) validate() error {
	if input.WorkID == uuid.Nil {
		return errors.New("visibility Work ID is required")
	}
	if input.PolicyVersion != VisibilityPolicyVersion {
		return fmt.Errorf(
			"visibility policy version must equal %s",
			VisibilityPolicyVersion,
		)
	}
	if input.EvaluatedAt.IsZero() {
		return errors.New("visibility evaluated_at is required")
	}
	if input.Admission.WorkID != input.WorkID.String() {
		return errors.New(
			"visibility admission decision must belong to the same Work",
		)
	}
	if err := input.Admission.Validate(); err != nil {
		return fmt.Errorf("validate visibility admission decision: %w", err)
	}
	switch input.Lifecycle {
	case scope.LifecycleStatePublished,
		scope.LifecycleStateAcceptedEarly,
		scope.LifecycleStatePreprintActive,
		scope.LifecycleStateConferencePublished,
		scope.LifecycleStateWithdrawn,
		scope.LifecycleStateMissing,
		scope.LifecycleStateConflict:
	default:
		return fmt.Errorf(
			"visibility lifecycle state %q is invalid",
			input.Lifecycle,
		)
	}
	if input.AnalysisCutoff != nil {
		if input.AnalysisCutoff.IsZero() {
			return errors.New(
				"visibility analysis cutoff must be absent or non-zero",
			)
		}
		if input.AnalysisCutoff.After(input.EvaluatedAt) {
			return errors.New(
				"visibility analysis cutoff must not follow evaluated_at",
			)
		}
	}
	if input.CanonicalChannelEventAt != nil &&
		input.CanonicalChannelEventAt.IsZero() {
		return errors.New(
			"visibility canonical channel event must be absent or non-zero",
		)
	}
	return nil
}

func eligibleLifecycle(
	channel scope.ContentChannel,
	lifecycle scope.LifecycleState,
) bool {
	switch channel {
	case scope.ContentChannelJournalPublished:
		return lifecycle == scope.LifecycleStatePublished
	case scope.ContentChannelAcceptedEarly:
		return lifecycle == scope.LifecycleStateAcceptedEarly
	case scope.ContentChannelPreprint:
		return lifecycle == scope.LifecycleStatePreprintActive
	case scope.ContentChannelConferenceProceeding:
		return lifecycle == scope.LifecycleStateConferencePublished
	default:
		return false
	}
}

type officialLinkEvaluation uint8

const (
	officialLinkValid officialLinkEvaluation = iota
	officialLinkMissing
	officialLinkInvalid
	officialLinkExpired
)

func officialLinkState(
	link *VisibilityOfficialLink,
	evaluatedAt time.Time,
) officialLinkEvaluation {
	if link == nil {
		return officialLinkMissing
	}
	if link.VerificationID == uuid.Nil ||
		link.URL == "" ||
		link.URL != strings.TrimSpace(link.URL) ||
		link.VerifierVersion == "" ||
		link.VerifierVersion != strings.TrimSpace(link.VerifierVersion) ||
		link.VerifiedAt.IsZero() ||
		link.ProjectedAt.IsZero() ||
		link.ExpiresAt.IsZero() ||
		!link.ExpiresAt.After(link.VerifiedAt) ||
		link.VerifiedAt.After(link.ProjectedAt) ||
		link.ProjectedAt.After(evaluatedAt) ||
		link.VerifiedAt.After(evaluatedAt) {
		return officialLinkInvalid
	}
	parsed, err := url.Parse(link.URL)
	if err != nil ||
		parsed.Scheme != "https" ||
		parsed.Hostname() == "" ||
		parsed.User != nil ||
		parsed.Fragment != "" {
		return officialLinkInvalid
	}
	if !link.ExpiresAt.After(evaluatedAt) {
		return officialLinkExpired
	}
	return officialLinkValid
}

func (state VisibilityState) Clone() VisibilityState {
	cloned := state
	cloned.Reasons = slices.Clone(state.Reasons)
	if state.AnalysisCutoff != nil {
		cutoff := *state.AnalysisCutoff
		cloned.AnalysisCutoff = &cutoff
	}
	return cloned
}
