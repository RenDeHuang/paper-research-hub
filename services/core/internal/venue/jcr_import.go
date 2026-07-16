package venue

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidJCRCSV        = errors.New("invalid JCR CSV")
	ErrNoVenueMatch         = errors.New("no venue matches exact ISSN identifiers")
	ErrMultipleVenueMatches = errors.New("multiple venues match exact ISSN identifiers")
	ErrConflictingMetric    = errors.New("conflicting JCR metric row")
	ErrJCRLimitExceeded     = errors.New("JCR CSV limit exceeded")
)

var decimalPattern = regexp.MustCompile(`^(0|[0-9]+)(\.[0-9]+)?$`)

type Decimal struct {
	digits string
	scale  int
}

func ParseDecimal(raw string) (Decimal, error) {
	value := strings.TrimSpace(raw)
	if !decimalPattern.MatchString(value) {
		return Decimal{}, fmt.Errorf("invalid nonnegative decimal %q", raw)
	}

	integer, fraction, _ := strings.Cut(value, ".")
	integer = strings.TrimLeft(integer, "0")
	if integer == "" {
		integer = "0"
	}
	fraction = strings.TrimRight(fraction, "0")
	if fraction == "" {
		return Decimal{digits: integer}, nil
	}

	digits := strings.TrimLeft(integer+fraction, "0")
	if digits == "" {
		return Decimal{digits: "0"}, nil
	}
	return Decimal{digits: digits, scale: len(fraction)}, nil
}

func (value Decimal) Valid() bool {
	if value.digits == "" || value.scale < 0 {
		return false
	}
	for _, digit := range value.digits {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	if value.digits == "0" {
		return value.scale == 0
	}
	return value.digits[0] != '0'
}

func (value Decimal) String() string {
	if !value.Valid() {
		return ""
	}
	if value.scale == 0 {
		return value.digits
	}
	if value.scale >= len(value.digits) {
		return "0." + strings.Repeat("0", value.scale-len(value.digits)) + value.digits
	}
	offset := len(value.digits) - value.scale
	return value.digits[:offset] + "." + value.digits[offset:]
}

func (value Decimal) Cmp(other Decimal) int {
	if !value.Valid() || !other.Valid() {
		panic("cannot compare invalid Decimal values")
	}

	left, ok := new(big.Int).SetString(value.digits, 10)
	if !ok {
		panic("valid Decimal has invalid coefficient")
	}
	right, ok := new(big.Int).SetString(other.digits, 10)
	if !ok {
		panic("valid Decimal has invalid coefficient")
	}
	switch {
	case value.scale < other.scale:
		left.Mul(left, decimalPower(other.scale-value.scale))
	case value.scale > other.scale:
		right.Mul(right, decimalPower(value.scale-other.scale))
	}
	return left.Cmp(right)
}

func decimalPower(exponent int) *big.Int {
	return new(big.Int).Exp(
		big.NewInt(10),
		big.NewInt(int64(exponent)),
		nil,
	)
}

type Quartile string

const (
	QuartileQ1 Quartile = "Q1"
	QuartileQ2 Quartile = "Q2"
	QuartileQ3 Quartile = "Q3"
	QuartileQ4 Quartile = "Q4"
)

func (quartile Quartile) Valid() bool {
	switch quartile {
	case QuartileQ1, QuartileQ2, QuartileQ3, QuartileQ4:
		return true
	default:
		return false
	}
}

type MetricStatus string

const (
	MetricStatusKnown   MetricStatus = "known"
	MetricStatusUnknown MetricStatus = "unknown"
)

func (status MetricStatus) Valid() bool {
	switch status {
	case MetricStatusKnown, MetricStatusUnknown:
		return true
	default:
		return false
	}
}

type MetricKey struct {
	venueID    string
	metricYear int
	category   string
}

func NewMetricKey(venueID string, metricYear int, category string) (MetricKey, error) {
	normalizedVenueID := strings.TrimSpace(venueID)
	if normalizedVenueID == "" {
		return MetricKey{}, errors.New("metric venue ID is required")
	}
	normalizedCategory := strings.TrimSpace(category)
	if normalizedCategory == "" {
		return MetricKey{}, errors.New("metric category is required")
	}
	if metricYear < 1900 || metricYear > 3000 {
		return MetricKey{}, fmt.Errorf("metric_year %d is outside 1900..3000", metricYear)
	}
	return MetricKey{
		venueID:    normalizedVenueID,
		metricYear: metricYear,
		category:   normalizedCategory,
	}, nil
}

func (key MetricKey) VenueID() string {
	return key.venueID
}

func (key MetricKey) MetricYear() int {
	return key.metricYear
}

func (key MetricKey) Category() string {
	return key.category
}

func (key MetricKey) String() string {
	if key.venueID == "" || key.category == "" {
		return ""
	}
	return fmt.Sprintf(
		"%d:%s:%d:%d:%s",
		len(key.venueID),
		key.venueID,
		key.metricYear,
		len(key.category),
		key.category,
	)
}

type MetricSnapshot struct {
	key      MetricKey
	jif      Decimal
	hasJIF   bool
	quartile Quartile
	status   MetricStatus
	source   string
}

func NewMetricSnapshot(
	venueID string,
	metricYear int,
	category string,
	jif *Decimal,
	quartile Quartile,
	status MetricStatus,
	source string,
) (MetricSnapshot, error) {
	key, err := NewMetricKey(venueID, metricYear, category)
	if err != nil {
		return MetricSnapshot{}, err
	}
	if !status.Valid() {
		return MetricSnapshot{}, fmt.Errorf("invalid metric status %q", status)
	}
	normalizedSource := strings.TrimSpace(source)
	if normalizedSource == "" {
		return MetricSnapshot{}, errors.New("metric source is required")
	}

	var storedJIF Decimal
	hasJIF := jif != nil
	if hasJIF {
		if !jif.Valid() {
			return MetricSnapshot{}, errors.New("metric JIF is invalid")
		}
		storedJIF = *jif
	}
	switch status {
	case MetricStatusKnown:
		if !hasJIF || !quartile.Valid() {
			return MetricSnapshot{}, errors.New("known metric requires JIF and quartile")
		}
	case MetricStatusUnknown:
		if hasJIF || quartile != "" {
			return MetricSnapshot{}, errors.New("unknown metric must not contain JIF or quartile")
		}
	}

	return MetricSnapshot{
		key:      key,
		jif:      storedJIF,
		hasJIF:   hasJIF,
		quartile: quartile,
		status:   status,
		source:   normalizedSource,
	}, nil
}

func (snapshot MetricSnapshot) Key() MetricKey {
	return snapshot.key
}

func (snapshot MetricSnapshot) VenueID() string {
	return snapshot.key.VenueID()
}

func (snapshot MetricSnapshot) MetricYear() int {
	return snapshot.key.MetricYear()
}

func (snapshot MetricSnapshot) Category() string {
	return snapshot.key.Category()
}

func (snapshot MetricSnapshot) HasJIF() bool {
	return snapshot.hasJIF
}

func (snapshot MetricSnapshot) JIF() Decimal {
	if !snapshot.hasJIF {
		return Decimal{}
	}
	return snapshot.jif
}

func (snapshot MetricSnapshot) Quartile() Quartile {
	return snapshot.quartile
}

func (snapshot MetricSnapshot) Status() MetricStatus {
	return snapshot.status
}

func (snapshot MetricSnapshot) Source() string {
	return snapshot.source
}

func (snapshot MetricSnapshot) Equal(other MetricSnapshot) bool {
	if snapshot.key != other.key ||
		snapshot.hasJIF != other.hasJIF ||
		snapshot.quartile != other.quartile ||
		snapshot.status != other.status ||
		snapshot.source != other.source {
		return false
	}
	return !snapshot.hasJIF || snapshot.jif.Cmp(other.jif) == 0
}

type ImportReceipt struct {
	fileSHA256    string
	source        string
	importedAt    time.Time
	inputRows     int
	insertedRows  int
	unchangedRows int
}

type ImportResult = ImportReceipt

func NewImportReceipt(
	fileSHA256 string,
	source string,
	importedAt time.Time,
	inputRows int,
	insertedRows int,
	unchangedRows int,
) (ImportReceipt, error) {
	normalizedSHA := strings.ToLower(strings.TrimSpace(fileSHA256))
	decoded, err := hex.DecodeString(normalizedSHA)
	if err != nil || len(decoded) != sha256.Size {
		return ImportReceipt{}, errors.New("import receipt requires a SHA-256 hex digest")
	}
	normalizedSource := strings.TrimSpace(source)
	if normalizedSource == "" {
		return ImportReceipt{}, errors.New("import receipt source is required")
	}
	if importedAt.IsZero() {
		return ImportReceipt{}, errors.New("import receipt requires an import timestamp")
	}
	if inputRows < 0 || insertedRows < 0 || unchangedRows < 0 {
		return ImportReceipt{}, errors.New("import receipt row counts must be nonnegative")
	}
	if insertedRows+unchangedRows != inputRows {
		return ImportReceipt{}, fmt.Errorf(
			"import receipt counts are inconsistent: input=%d inserted=%d unchanged=%d",
			inputRows,
			insertedRows,
			unchangedRows,
		)
	}
	return ImportReceipt{
		fileSHA256:    normalizedSHA,
		source:        normalizedSource,
		importedAt:    importedAt.UTC(),
		inputRows:     inputRows,
		insertedRows:  insertedRows,
		unchangedRows: unchangedRows,
	}, nil
}

func (receipt ImportReceipt) FileSHA256() string {
	return receipt.fileSHA256
}

func (receipt ImportReceipt) Source() string {
	return receipt.source
}

func (receipt ImportReceipt) ImportedAt() time.Time {
	return receipt.importedAt
}

func (receipt ImportReceipt) InputRows() int {
	return receipt.inputRows
}

func (receipt ImportReceipt) InsertedRows() int {
	return receipt.insertedRows
}

func (receipt ImportReceipt) UnchangedRows() int {
	return receipt.unchangedRows
}

func (receipt ImportReceipt) Valid() bool {
	_, err := NewImportReceipt(
		receipt.fileSHA256,
		receipt.source,
		receipt.importedAt,
		receipt.inputRows,
		receipt.insertedRows,
		receipt.unchangedRows,
	)
	return err == nil
}

type VenueAliasEvidence struct {
	venueID string
	alias   Alias
}

func NewVenueAliasEvidence(venueID string, alias Alias) (VenueAliasEvidence, error) {
	normalizedVenueID := strings.TrimSpace(venueID)
	if normalizedVenueID == "" {
		return VenueAliasEvidence{}, errors.New("alias evidence venue ID is required")
	}
	if !alias.Valid() {
		return VenueAliasEvidence{}, errors.New("alias evidence requires a valid alias")
	}
	return VenueAliasEvidence{venueID: normalizedVenueID, alias: alias}, nil
}

func (evidence VenueAliasEvidence) VenueID() string {
	return evidence.venueID
}

func (evidence VenueAliasEvidence) Alias() Alias {
	return evidence.alias
}

type JCRImport struct {
	fileSHA256    string
	source        string
	importedAt    time.Time
	inputRows     int
	unchangedRows int
	rows          []MetricSnapshot
	aliases       []VenueAliasEvidence
}

func newJCRImport(
	fileSHA256 string,
	source string,
	importedAt time.Time,
	inputRows int,
	unchangedRows int,
	rows []MetricSnapshot,
	aliases []VenueAliasEvidence,
) (JCRImport, error) {
	if inputRows < 0 || unchangedRows < 0 ||
		len(rows)+unchangedRows != inputRows {
		return JCRImport{}, errors.New("JCR import row counts are inconsistent")
	}
	receipt, err := NewImportReceipt(
		fileSHA256,
		source,
		importedAt,
		inputRows,
		len(rows),
		unchangedRows,
	)
	if err != nil {
		return JCRImport{}, err
	}
	return JCRImport{
		fileSHA256:    receipt.FileSHA256(),
		source:        receipt.Source(),
		importedAt:    receipt.ImportedAt(),
		inputRows:     inputRows,
		unchangedRows: unchangedRows,
		rows:          slices.Clone(rows),
		aliases:       slices.Clone(aliases),
	}, nil
}

func (batch JCRImport) FileSHA256() string {
	return batch.fileSHA256
}

func (batch JCRImport) Source() string {
	return batch.source
}

func (batch JCRImport) ImportedAt() time.Time {
	return batch.importedAt
}

func (batch JCRImport) InputRows() int {
	return batch.inputRows
}

func (batch JCRImport) UnchangedRows() int {
	return batch.unchangedRows
}

func (batch JCRImport) Rows() []MetricSnapshot {
	return slices.Clone(batch.rows)
}

func (batch JCRImport) Aliases() []VenueAliasEvidence {
	return slices.Clone(batch.aliases)
}

type JCRRepository interface {
	FindVenuesByISSNs(context.Context, ISSNSet) ([]Venue, error)
}

type JCRSink interface {
	// PersistJCRImport must atomically record the immutable file receipt,
	// insert-only metric rows, and aliases. It must return
	// ErrConflictingMetric if a concurrent writer stored a different row for
	// the same MetricKey. Identical rows and identical file hashes are
	// idempotent.
	PersistJCRImport(context.Context, JCRImport) (ImportReceipt, error)
}

type JCRImporter struct {
	repository     JCRRepository
	sink           JCRSink
	clock          func() time.Time
	limits         JCRImportLimits
	spoolDirectory string
}

type resolvedJCRRow struct {
	snapshot MetricSnapshot
	title    string
}

type JCRImportLimits struct {
	MaxBytes         int64
	MaxRows          int
	MaxFieldBytes    int
	MaxDecimalPlaces int
}

func DefaultJCRImportLimits() JCRImportLimits {
	return JCRImportLimits{
		MaxBytes:         32 << 20,
		MaxRows:          250_000,
		MaxFieldBytes:    16 << 10,
		MaxDecimalPlaces: 6,
	}
}

func (limits JCRImportLimits) validate() error {
	if limits.MaxBytes <= 0 {
		return errors.New("JCR maximum bytes must be positive")
	}
	if limits.MaxRows <= 0 {
		return errors.New("JCR maximum rows must be positive")
	}
	if limits.MaxFieldBytes <= 0 {
		return errors.New("JCR maximum field bytes must be positive")
	}
	if limits.MaxDecimalPlaces < 0 {
		return errors.New("JCR maximum decimal places must be nonnegative")
	}
	return nil
}

type JCRImporterConfig struct {
	Clock          func() time.Time
	Limits         JCRImportLimits
	SpoolDirectory string
}

func NewJCRImporter(
	repository JCRRepository,
	sink JCRSink,
	clock func() time.Time,
) (*JCRImporter, error) {
	return NewJCRImporterWithConfig(
		repository,
		sink,
		JCRImporterConfig{
			Clock:  clock,
			Limits: DefaultJCRImportLimits(),
		},
	)
}

func NewJCRImporterWithConfig(
	repository JCRRepository,
	sink JCRSink,
	config JCRImporterConfig,
) (*JCRImporter, error) {
	if repository == nil {
		return nil, errors.New("JCR repository is required")
	}
	if sink == nil {
		return nil, errors.New("JCR sink is required")
	}
	if config.Clock == nil {
		return nil, errors.New("JCR import clock is required")
	}
	if err := config.Limits.validate(); err != nil {
		return nil, err
	}
	return &JCRImporter{
		repository:     repository,
		sink:           sink,
		clock:          config.Clock,
		limits:         config.Limits,
		spoolDirectory: config.SpoolDirectory,
	}, nil
}

func (importer *JCRImporter) Import(
	ctx context.Context,
	source io.Reader,
) (result ImportResult, returnErr error) {
	if err := ctx.Err(); err != nil {
		return ImportResult{}, err
	}
	if source == nil {
		return ImportResult{}, errors.New("JCR CSV reader is required")
	}

	spool, fileSHA256, err := spoolJCRCSV(
		ctx,
		source,
		importer.limits,
		importer.spoolDirectory,
	)
	if err != nil {
		return ImportResult{}, err
	}
	defer func() {
		var cleanupErr error
		if err := spool.Close(); err != nil {
			cleanupErr = fmt.Errorf("close JCR CSV spool: %w", err)
		}
		if err := os.Remove(spool.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErr := fmt.Errorf("remove JCR CSV spool: %w", err)
			cleanupErr = errors.Join(cleanupErr, removeErr)
		}
		returnErr = errors.Join(returnErr, cleanupErr)
	}()

	parsedRows, err := parseJCRCSV(ctx, spool, importer.limits)
	if err != nil {
		return ImportResult{}, err
	}
	fileSource := parsedRows[0].source
	for index, row := range parsedRows[1:] {
		if row.source != fileSource {
			return ImportResult{}, fmt.Errorf(
				"%w: CSV rows must use a single source; row 2 has %q and row %d has %q",
				ErrInvalidJCRCSV,
				fileSource,
				index+3,
				row.source,
			)
		}
	}
	if err := ctx.Err(); err != nil {
		return ImportResult{}, err
	}

	resolved := make([]resolvedJCRRow, 0, len(parsedRows))
	for index, row := range parsedRows {
		if err := ctx.Err(); err != nil {
			return ImportResult{}, err
		}
		matches, err := importer.repository.FindVenuesByISSNs(ctx, row.identifiers)
		if err != nil {
			return ImportResult{}, fmt.Errorf("resolve JCR CSV row %d venue: %w", index+2, err)
		}
		matches = uniqueVenues(matches)
		switch len(matches) {
		case 0:
			return ImportResult{}, fmt.Errorf(
				"JCR CSV row %d: %w: %s",
				index+2,
				ErrNoVenueMatch,
				strings.Join(row.identifiers.ExactValues(), ", "),
			)
		case 1:
		default:
			return ImportResult{}, fmt.Errorf(
				"JCR CSV row %d: %w: matched venue IDs %s",
				index+2,
				ErrMultipleVenueMatches,
				joinedVenueIDs(matches),
			)
		}

		snapshot, err := NewMetricSnapshot(
			matches[0].ID(),
			row.metricYear,
			row.category,
			row.jif,
			row.quartile,
			row.status,
			row.source,
		)
		if err != nil {
			return ImportResult{}, fmt.Errorf("JCR CSV row %d: %w", index+2, err)
		}
		resolved = append(resolved, resolvedJCRRow{snapshot: snapshot, title: row.title})
	}

	rowsToPersist := make([]MetricSnapshot, 0, len(resolved))
	for _, row := range resolved {
		rowsToPersist = append(rowsToPersist, row.snapshot)
	}

	aliases, err := collectAliasEvidence(resolved)
	if err != nil {
		return ImportResult{}, err
	}
	importedAt := importer.clock()
	if importedAt.IsZero() {
		return ImportResult{}, errors.New("JCR import clock returned a zero timestamp")
	}
	batch, err := newJCRImport(
		fileSHA256,
		fileSource,
		importedAt,
		len(parsedRows),
		0,
		rowsToPersist,
		aliases,
	)
	if err != nil {
		return ImportResult{}, fmt.Errorf("build JCR import batch: %w", err)
	}

	receipt, err := importer.sink.PersistJCRImport(ctx, batch)
	if err != nil {
		return ImportResult{}, fmt.Errorf("persist JCR import: %w", err)
	}
	if !receipt.Valid() ||
		receipt.FileSHA256() != batch.FileSHA256() ||
		receipt.Source() != batch.Source() ||
		receipt.InputRows() != batch.InputRows() {
		return ImportResult{}, errors.New("JCR sink returned an inconsistent import receipt")
	}
	return receipt, nil
}

type parsedJCRRow struct {
	title       string
	identifiers ISSNSet
	metricYear  int
	category    string
	jif         *Decimal
	quartile    Quartile
	status      MetricStatus
	source      string
}

func spoolJCRCSV(
	ctx context.Context,
	source io.Reader,
	limits JCRImportLimits,
	directory string,
) (_ *os.File, _ string, returnErr error) {
	spool, err := os.CreateTemp(directory, "jcr-import-*.csv")
	if err != nil {
		return nil, "", fmt.Errorf("create JCR CSV spool: %w", err)
	}
	defer func() {
		if returnErr == nil {
			return
		}
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()
	if err := spool.Chmod(0o600); err != nil {
		return nil, "", fmt.Errorf("secure JCR CSV spool: %w", err)
	}

	hasher := sha256.New()
	contextSource := &contextReader{ctx: ctx, reader: source}
	written, err := io.CopyBuffer(
		io.MultiWriter(spool, hasher),
		io.LimitReader(contextSource, limits.MaxBytes),
		make([]byte, 32<<10),
	)
	if err != nil {
		return nil, "", fmt.Errorf("read JCR CSV: %w", err)
	}
	var extra [1]byte
	extraBytes, err := contextSource.Read(extra[:])
	if extraBytes > 0 {
		return nil, "", fmt.Errorf(
			"%w: input exceeds maximum bytes %d",
			ErrJCRLimitExceeded,
			limits.MaxBytes,
		)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, "", fmt.Errorf("read JCR CSV after %d bytes: %w", written, err)
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, "", fmt.Errorf("rewind JCR CSV spool: %w", err)
	}
	return spool, hex.EncodeToString(hasher.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	read, err := reader.reader.Read(buffer)
	if contextErr := reader.ctx.Err(); contextErr != nil {
		return read, contextErr
	}
	return read, err
}

func parseJCRCSV(
	ctx context.Context,
	source io.Reader,
	limits JCRImportLimits,
) ([]parsedJCRRow, error) {
	reader := csv.NewReader(&contextReader{ctx: ctx, reader: source})
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: CSV is empty", ErrInvalidJCRCSV)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read CSV header: %w", ErrInvalidJCRCSV, err)
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\uFEFF")
	}
	if err := validateJCRFields(header, 1, limits.MaxFieldBytes); err != nil {
		return nil, err
	}

	indexes := make(map[string]int, len(header))
	for index, raw := range header {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, fmt.Errorf("%w: blank CSV column at position %d", ErrInvalidJCRCSV, index+1)
		}
		if _, duplicate := indexes[name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate CSV column %q", ErrInvalidJCRCSV, name)
		}
		indexes[name] = index
	}
	for _, required := range []string{
		"issn_l",
		"issn",
		"eissn",
		"metric_year",
		"category",
		"jif",
		"quartile",
		"status",
		"source",
	} {
		if _, found := indexes[required]; !found {
			return nil, fmt.Errorf("%w: required CSV column %q is missing", ErrInvalidJCRCSV, required)
		}
	}

	var rows []parsedJCRRow
	for line := 2; ; line++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: CSV row %d: %w", ErrInvalidJCRCSV, line, err)
		}
		if len(rows) >= limits.MaxRows {
			return nil, fmt.Errorf(
				"%w: input exceeds maximum rows %d",
				ErrJCRLimitExceeded,
				limits.MaxRows,
			)
		}
		if err := validateJCRFields(record, line, limits.MaxFieldBytes); err != nil {
			return nil, err
		}
		row, err := parseJCRRow(record, indexes, limits.MaxDecimalPlaces)
		if err != nil {
			return nil, fmt.Errorf("%w: CSV row %d: %w", ErrInvalidJCRCSV, line, err)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: CSV contains no metric rows", ErrInvalidJCRCSV)
	}
	return rows, nil
}

func validateJCRFields(record []string, line int, maxFieldBytes int) error {
	for column, field := range record {
		if len(field) > maxFieldBytes {
			return fmt.Errorf(
				"%w: CSV row %d field %d exceeds maximum field bytes %d",
				ErrJCRLimitExceeded,
				line,
				column+1,
				maxFieldBytes,
			)
		}
	}
	return nil
}

func parseJCRRow(
	record []string,
	indexes map[string]int,
	maxDecimalPlaces int,
) (parsedJCRRow, error) {
	field := func(name string) string {
		return strings.TrimSpace(record[indexes[name]])
	}

	issnLRaw := field("issn_l")
	if issnLRaw == "" {
		return parsedJCRRow{}, errors.New("issn_l is required")
	}
	printRaw := field("issn")
	if printRaw == "" {
		return parsedJCRRow{}, errors.New("issn is required")
	}
	electronicRaw := field("eissn")
	if electronicRaw == "" {
		return parsedJCRRow{}, errors.New("eissn is required")
	}
	issnL, err := ParseISSN(ISSNRoleLinking, issnLRaw)
	if err != nil {
		return parsedJCRRow{}, err
	}
	printISSN, err := ParseISSN(ISSNRolePrint, printRaw)
	if err != nil {
		return parsedJCRRow{}, err
	}
	electronicISSN, err := ParseISSN(ISSNRoleElectronic, electronicRaw)
	if err != nil {
		return parsedJCRRow{}, err
	}
	identifiers, err := NewISSNSet(issnL, printISSN, electronicISSN)
	if err != nil {
		return parsedJCRRow{}, err
	}

	yearRaw := field("metric_year")
	metricYear, err := strconv.Atoi(yearRaw)
	if err != nil || metricYear < 1900 || metricYear > 3000 {
		return parsedJCRRow{}, fmt.Errorf("metric_year %q is outside 1900..3000", yearRaw)
	}
	category := field("category")
	if category == "" {
		return parsedJCRRow{}, errors.New("category is required")
	}
	source := field("source")
	if source == "" {
		return parsedJCRRow{}, errors.New("source is required")
	}

	status := MetricStatus(field("status"))
	if !status.Valid() {
		return parsedJCRRow{}, fmt.Errorf("invalid metric status %q", status)
	}
	jifRaw := field("jif")
	quartileRaw := field("quartile")
	var jif *Decimal
	var quartile Quartile
	switch status {
	case MetricStatusKnown:
		if jifRaw == "" || quartileRaw == "" {
			return parsedJCRRow{}, errors.New("known metric requires JIF and quartile")
		}
		if decimalPlaces(jifRaw) > maxDecimalPlaces {
			return parsedJCRRow{}, fmt.Errorf(
				"%w: JIF %q exceeds maximum decimal places %d",
				ErrJCRLimitExceeded,
				jifRaw,
				maxDecimalPlaces,
			)
		}
		parsedJIF, err := ParseDecimal(jifRaw)
		if err != nil {
			return parsedJCRRow{}, fmt.Errorf("invalid JIF: %w", err)
		}
		jif = &parsedJIF
		quartile = Quartile(strings.ToUpper(quartileRaw))
		if !quartile.Valid() {
			return parsedJCRRow{}, fmt.Errorf("invalid JCR quartile %q", quartileRaw)
		}
	case MetricStatusUnknown:
		if jifRaw != "" || quartileRaw != "" {
			return parsedJCRRow{}, errors.New("unknown metric must not contain JIF or quartile")
		}
	}

	title := ""
	if _, found := indexes["title"]; found {
		title = field("title")
	}
	return parsedJCRRow{
		title:       title,
		identifiers: identifiers,
		metricYear:  metricYear,
		category:    category,
		jif:         jif,
		quartile:    quartile,
		status:      status,
		source:      source,
	}, nil
}

func decimalPlaces(raw string) int {
	_, fraction, found := strings.Cut(strings.TrimSpace(raw), ".")
	if !found {
		return 0
	}
	return len(fraction)
}

func uniqueVenues(values []Venue) []Venue {
	seen := make(map[string]struct{}, len(values))
	result := make([]Venue, 0, len(values))
	for _, value := range values {
		if _, duplicate := seen[value.ID()]; duplicate {
			continue
		}
		seen[value.ID()] = struct{}{}
		result = append(result, value)
	}
	return result
}

func joinedVenueIDs(values []Venue) string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.ID())
	}
	slices.Sort(result)
	return strings.Join(result, ", ")
}

func collectAliasEvidence(rows []resolvedJCRRow) ([]VenueAliasEvidence, error) {
	seen := make(map[string]struct{}, len(rows))
	result := make([]VenueAliasEvidence, 0, len(rows))
	for _, row := range rows {
		if row.title == "" {
			continue
		}
		alias, err := NewAlias(row.title, row.snapshot.Source())
		if err != nil {
			return nil, err
		}
		evidence, err := NewVenueAliasEvidence(row.snapshot.VenueID(), alias)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf(
			"%d:%s:%d:%s:%d:%s",
			len(evidence.VenueID()),
			evidence.VenueID(),
			len(alias.Value()),
			alias.Value(),
			len(alias.Source()),
			alias.Source(),
		)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, evidence)
	}
	return result, nil
}
