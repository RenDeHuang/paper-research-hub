package urlverify

import (
	"errors"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

func TestCandidateAcceptsOnlySourceProvidedHTTPSURLs(t *testing.T) {
	t.Parallel()

	valid := Candidate{
		WorkID:                "work-1",
		ProjectionAssertionID: "projection-1",
		NormalizedAssertionID: "normalized-1",
		SourceRecordID:        "source-record-1",
		SourcePath:            "$.message.URL",
		Channel:               scope.ContentChannelJournalPublished,
		LinkRole:              LinkRoleOfficialArticle,
		URL: "https://resolver.example.test/" +
			"articles%2F123?view=%2Ffull",
		ParserVersion: "crossref-parser/v4",
		Identifier: StableIdentifier{
			Scheme: "doi",
			Value:  "10.1000/article",
		},
		AssertedAt: time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid source candidate error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Candidate)
		target error
	}{
		{
			name: "missing source path is guessed",
			mutate: func(candidate *Candidate) {
				candidate.SourcePath = ""
			},
			target: ErrCandidateProvenance,
		},
		{
			name: "missing source record is guessed",
			mutate: func(candidate *Candidate) {
				candidate.SourceRecordID = ""
			},
			target: ErrCandidateProvenance,
		},
		{
			name: "missing exact normalized revision",
			mutate: func(candidate *Candidate) {
				candidate.NormalizedAssertionID = ""
			},
			target: ErrCandidateProvenance,
		},
		{
			name: "HTTP source URL",
			mutate: func(candidate *Candidate) {
				candidate.URL = "http://publisher.example.test/articles/123"
			},
			target: ErrCandidateURL,
		},
		{
			name: "non HTTPS scheme",
			mutate: func(candidate *Candidate) {
				candidate.URL = "ftp://publisher.example.test/articles/123"
			},
			target: ErrCandidateURL,
		},
		{
			name: "relative URL",
			mutate: func(candidate *Candidate) {
				candidate.URL = "/articles/123"
			},
			target: ErrCandidateURL,
		},
		{
			name: "empty fragment marker",
			mutate: func(candidate *Candidate) {
				candidate.URL = "https://publisher.example.test/articles/123#"
			},
			target: ErrCandidateURL,
		},
		{
			name: "malformed percent encoding",
			mutate: func(candidate *Candidate) {
				candidate.URL = "https://publisher.example.test/articles/%zz"
			},
			target: ErrCandidateURL,
		},
		{
			name: "incomplete percent encoding",
			mutate: func(candidate *Candidate) {
				candidate.URL = "https://publisher.example.test/articles/%"
			},
			target: ErrCandidateURL,
		},
		{
			name: "malformed query percent encoding",
			mutate: func(candidate *Candidate) {
				candidate.URL = "https://publisher.example.test/articles?view=%zz"
			},
			target: ErrCandidateURL,
		},
		{
			name: "incomplete query percent encoding",
			mutate: func(candidate *Candidate) {
				candidate.URL = "https://publisher.example.test/articles?view=%"
			},
			target: ErrCandidateURL,
		},
		{
			name: "preprint role on journal channel",
			mutate: func(candidate *Candidate) {
				candidate.LinkRole = LinkRoleOfficialPreprint
			},
			target: ErrCandidateRole,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, test.target) {
				t.Fatalf("Candidate.Validate() error = %v, want %v", err, test.target)
			}
		})
	}
}

func TestStableIdentifierNormalizesWithoutBuildingURLs(t *testing.T) {
	t.Parallel()

	identifier, err := NormalizeStableIdentifier(StableIdentifier{
		Scheme: "doi",
		Value:  "HTTPS://DOI.ORG/10.1000/ABC",
	})
	if err != nil {
		t.Fatalf("NormalizeStableIdentifier() error = %v", err)
	}
	if identifier != (StableIdentifier{Scheme: "doi", Value: "10.1000/abc"}) {
		t.Fatalf("normalized identifier = %#v", identifier)
	}

	articleID, err := NormalizeStableIdentifier(StableIdentifier{
		Scheme: "publisher_article",
		Value:  "S1234-5678(26)00001-2",
	})
	if err != nil {
		t.Fatalf("NormalizeStableIdentifier(publisher article) error = %v", err)
	}
	if articleID.Value != "S1234-5678(26)00001-2" {
		t.Fatalf("publisher article identifier = %#v", articleID)
	}
}

func TestVerificationActiveRequiresVerifiedUnexpiredAssertion(t *testing.T) {
	t.Parallel()

	expiresAt := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	verification := Verification{
		State:     VerificationStateVerified,
		CheckedAt: expiresAt.Add(-time.Hour),
		ExpiresAt: expiresAt,
	}
	if !verification.Active(expiresAt.Add(-time.Nanosecond)) {
		t.Fatal("verified assertion was not active before expiry")
	}
	if verification.Active(expiresAt) {
		t.Fatal("verified assertion remained active at expiry")
	}
	verification.State = VerificationStateFailed
	if verification.Active(expiresAt.Add(-time.Hour)) {
		t.Fatal("failed assertion was active")
	}
}

func TestIdentifierEvidenceRequiresRawValueToNormalizeToIdentifier(
	t *testing.T,
) {
	t.Parallel()

	valid := []IdentifierEvidence{
		{
			Identifier: StableIdentifier{
				Scheme: "doi",
				Value:  "10.1000/raw-doi",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_doi"]@content`,
			RawValue:   " DOI:10.1000/RAW-DOI ",
		},
		{
			Identifier: StableIdentifier{
				Scheme: "pmid",
				Value:  "123456",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_pmid"]@content`,
			RawValue:   " 123456 ",
		},
		{
			Identifier: StableIdentifier{
				Scheme: "pmcid",
				Value:  "PMC123456",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_pmcid"]@content`,
			RawValue:   " pmc123456 ",
		},
		{
			Identifier: StableIdentifier{
				Scheme: "arxiv",
				Value:  "2401.01234",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_arxiv_id"]@content`,
			RawValue:   " arXiv:2401.01234V2 ",
		},
		{
			Identifier: StableIdentifier{
				Scheme: "doi",
				Value:  "10.1000/raw-dc",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="dc.identifier"]@content`,
			RawValue:   "https://doi.org/10.1000/RAW-DC",
		},
	}
	for _, evidence := range valid {
		if err := evidence.Validate(); err != nil {
			t.Fatalf("IdentifierEvidence.Validate(%#v) error = %v", evidence, err)
		}
	}

	invalid := []IdentifierEvidence{
		{
			Identifier: StableIdentifier{
				Scheme: "doi",
				Value:  "10.1000/raw-doi",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_doi"]@content`,
			RawValue:   "10.1000/different",
		},
		{
			Identifier: StableIdentifier{
				Scheme: "pmid",
				Value:  "123456",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_pmid"]@content`,
			RawValue:   "654321",
		},
		{
			Identifier: StableIdentifier{
				Scheme: "pmcid",
				Value:  "PMC123456",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_pmcid"]@content`,
			RawValue:   "PMC654321",
		},
		{
			Identifier: StableIdentifier{
				Scheme: "arxiv",
				Value:  "2401.01234",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_arxiv_id"]@content`,
			RawValue:   "arXiv:2401.54321",
		},
		{
			Identifier: StableIdentifier{
				Scheme: "doi",
				Value:  "10.1000/raw-dc",
			},
			SourceKind: IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="dc.identifier"]@content`,
			RawValue:   "10.1000/raw-dc",
		},
	}
	for _, evidence := range invalid {
		if err := evidence.Validate(); err == nil {
			t.Fatalf("IdentifierEvidence.Validate(%#v) error = nil", evidence)
		}
	}
}

func TestOfficialLinkRequiresHTTPSEvenForLoopback(t *testing.T) {
	t.Parallel()

	link := OfficialLink{
		ID:                "link-1",
		WorkID:            "work-1",
		Channel:           scope.ContentChannelJournalPublished,
		LinkRole:          LinkRoleOfficialArticle,
		VerificationID:    "verification-1",
		URL:               "http://127.0.0.1:8080/article",
		ProjectionVersion: 1,
		ProjectedAt: time.Date(
			2026,
			time.July,
			18,
			8,
			0,
			0,
			0,
			time.UTC,
		),
		ExpiresAt: time.Date(
			2026,
			time.July,
			19,
			8,
			0,
			0,
			0,
			time.UTC,
		),
	}
	if err := link.Validate(); err == nil {
		t.Fatal("OfficialLink.Validate(loopback HTTP) error = nil")
	}
}

func TestVerificationValidateRejectsUnknownFailureAndUnboundEvidence(
	t *testing.T,
) {
	t.Parallel()

	verification, err := Verify(successfulVerificationInput())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	unknownFailure := verification
	unknownFailure.State = VerificationStateFailed
	unknownFailure.FailureCode = FailureCode("unknown_failure")
	if err := unknownFailure.Validate(); err == nil {
		t.Fatal("Verification.Validate(unknown failure) error = nil")
	}

	unbound := verification.clone()
	unbound.RedirectChain[0].URL = "https://different.example.test/source"
	if err := unbound.Validate(); err == nil {
		t.Fatal("Verification.Validate(unbound source) error = nil")
	}

	oversized := verification.clone()
	oversized.RedirectChain = make([]RedirectHop, 12)
	for index := range oversized.RedirectChain {
		oversized.RedirectChain[index] = RedirectHop{
			URL:        verification.SourceURL,
			StatusCode: 302,
			Location:   verification.FinalURL,
		}
	}
	oversized.RedirectChain[11] = RedirectHop{
		URL:        verification.FinalURL,
		StatusCode: verification.HTTPStatus,
	}
	if err := oversized.Validate(); err == nil {
		t.Fatal("Verification.Validate(oversized chain) error = nil")
	}
}
