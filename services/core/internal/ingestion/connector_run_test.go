package ingestion

import (
	"strings"
	"testing"
	"time"
)

func TestConnectorRunClaimRequiresTypedBoundedWatermark(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
	until := from.Add(24 * time.Hour)
	claim, err := NewConnectorClaim(
		"crossref",
		"created",
		WatermarkTimestamp,
		from,
		until,
		"crossref-created:2026-07-17",
	)
	if err != nil {
		t.Fatalf("NewConnectorClaim() error = %v", err)
	}
	if claim.Source != "crossref" ||
		claim.Stream != "created" ||
		claim.WatermarkKind != WatermarkTimestamp ||
		!claim.From.Equal(from) ||
		!claim.Until.Equal(until) {
		t.Fatalf("claim = %#v", claim)
	}

	tests := []struct {
		name string
		edit func(*ConnectorClaim)
		want string
	}{
		{name: "source", edit: func(value *ConnectorClaim) { value.Source = "" }, want: "source"},
		{name: "stream", edit: func(value *ConnectorClaim) { value.Stream = "" }, want: "stream"},
		{
			name: "watermark kind",
			edit: func(value *ConnectorClaim) {
				value.WatermarkKind = WatermarkKind("cursor")
			},
			want: "watermark",
		},
		{name: "from", edit: func(value *ConnectorClaim) { value.From = time.Time{} }, want: "from"},
		{
			name: "interval",
			edit: func(value *ConnectorClaim) {
				value.Until = value.From
			},
			want: "until",
		},
		{
			name: "idempotency",
			edit: func(value *ConnectorClaim) {
				value.IdempotencyKey = ""
			},
			want: "idempotency",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			invalid := claim
			test.edit(&invalid)
			if err := invalid.Validate(); err == nil ||
				!strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestConnectorPageReceiptRequiresExactPageIdentity(t *testing.T) {
	t.Parallel()

	receipt, err := NewConnectorPageReceipt(
		"00000000-0000-0000-0000-000000000123",
		1,
		" opaque cursor in \t",
		" opaque cursor out \n",
		strings.Repeat("a", 64),
		100,
	)
	if err != nil {
		t.Fatalf("NewConnectorPageReceipt() error = %v", err)
	}
	if receipt.PageOrdinal != 1 ||
		receipt.CursorIn != " opaque cursor in \t" ||
		receipt.CursorOut != " opaque cursor out \n" ||
		receipt.RecordCount != 100 {
		t.Fatalf("receipt = %#v", receipt)
	}

	receipt.ContentSHA256 = "not-a-hash"
	if err := receipt.Validate(); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "sha256") {
		t.Fatalf("Validate() error = %v, want SHA256 rejection", err)
	}
}

func TestConnectorRunTransitionsDoNotAdvanceWatermarkInMemory(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
	claim, err := NewConnectorClaim(
		"crossref",
		"updated",
		WatermarkTimestamp,
		from,
		from.Add(24*time.Hour),
		"crossref-updated:2026-07-17",
	)
	if err != nil {
		t.Fatalf("NewConnectorClaim() error = %v", err)
	}
	run, err := RestoreConnectorRun(
		"00000000-0000-0000-0000-000000000124",
		claim,
		7,
		ConnectorRunRunning,
	)
	if err != nil {
		t.Fatalf("RestoreConnectorRun() error = %v", err)
	}
	failed, err := run.Fail("project", "projection_failed")
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if failed.Status != ConnectorRunFailed ||
		failed.ExpectedWatermarkVersion != 7 ||
		!failed.Claim.From.Equal(from) {
		t.Fatalf("failed run mutated claimed watermark = %#v", failed)
	}
	succeeded, err := run.Succeed()
	if err != nil {
		t.Fatalf("Succeed() error = %v", err)
	}
	if succeeded.Status != ConnectorRunSucceeded ||
		succeeded.ExpectedWatermarkVersion != 7 {
		t.Fatalf("succeeded run = %#v", succeeded)
	}
}
