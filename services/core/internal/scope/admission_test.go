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
				decision.AdmissionPolicyVersion != ChannelAdmissionPolicyVersion ||
				decision.DomainRegistryVersion != ResearchDomainRegistryVersion ||
				decision.JournalPolicyVersion != JournalAllQ1PolicyVersion ||
				decision.ChannelRegistryVersion != "" {
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
		AdmissionPolicyVersion: ChannelAdmissionPolicyVersion,
		DomainRegistryVersion:  ResearchDomainRegistryVersion,
		DecidedAt:              now,
	}
	decision, err := EvaluateAdmission(input, registry, ConferenceRegistry{})
	if err != nil {
		t.Fatalf("EvaluateAdmission(preprint) error = %v", err)
	}
	if decision.Decision != AdmissionAccepted ||
		decision.AdmissionPolicyVersion != ChannelAdmissionPolicyVersion ||
		decision.DomainRegistryVersion != ResearchDomainRegistryVersion ||
		decision.JournalPolicyVersion != "" ||
		decision.ChannelRegistryVersion != PreprintRegistryVersion {
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
		AdmissionPolicyVersion: ChannelAdmissionPolicyVersion,
		DomainRegistryVersion:  ResearchDomainRegistryVersion,
		DecidedAt:              now,
	}
	decision, err := EvaluateAdmission(input, PreprintRegistry{}, registry)
	if err != nil {
		t.Fatalf("EvaluateAdmission(conference) error = %v", err)
	}
	if decision.Decision != AdmissionAccepted ||
		decision.AdmissionPolicyVersion != ChannelAdmissionPolicyVersion ||
		decision.DomainRegistryVersion != ResearchDomainRegistryVersion ||
		decision.JournalPolicyVersion != "" ||
		decision.ChannelRegistryVersion != ConferenceRegistryVersion {
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
		AdmissionPolicyVersion: ChannelAdmissionPolicyVersion,
		DomainRegistryVersion:  ResearchDomainRegistryVersion,
		DecidedAt:              now,
	}
}

func TestAdmissionAllChannelsRequireExplicitDomainRegistryVersion(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
	preprints, err := ParsePreprintRegistry(strings.NewReader(
		"registry_name,registry_version,source_key,display_name,identifier_scheme,official_host,allowed_domain,lifecycle\n" +
			"medpaperhub-trusted-preprints,preprint-sources/v1,arxiv,arXiv,arxiv,arxiv.org,computer_science,active\n",
	))
	if err != nil {
		t.Fatalf("ParsePreprintRegistry() error = %v", err)
	}
	conferences, err := ParseConferenceRegistry(strings.NewReader(
		"registry_name,registry_version,provider,series_key,series_name,event_key,event_name,event_year,identifier_scheme,identifier_value,official_host,allowed_domain,lifecycle,reviewed\n" +
			"medpaperhub-conferences,conference-venues/v1,ieee,cvpr,IEEE/CVF Conference on Computer Vision and Pattern Recognition,cvpr-2026,CVPR 2026,2026,doi_prefix,10.1109,ieeexplore.ieee.org,computer_science,active,false\n",
	))
	if err != nil {
		t.Fatalf("ParseConferenceRegistry() error = %v", err)
	}
	preprintInput := AdmissionInput{
		WorkID: "00000000-0000-0000-0000-000000001101",
		ChannelDecision: resolvedChannelDecision(
			"00000000-0000-0000-0000-000000001101",
			ContentChannelPreprint,
			now,
		),
		Lifecycle: LifecycleProjection{
			WorkID:        "00000000-0000-0000-0000-000000001101",
			Channel:       ContentChannelPreprint,
			State:         LifecycleStatePreprintActive,
			PolicyVersion: "lifecycle-projection/v1",
			AssertionIDs:  []string{"00000000-0000-0000-0000-000000001102"},
			SourcePaths:   []string{"$.posted"},
			DecidedAt:     now,
		},
		Domain:           ResearchDomainComputerScience,
		DomainSourcePath: "$.domain",
		OfficialURL: VerifiedOfficialURL{
			URL:        "https://arxiv.org/abs/2607.12345",
			Verified:   true,
			SourcePath: "$.url",
			Verifier:   "official-url/v1",
			VerifiedAt: now,
		},
		Preprint: &PreprintAdmissionEvidence{
			RegistryVersion: PreprintRegistryVersion,
			SourceKey:       "arxiv",
			StableID:        "2607.12345",
			PostedDate:      now,
			Version:         "v1",
			SourcePath:      "$",
		},
		AdmissionPolicyVersion: ChannelAdmissionPolicyVersion,
		DomainRegistryVersion:  ResearchDomainRegistryVersion,
		DecidedAt:              now,
	}
	conferenceInput := AdmissionInput{
		WorkID: "00000000-0000-0000-0000-000000001201",
		ChannelDecision: resolvedChannelDecision(
			"00000000-0000-0000-0000-000000001201",
			ContentChannelConferenceProceeding,
			now,
		),
		Lifecycle: LifecycleProjection{
			WorkID:        "00000000-0000-0000-0000-000000001201",
			Channel:       ContentChannelConferenceProceeding,
			State:         LifecycleStateConferencePublished,
			PolicyVersion: "lifecycle-projection/v1",
			AssertionIDs:  []string{"00000000-0000-0000-0000-000000001202"},
			SourcePaths:   []string{"$.published"},
			DecidedAt:     now,
		},
		Domain:           ResearchDomainComputerScience,
		DomainSourcePath: "$.domain",
		OfficialURL: VerifiedOfficialURL{
			URL:        "https://ieeexplore.ieee.org/document/123456",
			Verified:   true,
			SourcePath: "$.url",
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
		AdmissionPolicyVersion: ChannelAdmissionPolicyVersion,
		DomainRegistryVersion:  ResearchDomainRegistryVersion,
		DecidedAt:              now,
	}
	tests := []struct {
		name        string
		input       AdmissionInput
		preprints   PreprintRegistry
		conferences ConferenceRegistry
	}{
		{
			name: "journal published",
			input: journalAdmissionInput(
				ContentChannelJournalPublished,
				LifecycleStatePublished,
				now,
			),
		},
		{
			name: "accepted early",
			input: journalAdmissionInput(
				ContentChannelAcceptedEarly,
				LifecycleStateAcceptedEarly,
				now,
			),
		},
		{
			name:      "preprint",
			input:     preprintInput,
			preprints: preprints,
		},
		{
			name:        "conference",
			input:       conferenceInput,
			conferences: conferences,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, version := range []string{
				"",
				"research-domains-jcr-subjects/v1",
			} {
				candidate := cloneAdmissionInput(test.input)
				candidate.DomainRegistryVersion = version
				if _, err := EvaluateAdmission(
					candidate,
					test.preprints,
					test.conferences,
				); err == nil {
					t.Fatalf(
						"EvaluateAdmission(domain Registry %q) error = nil",
						version,
					)
				}
			}
		})
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
