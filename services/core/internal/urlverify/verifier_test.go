package urlverify

import (
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

func TestVerifyAcceptsSourceHTTPSRedirectingToHTTPSWithMatchingIdentifier(
	t *testing.T,
) {
	t.Parallel()

	input := successfulVerificationInput()
	verification, err := Verify(input)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.State != VerificationStateVerified ||
		verification.FailureCode != FailureNone ||
		!verification.IdentifierMatch {
		t.Fatalf("verification = %#v", verification)
	}
	if verification.SourceURL != input.Candidate.URL ||
		verification.FinalURL != input.Observation.FinalURL ||
		verification.HTTPStatus != 200 {
		t.Fatalf("HTTP evidence = %#v", verification)
	}
	if len(verification.RedirectChain) != 2 {
		t.Fatalf("redirect chain = %#v", verification.RedirectChain)
	}
	if verification.MatchedIdentifier == nil ||
		*verification.MatchedIdentifier !=
			(StableIdentifier{Scheme: "doi", Value: "10.1000/article"}) {
		t.Fatalf("matched identifier = %#v", verification.MatchedIdentifier)
	}
}

func TestVerifyRejectsHTTPFinalURLEvenForLoopback(t *testing.T) {
	t.Parallel()

	input := successfulVerificationInput()
	input.Observation.FinalURL = "http://127.0.0.1:18080/article"
	input.Observation = HTTPObservation{
		FinalURL:   "http://127.0.0.1:18080/article",
		HTTPStatus: 200,
		RedirectChain: []RedirectHop{
			{
				URL:        input.Candidate.URL,
				StatusCode: 302,
				Location:   "http://127.0.0.1:18080/article",
			},
			{
				URL:        "http://127.0.0.1:18080/article",
				StatusCode: 200,
			},
		},
		IdentifierEvidence: testIdentifierEvidence(
			StableIdentifier{Scheme: "doi", Value: "10.1000/article"},
		),
	}
	verification, err := Verify(input)
	if err != nil {
		t.Fatalf("Verify(loopback) error = %v", err)
	}
	if verification.State != VerificationStateFailed ||
		verification.FailureCode != FailureNonHTTPSFinalURL {
		t.Fatalf("loopback verification = %#v", verification)
	}
}

func TestVerifyRejectsRedirectsBeyondExplicitBound(t *testing.T) {
	t.Parallel()

	input := successfulVerificationInput()
	input.Policy.MaxRedirects = 1
	input.Observation = HTTPObservation{
		FinalURL:   "https://publisher.example.test/final",
		HTTPStatus: 200,
		RedirectChain: []RedirectHop{
			{
				URL:        input.Candidate.URL,
				StatusCode: 302,
				Location:   "https://publisher.example.test/intermediate",
			},
			{
				URL:        "https://publisher.example.test/intermediate",
				StatusCode: 301,
				Location:   "/final",
			},
			{
				URL:        "https://publisher.example.test/final",
				StatusCode: 200,
			},
		},
		IdentifierEvidence: testIdentifierEvidence(
			StableIdentifier{Scheme: "doi", Value: "10.1000/article"},
		),
	}
	assertFailure(t, input, FailureRedirectLimitExceeded)
}

func TestVerifyRejectsRedirectLoop(t *testing.T) {
	t.Parallel()

	input := successfulVerificationInput()
	secondURL := "https://publisher.example.test/article"
	input.Observation = HTTPObservation{
		FinalURL:   input.Candidate.URL,
		HTTPStatus: 200,
		RedirectChain: []RedirectHop{
			{
				URL:        input.Candidate.URL,
				StatusCode: 302,
				Location:   secondURL,
			},
			{
				URL:        secondURL,
				StatusCode: 302,
				Location:   input.Candidate.URL,
			},
			{
				URL:        input.Candidate.URL,
				StatusCode: 200,
			},
		},
		IdentifierEvidence: testIdentifierEvidence(
			StableIdentifier{Scheme: "doi", Value: "10.1000/article"},
		),
	}
	assertFailure(t, input, FailureRedirectLoop)
}

func TestVerifyRejectsTerminalResponseThatStillProducesRedirectLocation(
	t *testing.T,
) {
	t.Parallel()

	input := successfulVerificationInput()
	input.Observation.RedirectChain[1].Location = "/unexpected-next"
	assertFailure(t, input, FailureInvalidRedirectChain)
}

func TestVerifyRejectsMissingStableIdentifier(t *testing.T) {
	t.Parallel()

	input := successfulVerificationInput()
	input.Observation.IdentifierEvidence = nil
	assertFailure(t, input, FailureMissingStableIdentifier)
}

func TestVerifyRejectsIdentifierMismatch(t *testing.T) {
	t.Parallel()

	input := successfulVerificationInput()
	input.Observation.IdentifierEvidence = testIdentifierEvidence(
		StableIdentifier{Scheme: "doi", Value: "10.1000/different"},
	)
	assertFailure(t, input, FailureIdentifierMismatch)
}

func TestVerifyRejectsExpiredAssertion(t *testing.T) {
	t.Parallel()

	input := successfulVerificationInput()
	input.EvaluatedAt = input.ExpiresAt
	assertFailure(t, input, FailureExpired)
}

func TestVerifyRejectsNonSuccessfulFinalHTTPStatus(t *testing.T) {
	t.Parallel()

	input := successfulVerificationInput()
	input.Observation.HTTPStatus = 404
	input.Observation.RedirectChain[len(input.Observation.RedirectChain)-1].
		StatusCode = 404
	assertFailure(t, input, FailureHTTPStatus)
}

func TestVerifySealsObserverFailuresWithoutForgingHTTPResponseEvidence(
	t *testing.T,
) {
	t.Parallel()

	for _, failureCode := range []FailureCode{
		FailureHostNotAllowed,
		FailureUnsafeResolvedAddress,
		FailureDNSResolution,
		FailureNetwork,
	} {
		failureCode := failureCode
		t.Run(string(failureCode), func(t *testing.T) {
			t.Parallel()

			input := successfulVerificationInput()
			input.Observation = HTTPObservation{
				FinalURL:    input.Candidate.URL,
				FailureCode: failureCode,
			}
			verification, err := Verify(input)
			if err != nil {
				t.Fatalf("Verify(observer failure) error = %v", err)
			}
			if verification.State != VerificationStateFailed ||
				verification.FailureCode != failureCode ||
				verification.HTTPStatus != 0 ||
				len(verification.RedirectChain) != 0 ||
				verification.FinalURL != input.Candidate.URL {
				t.Fatalf("observer failure verification = %#v", verification)
			}
		})
	}
}

func successfulVerificationInput() VerificationInput {
	checkedAt := time.Date(2026, time.July, 18, 8, 30, 0, 0, time.UTC)
	sourceURL := "https://resolver.example.test/article"
	finalURL := "https://publisher.example.test/article"
	return VerificationInput{
		Candidate: Candidate{
			ID:                    "candidate-1",
			WorkID:                "work-1",
			ProjectionAssertionID: "projection-1",
			NormalizedAssertionID: "normalized-1",
			SourceRecordID:        "source-record-1",
			SourcePath:            "$.message.URL",
			Channel:               scope.ContentChannelJournalPublished,
			LinkRole:              LinkRoleOfficialArticle,
			URL:                   sourceURL,
			ParserVersion:         "crossref-parser/v4",
			Identifier: StableIdentifier{
				Scheme: "doi",
				Value:  "10.1000/article",
			},
			AssertedAt: checkedAt.Add(-time.Hour),
		},
		ExpectedIdentifier: StableIdentifier{
			Scheme: "doi",
			Value:  "https://doi.org/10.1000/ARTICLE",
		},
		Observation: HTTPObservation{
			FinalURL:   finalURL,
			HTTPStatus: 200,
			RedirectChain: []RedirectHop{
				{
					URL:        sourceURL,
					StatusCode: 302,
					Location:   finalURL,
				},
				{
					URL:        finalURL,
					StatusCode: 200,
				},
			},
			IdentifierEvidence: testIdentifierEvidence(
				StableIdentifier{
					Scheme: "doi",
					Value:  "DOI:10.1000/article",
				},
			),
		},
		CheckedAt:       checkedAt,
		ExpiresAt:       checkedAt.Add(24 * time.Hour),
		EvaluatedAt:     checkedAt,
		VerifierVersion: "official-url-verifier/v1",
		Policy: Policy{
			Version:      "official-url/v1",
			MaxRedirects: 3,
		},
	}
}

func testIdentifierEvidence(
	identifier StableIdentifier,
) []IdentifierEvidence {
	return []IdentifierEvidence{{
		Identifier: identifier,
		SourceKind: IdentifierSourceHTMLMeta,
		SourcePath: `meta[name="citation_doi"]@content`,
		RawValue:   identifier.Value,
	}}
}

func assertFailure(
	t *testing.T,
	input VerificationInput,
	want FailureCode,
) {
	t.Helper()

	verification, err := Verify(input)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.State != VerificationStateFailed ||
		verification.FailureCode != want {
		t.Fatalf("verification = %#v, want failure %q", verification, want)
	}
}
