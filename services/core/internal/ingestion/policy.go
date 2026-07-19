package ingestion

import (
	"context"
	"errors"
	"strings"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

type ControlledIdentityScopePolicy struct {
	version string
}

func NewControlledIdentityScopePolicy(version string) ControlledIdentityScopePolicy {
	return ControlledIdentityScopePolicy{version: strings.TrimSpace(version)}
}

func (policy ControlledIdentityScopePolicy) Version() string {
	return policy.version
}

func (policy ControlledIdentityScopePolicy) Evaluate(
	ctx context.Context,
	record NormalizedRecord,
) (source.ScopeDecision, error) {
	if err := ctx.Err(); err != nil {
		return source.ScopeDecision{}, err
	}
	if strings.TrimSpace(policy.version) == "" {
		return source.ScopeDecision{}, errors.New("controlled identity scope policy version is required")
	}
	if err := record.Validate(); err != nil {
		return source.ScopeDecision{}, err
	}

	if canonicalizableRecord(record.Record) {
		return source.NewScopeDecision(
			source.ScopeIncluded,
			"controlled_canonical_identity_present",
			[]source.FieldEvidence{{
				Field:      "identity",
				SourcePath: "normalized.controlled_identifiers",
			}},
		)
	}
	return source.NewScopeDecision(
		source.ScopeExcluded,
		"no_supported_canonical_identity",
		[]source.FieldEvidence{{
			Field:      "identity",
			SourcePath: "normalized.controlled_identifiers",
		}},
	)
}

func canonicalizableRecord(record source.Record) bool {
	if record.Identity.Valid() {
		return true
	}
	identifiers := paper.Identifiers{}
	for _, identifier := range record.Identifiers {
		switch identifier.Scheme {
		case source.IdentifierDOI:
			identifiers.DOI = append(identifiers.DOI, identifier.Value)
		case source.IdentifierPMID:
			identifiers.PMID = append(identifiers.PMID, identifier.Value)
		case source.IdentifierArXiv:
			identifiers.ArXiv = append(identifiers.ArXiv, identifier.Value)
		case source.IdentifierOpenAlex:
			identifiers.OpenAlex = append(identifiers.OpenAlex, identifier.Value)
		}
	}
	_, err := paper.CanonicalIdentity(identifiers, "")
	return err == nil
}

type DeterministicProjectionPolicy struct {
	version string
}

func NewDeterministicProjectionPolicy(
	version string,
) DeterministicProjectionPolicy {
	return DeterministicProjectionPolicy{version: strings.TrimSpace(version)}
}

func (policy DeterministicProjectionPolicy) Version() string {
	return policy.version
}

func (policy DeterministicProjectionPolicy) Prepare(
	ctx context.Context,
	record NormalizedRecord,
	decision source.ScopeDecision,
) (ProjectionCandidate, error) {
	if err := ctx.Err(); err != nil {
		return ProjectionCandidate{}, err
	}
	if strings.TrimSpace(policy.version) == "" {
		return ProjectionCandidate{}, errors.New("deterministic projection policy version is required")
	}
	return NewProjectionCandidate(record, decision)
}
