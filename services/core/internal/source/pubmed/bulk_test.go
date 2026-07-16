package pubmed_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

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

func TestImporterResumesFromLastDurableFileOrdinal(t *testing.T) {
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
	if loadErr != nil || !ok {
		t.Fatalf("Load() = %#v, %v, %v", progress, ok, loadErr)
	}
	if progress.Ordinal != 1 || progress.Complete {
		t.Fatalf("progress after failure = %#v, want durable ordinal 1", progress)
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

type memorySink struct {
	mu           sync.Mutex
	projection   map[string]source.Record
	upserts      []pubmed.Upsert
	deletions    []pubmed.Deletion
	failPMIDOnce string
	failed       bool
}

func newMemorySink() *memorySink {
	return &memorySink{projection: make(map[string]source.Record)}
}

func (sink *memorySink) Replace(_ context.Context, upsert pubmed.Upsert) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if upsert.Record.SourceRecordID == sink.failPMIDOnce && !sink.failed {
		sink.failed = true
		return errors.New("injected sink failure")
	}
	sink.projection[upsert.Record.SourceRecordID] = upsert.Record
	sink.upserts = append(sink.upserts, upsert)
	return nil
}

func (sink *memorySink) Delete(_ context.Context, deletion pubmed.Deletion) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, pmid := range deletion.PMIDs {
		delete(sink.projection, pmid)
	}
	sink.deletions = append(sink.deletions, deletion)
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

func (checkpoint *memoryCheckpoint) Save(
	_ context.Context,
	progress pubmed.Progress,
) error {
	checkpoint.mu.Lock()
	defer checkpoint.mu.Unlock()
	checkpoint.progress[progress.Job] = progress
	return nil
}

var (
	_ pubmed.Sink       = (*memorySink)(nil)
	_ pubmed.Checkpoint = (*memoryCheckpoint)(nil)
)
