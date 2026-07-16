package source_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

func TestNewRawRecordPreservesPayloadAndHashesCanonicalJSON(t *testing.T) {
	t.Parallel()

	firstPayload := []byte("{\n  \"z\": 2,\n  \"nested\": {\"b\": true, \"a\": [3, 1]},\n  \"a\": \"x\"\n}")
	secondPayload := []byte(`{"a":"x","nested":{"a":[3,1],"b":true},"z":2}`)

	first, err := source.NewRawRecord(firstPayload)
	if err != nil {
		t.Fatalf("NewRawRecord(first) error = %v", err)
	}
	second, err := source.NewRawRecord(secondPayload)
	if err != nil {
		t.Fatalf("NewRawRecord(second) error = %v", err)
	}

	if !bytes.Equal(first.Payload, firstPayload) {
		t.Fatalf("Payload = %q, want original bytes %q", first.Payload, firstPayload)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("hashes differ for equivalent JSON: %q != %q", first.SHA256, second.SHA256)
	}
	if first.SHA256 != "33bf2674be7447e6f6d6957adbc9540f99d83cfe63b1d73f1a597fa0fc95a351" {
		t.Fatalf("SHA256 = %q, want deterministic canonical hash", first.SHA256)
	}

	firstPayload[0] = '['
	if first.Payload[0] != '{' {
		t.Fatal("RawRecord retained a mutable alias to caller payload")
	}
}

func TestNewRawRecordRejectsMalformedTrailingOrNonObjectJSON(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`{"id":`,
		`{"id":"W1"} {"id":"W2"}`,
		`["not", "a", "record"]`,
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := source.NewRawRecord([]byte(raw)); err == nil {
				t.Fatalf("NewRawRecord(%q) = %#v, want error", raw, got)
			}
		})
	}
}

func TestNewScopeDecisionRequiresExplicitStatusAndReason(t *testing.T) {
	t.Parallel()

	decision, err := source.NewScopeDecision(
		source.ScopePending,
		source.ScopeReasonAwaitingDeterministicEvaluation,
		nil,
	)
	if err != nil {
		t.Fatalf("NewScopeDecision() error = %v", err)
	}
	if decision.Status != source.ScopePending {
		t.Fatalf("Status = %q, want %q", decision.Status, source.ScopePending)
	}

	for _, tt := range []struct {
		name   string
		status source.ScopeStatus
		reason string
	}{
		{name: "unknown status", status: source.ScopeStatus("guessed"), reason: "title looked relevant"},
		{name: "missing reason", status: source.ScopePending},
		{name: "blank reason", status: source.ScopeIncluded, reason: " \t "},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := source.NewScopeDecision(tt.status, tt.reason, nil)
			if err == nil {
				t.Fatal("NewScopeDecision() accepted an invalid decision")
			}
			if strings.Contains(strings.ToLower(err.Error()), "fallback") {
				t.Fatalf("NewScopeDecision() returned unrelated fallback error: %v", err)
			}
		})
	}
}
