package scope

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	ChannelAdmissionPolicyVersion = "channel-admission/v1"
	JournalAllQ1PolicyVersion     = "journal-all-q1/v2"
)

type AdmissionStatus string

const (
	AdmissionAccepted AdmissionStatus = "accepted"
	AdmissionRejected AdmissionStatus = "rejected"
	AdmissionMissing  AdmissionStatus = "missing"
)

type AdmissionReason string

const (
	AdmissionReasonEligible                   AdmissionReason = "eligible"
	AdmissionReasonChannelUnresolved          AdmissionReason = "channel_unresolved"
	AdmissionReasonDomainUnresolved           AdmissionReason = "domain_unresolved"
	AdmissionReasonLifecycleIneligible        AdmissionReason = "lifecycle_ineligible"
	AdmissionReasonOfficialURLMissing         AdmissionReason = "official_url_missing"
	AdmissionReasonJournalQ1NotAccepted       AdmissionReason = "journal_q1_not_accepted"
	AdmissionReasonPreprintEvidenceMissing    AdmissionReason = "preprint_evidence_missing"
	AdmissionReasonPreprintRegistryMismatch   AdmissionReason = "preprint_registry_mismatch"
	AdmissionReasonConferenceEvidenceMissing  AdmissionReason = "conference_evidence_missing"
	AdmissionReasonConferenceRegistryMismatch AdmissionReason = "conference_registry_mismatch"
	AdmissionReasonJCRForbidden               AdmissionReason = "jcr_forbidden"
)

type VerifiedOfficialURL struct {
	URL        string
	Verified   bool
	SourcePath string
	Verifier   string
	VerifiedAt time.Time
}

func (evidence VerifiedOfficialURL) parsed() (*url.URL, bool) {
	if !evidence.Verified ||
		evidence.URL == "" ||
		evidence.URL != strings.TrimSpace(evidence.URL) ||
		evidence.SourcePath == "" ||
		evidence.SourcePath != strings.TrimSpace(evidence.SourcePath) ||
		evidence.Verifier == "" ||
		evidence.Verifier != strings.TrimSpace(evidence.Verifier) ||
		evidence.VerifiedAt.IsZero() {
		return nil, false
	}
	parsed, err := url.Parse(evidence.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, false
	}
	return parsed, true
}

type JournalAdmissionEvidence struct {
	AssessmentID  string
	PolicyVersion string
	Decision      string
	SourcePath    string
}

type PreprintAdmissionEvidence struct {
	RegistryVersion string
	SourceKey       string
	StableID        string
	PostedDate      time.Time
	Version         string
	SourcePath      string
}

type ConferenceAdmissionEvidence struct {
	RegistryVersion string
	Provider        string
	SeriesKey       string
	EventKey        string
	StableID        string
	SourcePath      string
}

type AdmissionInput struct {
	WorkID            string
	ChannelDecision   ChannelDecision
	Lifecycle         LifecycleProjection
	Domain            ResearchDomain
	DomainSourcePath  string
	OfficialURL       VerifiedOfficialURL
	JournalAssessment *JournalAdmissionEvidence
	Preprint          *PreprintAdmissionEvidence
	Conference        *ConferenceAdmissionEvidence
	PolicyVersion     string
	DecidedAt         time.Time
}

type AdmissionDecision struct {
	WorkID          string
	Channel         ContentChannel
	Decision        AdmissionStatus
	Reason          AdmissionReason
	PolicyVersion   string
	RegistryVersion string
	SourcePaths     []string
	DecidedAt       time.Time
}

func EvaluateAdmission(
	input AdmissionInput,
	preprints PreprintRegistry,
	conferences ConferenceRegistry,
) (AdmissionDecision, error) {
	if input.WorkID == "" || input.WorkID != strings.TrimSpace(input.WorkID) {
		return AdmissionDecision{}, errors.New("admission requires an exact Work ID")
	}
	if input.PolicyVersion != ChannelAdmissionPolicyVersion {
		return AdmissionDecision{}, fmt.Errorf(
			"unsupported admission policy version %q",
			input.PolicyVersion,
		)
	}
	if input.DecidedAt.IsZero() {
		return AdmissionDecision{}, errors.New("admission decided_at is required")
	}
	if err := input.ChannelDecision.Validate(); err != nil {
		return AdmissionDecision{}, fmt.Errorf("validate channel decision: %w", err)
	}
	if input.ChannelDecision.WorkID != input.WorkID {
		return AdmissionDecision{}, errors.New(
			"channel decision Work does not match admission Work",
		)
	}
	decision := AdmissionDecision{
		WorkID:        input.WorkID,
		PolicyVersion: input.PolicyVersion,
		DecidedAt:     input.DecidedAt.UTC(),
	}
	if input.ChannelDecision.Status != ChannelDecisionResolved {
		decision.Decision = AdmissionMissing
		decision.Reason = AdmissionReasonChannelUnresolved
		return decision, nil
	}
	decision.Channel = input.ChannelDecision.Channel
	if err := input.Lifecycle.Validate(); err != nil {
		return AdmissionDecision{}, fmt.Errorf("validate lifecycle projection: %w", err)
	}
	if input.Lifecycle.WorkID != input.WorkID ||
		input.Lifecycle.Channel != decision.Channel {
		return AdmissionDecision{}, errors.New(
			"lifecycle projection does not match admission channel and Work",
		)
	}
	if _, err := ParseResearchDomain(string(input.Domain)); err != nil ||
		input.DomainSourcePath == "" ||
		input.DomainSourcePath != strings.TrimSpace(input.DomainSourcePath) {
		return rejectAdmission(
			decision,
			AdmissionReasonDomainUnresolved,
			"",
			input,
		), nil
	}
	officialURL, official := input.OfficialURL.parsed()
	if !official {
		return rejectAdmission(
			decision,
			AdmissionReasonOfficialURLMissing,
			"",
			input,
		), nil
	}

	switch decision.Channel {
	case ContentChannelJournalPublished:
		if input.Lifecycle.State != LifecycleStatePublished {
			return rejectAdmission(
				decision,
				AdmissionReasonLifecycleIneligible,
				JournalAllQ1PolicyVersion,
				input,
			), nil
		}
		return evaluateJournalAdmission(decision, input), nil
	case ContentChannelAcceptedEarly:
		if input.Lifecycle.State != LifecycleStateAcceptedEarly {
			return rejectAdmission(
				decision,
				AdmissionReasonLifecycleIneligible,
				JournalAllQ1PolicyVersion,
				input,
			), nil
		}
		return evaluateJournalAdmission(decision, input), nil
	case ContentChannelPreprint:
		if input.JournalAssessment != nil {
			return rejectAdmission(
				decision,
				AdmissionReasonJCRForbidden,
				PreprintRegistryVersion,
				input,
			), nil
		}
		if input.Lifecycle.State != LifecycleStatePreprintActive {
			return rejectAdmission(
				decision,
				AdmissionReasonLifecycleIneligible,
				PreprintRegistryVersion,
				input,
			), nil
		}
		if input.Preprint == nil ||
			input.Preprint.StableID == "" ||
			input.Preprint.StableID != strings.TrimSpace(input.Preprint.StableID) ||
			input.Preprint.PostedDate.IsZero() ||
			input.Preprint.Version == "" ||
			input.Preprint.Version != strings.TrimSpace(input.Preprint.Version) ||
			input.Preprint.SourcePath == "" ||
			input.Preprint.SourcePath != strings.TrimSpace(input.Preprint.SourcePath) {
			return rejectAdmission(
				decision,
				AdmissionReasonPreprintEvidenceMissing,
				PreprintRegistryVersion,
				input,
			), nil
		}
		entry, found := preprints.Match(input.Preprint.SourceKey, input.Domain)
		if input.Preprint.RegistryVersion != PreprintRegistryVersion ||
			preprints.Version() != PreprintRegistryVersion ||
			!found ||
			!entry.MatchesOfficialURL(officialURL) {
			return rejectAdmission(
				decision,
				AdmissionReasonPreprintRegistryMismatch,
				PreprintRegistryVersion,
				input,
			), nil
		}
		return acceptAdmission(decision, PreprintRegistryVersion, input), nil
	case ContentChannelConferenceProceeding:
		if input.JournalAssessment != nil {
			return rejectAdmission(
				decision,
				AdmissionReasonJCRForbidden,
				ConferenceRegistryVersion,
				input,
			), nil
		}
		if input.Lifecycle.State != LifecycleStateConferencePublished {
			return rejectAdmission(
				decision,
				AdmissionReasonLifecycleIneligible,
				ConferenceRegistryVersion,
				input,
			), nil
		}
		if input.Conference == nil ||
			input.Conference.StableID == "" ||
			input.Conference.StableID != strings.TrimSpace(input.Conference.StableID) ||
			input.Conference.SourcePath == "" ||
			input.Conference.SourcePath != strings.TrimSpace(input.Conference.SourcePath) {
			return rejectAdmission(
				decision,
				AdmissionReasonConferenceEvidenceMissing,
				ConferenceRegistryVersion,
				input,
			), nil
		}
		entry, found := conferences.Match(
			input.Conference.Provider,
			input.Conference.SeriesKey,
			input.Conference.EventKey,
			input.Domain,
		)
		if input.Conference.RegistryVersion != ConferenceRegistryVersion ||
			conferences.Version() != ConferenceRegistryVersion ||
			!found ||
			!entry.MatchesOfficialURL(officialURL) {
			return rejectAdmission(
				decision,
				AdmissionReasonConferenceRegistryMismatch,
				ConferenceRegistryVersion,
				input,
			), nil
		}
		return acceptAdmission(decision, ConferenceRegistryVersion, input), nil
	default:
		return AdmissionDecision{}, fmt.Errorf(
			"unsupported admission channel %q",
			decision.Channel,
		)
	}
}

func evaluateJournalAdmission(
	decision AdmissionDecision,
	input AdmissionInput,
) AdmissionDecision {
	evidence := input.JournalAssessment
	if evidence == nil ||
		evidence.AssessmentID == "" ||
		evidence.AssessmentID != strings.TrimSpace(evidence.AssessmentID) ||
		evidence.PolicyVersion != JournalAllQ1PolicyVersion ||
		evidence.Decision != "accepted" ||
		evidence.SourcePath == "" ||
		evidence.SourcePath != strings.TrimSpace(evidence.SourcePath) {
		return rejectAdmission(
			decision,
			AdmissionReasonJournalQ1NotAccepted,
			JournalAllQ1PolicyVersion,
			input,
		)
	}
	return acceptAdmission(decision, JournalAllQ1PolicyVersion, input)
}

func acceptAdmission(
	decision AdmissionDecision,
	registryVersion string,
	input AdmissionInput,
) AdmissionDecision {
	decision.Decision = AdmissionAccepted
	decision.Reason = AdmissionReasonEligible
	decision.RegistryVersion = registryVersion
	decision.SourcePaths = admissionSourcePaths(input)
	return decision
}

func rejectAdmission(
	decision AdmissionDecision,
	reason AdmissionReason,
	registryVersion string,
	input AdmissionInput,
) AdmissionDecision {
	decision.Decision = AdmissionRejected
	decision.Reason = reason
	decision.RegistryVersion = registryVersion
	decision.SourcePaths = admissionSourcePaths(input)
	return decision
}

func admissionSourcePaths(input AdmissionInput) []string {
	result := append([]string(nil), input.ChannelDecision.SourcePaths...)
	result = append(result, input.Lifecycle.SourcePaths...)
	for _, value := range []string{
		input.DomainSourcePath,
		input.OfficialURL.SourcePath,
	} {
		if value != "" {
			result = append(result, value)
		}
	}
	if input.JournalAssessment != nil && input.JournalAssessment.SourcePath != "" {
		result = append(result, input.JournalAssessment.SourcePath)
	}
	if input.Preprint != nil && input.Preprint.SourcePath != "" {
		result = append(result, input.Preprint.SourcePath)
	}
	if input.Conference != nil && input.Conference.SourcePath != "" {
		result = append(result, input.Conference.SourcePath)
	}
	slices.Sort(result)
	return result
}
