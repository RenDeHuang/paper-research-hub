package pubmed

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

const (
	maxBulkRecordBytes            = 64 << 20
	DefaultMaxCompressedFileBytes = int64(1 << 30)
)

var (
	bulkFilePattern     = regexp.MustCompile(`^pubmed([0-9]{2})n([0-9]{4})\.xml\.gz$`)
	sha256Pattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ErrBulkFileTooLarge = errors.New("PubMed compressed bulk file exceeds configured spool limit")
)

type BulkFile struct {
	Name   string
	SHA256 string
	Open   func() (io.ReadCloser, error)
}

type SourcePosition struct {
	Job      string
	FileName string
	SHA256   string
	Sequence int
	Ordinal  int64
}

type Upsert struct {
	Position SourcePosition
	Record   source.Record
}

type Deletion struct {
	Position SourcePosition
	PMIDs    []string
	Raw      source.RawRecord
}

// Sink starts one isolated transaction for an already SHA256-verified source
// file. No staged mutation may become visible before FileTransaction.Commit.
type Sink interface {
	BeginFile(context.Context, SourcePosition) (FileTransaction, error)
}

// FileTransaction stages all projection replacements and deletion audit events
// for one source file. Commit must atomically make both the staged mutations and
// the supplied checkpoint durable. If Commit returns an error, no staged
// mutation or checkpoint may remain committed. Abort discards all staged work.
type FileTransaction interface {
	Replace(context.Context, Upsert) error
	Delete(context.Context, Deletion) error
	Commit(context.Context, Checkpoint, Progress) error
	Abort(context.Context) error
}

type Progress struct {
	Job      string
	FileName string
	SHA256   string
	Sequence int
	Ordinal  int64
	Complete bool
}

// Checkpoint persists the last durable source position for one import job.
// Save is a FileTransaction.Commit participant; the importer never advances
// progress independently of the file transaction.
type Checkpoint interface {
	Load(context.Context, string) (Progress, bool, error)
	Save(context.Context, Progress) error
}

type BaselineImport struct {
	Job  string
	Year int
}

type DailyImport struct {
	Job              string
	Year             int
	PreviousSequence int
}

type Importer struct {
	sink                   Sink
	checkpoints            Checkpoint
	maxCompressedFileBytes int64
	tempDir                string
}

func NewImporter(sink Sink, checkpoints Checkpoint) (*Importer, error) {
	return NewImporterWithConfig(sink, checkpoints, ImporterConfig{
		MaxCompressedFileBytes: DefaultMaxCompressedFileBytes,
		TempDir:                os.TempDir(),
	})
}

type ImporterConfig struct {
	MaxCompressedFileBytes int64
	TempDir                string
}

func NewImporterWithConfig(
	sink Sink,
	checkpoints Checkpoint,
	config ImporterConfig,
) (*Importer, error) {
	if sink == nil {
		return nil, errors.New("PubMed bulk sink is required")
	}
	if checkpoints == nil {
		return nil, errors.New("PubMed bulk checkpoint is required")
	}
	if config.MaxCompressedFileBytes <= 0 {
		return nil, errors.New("PubMed compressed bulk file spool limit must be positive")
	}
	tempDir := strings.TrimSpace(config.TempDir)
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	return &Importer{
		sink:                   sink,
		checkpoints:            checkpoints,
		maxCompressedFileBytes: config.MaxCompressedFileBytes,
		tempDir:                tempDir,
	}, nil
}

func (importer *Importer) ImportBaseline(
	ctx context.Context,
	spec BaselineImport,
	files []BulkFile,
) error {
	if importer == nil {
		return errors.New("PubMed bulk importer is nil")
	}
	job, err := validateImportIdentity(ctx, spec.Job, spec.Year)
	if err != nil {
		return err
	}
	progress, hasProgress, err := importer.checkpoints.Load(ctx, job)
	if err != nil {
		return fmt.Errorf("load PubMed baseline checkpoint for job %q: %w", job, err)
	}
	if hasProgress {
		if err := validateDurableProgress(job, progress); err != nil {
			return err
		}
	}
	ordered, err := validateBulkFiles(
		files,
		spec.Year,
		1,
		progress,
		hasProgress,
		"baseline",
	)
	if err != nil {
		return err
	}
	return importer.importFiles(ctx, job, ordered, progress, hasProgress)
}

func (importer *Importer) ImportDaily(
	ctx context.Context,
	spec DailyImport,
	files []BulkFile,
) error {
	if importer == nil {
		return errors.New("PubMed bulk importer is nil")
	}
	job, err := validateImportIdentity(ctx, spec.Job, spec.Year)
	if err != nil {
		return err
	}
	if spec.PreviousSequence < 0 {
		return errors.New("PubMed daily previous sequence must not be negative")
	}
	progress, hasProgress, err := importer.checkpoints.Load(ctx, job)
	if err != nil {
		return fmt.Errorf("load PubMed daily checkpoint for job %q: %w", job, err)
	}
	initialSequence := spec.PreviousSequence + 1
	if hasProgress {
		if err := validateDurableProgress(job, progress); err != nil {
			return err
		}
		if spec.PreviousSequence != progress.Sequence {
			return fmt.Errorf(
				"PubMed daily PreviousSequence %d conflicts with durable checkpoint sequence %d for job %q",
				spec.PreviousSequence,
				progress.Sequence,
				job,
			)
		}
		initialSequence = progress.Sequence + 1
	}
	ordered, err := validateBulkFiles(
		files,
		spec.Year,
		initialSequence,
		Progress{},
		false,
		"daily",
	)
	if err != nil {
		return err
	}
	return importer.importFiles(ctx, job, ordered, progress, hasProgress)
}

func validateDurableProgress(job string, progress Progress) error {
	if progress.Job != job {
		return fmt.Errorf(
			"PubMed checkpoint job %q conflicts with requested job %q",
			progress.Job,
			job,
		)
	}
	if progress.Sequence <= 0 ||
		progress.Ordinal < 0 ||
		strings.TrimSpace(progress.FileName) == "" ||
		!sha256Pattern.MatchString(progress.SHA256) {
		return fmt.Errorf("PubMed checkpoint for job %q is malformed", job)
	}
	if !progress.Complete {
		return fmt.Errorf(
			"PubMed checkpoint for job %q is not a durable completed file",
			job,
		)
	}
	return nil
}

func validateImportIdentity(
	ctx context.Context,
	rawJob string,
	year int,
) (string, error) {
	if ctx == nil {
		return "", errors.New("PubMed bulk import context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	job := strings.TrimSpace(rawJob)
	if job == "" {
		return "", errors.New("PubMed bulk import job is required")
	}
	if year < 1900 || year > 9999 {
		return "", errors.New("PubMed bulk import requires an explicit four-digit year")
	}
	return job, nil
}

type orderedBulkFile struct {
	BulkFile
	sequence int
}

func validateBulkFiles(
	files []BulkFile,
	year int,
	initialSequence int,
	progress Progress,
	hasProgress bool,
	kind string,
) ([]orderedBulkFile, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("PubMed %s import requires at least one file", kind)
	}

	ordered := make([]orderedBulkFile, len(files))
	seen := make(map[int]struct{}, len(files))
	for index, file := range files {
		sequence, err := parseBulkFileName(file.Name, year)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[sequence]; exists {
			return nil, fmt.Errorf(
				"PubMed %s import contains duplicate file sequence %d",
				kind,
				sequence,
			)
		}
		seen[sequence] = struct{}{}
		if !sha256Pattern.MatchString(strings.TrimSpace(file.SHA256)) {
			return nil, fmt.Errorf("PubMed bulk file %q requires a lowercase SHA256", file.Name)
		}
		if file.Open == nil {
			return nil, fmt.Errorf("PubMed bulk file %q requires an opener", file.Name)
		}
		ordered[index] = orderedBulkFile{BulkFile: file, sequence: sequence}
	}

	for index := 1; index < len(ordered); index++ {
		if ordered[index].sequence < ordered[index-1].sequence {
			return nil, fmt.Errorf(
				"PubMed %s files are out of order at %q",
				kind,
				ordered[index].Name,
			)
		}
	}

	first := ordered[0].sequence
	allowedFirst := first == initialSequence
	if hasProgress {
		if progress.Complete {
			allowedFirst = allowedFirst || first == progress.Sequence+1
		} else {
			allowedFirst = allowedFirst || first == progress.Sequence
		}
	}
	if !allowedFirst {
		if first < initialSequence ||
			(hasProgress && first < progress.Sequence) {
			return nil, fmt.Errorf(
				"PubMed %s file %q is out of order; first sequence is %d",
				kind,
				ordered[0].Name,
				first,
			)
		}
		return nil, fmt.Errorf(
			"PubMed %s file sequence is missing before %q",
			kind,
			ordered[0].Name,
		)
	}
	for index := 1; index < len(ordered); index++ {
		if ordered[index].sequence != ordered[index-1].sequence+1 {
			return nil, fmt.Errorf(
				"PubMed %s file sequence is missing between %q and %q",
				kind,
				ordered[index-1].Name,
				ordered[index].Name,
			)
		}
	}
	return ordered, nil
}

func parseBulkFileName(name string, year int) (int, error) {
	matches := bulkFilePattern.FindStringSubmatch(strings.TrimSpace(name))
	if matches == nil {
		return 0, fmt.Errorf("invalid PubMed bulk XML gzip file name %q", name)
	}
	wantYear := fmt.Sprintf("%02d", year%100)
	if matches[1] != wantYear {
		return 0, fmt.Errorf(
			"PubMed bulk file %q does not match declared year %d",
			name,
			year,
		)
	}
	sequence, err := strconv.Atoi(matches[2])
	if err != nil || sequence <= 0 {
		return 0, fmt.Errorf("invalid PubMed bulk file sequence in %q", name)
	}
	return sequence, nil
}

func (importer *Importer) importFiles(
	ctx context.Context,
	job string,
	files []orderedBulkFile,
	progress Progress,
	hasProgress bool,
) error {
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if hasProgress && file.sequence < progress.Sequence {
			continue
		}

		if hasProgress && file.sequence == progress.Sequence {
			if progress.FileName != file.Name || progress.SHA256 != file.SHA256 {
				return fmt.Errorf(
					"PubMed checkpoint for job %q conflicts with file %q or SHA256",
					job,
					file.Name,
				)
			}
			continue
		}

		nextProgress, err := importer.importFile(ctx, job, file)
		if err != nil {
			return err
		}
		progress = nextProgress
		hasProgress = true
	}
	return nil
}

func (importer *Importer) importFile(
	ctx context.Context,
	job string,
	file orderedBulkFile,
) (Progress, error) {
	spool, actualSHA256, err := importer.spoolFile(ctx, file)
	if err != nil {
		return Progress{}, err
	}
	spoolPath := spool.Name()
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spoolPath)
	}()

	if actualSHA256 != file.SHA256 {
		return Progress{}, fmt.Errorf(
			"PubMed bulk file %q SHA256 = %s, want %s",
			file.Name,
			actualSHA256,
			file.SHA256,
		)
	}

	filePosition := SourcePosition{
		Job:      job,
		FileName: file.Name,
		SHA256:   file.SHA256,
		Sequence: file.sequence,
	}
	transaction, err := importer.sink.BeginFile(ctx, filePosition)
	if err != nil {
		return Progress{}, fmt.Errorf(
			"begin PubMed file transaction for %q: %w",
			file.Name,
			err,
		)
	}
	if transaction == nil {
		return Progress{}, fmt.Errorf(
			"begin PubMed file transaction for %q returned nil transaction",
			file.Name,
		)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Abort(context.WithoutCancel(ctx))
		}
	}()

	lastOrdinal, err := importSpooledFile(ctx, file, filePosition, spool, transaction)
	if err != nil {
		return Progress{}, err
	}
	if err := spool.Close(); err != nil {
		return Progress{}, fmt.Errorf("close PubMed spool for file %q: %w", file.Name, err)
	}

	progress := Progress{
		Job:      job,
		FileName: file.Name,
		SHA256:   file.SHA256,
		Sequence: file.sequence,
		Ordinal:  lastOrdinal,
		Complete: true,
	}
	if err := transaction.Commit(ctx, importer.checkpoints, progress); err != nil {
		return Progress{}, fmt.Errorf(
			"commit PubMed file transaction for %q: %w",
			file.Name,
			err,
		)
	}
	committed = true
	return progress, nil
}

func (importer *Importer) spoolFile(
	ctx context.Context,
	file orderedBulkFile,
) (*os.File, string, error) {
	sourceReader, err := file.Open()
	if err != nil {
		return nil, "", fmt.Errorf("open PubMed bulk file %q: %w", file.Name, err)
	}
	sourceClosed := false
	defer func() {
		if !sourceClosed {
			_ = sourceReader.Close()
		}
	}()

	spool, err := os.CreateTemp(importer.tempDir, "pubmed-import-*.xml.gz")
	if err != nil {
		return nil, "", fmt.Errorf("create controlled PubMed spool for %q: %w", file.Name, err)
	}
	spoolPath := spool.Name()
	succeeded := false
	defer func() {
		if !succeeded {
			_ = spool.Close()
			_ = os.Remove(spoolPath)
		}
	}()

	hasher := sha256.New()
	buffer := make([]byte, 32<<10)
	var total int64
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		count, readErr := sourceReader.Read(buffer)
		if count > 0 {
			emptyReads = 0
			if total > importer.maxCompressedFileBytes-int64(count) {
				return nil, "", fmt.Errorf(
					"%w: file %q limit %d bytes",
					ErrBulkFileTooLarge,
					file.Name,
					importer.maxCompressedFileBytes,
				)
			}
			chunk := buffer[:count]
			if _, err := spool.Write(chunk); err != nil {
				return nil, "", fmt.Errorf("write PubMed spool for %q: %w", file.Name, err)
			}
			_, _ = hasher.Write(chunk)
			total += int64(count)
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return nil, "", fmt.Errorf("read PubMed bulk file %q: %w", file.Name, io.ErrNoProgress)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, "", fmt.Errorf("read PubMed bulk file %q: %w", file.Name, readErr)
		}
	}
	if err := sourceReader.Close(); err != nil {
		return nil, "", fmt.Errorf("close PubMed bulk file %q: %w", file.Name, err)
	}
	sourceClosed = true
	if err := spool.Sync(); err != nil {
		return nil, "", fmt.Errorf("sync PubMed spool for %q: %w", file.Name, err)
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, "", fmt.Errorf("rewind PubMed spool for %q: %w", file.Name, err)
	}
	succeeded = true
	return spool, hex.EncodeToString(hasher.Sum(nil)), nil
}

func importSpooledFile(
	ctx context.Context,
	file orderedBulkFile,
	filePosition SourcePosition,
	spool io.Reader,
	transaction FileTransaction,
) (int64, error) {
	gzipReader, err := gzip.NewReader(spool)
	if err != nil {
		return 0, fmt.Errorf("open PubMed gzip stream %q: %w", file.Name, err)
	}

	var lastOrdinal int64
	scanErr := scanBulkEvents(gzipReader, func(event bulkEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastOrdinal = event.ordinal
		position := filePosition
		position.Ordinal = event.ordinal
		switch event.kind {
		case bulkEventUpsert:
			record, err := ParseRecord(event.raw)
			if err != nil {
				return fmt.Errorf(
					"parse PubMed bulk file %q record %d: %w",
					file.Name,
					event.ordinal,
					err,
				)
			}
			if err := transaction.Replace(ctx, Upsert{
				Position: position,
				Record:   record,
			}); err != nil {
				return fmt.Errorf(
					"replace PubMed projection from file %q record %d: %w",
					file.Name,
					event.ordinal,
					err,
				)
			}
		case bulkEventDelete:
			deletion, err := parseDeletion(event.raw, position)
			if err != nil {
				return fmt.Errorf(
					"parse PubMed deletion from file %q record %d: %w",
					file.Name,
					event.ordinal,
					err,
				)
			}
			if err := transaction.Delete(ctx, deletion); err != nil {
				return fmt.Errorf(
					"apply PubMed deletion from file %q record %d: %w",
					file.Name,
					event.ordinal,
					err,
				)
			}
		default:
			return fmt.Errorf("unknown PubMed bulk event kind %q", event.kind)
		}
		return nil
	})
	gzipCloseErr := gzipReader.Close()
	if scanErr != nil {
		return lastOrdinal, scanErr
	}
	if gzipCloseErr != nil {
		return lastOrdinal, fmt.Errorf("close PubMed gzip stream %q: %w", file.Name, gzipCloseErr)
	}
	return lastOrdinal, nil
}

type bulkEventKind string

const (
	bulkEventUpsert bulkEventKind = "upsert"
	bulkEventDelete bulkEventKind = "delete"
)

type bulkEvent struct {
	kind    bulkEventKind
	ordinal int64
	raw     []byte
}

func scanBulkEvents(reader io.Reader, yield func(bulkEvent) error) error {
	capture := newCaptureReader(reader)
	decoder := xml.NewDecoder(capture)
	depth := 0
	var ordinal int64

	for {
		startOffset := decoder.InputOffset()
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode PubMed bulk XML stream: %w", err)
		}

		switch value := token.(type) {
		case xml.StartElement:
			if depth == 1 &&
				(value.Name.Local == "PubmedArticle" ||
					value.Name.Local == "DeleteCitation") {
				eventKind := bulkEventUpsert
				if value.Name.Local == "DeleteCitation" {
					eventKind = bulkEventDelete
				}
				elementDepth := 1
				for elementDepth > 0 {
					next, err := decoder.Token()
					if err != nil {
						return fmt.Errorf(
							"decode PubMed bulk %s element: %w",
							value.Name.Local,
							err,
						)
					}
					switch next.(type) {
					case xml.StartElement:
						elementDepth++
					case xml.EndElement:
						elementDepth--
					}
				}
				endOffset := decoder.InputOffset()
				if endOffset-startOffset > maxBulkRecordBytes {
					return fmt.Errorf(
						"PubMed bulk %s element exceeds %d bytes",
						value.Name.Local,
						maxBulkRecordBytes,
					)
				}
				raw, err := capture.slice(startOffset, endOffset)
				if err != nil {
					return err
				}
				ordinal++
				if err := yield(bulkEvent{
					kind:    eventKind,
					ordinal: ordinal,
					raw:     raw,
				}); err != nil {
					return err
				}
				capture.discardBefore(endOffset)
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
}

type deleteCitationXML struct {
	PMIDs []string `xml:"PMID"`
}

func parseDeletion(raw []byte, position SourcePosition) (Deletion, error) {
	rawRecord, err := source.NewRawXMLRecord(raw)
	if err != nil {
		return Deletion{}, err
	}
	var payload deleteCitationXML
	if err := xml.Unmarshal(raw, &payload); err != nil {
		return Deletion{}, fmt.Errorf("decode DeleteCitation XML: %w", err)
	}
	if len(payload.PMIDs) == 0 {
		return Deletion{}, errors.New("DeleteCitation requires at least one PMID")
	}
	pmids := make([]string, 0, len(payload.PMIDs))
	for _, rawPMID := range payload.PMIDs {
		pmid, err := requiredPMID(rawPMID)
		if err != nil {
			return Deletion{}, err
		}
		pmids = appendUnique(pmids, pmid)
	}
	return Deletion{
		Position: position,
		PMIDs:    pmids,
		Raw:      rawRecord,
	}, nil
}
