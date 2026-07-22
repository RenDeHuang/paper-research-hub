package urlverify

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

var (
	ErrCandidateProvenance = errors.New(
		"official URL candidate requires source provenance",
	)
	ErrCandidateURL  = errors.New("invalid official URL candidate")
	ErrCandidateRole = errors.New(
		"official URL candidate role does not match content channel",
	)
)

var (
	stableIdentifierSchemePattern = regexp.MustCompile(
		`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`,
	)
	acceptedHTTPAuthorityPattern = regexp.MustCompile(
		`^(?:[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?::[0-9]+)?|\[[0-9A-Fa-f:.]+\](?::[0-9]+)?)$`,
	)
	pmidPattern  = regexp.MustCompile(`^[0-9]+$`)
	pmcidPattern = regexp.MustCompile(`^PMC[0-9]+$`)
)

type LinkRole string

const (
	LinkRoleOfficialArticle    LinkRole = "official_article"
	LinkRoleDOIResolver        LinkRole = "doi_url"
	LinkRoleOfficialPreprint   LinkRole = "official_preprint"
	LinkRoleOfficialProceeding LinkRole = "official_proceeding"
	LinkRoleAuxiliary          LinkRole = "auxiliary"
)

func ParseLinkRole(value string) (LinkRole, error) {
	role := LinkRole(value)
	switch role {
	case LinkRoleOfficialArticle,
		LinkRoleDOIResolver,
		LinkRoleOfficialPreprint,
		LinkRoleOfficialProceeding,
		LinkRoleAuxiliary:
		return role, nil
	default:
		return "", fmt.Errorf("invalid official URL link role %q", value)
	}
}

type Candidate struct {
	ID                    string
	WorkID                string
	ProjectionAssertionID string
	NormalizedAssertionID string
	SourceRecordID        string
	SourcePath            string
	Channel               scope.ContentChannel
	LinkRole              LinkRole
	URL                   string
	ParserVersion         string
	Identifier            StableIdentifier
	AssertedAt            time.Time
}

func (candidate Candidate) Validate() error {
	if candidate.ID != "" && candidate.ID != strings.TrimSpace(candidate.ID) {
		return errors.New("official URL candidate ID must be exact")
	}
	if !exactNonEmpty(candidate.WorkID) ||
		!exactNonEmpty(candidate.ProjectionAssertionID) ||
		!exactNonEmpty(candidate.NormalizedAssertionID) ||
		!exactNonEmpty(candidate.SourceRecordID) ||
		!exactNonEmpty(candidate.SourcePath) ||
		!exactNonEmpty(candidate.ParserVersion) {
		return ErrCandidateProvenance
	}
	if _, err := scope.ParseContentChannel(string(candidate.Channel)); err != nil {
		return err
	}
	if _, err := ParseLinkRole(string(candidate.LinkRole)); err != nil {
		return err
	}
	if !roleAllowedForChannel(candidate.Channel, candidate.LinkRole) {
		return ErrCandidateRole
	}
	sourceURL, err := parseAbsoluteHTTPURL(candidate.URL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCandidateURL, err)
	}
	if sourceURL.Scheme != "https" {
		return fmt.Errorf(
			"%w: source URL scheme must be HTTPS",
			ErrCandidateURL,
		)
	}
	if candidate.AssertedAt.IsZero() {
		return ErrCandidateProvenance
	}
	identifier, err := NormalizeStableIdentifier(candidate.Identifier)
	if err != nil || identifier != candidate.Identifier {
		return errors.New(
			"official URL candidate requires a normalized stable identifier",
		)
	}
	return nil
}

func roleAllowedForChannel(
	channel scope.ContentChannel,
	role LinkRole,
) bool {
	switch channel {
	case scope.ContentChannelJournalPublished,
		scope.ContentChannelAcceptedEarly:
		return role == LinkRoleOfficialArticle ||
			role == LinkRoleDOIResolver ||
			role == LinkRoleAuxiliary
	case scope.ContentChannelPreprint:
		return role == LinkRoleOfficialPreprint ||
			role == LinkRoleDOIResolver ||
			role == LinkRoleAuxiliary
	case scope.ContentChannelConferenceProceeding:
		return role == LinkRoleOfficialProceeding ||
			role == LinkRoleDOIResolver ||
			role == LinkRoleAuxiliary
	default:
		return false
	}
}

func projectableRole(role LinkRole) bool {
	switch role {
	case LinkRoleOfficialArticle,
		LinkRoleDOIResolver,
		LinkRoleOfficialPreprint,
		LinkRoleOfficialProceeding:
		return true
	default:
		return false
	}
}

type StableIdentifier struct {
	Scheme string `json:"scheme"`
	Value  string `json:"value"`
}

func NormalizeStableIdentifier(
	identifier StableIdentifier,
) (StableIdentifier, error) {
	scheme := strings.ToLower(strings.TrimSpace(identifier.Scheme))
	if !stableIdentifierSchemePattern.MatchString(scheme) {
		return StableIdentifier{}, fmt.Errorf(
			"invalid stable identifier scheme %q",
			identifier.Scheme,
		)
	}

	switch scheme {
	case string(paper.SchemeDOI),
		string(paper.SchemeArXiv),
		string(paper.SchemeOpenReview),
		string(paper.SchemeSemanticScholar),
		string(paper.SchemeOpenAlex):
		normalized, err := paper.NewIdentifier(
			paper.Scheme(scheme),
			identifier.Value,
		)
		if err != nil {
			return StableIdentifier{}, err
		}
		return StableIdentifier{
			Scheme: scheme,
			Value:  normalized.Value(),
		}, nil
	case "pmid":
		value := strings.TrimSpace(identifier.Value)
		if !pmidPattern.MatchString(value) {
			return StableIdentifier{}, fmt.Errorf(
				"invalid PMID %q",
				identifier.Value,
			)
		}
		return StableIdentifier{Scheme: scheme, Value: value}, nil
	case "pmcid":
		value := strings.ToUpper(strings.TrimSpace(identifier.Value))
		if !pmcidPattern.MatchString(value) {
			return StableIdentifier{}, fmt.Errorf(
				"invalid PMCID %q",
				identifier.Value,
			)
		}
		return StableIdentifier{Scheme: scheme, Value: value}, nil
	default:
		value := strings.TrimSpace(identifier.Value)
		if containsSpaceOrControl(value) {
			return StableIdentifier{}, fmt.Errorf(
				"invalid stable identifier value %q",
				identifier.Value,
			)
		}
		return StableIdentifier{Scheme: scheme, Value: value}, nil
	}
}

type RedirectHop struct {
	URL        string           `json:"url"`
	StatusCode int              `json:"status_code"`
	Location   string           `json:"location,omitempty"`
	Metadata   ResponseMetadata `json:"metadata"`
}

type ResponseMetadata struct {
	ContentType  string `json:"content_type"`
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
}

func (metadata ResponseMetadata) Validate() error {
	for name, value := range map[string]string{
		"content-type":  metadata.ContentType,
		"etag":          metadata.ETag,
		"last-modified": metadata.LastModified,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf(
				"official URL response %s metadata contains a line break",
				name,
			)
		}
	}
	return nil
}

const IdentifierSourceHTMLMeta = "html_meta"

type IdentifierEvidence struct {
	Identifier StableIdentifier `json:"identifier"`
	SourceKind string           `json:"source_kind"`
	SourcePath string           `json:"source_path"`
	RawValue   string           `json:"raw_value"`
}

func (evidence IdentifierEvidence) Validate() error {
	normalized, err := NormalizeStableIdentifier(evidence.Identifier)
	if err != nil || normalized != evidence.Identifier {
		return errors.New(
			"official URL identifier evidence requires a normalized identifier",
		)
	}
	if evidence.SourceKind != IdentifierSourceHTMLMeta {
		return fmt.Errorf(
			"unsupported official URL identifier evidence source kind %q",
			evidence.SourceKind,
		)
	}
	metaName, found := htmlMetaSourcePaths[evidence.SourcePath]
	if !found {
		return fmt.Errorf(
			"unsupported official URL identifier evidence source path %q",
			evidence.SourcePath,
		)
	}
	if !identifierSchemeAllowedForMeta(metaName, evidence.Identifier.Scheme) {
		return errors.New(
			"official URL identifier evidence scheme conflicts with its source path",
		)
	}
	if strings.TrimSpace(evidence.RawValue) == "" ||
		strings.ContainsRune(evidence.RawValue, '\x00') {
		return errors.New(
			"official URL identifier evidence requires a raw value",
		)
	}
	rawScheme, rawValue, accepted := stableIdentifierFromMeta(
		metaName,
		evidence.RawValue,
	)
	if !accepted {
		return errors.New(
			"official URL identifier evidence raw value lacks required source prefix",
		)
	}
	rawIdentifier, err := NormalizeStableIdentifier(StableIdentifier{
		Scheme: rawScheme,
		Value:  rawValue,
	})
	if err != nil || rawIdentifier != evidence.Identifier {
		return errors.New(
			"official URL identifier evidence raw value conflicts with its identifier",
		)
	}
	return nil
}

type HTTPObservation struct {
	FinalURL           string
	HTTPStatus         int
	RedirectChain      []RedirectHop
	IdentifierEvidence []IdentifierEvidence
	ResponseMetadata   ResponseMetadata
	FailureCode        FailureCode
}

type VerificationState string

const (
	VerificationStateVerified VerificationState = "verified"
	VerificationStateFailed   VerificationState = "failed"
)

type FailureCode string

const (
	FailureNone                    FailureCode = ""
	FailureInvalidRedirectChain    FailureCode = "invalid_redirect_chain"
	FailureRedirectLimitExceeded   FailureCode = "redirect_limit_exceeded"
	FailureRedirectLoop            FailureCode = "redirect_loop"
	FailureNonHTTPSFinalURL        FailureCode = "non_https_final_url"
	FailureHTTPStatus              FailureCode = "http_status_not_successful"
	FailureIneligibleLinkRole      FailureCode = "ineligible_link_role"
	FailureMissingStableIdentifier FailureCode = "missing_stable_identifier"
	FailureIdentifierMismatch      FailureCode = "identifier_mismatch"
	FailureExpired                 FailureCode = "expired"
	FailureHostNotAllowed          FailureCode = "host_not_allowed"
	FailureUnsafeResolvedAddress   FailureCode = "unsafe_resolved_address"
	FailureDNSResolution           FailureCode = "dns_resolution_failed"
	FailureNetwork                 FailureCode = "network_error"
	FailureResponseBodyLimit       FailureCode = "response_body_limit_exceeded"
)

type Verification struct {
	ID                  string
	CandidateID         string
	WorkID              string
	Channel             scope.ContentChannel
	LinkRole            LinkRole
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
	integrity           [32]byte
}

func (verification Verification) Validate() error {
	if verification.ID != "" &&
		verification.ID != strings.TrimSpace(verification.ID) {
		return errors.New("official URL verification ID must be exact")
	}
	for field, value := range map[string]string{
		"candidate ID":     verification.CandidateID,
		"Work ID":          verification.WorkID,
		"verifier version": verification.VerifierVersion,
		"policy version":   verification.PolicyVersion,
	} {
		if !exactNonEmpty(value) {
			return fmt.Errorf("official URL verification requires exact %s", field)
		}
	}
	if _, err := scope.ParseContentChannel(string(verification.Channel)); err != nil {
		return err
	}
	if _, err := ParseLinkRole(string(verification.LinkRole)); err != nil {
		return err
	}
	if !roleAllowedForChannel(verification.Channel, verification.LinkRole) {
		return ErrCandidateRole
	}
	if err := ValidateSourceHTTPSURL(verification.SourceURL); err != nil {
		return fmt.Errorf("invalid verification source URL: %w", err)
	}
	final, err := parseAbsoluteHTTPURL(verification.FinalURL)
	if err != nil {
		return fmt.Errorf("invalid verification final URL: %w", err)
	}
	emptyObserverFailure := verification.State ==
		VerificationStateFailed &&
		observerFailureCode(verification.FailureCode) &&
		len(verification.RedirectChain) == 0
	if len(verification.RedirectChain) == 0 && !emptyObserverFailure {
		return errors.New(
			"official URL verification requires a redirect chain",
		)
	}
	if len(verification.RedirectChain) > MaximumRedirects+1 {
		return errors.New(
			"official URL verification redirect chain exceeds the maximum bound",
		)
	}
	if emptyObserverFailure {
		if verification.HTTPStatus != 0 {
			return errors.New(
				"observer failure without an HTTP response requires status zero",
			)
		}
	} else if verification.HTTPStatus < 100 ||
		verification.HTTPStatus > 599 {
		return errors.New("official URL verification HTTP status is invalid")
	}
	sourceCanonical, err := canonicalHTTPURL(verification.SourceURL)
	if err != nil {
		return err
	}
	finalCanonical, err := canonicalHTTPURL(verification.FinalURL)
	if err != nil {
		return err
	}
	if emptyObserverFailure && finalCanonical != sourceCanonical {
		return errors.New(
			"observer failure without an HTTP response must remain bound to its source URL",
		)
	}
	for index, hop := range verification.RedirectChain {
		hopCanonical, hopErr := canonicalHTTPURL(hop.URL)
		if hopErr != nil {
			return fmt.Errorf(
				"invalid official URL redirect hop %d: %w",
				index,
				hopErr,
			)
		}
		if hop.StatusCode < 100 || hop.StatusCode > 599 {
			return fmt.Errorf(
				"invalid official URL redirect hop %d status",
				index,
			)
		}
		if strings.ContainsAny(hop.Location, "\r\n") {
			return fmt.Errorf(
				"invalid official URL redirect hop %d location",
				index,
			)
		}
		if err := hop.Metadata.Validate(); err != nil {
			return err
		}
		if index == 0 && hopCanonical != sourceCanonical {
			return errors.New(
				"official URL redirect chain is not bound to its source URL",
			)
		}
		if index == len(verification.RedirectChain)-1 &&
			(hopCanonical != finalCanonical ||
				hop.StatusCode != verification.HTTPStatus) {
			return errors.New(
				"official URL redirect chain is not bound to its final response",
			)
		}
	}
	expected, err := NormalizeStableIdentifier(
		verification.ExpectedIdentifier,
	)
	if err != nil || expected != verification.ExpectedIdentifier {
		return errors.New(
			"official URL verification expected identifier is not normalized",
		)
	}
	for _, identifier := range verification.ObservedIdentifiers {
		normalized, normalizeErr := NormalizeStableIdentifier(identifier)
		if normalizeErr != nil || normalized != identifier {
			return errors.New(
				"official URL verification observed identifier is not normalized",
			)
		}
	}
	if len(verification.IdentifierEvidence) !=
		len(verification.ObservedIdentifiers) {
		return errors.New(
			"official URL verification identifier evidence is incomplete",
		)
	}
	for index, evidence := range verification.IdentifierEvidence {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf(
				"invalid official URL identifier evidence %d: %w",
				index,
				err,
			)
		}
		if evidence.Identifier != verification.ObservedIdentifiers[index] {
			return errors.New(
				"official URL verification evidence conflicts with observed identifiers",
			)
		}
	}
	if err := verification.ResponseMetadata.Validate(); err != nil {
		return err
	}
	if verification.MatchedIdentifier != nil {
		normalized, normalizeErr := NormalizeStableIdentifier(
			*verification.MatchedIdentifier,
		)
		if normalizeErr != nil || normalized != *verification.MatchedIdentifier {
			return errors.New(
				"official URL verification matched identifier is not normalized",
			)
		}
	}
	if verification.CheckedAt.IsZero() ||
		!verification.ExpiresAt.After(verification.CheckedAt) {
		return errors.New(
			"official URL verification requires checked_at before expires_at",
		)
	}

	switch verification.State {
	case VerificationStateVerified:
		if verification.FailureCode != FailureNone ||
			!projectableRole(verification.LinkRole) ||
			!verification.IdentifierMatch ||
			verification.MatchedIdentifier == nil ||
			*verification.MatchedIdentifier != expected ||
			verification.HTTPStatus < 200 ||
			verification.HTTPStatus >= 300 ||
			!acceptableFinalURL(final) {
			return errors.New("invalid verified official URL assertion")
		}
	case VerificationStateFailed:
		if !validFailureCode(verification.FailureCode) {
			return errors.New(
				"failed official URL verification requires a valid failure code",
			)
		}
		if emptyObserverFailure &&
			(len(verification.ObservedIdentifiers) != 0 ||
				len(verification.IdentifierEvidence) != 0 ||
				verification.MatchedIdentifier != nil ||
				verification.IdentifierMatch ||
				verification.ResponseMetadata != (ResponseMetadata{})) {
			return errors.New(
				"observer failure without an HTTP response must not forge response evidence",
			)
		}
	default:
		return fmt.Errorf(
			"invalid official URL verification state %q",
			verification.State,
		)
	}
	return nil
}

func (verification Verification) Active(at time.Time) bool {
	return verification.State == VerificationStateVerified &&
		!at.IsZero() &&
		!at.Before(verification.CheckedAt) &&
		at.Before(verification.ExpiresAt)
}

func (verification Verification) clone() Verification {
	verification.RedirectChain = slices.Clone(verification.RedirectChain)
	verification.ObservedIdentifiers = slices.Clone(
		verification.ObservedIdentifiers,
	)
	verification.IdentifierEvidence = slices.Clone(
		verification.IdentifierEvidence,
	)
	if verification.MatchedIdentifier != nil {
		matched := *verification.MatchedIdentifier
		verification.MatchedIdentifier = &matched
	}
	return verification
}

type OfficialLink struct {
	ID                string
	WorkID            string
	Channel           scope.ContentChannel
	LinkRole          LinkRole
	VerificationID    string
	URL               string
	ProjectionVersion int64
	ProjectedAt       time.Time
	ExpiresAt         time.Time
}

func (link OfficialLink) Validate() error {
	for field, value := range map[string]string{
		"ID":              link.ID,
		"Work ID":         link.WorkID,
		"verification ID": link.VerificationID,
	} {
		if !exactNonEmpty(value) {
			return fmt.Errorf("official link requires exact %s", field)
		}
	}
	if _, err := scope.ParseContentChannel(string(link.Channel)); err != nil {
		return err
	}
	if _, err := ParseLinkRole(string(link.LinkRole)); err != nil {
		return err
	}
	if !roleAllowedForChannel(link.Channel, link.LinkRole) {
		return ErrCandidateRole
	}
	if !projectableRole(link.LinkRole) {
		return errors.New("official link role is not projectable")
	}
	final, err := parseAbsoluteHTTPURL(link.URL)
	if err != nil || final.Scheme != "https" {
		return errors.New("official link requires an HTTPS final URL")
	}
	if link.ProjectionVersion <= 0 {
		return errors.New("official link projection version must be positive")
	}
	if link.ProjectedAt.IsZero() || !link.ProjectedAt.Before(link.ExpiresAt) {
		return errors.New("official link must be projected before expiry")
	}
	return nil
}

func (link OfficialLink) Active(at time.Time) bool {
	return !at.IsZero() &&
		!at.Before(link.ProjectedAt) &&
		at.Before(link.ExpiresAt)
}

func exactNonEmpty(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func containsSpaceOrControl(value string) bool {
	return value == "" || strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) >= 0
}

func ValidateRawPercentEncoding(value string) error {
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			continue
		}
		if index+2 >= len(value) ||
			!isASCIIHex(value[index+1]) ||
			!isASCIIHex(value[index+2]) {
			return errors.New(
				"raw percent escape must contain exactly two hexadecimal digits",
			)
		}
		index += 2
	}
	return nil
}

func isASCIIHex(value byte) bool {
	return value >= '0' && value <= '9' ||
		value >= 'A' && value <= 'F' ||
		value >= 'a' && value <= 'f'
}

func ValidateSourceHTTPSURL(value string) error {
	parsed, err := parseAbsoluteHTTPURL(value)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" {
		return errors.New("source URL scheme must be HTTPS")
	}
	return nil
}

func parseAbsoluteHTTPURL(value string) (*url.URL, error) {
	if value == "" ||
		value != strings.TrimSpace(value) ||
		containsSpaceOrControl(value) {
		return nil, errors.New("URL must be exact and non-empty")
	}
	if err := ValidateRawPercentEncoding(value); err != nil {
		return nil, err
	}
	if strings.Contains(value, "#") {
		return nil, errors.New("URL fragments are forbidden")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("URL scheme must be HTTP or HTTPS")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, errors.New("URL must be absolute")
	}
	if !acceptedHTTPAuthorityPattern.MatchString(parsed.Host) {
		return nil, errors.New("URL authority is outside the accepted subset")
	}
	if parsed.User != nil {
		return nil, errors.New("URL user information is forbidden")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return nil, errors.New("URL fragments are forbidden")
	}
	return parsed, nil
}

func acceptableFinalURL(parsed *url.URL) bool {
	return parsed != nil && parsed.Scheme == "https"
}

var htmlMetaSourcePaths = map[string]string{
	`meta[name="citation_doi"]@content`:      "citation_doi",
	`meta[name="dc.identifier"]@content`:     "dc.identifier",
	`meta[name="dc.identifier.doi"]@content`: "dc.identifier.doi",
	`meta[name="prism.doi"]@content`:         "prism.doi",
	`meta[name="citation_pmid"]@content`:     "citation_pmid",
	`meta[name="citation_pmcid"]@content`:    "citation_pmcid",
	`meta[name="citation_arxiv_id"]@content`: "citation_arxiv_id",
}

func identifierSchemeAllowedForMeta(metaName string, scheme string) bool {
	switch metaName {
	case "citation_doi", "dc.identifier.doi", "prism.doi":
		return scheme == "doi"
	case "citation_pmid":
		return scheme == "pmid"
	case "citation_pmcid":
		return scheme == "pmcid"
	case "citation_arxiv_id":
		return scheme == "arxiv"
	case "dc.identifier":
		return scheme == "doi" ||
			scheme == "pmid" ||
			scheme == "pmcid" ||
			scheme == "arxiv"
	default:
		return false
	}
}

func validFailureCode(code FailureCode) bool {
	switch code {
	case FailureInvalidRedirectChain,
		FailureRedirectLimitExceeded,
		FailureRedirectLoop,
		FailureNonHTTPSFinalURL,
		FailureHTTPStatus,
		FailureIneligibleLinkRole,
		FailureMissingStableIdentifier,
		FailureIdentifierMismatch,
		FailureExpired,
		FailureHostNotAllowed,
		FailureUnsafeResolvedAddress,
		FailureDNSResolution,
		FailureNetwork,
		FailureResponseBodyLimit:
		return true
	default:
		return false
	}
}

func observerFailureCode(code FailureCode) bool {
	switch code {
	case FailureNonHTTPSFinalURL,
		FailureHostNotAllowed,
		FailureUnsafeResolvedAddress,
		FailureDNSResolution,
		FailureNetwork,
		FailureResponseBodyLimit:
		return true
	default:
		return false
	}
}
