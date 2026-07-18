package scope

import (
	"strings"
	"testing"
	"time"
)

func TestAdmissionJournalChannelsRequireAcceptedJournalAllQ1Assessment(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 18, 3, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		channel   ContentChannel
		lifecycle LifecycleState
	}{
		{
			channel:   ContentChannelJournalPublished,
			lifecycle: LifecycleStatePublished,
		},
		{
			channel:   ContentChannelAcceptedEarly,
			lifecycle: LifecycleStateAcceptedEarly,
		},
	} {
		test := test
		t.Run(string(test.channel), func(t *testing.T) {
			t.Parallel()
			input := journalAdmissionInput(test.channel, test.lifecycle, now)
			decision, err := EvaluateAdmission(
				input,
				PreprintRegistry{},
				ConferenceRegistry{},
			)
			if err != nil {
				t.Fatalf("EvaluateAdmission() error = %v", err)
			}
			if decision.Decision != AdmissionAccepted ||
				decision.Channel != test.channel ||
				decision.PolicyVersion != ChannelAdmissionPolicyVersion ||
				decision.RegistryVersion != JournalAllQ1PolicyVersion {
				t.Fatalf("journal admission = %#v", decision)
			}

			input.JournalAssessment.Decision = "rejected"
			decision, err = EvaluateAdmission(
				input,
				PreprintRegistry{},
				ConferenceRegistry{},
			)
			if err != nil {
				t.Fatalf("EvaluateAdmission(rejected Q1) error = %v", err)
			}
			if decision.Decision != AdmissionRejected ||
				decision.Reason != AdmissionReasonJournalQ1NotAccepted {
				t.Fatalf("rejected Q1 admission = %#v", decision)
			}

			input.JournalAssessment.Decision = "accepted"
			input.JournalAssessment.PolicyVersion = "journal-all-q1/v1"
			decision, err = EvaluateAdmission(
				input,
				PreprintRegistry{},
				ConferenceRegistry{},
			)
			if err != nil {
				t.Fatalf("EvaluateAdmission(wrong Q1 policy) error = %v", err)
			}
			if decision.Decision != AdmissionRejected ||
				decision.Reason != AdmissionReasonJournalQ1NotAccepted {
				t.Fatalf("wrong Q1 policy admission = %#v", decision)
			}
		})
	}
}

func TestAdmissionPreprintRequiresExactRegistryEvidenceAndRejectsJCR(t *testing.T) {
	t.Parallel()

	registry, err := ParsePreprintRegistry(strings.NewReader(
		"registry_name,registry_version,source_key,display_name,identifier_scheme,official_host,allowed_domain,lifecycle\n" +
			"medpaperhub-trusted-preprints,preprint-sources/v1,arxiv,arXiv,arxiv,arxiv.org,computer_science,active\n",
	))
	if err != nil {
		t.Fatalf("ParsePreprintRegistry() error = %v", err)
	}
	now := time.Date(2026, time.July, 18, 3, 10, 0, 0, time.UTC)
	input := AdmissionInput{
		WorkID: "00000000-0000-0000-0000-000000000701",
		ChannelDecision: resolvedChannelDecision(
			"00000000-0000-0000-0000-000000000701",
			ContentChannelPreprint,
			now,
		),
		Lifecycle: LifecycleProjection{
			WorkID:        "00000000-0000-0000-0000-000000000701",
			Channel:       ContentChannelPreprint,
			State:         LifecycleStatePreprintActive,
			PolicyVersion: "lifecycle-projection/v1",
			AssertionIDs:  []string{"00000000-0000-0000-0000-000000000702"},
			SourcePaths:   []string{"$.posted"},
			DecidedAt:     now,
		},
		Domain:           ResearchDomainComputerScience,
		DomainSourcePath: "$.primary_category",
		OfficialURL: VerifiedOfficialURL{
			URL:        "https://arxiv.org/abs/2607.12345",
			Verified:   true,
			SourcePath: "$.id",
			Verifier:   "official-url/v1",
			VerifiedAt: now,
		},
		Preprint: &PreprintAdmissionEvidence{
			RegistryVersion: PreprintRegistryVersion,
			SourceKey:       "arxiv",
			StableID:        "2607.12345",
			PostedDate:      time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC),
			Version:         "v1",
			SourcePath:      "$",
		},
		PolicyVersion: ChannelAdmissionPolicyVersion,
		DecidedAt:     now,
	}
	decision, err := EvaluateAdmission(input, registry, ConferenceRegistry{})
	if err != nil {
		t.Fatalf("EvaluateAdmission(preprint) error = %v", err)
	}
	if decision.Decision != AdmissionAccepted ||
		decision.RegistryVersion != PreprintRegistryVersion {
		t.Fatalf("preprint admission = %#v", decision)
	}

	tests := []struct {
		name   string
		mutate func(*AdmissionInput)
		reason AdmissionReason
	}{
		{
			name: "unknown source case",
			mutate: func(value *AdmissionInput) {
				value.Preprint.SourceKey = "ArXiv"
			},
			reason: AdmissionReasonPreprintRegistryMismatch,
		},
		{
			name: "missing stable identifier",
			mutate: func(value *AdmissionInput) {
				value.Preprint.StableID = ""
			},
			reason: AdmissionReasonPreprintEvidenceMissing,
		},
		{
			name: "missing posted date",
			mutate: func(value *AdmissionInput) {
				value.Preprint.PostedDate = time.Time{}
			},
			reason: AdmissionReasonPreprintEvidenceMissing,
		},
		{
			name: "missing version",
			mutate: func(value *AdmissionInput) {
				value.Preprint.Version = ""
			},
			reason: AdmissionReasonPreprintEvidenceMissing,
		},
		{
			name: "withdrawn",
			mutate: func(value *AdmissionInput) {
				value.Lifecycle.State = LifecycleStateWithdrawn
			},
			reason: AdmissionReasonLifecycleIneligible,
		},
		{
			name: "unverified URL",
			mutate: func(value *AdmissionInput) {
				value.OfficialURL.Verified = false
			},
			reason: AdmissionReasonOfficialURLMissing,
		},
		{
			name: "wrong official host",
			mutate: func(value *AdmissionInput) {
				value.OfficialURL.URL = "https://export.arxiv.org/abs/2607.12345"
			},
			reason: AdmissionReasonPreprintRegistryMismatch,
		},
		{
			name: "JCR evidence forbidden",
			mutate: func(value *AdmissionInput) {
				value.JournalAssessment = &JournalAdmissionEvidence{
					AssessmentID:  "00000000-0000-0000-0000-000000000799",
					PolicyVersion: JournalAllQ1PolicyVersion,
					Decision:      "accepted",
					SourcePath:    "$.jcr",
				}
			},
			reason: AdmissionReasonJCRForbidden,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := cloneAdmissionInput(input)
			test.mutate(&candidate)
			got, err := EvaluateAdmission(candidate, registry, ConferenceRegistry{})
			if err != nil {
				t.Fatalf("EvaluateAdmission() error = %v", err)
			}
			if got.Decision != AdmissionRejected || got.Reason != test.reason {
				t.Fatalf("admission = %#v, want rejected %q", got, test.reason)
			}
		})
	}
}

func TestAdmissionConferenceRequiresExactEventAndRejectsJCR(t *testing.T) {
	t.Parallel()

	registry, err := ParseConferenceRegistry(strings.NewReader(
		"registry_name,registry_version,provider,series_key,series_name,event_key,event_name,event_year,identifier_scheme,identifier_value,official_host,allowed_domain,lifecycle,reviewed\n" +
			"medpaperhub-conferences,conference-venues/v1,ieee,cvpr,IEEE/CVF Conference on Computer Vision and Pattern Recognition,cvpr-2026,CVPR 2026,2026,doi_prefix,10.1109,ieeexplore.ieee.org,computer_science,active,false\n",
	))
	if err != nil {
		t.Fatalf("ParseConferenceRegistry() error = %v", err)
	}
	now := time.Date(2026, time.July, 18, 3, 20, 0, 0, time.UTC)
	input := AdmissionInput{
		WorkID: "00000000-0000-0000-0000-000000000801",
		ChannelDecision: resolvedChannelDecision(
			"00000000-0000-0000-0000-000000000801",
			ContentChannelConferenceProceeding,
			now,
		),
		Lifecycle: LifecycleProjection{
			WorkID:        "00000000-0000-0000-0000-000000000801",
			Channel:       ContentChannelConferenceProceeding,
			State:         LifecycleStateConferencePublished,
			PolicyVersion: "lifecycle-projection/v1",
			AssertionIDs:  []string{"00000000-0000-0000-0000-000000000802"},
			SourcePaths:   []string{"$.publication_year"},
			DecidedAt:     now,
		},
		Domain:           ResearchDomainComputerScience,
		DomainSourcePath: "$.domain",
		OfficialURL: VerifiedOfficialURL{
			URL:        "https://ieeexplore.ieee.org/document/12345678",
			Verified:   true,
			SourcePath: "$.html_url",
			Verifier:   "official-url/v1",
			VerifiedAt: now,
		},
		Conference: &ConferenceAdmissionEvidence{
			RegistryVersion: ConferenceRegistryVersion,
			Provider:        "ieee",
			SeriesKey:       "cvpr",
			EventKey:        "cvpr-2026",
			StableID:        "10.1109/CVPR.2026.123",
			SourcePath:      "$",
		},
		PolicyVersion: ChannelAdmissionPolicyVersion,
		DecidedAt:     now,
	}
	decision, err := EvaluateAdmission(input, PreprintRegistry{}, registry)
	if err != nil {
		t.Fatalf("EvaluateAdmission(conference) error = %v", err)
	}
	if decision.Decision != AdmissionAccepted ||
		decision.RegistryVersion != ConferenceRegistryVersion {
		t.Fatalf("conference admission = %#v", decision)
	}

	for _, test := range []struct {
		name   string
		mutate func(*AdmissionInput)
		reason AdmissionReason
	}{
		{
			name: "guessed acronym",
			mutate: func(value *AdmissionInput) {
				value.Conference.SeriesKey = "CVPR"
			},
			reason: AdmissionReasonConferenceRegistryMismatch,
		},
		{
			name: "unknown event",
			mutate: func(value *AdmissionInput) {
				value.Conference.EventKey = "cvpr-2025"
			},
			reason: AdmissionReasonConferenceRegistryMismatch,
		},
		{
			name: "missing stable identifier",
			mutate: func(value *AdmissionInput) {
				value.Conference.StableID = ""
			},
			reason: AdmissionReasonConferenceEvidenceMissing,
		},
		{
			name: "JCR evidence forbidden",
			mutate: func(value *AdmissionInput) {
				value.JournalAssessment = &JournalAdmissionEvidence{
					AssessmentID:  "00000000-0000-0000-0000-000000000899",
					PolicyVersion: JournalAllQ1PolicyVersion,
					Decision:      "accepted",
					SourcePath:    "$.jcr",
				}
			},
			reason: AdmissionReasonJCRForbidden,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := cloneAdmissionInput(input)
			test.mutate(&candidate)
			got, err := EvaluateAdmission(candidate, PreprintRegistry{}, registry)
			if err != nil {
				t.Fatalf("EvaluateAdmission() error = %v", err)
			}
			if got.Decision != AdmissionRejected || got.Reason != test.reason {
				t.Fatalf("admission = %#v, want rejected %q", got, test.reason)
			}
		})
	}
}

func journalAdmissionInput(
	channel ContentChannel,
	lifecycle LifecycleState,
	now time.Time,
) AdmissionInput {
	workID := "00000000-0000-0000-0000-000000000601"
	return AdmissionInput{
		WorkID: workID,
		ChannelDecision: resolvedChannelDecision(
			workID,
			channel,
			now,
		),
		Lifecycle: LifecycleProjection{
			WorkID:        workID,
			Channel:       channel,
			State:         lifecycle,
			PolicyVersion: "lifecycle-projection/v1",
			AssertionIDs:  []string{"00000000-0000-0000-0000-000000000603"},
			SourcePaths:   []string{"$.publication_status"},
			DecidedAt:     now,
		},
		Domain:           ResearchDomainMedicine,
		DomainSourcePath: "$.jcr_category",
		OfficialURL: VerifiedOfficialURL{
			URL:        "https://doi.org/10.1000/example",
			Verified:   true,
			SourcePath: "$.doi",
			Verifier:   "official-url/v1",
			VerifiedAt: now,
		},
		JournalAssessment: &JournalAdmissionEvidence{
			AssessmentID:  "00000000-0000-0000-0000-000000000602",
			PolicyVersion: JournalAllQ1PolicyVersion,
			Decision:      "accepted",
			SourcePath:    "$.venue_assessment",
		},
		PolicyVersion: ChannelAdmissionPolicyVersion,
		DecidedAt:     now,
	}
}

func resolvedChannelDecision(
	workID string,
	channel ContentChannel,
	now time.Time,
) ChannelDecision {
	return ChannelDecision{
		WorkID:        workID,
		Status:        ChannelDecisionResolved,
		Channel:       channel,
		PolicyVersion: "channel-projection/v1",
		AssertionIDs:  []string{"00000000-0000-0000-0000-000000000901"},
		SourcePaths:   []string{"$.channel"},
		DecidedAt:     now,
	}
}

func cloneAdmissionInput(input AdmissionInput) AdmissionInput {
	if input.JournalAssessment != nil {
		journal := *input.JournalAssessment
		input.JournalAssessment = &journal
	}
	if input.Preprint != nil {
		preprint := *input.Preprint
		input.Preprint = &preprint
	}
	if input.Conference != nil {
		conference := *input.Conference
		input.Conference = &conference
	}
	return input
}
