package pubmed_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
)

func TestBaselineRequiresYearAndPersistsDurableSourcePosition(t *testing.T) {
	t.Parallel()

	sink := newMemorySink()
	checkpoints := newMemoryCheckpoint()
	importer, err := pubmed.NewImporter(sink, checkpoints)
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}
	file := fixtureBulkFile(t, "baseline.xml.gz", "pubmed26n0001.xml.gz")

	if err := importer.ImportBaseline(context.Background(), pubmed.BaselineImport{
		Job: "baseline-2026",
	}, []pubmed.BulkFile{file}); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "year") {
		t.Fatalf("ImportBaseline() error = %v, want required year", err)
	}

	if err := importer.ImportBaseline(context.Background(), pubmed.BaselineImport{
		Job:  "baseline-2026",
		Year: 2026,
	}, []pubmed.BulkFile{file}); err != nil {
		t.Fatalf("ImportBaseline() error = %v", err)
	}

	upserts := sink.Upserts()
	if len(upserts) != 2 {
		t.Fatalf("upserts = %#v, want two baseline records", upserts)
	}
	for index, upsert := range upserts {
		wantOrdinal := int64(index + 1)
		if upsert.Position.Job != "baseline-2026" ||
			upsert.Position.FileName != "pubmed26n0001.xml.gz" ||
			upsert.Position.SHA256 != file.SHA256 ||
			upsert.Position.Ordinal != wantOrdinal ||
			upsert.Position.Sequence != 1 {
			t.Fatalf("upsert %d position = %#v", index, upsert.Position)
		}
	}
	progress, ok, err := checkpoints.Load(context.Background(), "baseline-2026")
	if err != nil || !ok {
		t.Fatalf("Load() = %#v, %v, %v", progress, ok, err)
	}
	if progress.Job != "baseline-2026" ||
		progress.FileName != "pubmed26n0001.xml.gz" ||
		progress.SHA256 != file.SHA256 ||
		progress.Sequence != 1 ||
		progress.Revision != 1 ||
		progress.Ordinal != 2 ||
		!progress.Complete {
		t.Fatalf("progress = %#v, want durable completed file/ordinal", progress)
	}
}

func TestDailyRevisionReplacesProjectionAndDeletionIsAuditable(t *testing.T) {
	t.Parallel()

	sink := newMemorySink()
	checkpoints := newMemoryCheckpoint()
	importer, err := pubmed.NewImporter(sink, checkpoints)
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}
	baseline := fixtureBulkFile(t, "baseline.xml.gz", "pubmed26n0001.xml.gz")
	if err := importer.ImportBaseline(context.Background(), pubmed.BaselineImport{
		Job:  "baseline-2026",
		Year: 2026,
	}, []pubmed.BulkFile{baseline}); err != nil {
		t.Fatalf("ImportBaseline() error = %v", err)
	}

	update := fixtureBulkFile(t, "update.xml.gz", "pubmed26n0002.xml.gz")
	if err := importer.ImportDaily(context.Background(), pubmed.DailyImport{
		Job:              "daily-2026",
		Year:             2026,
		PreviousSequence: 1,
	}, []pubmed.BulkFile{update}); err != nil {
		t.Fatalf("ImportDaily() error = %v", err)
	}

	projection := sink.Projection()
	if len(projection) != 1 {
		t.Fatalf("projection = %#v, want revised PMID only", projection)
	}
	if got := projection["1001"].Title; got != "Revised title" {
		t.Fatalf("PMID 1001 title = %q, want replacement projection", got)
	}
	if _, exists := projection["1002"]; exists {
		t.Fatal("deleted PMID 1002 remained in public projection")
	}
	deletions := sink.Deletions()
	if len(deletions) != 1 ||
		!slices.Equal(deletions[0].PMIDs, []string{"1002"}) ||
		deletions[0].Position.Job != "daily-2026" ||
		deletions[0].Position.FileName != "pubmed26n0002.xml.gz" ||
		deletions[0].Position.SHA256 != update.SHA256 ||
		deletions[0].Position.Ordinal != 2 ||
		len(deletions[0].Raw.Payload) == 0 {
		t.Fatalf("deletions = %#v, want auditable deletion event", deletions)
	}
}

func TestDailyRejectsMissingDuplicateAndOutOfOrderFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{name: "missing", files: []string{"pubmed26n0003.xml.gz"}, want: "missing"},
		{
			name:  "duplicate",
			files: []string{"pubmed26n0002.xml.gz", "pubmed26n0002.xml.gz"},
			want:  "duplicate",
		},
		{
			name:  "out of order",
			files: []string{"pubmed26n0003.xml.gz", "pubmed26n0002.xml.gz"},
			want:  "order",
		},
		{name: "already applied", files: []string{"pubmed26n0001.xml.gz"}, want: "order"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			importer, err := pubmed.NewImporter(newMemorySink(), newMemoryCheckpoint())
			if err != nil {
				t.Fatalf("NewImporter() error = %v", err)
			}
			files := make([]pubmed.BulkFile, 0, len(tt.files))
			for _, name := range tt.files {
				files = append(files, fixtureBulkFile(t, "update.xml.gz", name))
			}
			err = importer.ImportDaily(context.Background(), pubmed.DailyImport{
				Job:              "daily-ordering",
				Year:             2026,
				PreviousSequence: 1,
			}, files)
			if err == nil {
				t.Fatal("ImportDaily() accepted invalid sequence")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tt.want) {
				t.Fatalf("ImportDaily() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestImporterAbortsWholeFileAndRetriesFromLastDurableCheckpoint(t *testing.T) {
	t.Parallel()

	sink := newMemorySink()
	sink.failPMIDOnce = "1002"
	checkpoints := newMemoryCheckpoint()
	importer, err := pubmed.NewImporter(sink, checkpoints)
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}
	file := fixtureBulkFile(t, "baseline.xml.gz", "pubmed26n0001.xml.gz")
	spec := pubmed.BaselineImport{Job: "baseline-resume", Year: 2026}

	err = importer.ImportBaseline(context.Background(), spec, []pubmed.BulkFile{file})
	if err == nil {
		t.Fatal("ImportBaseline() accepted injected sink failure")
	}
	progress, ok, loadErr := checkpoints.Load(context.Background(), spec.Job)
	if loadErr != nil {
		t.Fatalf("Load() = %#v, %v, %v", progress, ok, loadErr)
	}
	if ok {
		t.Fatalf("progress after aborted file = %#v, want no partial checkpoint", progress)
	}
	if len(sink.Upserts()) != 0 {
		t.Fatalf("aborted file committed upserts = %#v", sink.Upserts())
	}

	if err := importer.ImportBaseline(context.Background(), spec, []pubmed.BulkFile{file}); err != nil {
		t.Fatalf("resumed ImportBaseline() error = %v", err)
	}
	upserts := sink.Upserts()
	if got := upsertPMIDs(upserts); !slices.Equal(got, []string{"1001", "1002"}) {
		t.Fatalf("successful upserts = %v, want no replay before durable ordinal", got)
	}
	progress, ok, loadErr = checkpoints.Load(context.Background(), spec.Job)
	if loadErr != nil || !ok || progress.Ordinal != 2 || !progress.Complete {
		t.Fatalf("completed progress = %#v, %v, %v", progress, ok, loadErr)
	}

	if err := importer.ImportBaseline(context.Background(), spec, []pubmed.BulkFile{file}); err != nil {
		t.Fatalf("idempotent completed ImportBaseline() error = %v", err)
	}
	if got := upsertPMIDs(sink.Upserts()); !slices.Equal(got, []string{"1001", "1002"}) {
		t.Fatalf("completed file replayed mutations: %v", got)
	}
}

func TestImporterVerifiesCompressedFileSHA256BeforeMutation(t *testing.T) {
	t.Parallel()

	sink := newMemorySink()
	importer, err := pubmed.NewImporter(sink, newMemoryCheckpoint())
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}
	file := fixtureBulkFile(t, "baseline.xml.gz", "pubmed26n0001.xml.gz")
	file.SHA256 = strings.Repeat("0", 64)

	err = importer.ImportBaseline(context.Background(), pubmed.BaselineImport{
		Job:  "baseline-bad-sha",
		Year: 2026,
	}, []pubmed.BulkFile{file})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "sha256") {
		t.Fatalf("ImportBaseline() error = %v, want SHA256 rejection", err)
	}
	if len(sink.Upserts()) != 0 || len(sink.Deletions()) != 0 {
		t.Fatal("checksum mismatch mutated sink")
	}
}

func TestImporterParsesTheSameSingleOpenedByteStreamWhoseSHA256WasVerified(t *testing.T) {
	t.Parallel()

	baselinePayload, err := os.ReadFile("testdata/baseline.xml.gz")
	if err != nil {
		t.Fatalf("ReadFile(baseline) error = %v", err)
	}
	changedPayload, err := os.ReadFile("testdata/update.xml.gz")
	if err != nil {
		t.Fatalf("ReadFile(update) error = %v", err)
	}
	digest := sha256.Sum256(baselinePayload)
	var opens atomic.Int32
	file := pubmed.BulkFile{
		Name:   "pubmed26n0001.xml.gz",
		SHA256: hex.EncodeToString(digest[:]),
		Open: func() (io.ReadCloser, error) {
			if opens.Add(1) == 1 {
				return io.NopCloser(bytes.NewReader(baselinePayload)), nil
			}
			return io.NopCloser(bytes.NewReader(changedPayload)), nil
		},
	}

	sink := newMemorySink()
	checkpoints := newMemoryCheckpoint()
	importer, err := pubmed.NewImporter(sink, checkpoints)
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}
	if err := importer.ImportBaseline(context.Background(), pubmed.BaselineImport{
		Job:  "single-open",
		Year: 2026,
	}, []pubmed.BulkFile{file}); err != nil {
		t.Fatalf("ImportBaseline() error = %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("BulkFile.Open calls = %d, want exactly one immutable source stream", opens.Load())
	}
	projection := sink.Projection()
	if len(projection) != 2 ||
		projection["1001"].Title != "Baseline title" ||
		projection["1002"].Title != "Article to delete" {
		t.Fatalf("projection = %#v, want records from the verified first stream", projection)
	}
}

func TestImporterRejectsCompressedInputBeyondExplicitSpoolLimit(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("testdata/baseline.xml.gz")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	sink := newMemorySink()
	checkpoints := newMemoryCheckpoint()
	importer, err := pubmed.NewImporterWithConfig(sink, checkpoints, pubmed.ImporterConfig{
		MaxCompressedFileBytes: int64(len(payload) - 1),
	})
	if err != nil {
		t.Fatalf("NewImporterWithConfig() error = %v", err)
	}
	file := fixtureBulkFile(t, "baseline.xml.gz", "pubmed26n0001.xml.gz")

	err = importer.ImportBaseline(context.Background(), pubmed.BaselineImport{
		Job:  "bounded-spool",
		Year: 2026,
	}, []pubmed.BulkFile{file})
	if !errors.Is(err, pubmed.ErrBulkFileTooLarge) {
		t.Fatalf("ImportBaseline() error = %v, want ErrBulkFileTooLarge", err)
	}
	if len(sink.Upserts()) != 0 || len(sink.Deletions()) != 0 {
		t.Fatal("oversized compressed input mutated sink")
	}
	if _, ok, loadErr := checkpoints.Load(context.Background(), "bounded-spool"); loadErr != nil || ok {
		t.Fatalf("checkpoint after oversized input = ok %v, error %v", ok, loadErr)
	}
}

func TestDailyRejectsPreviousSequenceThatConflictsWithDurableCheckpoint(t *testing.T) {
	t.Parallel()

	sink := newMemorySink()
	checkpoints := newMemoryCheckpoint()
	checkpoints.Seed(pubmed.Progress{
		Job:      "daily-conflict",
		FileName: "pubmed26n0002.xml.gz",
		SHA256:   strings.Repeat("a", 64),
		Sequence: 2,
		Revision: 7,
		Ordinal:  2,
		Complete: true,
	})
	var opens atomic.Int32
	file := fixtureBulkFile(t, "update.xml.gz", "pubmed26n0101.xml.gz")
	originalOpen := file.Open
	file.Open = func() (io.ReadCloser, error) {
		opens.Add(1)
		return originalOpen()
	}
	importer, err := pubmed.NewImporter(sink, checkpoints)
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}

	err = importer.ImportDaily(context.Background(), pubmed.DailyImport{
		Job:              "daily-conflict",
		Year:             2026,
		PreviousSequence: 100,
	}, []pubmed.BulkFile{file})
	if err == nil {
		t.Fatal("ImportDaily() accepted PreviousSequence conflicting with checkpoint")
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "checkpoint") ||
		!strings.Contains(message, "previous") ||
		!strings.Contains(message, "2") ||
		!strings.Contains(message, "100") {
		t.Fatalf("ImportDaily() error = %v, want explicit sequence conflict", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("BulkFile.Open calls = %d, want conflict rejected before reading file", opens.Load())
	}
	if len(sink.Upserts()) != 0 || len(sink.Deletions()) != 0 {
		t.Fatal("sequence conflict mutated sink")
	}
	progress, ok, loadErr := checkpoints.Load(context.Background(), "daily-conflict")
	if loadErr != nil || !ok || progress.Sequence != 2 {
		t.Fatalf("checkpoint changed after conflict: %#v, %v, %v", progress, ok, loadErr)
	}
}

func TestImporterAbortsOversizedExpandedRecordWithoutDurableMutation(t *testing.T) {
	t.Parallel()

	payload := `<PubmedArticleSet><PubmedArticle><MedlineCitation>` +
		`<PMID>123</PMID><Article><Unknown>` +
		strings.Repeat("x", 1<<20) +
		`</Unknown></Article></MedlineCitation></PubmedArticle></PubmedArticleSet>`
	file := compressedBulkFile(t, "pubmed26n0001.xml.gz", []byte(payload))
	sink := newMemorySink()
	checkpoints := newMemoryCheckpoint()
	importer, err := pubmed.NewImporterWithConfig(sink, checkpoints, pubmed.ImporterConfig{
		MaxCompressedFileBytes: int64(len(payload)),
		MaxRecordBytes:         512,
	})
	if err != nil {
		t.Fatalf("NewImporterWithConfig() error = %v", err)
	}

	err = importer.ImportBaseline(context.Background(), pubmed.BaselineImport{
		Job:  "oversized-expanded-record",
		Year: 2026,
	}, []pubmed.BulkFile{file})
	if !errors.Is(err, pubmed.ErrBulkRecordTooLarge) {
		t.Fatalf("ImportBaseline() error = %v, want ErrBulkRecordTooLarge", err)
	}
	if sink.Aborts() != 1 {
		t.Fatalf("transaction aborts = %d, want 1", sink.Aborts())
	}
	if len(sink.Upserts()) != 0 || len(sink.Deletions()) != 0 {
		t.Fatal("oversized expanded record committed mutations")
	}
	if _, ok, loadErr := checkpoints.Load(context.Background(), "oversized-expanded-record"); loadErr != nil || ok {
		t.Fatalf("checkpoint after oversized record = ok %v, error %v", ok, loadErr)
	}
}

func TestConcurrentImportersUseCheckpointCASWithoutDuplicateEvents(t *testing.T) {
	t.Parallel()

	sink := newMemorySink()
	stored := newMemoryCheckpoint()
	stored.Seed(pubmed.Progress{
		Job:      "daily-cas",
		FileName: "pubmed26n0002.xml.gz",
		SHA256:   strings.Repeat("a", 64),
		Sequence: 2,
		Revision: 7,
		Ordinal:  2,
		Complete: true,
	})
	checkpoints := newLoadBarrierCheckpoint(stored, 2)
	file := fixtureBulkFile(t, "update.xml.gz", "pubmed26n0003.xml.gz")
	spec := pubmed.DailyImport{
		Job:              "daily-cas",
		Year:             2026,
		PreviousSequence: 2,
	}

	importers := make([]*pubmed.Importer, 2)
	for index := range importers {
		importer, err := pubmed.NewImporter(sink, checkpoints)
		if err != nil {
			t.Fatalf("NewImporter() error = %v", err)
		}
		importers[index] = importer
	}

	errs := make(chan error, len(importers))
	for _, importer := range importers {
		go func(importer *pubmed.Importer) {
			errs <- importer.ImportDaily(context.Background(), spec, []pubmed.BulkFile{file})
		}(importer)
	}
	checkpoints.WaitForLoads()
	checkpoints.Release()

	results := []error{<-errs, <-errs}
	successes := 0
	conflicts := 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, pubmed.ErrCheckpointConflict):
			conflicts++
		default:
			t.Fatalf("ImportDaily() errors = %v, want success and checkpoint conflict", results)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("ImportDaily() errors = %v, want one success and one conflict", results)
	}
	if got := upsertPMIDs(sink.Upserts()); !slices.Equal(got, []string{"1001"}) {
		t.Fatalf("committed upserts = %v, want exactly one revision event", got)
	}
	if deletions := sink.Deletions(); len(deletions) != 1 ||
		!slices.Equal(deletions[0].PMIDs, []string{"1002"}) {
		t.Fatalf("committed deletions = %#v, want exactly one deletion event", deletions)
	}
	if sink.Aborts() != 1 {
		t.Fatalf("loser transaction aborts = %d, want 1", sink.Aborts())
	}
	progress, ok, err := stored.Load(context.Background(), spec.Job)
	if err != nil || !ok {
		t.Fatalf("Load() = %#v, %v, %v", progress, ok, err)
	}
	if progress.Sequence != 3 || progress.Revision != 8 {
		t.Fatalf("progress = %#v, want sequence 3 revision 8", progress)
	}
}

func TestImporterAbortUsesIndependentTimeoutAndJoinsFailure(t *testing.T) {
	t.Parallel()

	replaceErr := errors.New("replace failed")
	abortErr := errors.New("abort failed")
	ctx, cancel := context.WithCancel(context.Background())
	transaction := &abortProbeTransaction{
		cancelParent: cancel,
		replaceErr:   replaceErr,
		abortErr:     abortErr,
	}
	importer, err := pubmed.NewImporter(
		&singleTransactionSink{transaction: transaction},
		newMemoryCheckpoint(),
	)
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}
	file := fixtureBulkFile(t, "baseline.xml.gz", "pubmed26n0001.xml.gz")

	err = importer.ImportBaseline(ctx, pubmed.BaselineImport{
		Job:  "abort-context",
		Year: 2026,
	}, []pubmed.BulkFile{file})
	if !errors.Is(err, replaceErr) || !errors.Is(err, abortErr) {
		t.Fatalf("ImportBaseline() error = %v, want joined replace and abort failures", err)
	}
	if transaction.abortContextErr != nil {
		t.Fatalf("Abort() context error at entry = %v, want independent live context", transaction.abortContextErr)
	}
	if !transaction.abortHasDeadline {
		t.Fatal("Abort() context has no deadline")
	}
	if transaction.abortTimeout < 4*time.Second || transaction.abortTimeout > 5*time.Second {
		t.Fatalf("Abort() timeout = %v, want independent 5s timeout", transaction.abortTimeout)
	}
}

func TestSuccessfulAbortDoesNotMaskImportFailure(t *testing.T) {
	t.Parallel()

	replaceErr := errors.New("replace failed")
	ctx, cancel := context.WithCancel(context.Background())
	transaction := &abortProbeTransaction{
		cancelParent: cancel,
		replaceErr:   replaceErr,
	}
	importer, err := pubmed.NewImporter(
		&singleTransactionSink{transaction: transaction},
		newMemoryCheckpoint(),
	)
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}
	file := fixtureBulkFile(t, "baseline.xml.gz", "pubmed26n0001.xml.gz")

	err = importer.ImportBaseline(ctx, pubmed.BaselineImport{
		Job:  "abort-success",
		Year: 2026,
	}, []pubmed.BulkFile{file})
	if !errors.Is(err, replaceErr) {
		t.Fatalf("ImportBaseline() error = %v, want original replace failure", err)
	}
	if transaction.abortCalls != 1 {
		t.Fatalf("Abort() calls = %d, want 1", transaction.abortCalls)
	}
}

func fixtureBulkFile(t *testing.T, fixtureName, fileName string) pubmed.BulkFile {
	t.Helper()

	path := "testdata/" + fixtureName
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	digest := sha256.Sum256(payload)
	return pubmed.BulkFile{
		Name:   fileName,
		SHA256: hex.EncodeToString(digest[:]),
		Open: func() (io.ReadCloser, error) {
			return os.Open(path)
		},
	}
}

func compressedBulkFile(t *testing.T, fileName string, payload []byte) pubmed.BulkFile {
	t.Helper()

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("gzip Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	data := compressed.Bytes()
	digest := sha256.Sum256(data)
	return pubmed.BulkFile{
		Name:   fileName,
		SHA256: hex.EncodeToString(digest[:]),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}
}

type memorySink struct {
	mu           sync.Mutex
	projection   map[string]source.Record
	upserts      []pubmed.Upsert
	deletions    []pubmed.Deletion
	aborts       int
	failPMIDOnce string
	failed       bool
}

func newMemorySink() *memorySink {
	return &memorySink{projection: make(map[string]source.Record)}
}

func (sink *memorySink) BeginFile(
	_ context.Context,
	_ pubmed.SourcePosition,
	expected pubmed.CheckpointExpectation,
) (pubmed.FileTransaction, error) {
	return &memoryFileTransaction{sink: sink, expected: expected}, nil
}

type memoryFileTransaction struct {
	sink      *memorySink
	expected  pubmed.CheckpointExpectation
	upserts   []pubmed.Upsert
	deletions []pubmed.Deletion
	closed    bool
}

func (transaction *memoryFileTransaction) Replace(
	_ context.Context,
	upsert pubmed.Upsert,
) error {
	if transaction.closed {
		return errors.New("transaction is closed")
	}
	sink := transaction.sink
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if upsert.Record.SourceRecordID == sink.failPMIDOnce && !sink.failed {
		sink.failed = true
		return errors.New("injected sink failure")
	}
	transaction.upserts = append(transaction.upserts, upsert)
	return nil
}

func (transaction *memoryFileTransaction) Delete(
	_ context.Context,
	deletion pubmed.Deletion,
) error {
	if transaction.closed {
		return errors.New("transaction is closed")
	}
	transaction.deletions = append(transaction.deletions, deletion)
	return nil
}

func (transaction *memoryFileTransaction) Commit(
	ctx context.Context,
	checkpoint pubmed.Checkpoint,
	expected pubmed.CheckpointExpectation,
	progress pubmed.Progress,
) error {
	if transaction.closed {
		return errors.New("transaction is closed")
	}
	if expected != transaction.expected {
		return errors.New("checkpoint expectation changed after BeginFile")
	}
	if err := checkpoint.CompareAndSwap(ctx, expected, progress); err != nil {
		return err
	}
	sink := transaction.sink
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, upsert := range transaction.upserts {
		sink.projection[upsert.Record.SourceRecordID] = upsert.Record
		sink.upserts = append(sink.upserts, upsert)
	}
	for _, deletion := range transaction.deletions {
		for _, pmid := range deletion.PMIDs {
			delete(sink.projection, pmid)
		}
		sink.deletions = append(sink.deletions, deletion)
	}
	transaction.closed = true
	return nil
}

func (transaction *memoryFileTransaction) Abort(context.Context) error {
	if transaction.closed {
		return nil
	}
	transaction.sink.mu.Lock()
	transaction.sink.aborts++
	transaction.sink.mu.Unlock()
	transaction.upserts = nil
	transaction.deletions = nil
	transaction.closed = true
	return nil
}

func (sink *memorySink) Projection() map[string]source.Record {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	result := make(map[string]source.Record, len(sink.projection))
	for pmid, record := range sink.projection {
		result[pmid] = record
	}
	return result
}

func (sink *memorySink) Upserts() []pubmed.Upsert {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]pubmed.Upsert(nil), sink.upserts...)
}

func (sink *memorySink) Deletions() []pubmed.Deletion {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]pubmed.Deletion(nil), sink.deletions...)
}

func (sink *memorySink) Aborts() int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.aborts
}

func upsertPMIDs(upserts []pubmed.Upsert) []string {
	result := make([]string, len(upserts))
	for index, upsert := range upserts {
		result[index] = upsert.Record.SourceRecordID
	}
	return result
}

type memoryCheckpoint struct {
	mu       sync.Mutex
	progress map[string]pubmed.Progress
}

func newMemoryCheckpoint() *memoryCheckpoint {
	return &memoryCheckpoint{progress: make(map[string]pubmed.Progress)}
}

func (checkpoint *memoryCheckpoint) Load(
	_ context.Context,
	job string,
) (pubmed.Progress, bool, error) {
	checkpoint.mu.Lock()
	defer checkpoint.mu.Unlock()
	progress, ok := checkpoint.progress[job]
	return progress, ok, nil
}

func (checkpoint *memoryCheckpoint) CompareAndSwap(
	_ context.Context,
	expected pubmed.CheckpointExpectation,
	progress pubmed.Progress,
) error {
	checkpoint.mu.Lock()
	defer checkpoint.mu.Unlock()
	current, exists := checkpoint.progress[progress.Job]
	if exists != expected.Exists ||
		(exists &&
			(current.Revision != expected.Revision ||
				current.Sequence != expected.Sequence)) ||
		(!exists && (expected.Revision != 0 || expected.Sequence != 0)) {
		return pubmed.ErrCheckpointConflict
	}
	if progress.Revision != expected.Revision+1 {
		return errors.New("checkpoint revision must advance exactly once")
	}
	checkpoint.progress[progress.Job] = progress
	return nil
}

func (checkpoint *memoryCheckpoint) Seed(progress pubmed.Progress) {
	checkpoint.mu.Lock()
	defer checkpoint.mu.Unlock()
	checkpoint.progress[progress.Job] = progress
}

type loadBarrierCheckpoint struct {
	stored  *memoryCheckpoint
	loaded  sync.WaitGroup
	release chan struct{}
}

func newLoadBarrierCheckpoint(
	stored *memoryCheckpoint,
	loads int,
) *loadBarrierCheckpoint {
	checkpoint := &loadBarrierCheckpoint{
		stored:  stored,
		release: make(chan struct{}),
	}
	checkpoint.loaded.Add(loads)
	return checkpoint
}

func (checkpoint *loadBarrierCheckpoint) Load(
	ctx context.Context,
	job string,
) (pubmed.Progress, bool, error) {
	progress, ok, err := checkpoint.stored.Load(ctx, job)
	checkpoint.loaded.Done()
	select {
	case <-checkpoint.release:
		return progress, ok, err
	case <-ctx.Done():
		return pubmed.Progress{}, false, ctx.Err()
	}
}

func (checkpoint *loadBarrierCheckpoint) CompareAndSwap(
	ctx context.Context,
	expected pubmed.CheckpointExpectation,
	progress pubmed.Progress,
) error {
	return checkpoint.stored.CompareAndSwap(ctx, expected, progress)
}

func (checkpoint *loadBarrierCheckpoint) WaitForLoads() {
	checkpoint.loaded.Wait()
}

func (checkpoint *loadBarrierCheckpoint) Release() {
	close(checkpoint.release)
}

type singleTransactionSink struct {
	transaction pubmed.FileTransaction
}

func (sink *singleTransactionSink) BeginFile(
	context.Context,
	pubmed.SourcePosition,
	pubmed.CheckpointExpectation,
) (pubmed.FileTransaction, error) {
	return sink.transaction, nil
}

type abortProbeTransaction struct {
	cancelParent     context.CancelFunc
	replaceErr       error
	abortErr         error
	abortCalls       int
	abortContextErr  error
	abortHasDeadline bool
	abortTimeout     time.Duration
}

func (transaction *abortProbeTransaction) Replace(context.Context, pubmed.Upsert) error {
	transaction.cancelParent()
	return transaction.replaceErr
}

func (*abortProbeTransaction) Delete(context.Context, pubmed.Deletion) error {
	return nil
}

func (*abortProbeTransaction) Commit(
	context.Context,
	pubmed.Checkpoint,
	pubmed.CheckpointExpectation,
	pubmed.Progress,
) error {
	return errors.New("unexpected Commit call")
}

func (transaction *abortProbeTransaction) Abort(ctx context.Context) error {
	transaction.abortCalls++
	transaction.abortContextErr = ctx.Err()
	deadline, ok := ctx.Deadline()
	transaction.abortHasDeadline = ok
	if ok {
		transaction.abortTimeout = time.Until(deadline)
	}
	return transaction.abortErr
}

var (
	_ pubmed.Sink            = (*memorySink)(nil)
	_ pubmed.FileTransaction = (*memoryFileTransaction)(nil)
	_ pubmed.Checkpoint      = (*memoryCheckpoint)(nil)
	_ pubmed.Checkpoint      = (*loadBarrierCheckpoint)(nil)
	_ pubmed.Sink            = (*singleTransactionSink)(nil)
	_ pubmed.FileTransaction = (*abortProbeTransaction)(nil)
)
