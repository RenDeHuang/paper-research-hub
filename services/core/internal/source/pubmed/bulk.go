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
	"regexp"
	"strconv"
	"strings"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

const maxBulkRecordBytes = 64 << 20

var (
	bulkFilePattern = regexp.MustCompile(`^pubmed([0-9]{2})n([0-9]{4})\.xml\.gz$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
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

// Sink replaces the current PMID projection for every Upsert and atomically
// removes all listed PMIDs while retaining each Deletion as an audit event.
// Implementations must make the source position idempotent because a process
// can fail after the sink commits and before the checkpoint becomes durable.
type Sink interface {
	Replace(context.Context, Upsert) error
	Delete(context.Context, Deletion) error
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
// Save is called only after the corresponding sink mutation succeeds.
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
	sink        Sink
	checkpoints Checkpoint
}

func NewImporter(sink Sink, checkpoints Checkpoint) (*Importer, error) {
	if sink == nil {
		return nil, errors.New("PubMed bulk sink is required")
	}
	if checkpoints == nil {
		return nil, errors.New("PubMed bulk checkpoint is required")
	}
	return &Importer{sink: sink, checkpoints: checkpoints}, nil
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
	ordered, err := validateBulkFiles(
		files,
		spec.Year,
		spec.PreviousSequence+1,
		progress,
		hasProgress,
		"daily",
	)
	if err != nil {
		return err
	}
	return importer.importFiles(ctx, job, ordered, progress, hasProgress)
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

		resumeOrdinal := int64(0)
		if hasProgress && file.sequence == progress.Sequence {
			if progress.FileName != file.Name || progress.SHA256 != file.SHA256 {
				return fmt.Errorf(
					"PubMed checkpoint for job %q conflicts with file %q or SHA256",
					job,
					file.Name,
				)
			}
			if progress.Complete {
				continue
			}
			resumeOrdinal = progress.Ordinal
		}

		if err := verifyBulkFile(ctx, file); err != nil {
			return err
		}
		lastOrdinal, err := importer.importFile(ctx, job, file, resumeOrdinal)
		if err != nil {
			return err
		}
		progress = Progress{
			Job:      job,
			FileName: file.Name,
			SHA256:   file.SHA256,
			Sequence: file.sequence,
			Ordinal:  lastOrdinal,
			Complete: true,
		}
		if err := importer.checkpoints.Save(ctx, progress); err != nil {
			return fmt.Errorf(
				"save completed PubMed checkpoint for file %q: %w",
				file.Name,
				err,
			)
		}
		hasProgress = true
	}
	return nil
}

func verifyBulkFile(ctx context.Context, file orderedBulkFile) error {
	reader, err := file.Open()
	if err != nil {
		return fmt.Errorf("open PubMed bulk file %q for SHA256 verification: %w", file.Name, err)
	}
	hasher := sha256.New()
	payload := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			_ = reader.Close()
			return err
		}
		count, readErr := reader.Read(payload)
		if count > 0 {
			_, _ = hasher.Write(payload[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = reader.Close()
			return fmt.Errorf("hash PubMed bulk file %q: %w", file.Name, readErr)
		}
	}
	if err := reader.Close(); err != nil {
		return fmt.Errorf("close PubMed bulk file %q after SHA256 verification: %w", file.Name, err)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != file.SHA256 {
		return fmt.Errorf(
			"PubMed bulk file %q SHA256 = %s, want %s",
			file.Name,
			actual,
			file.SHA256,
		)
	}
	return nil
}

func (importer *Importer) importFile(
	ctx context.Context,
	job string,
	file orderedBulkFile,
	resumeOrdinal int64,
) (int64, error) {
	compressed, err := file.Open()
	if err != nil {
		return 0, fmt.Errorf("open PubMed bulk file %q: %w", file.Name, err)
	}
	gzipReader, err := gzip.NewReader(compressed)
	if err != nil {
		_ = compressed.Close()
		return 0, fmt.Errorf("open PubMed gzip stream %q: %w", file.Name, err)
	}

	var lastOrdinal int64
	scanErr := scanBulkEvents(gzipReader, func(event bulkEvent) error {
		lastOrdinal = event.ordinal
		if event.ordinal <= resumeOrdinal {
			return nil
		}
		position := SourcePosition{
			Job:      job,
			FileName: file.Name,
			SHA256:   file.SHA256,
			Sequence: file.sequence,
			Ordinal:  event.ordinal,
		}
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
			if err := importer.sink.Replace(ctx, Upsert{
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
			if err := importer.sink.Delete(ctx, deletion); err != nil {
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

		if err := importer.checkpoints.Save(ctx, Progress{
			Job:      job,
			FileName: file.Name,
			SHA256:   file.SHA256,
			Sequence: file.sequence,
			Ordinal:  event.ordinal,
			Complete: false,
		}); err != nil {
			return fmt.Errorf(
				"save PubMed checkpoint for file %q record %d: %w",
				file.Name,
				event.ordinal,
				err,
			)
		}
		return nil
	})
	gzipCloseErr := gzipReader.Close()
	compressedCloseErr := compressed.Close()
	if scanErr != nil {
		return lastOrdinal, scanErr
	}
	if gzipCloseErr != nil {
		return lastOrdinal, fmt.Errorf("close PubMed gzip stream %q: %w", file.Name, gzipCloseErr)
	}
	if compressedCloseErr != nil {
		return lastOrdinal, fmt.Errorf("close PubMed bulk file %q: %w", file.Name, compressedCloseErr)
	}
	if lastOrdinal < resumeOrdinal {
		return lastOrdinal, fmt.Errorf(
			"PubMed checkpoint ordinal %d exceeds file %q record count %d",
			resumeOrdinal,
			file.Name,
			lastOrdinal,
		)
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
