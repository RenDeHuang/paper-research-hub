package source_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

func TestCrossrefHasAFirstClassSourceAndPublisherField(t *testing.T) {
	t.Parallel()

	record := source.Record{
		Source:    source.Crossref,
		Publisher: "Association for Computing Machinery",
	}

	if record.Source != "crossref" {
		t.Fatalf("Source = %q, want crossref", record.Source)
	}
	if record.Publisher != "Association for Computing Machinery" {
		t.Fatalf("Publisher = %q, want exact source value", record.Publisher)
	}
}

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

func TestNewRawXMLRecordPreservesAndHashesExactElementBytes(t *testing.T) {
	t.Parallel()

	payload := []byte("<PubmedArticle z=\"2\" a=\"1\">\n  <PMID>123</PMID>\n</PubmedArticle>")
	record, err := source.NewRawXMLRecord(payload)
	if err != nil {
		t.Fatalf("NewRawXMLRecord() error = %v", err)
	}
	if !bytes.Equal(record.Payload, payload) {
		t.Fatalf("Payload = %q, want exact original XML bytes %q", record.Payload, payload)
	}
	digest := sha256.Sum256(payload)
	if record.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("SHA256 = %q, want exact byte hash", record.SHA256)
	}

	payload[0] = '['
	if record.Payload[0] != '<' {
		t.Fatal("RawRecord retained a mutable alias to caller XML")
	}
}

func TestNewRawXMLRecordRejectsMalformedMultipleOrTextRoots(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`<PubmedArticle>`,
		`<PubmedArticle/><PubmedArticle/>`,
		`text<PubmedArticle/>`,
		`<PubmedArticle/>text`,
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if got, err := source.NewRawXMLRecord([]byte(raw)); err == nil {
				t.Fatalf("NewRawXMLRecord(%q) = %#v, want error", raw, got)
			}
		})
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
