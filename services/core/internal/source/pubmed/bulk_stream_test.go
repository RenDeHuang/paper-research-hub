package pubmed

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestScanBulkEventsStopsGzipExpansionAtCaptureLimit(t *testing.T) {
	t.Parallel()

	payload := `<PubmedArticleSet><PubmedArticle><MedlineCitation>` +
		`<PMID>123</PMID><Article><Unknown>` +
		strings.Repeat("x", 1<<20) +
		`</Unknown></Article></MedlineCitation></PubmedArticle></PubmedArticleSet>`
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := io.WriteString(writer, payload); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatalf("gzip NewReader() error = %v", err)
	}
	counted := &countingReader{reader: gzipReader}
	const limit int64 = 512

	err = scanBulkEvents(context.Background(), counted, BulkLimits{
		MaxRecordBytes:       limit,
		MaxUncompressedBytes: 1 << 20,
		MaxEvents:            100,
	}, func(bulkEvent) error {
		t.Fatal("scanBulkEvents() yielded an oversized record")
		return nil
	})
	if !errors.Is(err, ErrBulkRecordTooLarge) {
		t.Fatalf("scanBulkEvents() error = %v, want ErrBulkRecordTooLarge", err)
	}
	if counted.bytes > limit+128 {
		t.Fatalf(
			"decompressed bytes read = %d, want capture to stop near %d-byte limit",
			counted.bytes,
			limit,
		)
	}
}

func TestScanBulkEventsBoundsCaptureWhileReadingOversizedStartElement(t *testing.T) {
	t.Parallel()

	payload := `<PubmedArticleSet><PubmedArticle data="` +
		strings.Repeat("x", 1<<20) +
		`"><MedlineCitation><PMID>123</PMID><Article/></MedlineCitation>` +
		`</PubmedArticle></PubmedArticleSet>`
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := io.WriteString(writer, payload); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatalf("gzip NewReader() error = %v", err)
	}
	counted := &countingReader{reader: gzipReader}
	const limit int64 = 512

	err = scanBulkEvents(context.Background(), counted, BulkLimits{
		MaxRecordBytes:       limit,
		MaxUncompressedBytes: 1 << 20,
		MaxEvents:            100,
	}, func(bulkEvent) error {
		t.Fatal("scanBulkEvents() yielded an oversized record")
		return nil
	})
	if !errors.Is(err, ErrBulkRecordTooLarge) {
		t.Fatalf("scanBulkEvents() error = %v, want ErrBulkRecordTooLarge", err)
	}
	if counted.bytes > limit+128 {
		t.Fatalf(
			"decompressed bytes read = %d, want start-element capture bounded near %d bytes",
			counted.bytes,
			limit,
		)
	}
}

func TestScanBulkEventsStopsHighlyCompressedSmallTokensAtTotalByteLimit(t *testing.T) {
	t.Parallel()

	payload := `<PubmedArticleSet>` +
		strings.Repeat(`<!--small-->`, 120_000) +
		`</PubmedArticleSet>`
	compressed := gzipPayload(t, payload)
	gzipReader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip NewReader() error = %v", err)
	}
	counted := &countingReader{reader: gzipReader}
	const limit int64 = 4096

	err = scanBulkEvents(context.Background(), counted, BulkLimits{
		MaxRecordBytes:       512,
		MaxUncompressedBytes: limit,
		MaxEvents:            100,
	}, func(bulkEvent) error {
		t.Fatal("scanBulkEvents() yielded a record from comment-only XML")
		return nil
	})
	if !errors.Is(err, ErrBulkUncompressedTooLarge) {
		t.Fatalf("scanBulkEvents() error = %v, want ErrBulkUncompressedTooLarge", err)
	}
	var limitErr *BulkLimitError
	if !errors.As(err, &limitErr) ||
		limitErr.Kind != BulkLimitUncompressedBytes ||
		limitErr.Limit != limit ||
		limitErr.Observed != limit+1 {
		t.Fatalf("scanBulkEvents() limit error = %#v", limitErr)
	}
	if counted.bytes > limit+1 {
		t.Fatalf(
			"decompressed bytes read = %d, want stop at %d-byte probe",
			counted.bytes,
			limit+1,
		)
	}
}

func TestScanBulkEventsRejectsEventBeyondConfiguredTotal(t *testing.T) {
	t.Parallel()

	payload := `<PubmedArticleSet>` +
		smallArticleXML("101") +
		smallArticleXML("102") +
		smallArticleXML("103") +
		`</PubmedArticleSet>`
	compressed := gzipPayload(t, payload)
	gzipReader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip NewReader() error = %v", err)
	}
	yielded := 0

	err = scanBulkEvents(context.Background(), gzipReader, BulkLimits{
		MaxRecordBytes:       512,
		MaxUncompressedBytes: int64(len(payload)),
		MaxEvents:            2,
	}, func(bulkEvent) error {
		yielded++
		return nil
	})
	if !errors.Is(err, ErrBulkTooManyEvents) {
		t.Fatalf("scanBulkEvents() error = %v, want ErrBulkTooManyEvents", err)
	}
	var limitErr *BulkLimitError
	if !errors.As(err, &limitErr) ||
		limitErr.Kind != BulkLimitEvents ||
		limitErr.Limit != 2 ||
		limitErr.Observed != 3 {
		t.Fatalf("scanBulkEvents() limit error = %#v", limitErr)
	}
	if yielded != 2 {
		t.Fatalf("yielded events = %d, want 2 before rejection", yielded)
	}
}

func TestScanBulkEventsHonorsCanceledContextBeforeExpansion(t *testing.T) {
	t.Parallel()

	payload := `<PubmedArticleSet>` +
		strings.Repeat(`<!--small-->`, 10_000) +
		`</PubmedArticleSet>`
	compressed := gzipPayload(t, payload)
	gzipReader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip NewReader() error = %v", err)
	}
	counted := &countingReader{reader: gzipReader}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = scanBulkEvents(ctx, counted, BulkLimits{
		MaxRecordBytes:       512,
		MaxUncompressedBytes: int64(len(payload)),
		MaxEvents:            100,
	}, func(bulkEvent) error {
		t.Fatal("scanBulkEvents() yielded after context cancellation")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scanBulkEvents() error = %v, want context.Canceled", err)
	}
	if counted.bytes != 0 {
		t.Fatalf("decompressed bytes read = %d after pre-canceled context", counted.bytes)
	}
}

func gzipPayload(t *testing.T, payload string) []byte {
	t.Helper()

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := io.WriteString(writer, payload); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	return compressed.Bytes()
}

func smallArticleXML(pmid string) string {
	return `<PubmedArticle><MedlineCitation><PMID>` + pmid +
		`</PMID><Article/></MedlineCitation></PubmedArticle>`
}

type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (reader *countingReader) Read(payload []byte) (int, error) {
	count, err := reader.reader.Read(payload)
	reader.bytes += int64(count)
	return count, err
}
