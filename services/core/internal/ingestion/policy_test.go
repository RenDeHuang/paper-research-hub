package ingestion

import (
	"context"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

func TestControlledIdentityPoliciesIncludeOnlyCanonicalizableRecords(t *testing.T) {
	raw := PersistedRaw{
		ID:          "raw-1",
		JobID:       "job-1",
		Disposition: RawDispositionInserted,
		Envelope:    testEnvelope(t, source.ScopePending),
	}
	normalized, err := NewNormalizedRecord(
		raw,
		raw.Envelope.Record,
		"normalized-assertion-1",
		normalizedPayloadSchemaVersion,
	)
	if err != nil {
		t.Fatalf("NewNormalizedRecord() error = %v", err)
	}
	scopePolicy := NewControlledIdentityScopePolicy("scope/controlled-identity/v2")
	decision, err := scopePolicy.Evaluate(context.Background(), normalized)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if decision.Status != source.ScopeIncluded ||
		decision.Reason != "controlled_canonical_identity_present" {
		t.Fatalf("scope decision = %#v", decision)
	}

	projectionPolicy := NewDeterministicProjectionPolicy(
		"projection/latest-source-revision/v2",
	)
	candidate, err := projectionPolicy.Prepare(
		context.Background(),
		normalized,
		decision,
	)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if candidate.EventKey != normalized.EventKey ||
		!candidate.SourceTime.Equal(normalized.SourceTime) {
		t.Fatalf("projection candidate = %#v", candidate)
	}

	pmidOnly := normalized.Clone()
	pmidOnly.Record.Identity = source.Record{}.Identity
	pmidOnly.Record.Identifiers = []source.Identifier{{
		Scheme: source.IdentifierPMID,
		Value:  "12345678",
	}}
	pmidOnly.SourceTime = time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)
	decision, err = scopePolicy.Evaluate(context.Background(), pmidOnly)
	if err != nil {
		t.Fatalf("Evaluate(PMID-only) error = %v", err)
	}
	if decision.Status != source.ScopeIncluded ||
		decision.Reason != "controlled_canonical_identity_present" {
		t.Fatalf("PMID-only scope decision = %#v", decision)
	}
}
