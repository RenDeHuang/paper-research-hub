package workfamily

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrUnsupportedEvidence = errors.New(
		"only explicit source relations or stable shared identifiers are accepted",
	)
	ErrRelationSelfLoop  = errors.New("Work relation self-loop is forbidden")
	ErrAssertionConflict = errors.New(
		"relation assertion identity already exists with different immutable values",
	)
	ErrDecisionConflict = errors.New(
		"relation decision does not extend the current immutable decision chain",
	)
	ErrWorkNotFound = errors.New("Work does not exist")
)

type RelationKind string

const (
	RelationIsPreprintOf RelationKind = "is_preprint_of"
	RelationHasPreprint  RelationKind = "has_preprint"
	RelationIsVersionOf  RelationKind = "is_version_of"
	RelationHasVersion   RelationKind = "has_version"
	RelationReplaces     RelationKind = "replaces"
	RelationIsReplacedBy RelationKind = "is_replaced_by"
	RelationIsCorrection RelationKind = "is_correction_of"
	RelationIsRetraction RelationKind = "is_retraction_of"
)

type EvidenceKind string

const (
	EvidenceExplicitSourceRelation EvidenceKind = "explicit_source_relation"
	EvidenceStableSharedIdentifier EvidenceKind = "stable_shared_identifier"
)

type DecisionOutcome string

const (
	DecisionAccepted DecisionOutcome = "accepted"
	DecisionRejected DecisionOutcome = "rejected"
)

type MembershipOrigin string

const (
	MembershipOriginSingleton        MembershipOrigin = "singleton"
	MembershipOriginRelationDecision MembershipOrigin = "relation_decision"
)

type RelationAssertion struct {
	ID                    uuid.UUID
	SubjectWorkID         uuid.UUID
	ObjectWorkID          uuid.UUID
	RelationKind          RelationKind
	EvidenceKind          EvidenceKind
	SubjectSourceRecordID uuid.UUID
	SubjectSourcePath     string
	ObjectSourceRecordID  uuid.UUID
	ObjectSourcePath      string
	IdentifierScheme      string
	IdentifierValue       string
	AssertedAt            time.Time
}

func (assertion RelationAssertion) Validate() error {
	if assertion.SubjectWorkID == uuid.Nil {
		return errors.New("relation assertion subject Work ID is required")
	}
	if assertion.ObjectWorkID == uuid.Nil {
		return errors.New("relation assertion object Work ID is required")
	}
	if assertion.SubjectWorkID == assertion.ObjectWorkID {
		return ErrRelationSelfLoop
	}
	if !assertion.RelationKind.valid() {
		return fmt.Errorf(
			"unsupported Work relation kind %q",
			assertion.RelationKind,
		)
	}
	if assertion.SubjectSourceRecordID == uuid.Nil {
		return errors.New(
			"relation assertion subject source record ID is required",
		)
	}
	if err := validateExactText(
		assertion.SubjectSourcePath,
		"relation assertion subject source path",
	); err != nil {
		return err
	}
	if assertion.AssertedAt.IsZero() {
		return errors.New("relation assertion asserted_at is required")
	}
	if assertion.AssertedAt != normalizeTimestamp(assertion.AssertedAt) {
		return errors.New(
			"relation assertion asserted_at requires UTC PostgreSQL microsecond precision",
		)
	}

	switch assertion.EvidenceKind {
	case EvidenceExplicitSourceRelation:
		if assertion.ObjectSourceRecordID != uuid.Nil ||
			assertion.ObjectSourcePath != "" ||
			assertion.IdentifierScheme != "" ||
			assertion.IdentifierValue != "" {
			return errors.New(
				"explicit source relation cannot contain shared identifier evidence",
			)
		}
	case EvidenceStableSharedIdentifier:
		if assertion.ObjectSourceRecordID == uuid.Nil {
			return errors.New(
				"stable shared identifier requires an object source record ID",
			)
		}
		if err := validateExactText(
			assertion.ObjectSourcePath,
			"stable shared identifier object source path",
		); err != nil {
			return err
		}
		if !stableIdentifierSchemeRegistered(assertion.IdentifierScheme) {
			return errors.New(
				"stable shared identifier requires a registered stable identifier scheme",
			)
		}
		if err := validateExactText(
			assertion.IdentifierValue,
			"stable shared identifier value",
		); err != nil {
			return err
		}
	default:
		return ErrUnsupportedEvidence
	}
	return nil
}

func stableIdentifierSchemeRegistered(scheme string) bool {
	switch scheme {
	case "doi",
		"arxiv",
		"openreview",
		"semantic_scholar",
		"openalex",
		"pmid",
		"pmcid":
		return true
	default:
		return false
	}
}

func (kind RelationKind) valid() bool {
	switch kind {
	case RelationIsPreprintOf,
		RelationHasPreprint,
		RelationIsVersionOf,
		RelationHasVersion,
		RelationReplaces,
		RelationIsReplacedBy,
		RelationIsCorrection,
		RelationIsRetraction:
		return true
	default:
		return false
	}
}

type RelationDecision struct {
	ID                   uuid.UUID
	AssertionID          uuid.UUID
	SupersedesDecisionID uuid.UUID
	Outcome              DecisionOutcome
	Reason               string
	PolicyVersion        string
	DecidedAt            time.Time
}

func (decision RelationDecision) Validate() error {
	if decision.AssertionID == uuid.Nil {
		return errors.New("relation decision assertion ID is required")
	}
	switch decision.Outcome {
	case DecisionAccepted, DecisionRejected:
	default:
		return fmt.Errorf(
			"unsupported relation decision outcome %q",
			decision.Outcome,
		)
	}
	if err := validateExactText(
		decision.Reason,
		"relation decision reason",
	); err != nil {
		return err
	}
	if err := validateExactText(
		decision.PolicyVersion,
		"relation decision policy version",
	); err != nil {
		return err
	}
	if decision.DecidedAt.IsZero() {
		return errors.New("relation decision decided_at is required")
	}
	if decision.DecidedAt != normalizeTimestamp(decision.DecidedAt) {
		return errors.New(
			"relation decision decided_at requires UTC PostgreSQL microsecond precision",
		)
	}
	return nil
}

type Membership struct {
	ID                 uuid.UUID
	FamilyID           uuid.UUID
	WorkID             uuid.UUID
	Origin             MembershipOrigin
	RelationDecisionID uuid.UUID
	StartedAt          time.Time
	EndedAt            *time.Time
}

type Version struct {
	WorkID              uuid.UUID
	CanonicalKey        string
	Title               string
	PublishedAt         *time.Time
	MembershipStartedAt time.Time
}

type TimelineRelation struct {
	Assertion RelationAssertion
	Decision  RelationDecision
}

type Timeline struct {
	FamilyID        uuid.UUID
	CanonicalWorkID uuid.UUID
	Versions        []Version
	Relations       []TimelineRelation
}

func normalizeTimestamp(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func validateExactText(value string, label string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be exact and non-empty", label)
	}
	return nil
}
