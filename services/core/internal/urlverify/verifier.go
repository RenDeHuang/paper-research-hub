package urlverify

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const MaximumRedirects = 10

type Policy struct {
	Version      string
	MaxRedirects int
}

func (policy Policy) Validate() error {
	if !exactNonEmpty(policy.Version) {
		return errors.New(
			"official URL policy requires an exact version",
		)
	}
	if policy.MaxRedirects < 0 ||
		policy.MaxRedirects > MaximumRedirects {
		return fmt.Errorf(
			"official URL redirect bound must be between 0 and %d",
			MaximumRedirects,
		)
	}
	return nil
}

type VerificationInput struct {
	Candidate          Candidate
	ExpectedIdentifier StableIdentifier
	Observation        HTTPObservation
	CheckedAt          time.Time
	ExpiresAt          time.Time
	EvaluatedAt        time.Time
	VerifierVersion    string
	Policy             Policy
}

func Verify(input VerificationInput) (Verification, error) {
	if err := input.Candidate.Validate(); err != nil {
		return Verification{}, err
	}
	if !exactNonEmpty(input.Candidate.ID) {
		return Verification{}, errors.New(
			"official URL verification requires a persisted candidate",
		)
	}
	if err := input.Policy.Validate(); err != nil {
		return Verification{}, err
	}
	if !exactNonEmpty(input.VerifierVersion) {
		return Verification{}, errors.New(
			"official URL verification requires an exact verifier version",
		)
	}
	if input.CheckedAt.IsZero() ||
		input.EvaluatedAt.IsZero() ||
		!input.ExpiresAt.After(input.CheckedAt) {
		return Verification{}, errors.New(
			"official URL verification requires checked_at, evaluated_at, and a future expiry",
		)
	}
	expected, err := NormalizeStableIdentifier(input.ExpectedIdentifier)
	if err != nil {
		return Verification{}, fmt.Errorf(
			"normalize expected stable identifier: %w",
			err,
		)
	}
	if expected != input.Candidate.Identifier {
		return Verification{}, errors.New(
			"expected identifier must equal the source candidate identifier",
		)
	}
	if input.Observation.FailureCode != FailureNone &&
		!observerFailureCode(input.Observation.FailureCode) {
		return Verification{}, errors.New(
			"HTTP observation provided an invalid failure code",
		)
	}
	finalURL, err := parseAbsoluteHTTPURL(input.Observation.FinalURL)
	if err != nil {
		return Verification{}, fmt.Errorf(
			"parse observed final URL: %w",
			err,
		)
	}
	emptyObserverFailure := input.Observation.FailureCode != FailureNone &&
		len(input.Observation.RedirectChain) == 0
	if emptyObserverFailure {
		if input.Observation.HTTPStatus != 0 ||
			input.Observation.FinalURL != input.Candidate.URL {
			return Verification{}, errors.New(
				"HTTP observer failure without a response must remain bound to the candidate",
			)
		}
	} else if input.Observation.HTTPStatus < 100 ||
		input.Observation.HTTPStatus > 599 {
		return Verification{}, errors.New(
			"observed HTTP status must be between 100 and 599",
		)
	}
	if len(input.Observation.RedirectChain) == 0 &&
		input.Observation.FailureCode == FailureNone {
		return Verification{}, errors.New(
			"HTTP observation requires a complete redirect chain",
		)
	}
	observed := make([]StableIdentifier, 0, len(
		input.Observation.IdentifierEvidence,
	))
	evidence := make([]IdentifierEvidence, 0, len(
		input.Observation.IdentifierEvidence,
	))
	for index, item := range input.Observation.IdentifierEvidence {
		normalized, normalizeErr := NormalizeStableIdentifier(
			item.Identifier,
		)
		if normalizeErr != nil {
			return Verification{}, fmt.Errorf(
				"normalize observed stable identifier %d: %w",
				index,
				normalizeErr,
			)
		}
		item.Identifier = normalized
		if evidenceErr := item.Validate(); evidenceErr != nil {
			return Verification{}, fmt.Errorf(
				"validate observed stable identifier evidence %d: %w",
				index,
				evidenceErr,
			)
		}
		observed = append(observed, normalized)
		evidence = append(evidence, item)
	}
	if err := input.Observation.ResponseMetadata.Validate(); err != nil {
		return Verification{}, err
	}

	verification := Verification{
		CandidateID: input.Candidate.ID,
		WorkID:      input.Candidate.WorkID,
		Channel:     input.Candidate.Channel,
		LinkRole:    input.Candidate.LinkRole,
		SourceURL:   input.Candidate.URL,
		FinalURL:    input.Observation.FinalURL,
		RedirectChain: append(
			[]RedirectHop{},
			input.Observation.RedirectChain...,
		),
		HTTPStatus:          input.Observation.HTTPStatus,
		ExpectedIdentifier:  expected,
		ObservedIdentifiers: observed,
		IdentifierEvidence:  evidence,
		ResponseMetadata:    input.Observation.ResponseMetadata,
		CheckedAt:           normalizeTime(input.CheckedAt),
		ExpiresAt:           normalizeTime(input.ExpiresAt),
		VerifierVersion:     input.VerifierVersion,
		PolicyVersion:       input.Policy.Version,
	}

	if input.Observation.FailureCode != FailureNone {
		return failedVerification(
			verification,
			input.Observation.FailureCode,
		)
	}
	if !projectableRole(verification.LinkRole) {
		return failedVerification(
			verification,
			FailureIneligibleLinkRole,
		)
	}
	redirects := 0
	for _, hop := range verification.RedirectChain {
		if redirectStatus(hop.StatusCode) {
			redirects++
		}
	}
	if redirects > input.Policy.MaxRedirects {
		return failedVerification(
			verification,
			FailureRedirectLimitExceeded,
		)
	}
	chainFailure, chainErr := validateRedirectChain(
		input.Candidate.URL,
		input.Observation,
	)
	if chainErr != nil {
		return Verification{}, chainErr
	}
	if chainFailure != FailureNone {
		return failedVerification(verification, chainFailure)
	}
	if !acceptableFinalURL(finalURL) {
		return failedVerification(
			verification,
			FailureNonHTTPSFinalURL,
		)
	}
	if verification.HTTPStatus < 200 ||
		verification.HTTPStatus >= 300 {
		return failedVerification(verification, FailureHTTPStatus)
	}
	if len(observed) == 0 {
		return failedVerification(
			verification,
			FailureMissingStableIdentifier,
		)
	}
	for _, identifier := range observed {
		if identifier == expected {
			matched := identifier
			verification.MatchedIdentifier = &matched
			verification.IdentifierMatch = true
			break
		}
	}
	if !verification.IdentifierMatch {
		return failedVerification(
			verification,
			FailureIdentifierMismatch,
		)
	}
	if !input.EvaluatedAt.Before(input.ExpiresAt) {
		return failedVerification(verification, FailureExpired)
	}

	verification.State = VerificationStateVerified
	return sealVerifiedResult(verification)
}

func failedVerification(
	verification Verification,
	code FailureCode,
) (Verification, error) {
	verification.State = VerificationStateFailed
	verification.FailureCode = code
	return sealVerifiedResult(verification)
}

func sealVerifiedResult(
	verification Verification,
) (Verification, error) {
	if err := verification.Validate(); err != nil {
		return Verification{}, err
	}
	integrity, err := verificationIntegrity(verification)
	if err != nil {
		return Verification{}, err
	}
	verification.integrity = integrity
	return verification.clone(), nil
}

func verificationIntegrity(verification Verification) ([32]byte, error) {
	payload := struct {
		CandidateID         string
		WorkID              string
		Channel             string
		LinkRole            string
		SourceURL           string
		FinalURL            string
		RedirectChain       []RedirectHop
		HTTPStatus          int
		ExpectedIdentifier  StableIdentifier
		ObservedIdentifiers []StableIdentifier
		IdentifierEvidence  []IdentifierEvidence
		ResponseMetadata    ResponseMetadata
		MatchedIdentifier   *StableIdentifier
		IdentifierMatch     bool
		State               VerificationState
		CheckedAt           time.Time
		ExpiresAt           time.Time
		VerifierVersion     string
		PolicyVersion       string
		FailureCode         FailureCode
	}{
		CandidateID:         verification.CandidateID,
		WorkID:              verification.WorkID,
		Channel:             string(verification.Channel),
		LinkRole:            string(verification.LinkRole),
		SourceURL:           verification.SourceURL,
		FinalURL:            verification.FinalURL,
		RedirectChain:       verification.RedirectChain,
		HTTPStatus:          verification.HTTPStatus,
		ExpectedIdentifier:  verification.ExpectedIdentifier,
		ObservedIdentifiers: verification.ObservedIdentifiers,
		IdentifierEvidence:  verification.IdentifierEvidence,
		ResponseMetadata:    verification.ResponseMetadata,
		MatchedIdentifier:   verification.MatchedIdentifier,
		IdentifierMatch:     verification.IdentifierMatch,
		State:               verification.State,
		CheckedAt:           verification.CheckedAt,
		ExpiresAt:           verification.ExpiresAt,
		VerifierVersion:     verification.VerifierVersion,
		PolicyVersion:       verification.PolicyVersion,
		FailureCode:         verification.FailureCode,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return [32]byte{}, fmt.Errorf(
			"encode official URL verification integrity payload: %w",
			err,
		)
	}
	return sha256.Sum256(encoded), nil
}

func verificationIntegrityValid(verification Verification) bool {
	expected, err := verificationIntegrity(verification)
	return err == nil && expected == verification.integrity
}

func validateRedirectChain(
	sourceURL string,
	observation HTTPObservation,
) (FailureCode, error) {
	sourceCanonical, err := canonicalHTTPURL(sourceURL)
	if err != nil {
		return FailureNone, err
	}
	finalCanonical, err := canonicalHTTPURL(observation.FinalURL)
	if err != nil {
		return FailureNone, err
	}

	seen := make(map[string]struct{}, len(observation.RedirectChain))
	for index, hop := range observation.RedirectChain {
		hopCanonical, canonicalErr := canonicalHTTPURL(hop.URL)
		if canonicalErr != nil {
			return FailureNone, fmt.Errorf(
				"parse redirect hop %d URL: %w",
				index,
				canonicalErr,
			)
		}
		if hop.StatusCode < 100 || hop.StatusCode > 599 {
			return FailureNone, fmt.Errorf(
				"redirect hop %d has invalid HTTP status %d",
				index,
				hop.StatusCode,
			)
		}
		if _, duplicate := seen[hopCanonical]; duplicate {
			return FailureRedirectLoop, nil
		}
		seen[hopCanonical] = struct{}{}

		if index == 0 && hopCanonical != sourceCanonical {
			return FailureInvalidRedirectChain, nil
		}
		last := index == len(observation.RedirectChain)-1
		if last &&
			(hopCanonical != finalCanonical ||
				hop.StatusCode != observation.HTTPStatus) {
			return FailureInvalidRedirectChain, nil
		}
		if redirectStatus(hop.StatusCode) {
			if !exactNonEmpty(hop.Location) {
				return FailureInvalidRedirectChain, nil
			}
			current, parseErr := parseAbsoluteHTTPURL(hop.URL)
			if parseErr != nil {
				return FailureNone, parseErr
			}
			if parseErr := ValidateRawPercentEncoding(
				hop.Location,
			); parseErr != nil {
				return FailureInvalidRedirectChain, nil
			}
			location, parseErr := url.Parse(hop.Location)
			if parseErr != nil {
				return FailureInvalidRedirectChain, nil
			}
			nextURL := current.ResolveReference(location)
			nextCanonical, canonicalErr := canonicalHTTPURL(nextURL.String())
			if canonicalErr != nil {
				return FailureInvalidRedirectChain, nil
			}
			if _, loop := seen[nextCanonical]; loop {
				return FailureRedirectLoop, nil
			}
			if last {
				return FailureInvalidRedirectChain, nil
			}
			observedNext, canonicalErr := canonicalHTTPURL(
				observation.RedirectChain[index+1].URL,
			)
			if canonicalErr != nil {
				return FailureNone, canonicalErr
			}
			if nextCanonical != observedNext {
				return FailureInvalidRedirectChain, nil
			}
		} else {
			if !last || hop.Location != "" {
				return FailureInvalidRedirectChain, nil
			}
		}
	}
	return FailureNone, nil
}

func redirectStatus(statusCode int) bool {
	switch statusCode {
	case 301, 302, 303, 307, 308:
		return true
	default:
		return false
	}
}

func canonicalHTTPURL(value string) (string, error) {
	parsed, err := parseAbsoluteHTTPURL(value)
	if err != nil {
		return "", err
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") ||
		(parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	} else {
		parsed.Host = host
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String(), nil
}

func normalizeTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}
