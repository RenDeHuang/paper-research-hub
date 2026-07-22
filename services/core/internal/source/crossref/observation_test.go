package crossref_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/crossref"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
)

func TestPageReceiptCapturesExplicitReceiveBoundary(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(envelopePayload(
			t,
			noCursor(),
			item("10.1000/observed"),
		))
	}))
	defer server.Close()

	observedAt := time.Date(
		2026,
		time.July,
		19,
		8,
		30,
		0,
		123456000,
		time.UTC,
	)
	client := newClient(
		t,
		server,
		nil,
		httpclient.Dependencies{
			Now: func() time.Time { return observedAt },
		},
	)
	var receipts []crossref.PageReceipt
	for _, err := range client.FetchWithPageReceipts(
		context.Background(),
		boundedQuery(2),
		func(_ context.Context, receipt crossref.PageReceipt) error {
			receipts = append(receipts, receipt)
			return nil
		},
	) {
		if err != nil {
			t.Fatalf("FetchWithPageReceipts() error = %v", err)
		}
	}
	if len(receipts) != 1 {
		t.Fatalf("receipts = %d, want 1", len(receipts))
	}
	if !receipts[0].ObservedAt.Equal(observedAt) {
		t.Fatalf(
			"ObservedAt = %s, want explicit receive boundary %s",
			receipts[0].ObservedAt,
			observedAt,
		)
	}
}
