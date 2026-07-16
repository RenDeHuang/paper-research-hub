package pubmed

import (
	"bytes"
	"compress/gzip"
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

	err = scanBulkEvents(counted, limit, func(bulkEvent) error {
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

	err = scanBulkEvents(counted, limit, func(bulkEvent) error {
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

type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (reader *countingReader) Read(payload []byte) (int, error) {
	count, err := reader.reader.Read(payload)
	reader.bytes += int64(count)
	return count, err
}
