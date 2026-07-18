package biomed

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	researchscope "github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

const BiomedicalPublicEligibilityPolicyVersion = "biomedical-public-eligibility/v1"

const ResearchDomainRegistryVersion = researchscope.ResearchDomainRegistryVersion

type PublicEligibilityDecision string

const (
	PublicEligibilityDecisionAccepted PublicEligibilityDecision = "accepted"
	PublicEligibilityDecisionRejected PublicEligibilityDecision = "rejected"
	PublicEligibilityDecisionMissing  PublicEligibilityDecision = "missing"
)

func (decision PublicEligibilityDecision) Valid() bool {
	switch decision {
	case PublicEligibilityDecisionAccepted,
		PublicEligibilityDecisionRejected,
		PublicEligibilityDecisionMissing:
		return true
	default:
		return false
	}
}

type PublicEligibilityReason string

const (
	PublicEligibilityReasonVenueMissing             PublicEligibilityReason = "venue_not_bound"
	PublicEligibilityReasonVenueNotVerifiedJournal  PublicEligibilityReason = "venue_not_verified_journal"
	PublicEligibilityReasonMetricYearMissing        PublicEligibilityReason = "metric_year_evidence_missing"
	PublicEligibilityReasonNoExactSubjectMetricLink PublicEligibilityReason = "no_exact_subject_metric_link"
)

func (reason PublicEligibilityReason) missing() bool {
	switch reason {
	case PublicEligibilityReasonVenueMissing,
		PublicEligibilityReasonVenueNotVerifiedJournal,
		PublicEligibilityReasonMetricYearMissing:
		return true
	default:
		return false
	}
}

type PublicEligibilityInput struct {
	WorkID            string
	PolicyVersion     string
	MetricYear        int
	SubjectVersionKey string
	AssessedAt        time.Time
}

func (input PublicEligibilityInput) validate() error {
	if err := validatePublicEligibilityIdentity(
		input.WorkID,
		input.PolicyVersion,
		input.MetricYear,
		input.SubjectVersionKey,
	); err != nil {
		return err
	}
	if input.AssessedAt.IsZero() {
		return errors.New("biomedical public eligibility assessed_at is required")
	}
	return nil
}

func validatePublicEligibilityIdentity(
	workID string,
	policyVersion string,
	metricYear int,
	subjectVersionKey string,
) error {
	if strings.TrimSpace(workID) == "" {
		return errors.New("biomedical public eligibility requires a Work ID")
	}
	if workID != strings.TrimSpace(workID) {
		return errors.New("biomedical public eligibility Work ID must be trimmed")
	}
	if policyVersion != BiomedicalPublicEligibilityPolicyVersion {
		return fmt.Errorf(
			"unsupported biomedical public eligibility policy version %q; expected %s",
			policyVersion,
			BiomedicalPublicEligibilityPolicyVersion,
		)
	}
	if metricYear < 1900 || metricYear > 3000 {
		return fmt.Errorf(
			"biomedical public eligibility metric year %d is outside 1900..3000",
			metricYear,
		)
	}
	if strings.TrimSpace(subjectVersionKey) == "" {
		return errors.New(
			"biomedical public eligibility requires an exact Subject version key",
		)
	}
	if subjectVersionKey != strings.TrimSpace(subjectVersionKey) {
		return errors.New(
			"biomedical public eligibility Subject version key must be trimmed",
		)
	}
	if err := ValidateLegacyBiomedicalSubjectVersion(subjectVersionKey); err != nil {
		return err
	}
	return nil
}

func ValidateLegacyBiomedicalSubjectVersion(version string) error {
	if version == ResearchDomainRegistryVersion {
		return fmt.Errorf(
			"research domain Registry %q requires scope channel admission and cannot use legacy biomedical eligibility",
			version,
		)
	}
	return nil
}

type PublicEligibilityVenueEvidence struct {
	VenueID string `json:"venue_id"`
	ISSNL   string `json:"issn_l,omitempty"`
	ISSN    string `json:"issn,omitempty"`
	EISSN   string `json:"eissn,omitempty"`
}

func (evidence PublicEligibilityVenueEvidence) validate() error {
	if strings.TrimSpace(evidence.VenueID) == "" {
		return errors.New("verified journal Venue evidence requires a Venue ID")
	}
	if evidence.VenueID != strings.TrimSpace(evidence.VenueID) {
		return errors.New("verified journal Venue ID must be trimmed")
	}
	for field, value := range map[string]string{
		"ISSN-L": evidence.ISSNL,
		"ISSN":   evidence.ISSN,
		"eISSN":  evidence.EISSN,
	} {
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("verified journal Venue %s must be trimmed", field)
		}
	}
	if evidence.ISSNL == "" && evidence.ISSN == "" && evidence.EISSN == "" {
		return errors.New(
			"verified journal Venue requires strict ISSN evidence",
		)
	}
	return nil
}

func (evidence *PublicEligibilityVenueEvidence) clone() *PublicEligibilityVenueEvidence {
	if evidence == nil {
		return nil
	}
	cloned := *evidence
	return &cloned
}

type PublicEligibilityMetricEvidence struct {
	VenueMetricSnapshotID string `json:"venue_metric_snapshot_id"`
	VenueID               string `json:"venue_id"`
	MetricYear            int    `json:"metric_year"`
	JCRCategory           string `json:"jcr_category"`
}

func (evidence PublicEligibilityMetricEvidence) validate(
	venueID string,
	metricYear int,
) error {
	if strings.TrimSpace(evidence.VenueMetricSnapshotID) == "" {
		return errors.New("journal metric evidence requires a snapshot ID")
	}
	if evidence.VenueMetricSnapshotID !=
		strings.TrimSpace(evidence.VenueMetricSnapshotID) {
		return errors.New("journal metric snapshot ID must be trimmed")
	}
	if evidence.VenueID != venueID {
		return fmt.Errorf(
			"journal metric Venue %q does not match verified Venue %q",
			evidence.VenueID,
			venueID,
		)
	}
	if evidence.MetricYear != metricYear {
		return fmt.Errorf(
			"journal metric year %d does not match declared metric year %d",
			evidence.MetricYear,
			metricYear,
		)
	}
	if strings.TrimSpace(evidence.JCRCategory) == "" {
		return errors.New("journal metric requires a nonblank JCR Category")
	}
	return nil
}

type PublicEligibilitySubjectEvidence struct {
	JournalSubjectMetricID string `json:"journal_subject_metric_id"`
	VenueMetricSnapshotID  string `json:"venue_metric_snapshot_id"`
	VenueID                string `json:"venue_id"`
	MetricYear             int    `json:"metric_year"`
	SubjectVersionID       string `json:"subject_version_id"`
	SubjectID              string `json:"subject_id"`
	SubjectRuleID          string `json:"subject_rule_id"`
	SubjectSlug            string `json:"subject_slug"`
	JCRCategory            string `json:"jcr_category"`
}

func (evidence PublicEligibilitySubjectEvidence) validate(
	load PublicEligibilityLoad,
	metrics map[string]PublicEligibilityMetricEvidence,
) error {
	required := map[string]string{
		"journal Subject metric ID": evidence.JournalSubjectMetricID,
		"Venue metric snapshot ID":  evidence.VenueMetricSnapshotID,
		"Subject version ID":        evidence.SubjectVersionID,
		"Subject ID":                evidence.SubjectID,
		"Subject rule ID":           evidence.SubjectRuleID,
		"Subject slug":              evidence.SubjectSlug,
		"JCR Category":              evidence.JCRCategory,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("controlled Subject evidence requires exact %s", field)
		}
	}
	if load.Venue == nil || evidence.VenueID != load.Venue.VenueID {
		return errors.New(
			"controlled Subject evidence Venue does not match verified journal Venue",
		)
	}
	if evidence.MetricYear != load.MetricYear {
		return fmt.Errorf(
			"controlled Subject evidence metric year %d does not match declared metric year %d",
			evidence.MetricYear,
			load.MetricYear,
		)
	}
	if evidence.SubjectVersionID != load.SubjectVersionID {
		return fmt.Errorf(
			"controlled Subject evidence Subject version %q does not match declared exact Subject version %q",
			evidence.SubjectVersionID,
			load.SubjectVersionID,
		)
	}
	metric, exists := metrics[evidence.VenueMetricSnapshotID]
	if !exists {
		return fmt.Errorf(
			"controlled Subject evidence metric %q is not in declared year evidence",
			evidence.VenueMetricSnapshotID,
		)
	}
	if evidence.JCRCategory != metric.JCRCategory {
		return fmt.Errorf(
			"controlled Subject evidence exact category %q does not equal journal metric category %q",
			evidence.JCRCategory,
			metric.JCRCategory,
		)
	}
	return nil
}

type PublicEligibilityLoad struct {
	WorkID            string
	MetricYear        int
	SubjectVersionID  string
	SubjectVersionKey string
	Venue             *PublicEligibilityVenueEvidence
	Metrics           []PublicEligibilityMetricEvidence
	Matches           []PublicEligibilitySubjectEvidence
	MissingReason     PublicEligibilityReason
}

func (load PublicEligibilityLoad) Clone() PublicEligibilityLoad {
	load.Venue = load.Venue.clone()
	load.Metrics = slices.Clone(load.Metrics)
	load.Matches = slices.Clone(load.Matches)
	return load
}

func (load PublicEligibilityLoad) validate() error {
	if strings.TrimSpace(load.WorkID) == "" ||
		load.WorkID != strings.TrimSpace(load.WorkID) {
		return errors.New("public eligibility evidence requires an exact Work ID")
	}
	if load.MetricYear < 1900 || load.MetricYear > 3000 {
		return fmt.Errorf(
			"public eligibility metric year %d is outside 1900..3000",
			load.MetricYear,
		)
	}
	if strings.TrimSpace(load.SubjectVersionID) == "" ||
		load.SubjectVersionID != strings.TrimSpace(load.SubjectVersionID) {
		return errors.New(
			"public eligibility evidence requires an exact Subject version ID",
		)
	}
	if strings.TrimSpace(load.SubjectVersionKey) == "" ||
		load.SubjectVersionKey != strings.TrimSpace(load.SubjectVersionKey) {
		return errors.New(
			"public eligibility evidence requires an exact Subject version key",
		)
	}

	if load.MissingReason != "" {
		if !load.MissingReason.missing() {
			return fmt.Errorf(
				"invalid missing eligibility reason %q",
				load.MissingReason,
			)
		}
		if len(load.Matches) != 0 {
			return errors.New(
				"missing public eligibility evidence cannot contain an exact Subject match",
			)
		}
		switch load.MissingReason {
		case PublicEligibilityReasonVenueMissing,
			PublicEligibilityReasonVenueNotVerifiedJournal:
			if load.Venue != nil || len(load.Metrics) != 0 {
				return errors.New(
					"missing or unverified Venue cannot carry verified journal evidence",
				)
			}
		case PublicEligibilityReasonMetricYearMissing:
			if load.Venue == nil {
				return errors.New(
					"missing metric-year evidence requires a verified journal Venue",
				)
			}
			if err := load.Venue.validate(); err != nil {
				return err
			}
			if len(load.Metrics) != 0 {
				return errors.New(
					"metric-year missing evidence cannot contain journal metrics",
				)
			}
		}
		return nil
	}

	if load.Venue == nil {
		return errors.New(
			"controlled public eligibility evidence requires a verified journal Venue",
		)
	}
	if err := load.Venue.validate(); err != nil {
		return err
	}
	if len(load.Metrics) == 0 {
		return errors.New(
			"controlled public eligibility evidence requires declared metric-year evidence",
		)
	}
	metrics := make(
		map[string]PublicEligibilityMetricEvidence,
		len(load.Metrics),
	)
	for index, metric := range load.Metrics {
		if err := metric.validate(load.Venue.VenueID, load.MetricYear); err != nil {
			return fmt.Errorf("journal metric evidence %d: %w", index, err)
		}
		if _, duplicate := metrics[metric.VenueMetricSnapshotID]; duplicate {
			return fmt.Errorf(
				"duplicate journal metric evidence %q",
				metric.VenueMetricSnapshotID,
			)
		}
		metrics[metric.VenueMetricSnapshotID] = metric
	}
	links := make(map[string]struct{}, len(load.Matches))
	for index, match := range load.Matches {
		if err := match.validate(load, metrics); err != nil {
			return fmt.Errorf("controlled Subject evidence %d: %w", index, err)
		}
		if _, duplicate := links[match.JournalSubjectMetricID]; duplicate {
			return fmt.Errorf(
				"duplicate controlled Subject evidence %q",
				match.JournalSubjectMetricID,
			)
		}
		links[match.JournalSubjectMetricID] = struct{}{}
	}
	return nil
}

type PublicEligibilityEvidence struct {
	Reason  PublicEligibilityReason            `json:"reason,omitempty"`
	Venue   *PublicEligibilityVenueEvidence    `json:"venue,omitempty"`
	Metrics []PublicEligibilityMetricEvidence  `json:"metrics"`
	Matches []PublicEligibilitySubjectEvidence `json:"matches"`
}

func (evidence PublicEligibilityEvidence) Clone() PublicEligibilityEvidence {
	evidence.Venue = evidence.Venue.clone()
	evidence.Metrics = slices.Clone(evidence.Metrics)
	evidence.Matches = slices.Clone(evidence.Matches)
	return evidence
}

type PublicEligibilityAssessment struct {
	WorkID            string
	PolicyVersion     string
	MetricYear        int
	SubjectVersionID  string
	SubjectVersionKey string
	Decision          PublicEligibilityDecision
	Evidence          PublicEligibilityEvidence
	AssessedAt        time.Time
}

func (assessment PublicEligibilityAssessment) Clone() PublicEligibilityAssessment {
	assessment.Evidence = assessment.Evidence.Clone()
	return assessment
}

func (assessment PublicEligibilityAssessment) Validate() error {
	if strings.TrimSpace(assessment.WorkID) == "" ||
		assessment.WorkID != strings.TrimSpace(assessment.WorkID) {
		return errors.New("public eligibility assessment requires an exact Work ID")
	}
	if assessment.PolicyVersion != BiomedicalPublicEligibilityPolicyVersion {
		return fmt.Errorf(
			"unsupported public eligibility policy version %q",
			assessment.PolicyVersion,
		)
	}
	if assessment.MetricYear < 1900 || assessment.MetricYear > 3000 {
		return fmt.Errorf(
			"public eligibility metric year %d is outside 1900..3000",
			assessment.MetricYear,
		)
	}
	if strings.TrimSpace(assessment.SubjectVersionID) == "" ||
		assessment.SubjectVersionID != strings.TrimSpace(assessment.SubjectVersionID) {
		return errors.New(
			"public eligibility assessment requires an exact Subject version ID",
		)
	}
	if strings.TrimSpace(assessment.SubjectVersionKey) == "" ||
		assessment.SubjectVersionKey != strings.TrimSpace(assessment.SubjectVersionKey) {
		return errors.New(
			"public eligibility assessment requires an exact Subject version key",
		)
	}
	if !assessment.Decision.Valid() {
		return fmt.Errorf(
			"invalid public eligibility decision %q",
			assessment.Decision,
		)
	}
	if assessment.AssessedAt.IsZero() {
		return errors.New("public eligibility assessment assessed_at is required")
	}
	load := PublicEligibilityLoad{
		WorkID:            assessment.WorkID,
		MetricYear:        assessment.MetricYear,
		SubjectVersionID:  assessment.SubjectVersionID,
		SubjectVersionKey: assessment.SubjectVersionKey,
		Venue:             assessment.Evidence.Venue.clone(),
		Metrics:           slices.Clone(assessment.Evidence.Metrics),
		Matches:           slices.Clone(assessment.Evidence.Matches),
	}
	switch assessment.Decision {
	case PublicEligibilityDecisionAccepted:
		if assessment.Evidence.Reason != "" || len(load.Matches) == 0 {
			return errors.New(
				"accepted public eligibility requires exact Subject metric evidence and no missing reason",
			)
		}
	case PublicEligibilityDecisionRejected:
		if assessment.Evidence.Reason !=
			PublicEligibilityReasonNoExactSubjectMetricLink ||
			len(load.Matches) != 0 {
			return errors.New(
				"rejected public eligibility requires no exact Subject metric link",
			)
		}
	case PublicEligibilityDecisionMissing:
		if !assessment.Evidence.Reason.missing() {
			return errors.New(
				"missing public eligibility requires an explicit missing evidence reason",
			)
		}
		load.MissingReason = assessment.Evidence.Reason
	}
	return load.validate()
}

func (assessment PublicEligibilityAssessment) DecisiveJournalSubjectMetricID() string {
	if assessment.Decision != PublicEligibilityDecisionAccepted ||
		len(assessment.Evidence.Matches) == 0 {
		return ""
	}
	return assessment.Evidence.Matches[0].JournalSubjectMetricID
}

type PublicEligibilityPolicy struct {
	version string
}

func NewPublicEligibilityPolicy(
	version string,
) (PublicEligibilityPolicy, error) {
	if version != BiomedicalPublicEligibilityPolicyVersion {
		return PublicEligibilityPolicy{}, fmt.Errorf(
			"unsupported biomedical public eligibility policy version %q; expected %s",
			version,
			BiomedicalPublicEligibilityPolicyVersion,
		)
	}
	return PublicEligibilityPolicy{version: version}, nil
}

func (policy PublicEligibilityPolicy) Version() string {
	return policy.version
}

func (policy PublicEligibilityPolicy) Evaluate(
	load PublicEligibilityLoad,
	assessedAt time.Time,
) (PublicEligibilityAssessment, error) {
	if policy.version != BiomedicalPublicEligibilityPolicyVersion {
		return PublicEligibilityAssessment{}, errors.New(
			"biomedical public eligibility policy is not initialized",
		)
	}
	if assessedAt.IsZero() {
		return PublicEligibilityAssessment{}, errors.New(
			"biomedical public eligibility assessed_at is required",
		)
	}
	load = load.Clone()
	if err := load.validate(); err != nil {
		return PublicEligibilityAssessment{}, err
	}

	decision := PublicEligibilityDecisionRejected
	reason := PublicEligibilityReasonNoExactSubjectMetricLink
	switch {
	case load.MissingReason != "":
		decision = PublicEligibilityDecisionMissing
		reason = load.MissingReason
	case len(load.Matches) > 0:
		decision = PublicEligibilityDecisionAccepted
		reason = ""
	}
	assessment := PublicEligibilityAssessment{
		WorkID:            load.WorkID,
		PolicyVersion:     policy.version,
		MetricYear:        load.MetricYear,
		SubjectVersionID:  load.SubjectVersionID,
		SubjectVersionKey: load.SubjectVersionKey,
		Decision:          decision,
		Evidence: PublicEligibilityEvidence{
			Reason:  reason,
			Venue:   load.Venue.clone(),
			Metrics: slices.Clone(load.Metrics),
			Matches: slices.Clone(load.Matches),
		},
		AssessedAt: assessedAt.UTC(),
	}
	if err := assessment.Validate(); err != nil {
		return PublicEligibilityAssessment{}, err
	}
	return assessment, nil
}

type PublicEligibilityStore interface {
	LoadEligibility(
		context.Context,
		string,
		int,
		string,
	) (PublicEligibilityLoad, error)
	PersistEligibility(
		context.Context,
		PublicEligibilityAssessment,
	) (PublicEligibilityAssessment, error)
}

type PublicEligibilityReader interface {
	FindEligibility(
		context.Context,
		string,
		string,
		int,
		string,
	) (PublicEligibilityAssessment, bool, error)
}

type PublicEligibilityService struct {
	store PublicEligibilityStore
}

func NewPublicEligibilityService(
	store PublicEligibilityStore,
) (*PublicEligibilityService, error) {
	if interfaceIsNil(store) {
		return nil, errors.New("public eligibility store is required")
	}
	return &PublicEligibilityService{store: store}, nil
}

func (service *PublicEligibilityService) Assess(
	ctx context.Context,
	input PublicEligibilityInput,
) (PublicEligibilityAssessment, error) {
	if ctx == nil {
		return PublicEligibilityAssessment{}, errors.New(
			"public eligibility context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return PublicEligibilityAssessment{}, err
	}
	if service == nil || interfaceIsNil(service.store) {
		return PublicEligibilityAssessment{}, errors.New(
			"public eligibility service is not initialized",
		)
	}
	if err := input.validate(); err != nil {
		return PublicEligibilityAssessment{}, err
	}
	policy, err := NewPublicEligibilityPolicy(input.PolicyVersion)
	if err != nil {
		return PublicEligibilityAssessment{}, err
	}
	loaded, err := service.store.LoadEligibility(
		ctx,
		input.WorkID,
		input.MetricYear,
		input.SubjectVersionKey,
	)
	if err != nil {
		return PublicEligibilityAssessment{}, fmt.Errorf(
			"load biomedical public eligibility evidence: %w",
			err,
		)
	}
	if loaded.WorkID != input.WorkID ||
		loaded.MetricYear != input.MetricYear ||
		loaded.SubjectVersionKey != input.SubjectVersionKey {
		return PublicEligibilityAssessment{}, errors.New(
			"loaded biomedical public eligibility evidence does not match the explicit request",
		)
	}
	assessment, err := policy.Evaluate(loaded, input.AssessedAt)
	if err != nil {
		return PublicEligibilityAssessment{}, fmt.Errorf(
			"evaluate biomedical public eligibility: %w",
			err,
		)
	}
	persisted, err := service.store.PersistEligibility(ctx, assessment)
	if err != nil {
		return PublicEligibilityAssessment{}, fmt.Errorf(
			"persist biomedical public eligibility: %w",
			err,
		)
	}
	if !reflect.DeepEqual(persisted, assessment) {
		return PublicEligibilityAssessment{}, errors.New(
			"persisted biomedical public eligibility differs from evaluated decision",
		)
	}
	return persisted.Clone(), nil
}

func interfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
