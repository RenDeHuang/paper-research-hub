package venueenrich

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const VerificationStatusPendingClarivate = "pending_clarivate_verification"

var sourceCSVHeader = []string{
	"domain",
	"source_order",
	"journal_name",
	"impact_factor",
	"jcr_value",
	"cass_value",
	"source_url",
	"verification_status",
}

type sourceKey struct {
	domain      string
	sourceOrder int
}

type sourceLocation struct {
	path      string
	rowNumber int
}

func LoadSourceFiles(paths []string) ([]SourceRow, error) {
	rows := make([]SourceRow, 0)
	seen := make(map[sourceKey]sourceLocation)

	for _, path := range paths {
		fileRows, err := loadSourceFile(path, seen)
		if err != nil {
			return nil, err
		}
		rows = append(rows, fileRows...)
	}
	return rows, nil
}

func loadSourceFile(
	path string,
	seen map[sourceKey]sourceLocation,
) ([]SourceRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: open source CSV: %w", path, err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: row 1: source CSV is empty", path)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: row 1: read CSV header: %w", path, err)
	}
	if err := validateSourceCSVUTF8(path, 1, header); err != nil {
		return nil, err
	}
	if !slices.Equal(header, sourceCSVHeader) {
		return nil, fmt.Errorf(
			"%s: row 1: exact header mismatch: expected %q, actual %q",
			path,
			strings.Join(sourceCSVHeader, ","),
			strings.Join(header, ","),
		)
	}
	reader.FieldsPerRecord = len(sourceCSVHeader)

	rows := make([]SourceRow, 0)
	for rowNumber := 2; ; rowNumber++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf(
				"%s: row %d: read CSV record: %w",
				path,
				rowNumber,
				readErr,
			)
		}
		if err := validateSourceCSVUTF8(path, rowNumber, record); err != nil {
			return nil, err
		}

		sourceOrderRaw := strings.TrimSpace(record[1])
		sourceOrder, err := strconv.Atoi(sourceOrderRaw)
		if err != nil {
			return nil, fmt.Errorf(
				"%s: row %d: source_order %q must be an integer: %w",
				path,
				rowNumber,
				sourceOrderRaw,
				err,
			)
		}

		row := SourceRow{
			Domain:            strings.TrimSpace(record[0]),
			SourceOrder:       sourceOrder,
			SourceJournalName: strings.TrimSpace(record[2]),
			ImpactFactor:      strings.TrimSpace(record[3]),
			JCRValue:          strings.TrimSpace(record[4]),
			CASSValue:         strings.TrimSpace(record[5]),
			SourceURL:         strings.TrimSpace(record[6]),
		}
		if err := row.Validate(); err != nil {
			return nil, fmt.Errorf(
				"%s: row %d: invalid source row: %w",
				path,
				rowNumber,
				err,
			)
		}

		verificationStatus := strings.TrimSpace(record[7])
		if verificationStatus != VerificationStatusPendingClarivate {
			return nil, fmt.Errorf(
				"%s: row %d: verification_status %q must equal %q",
				path,
				rowNumber,
				verificationStatus,
				VerificationStatusPendingClarivate,
			)
		}

		key := sourceKey{
			domain:      row.Domain,
			sourceOrder: row.SourceOrder,
		}
		if first, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf(
				"%s: row %d: duplicate source key (%q, %d); first seen at %s row %d",
				path,
				rowNumber,
				key.domain,
				key.sourceOrder,
				first.path,
				first.rowNumber,
			)
		}
		seen[key] = sourceLocation{path: path, rowNumber: rowNumber}
		rows = append(rows, row)
	}
	return rows, nil
}

func validateSourceCSVUTF8(path string, rowNumber int, fields []string) error {
	for fieldIndex, field := range fields {
		if utf8.ValidString(field) {
			continue
		}

		if fieldIndex < len(sourceCSVHeader) {
			return fmt.Errorf(
				"%s: row %d: field %q (index %d) contains invalid UTF-8",
				path,
				rowNumber,
				sourceCSVHeader[fieldIndex],
				fieldIndex,
			)
		}
		return fmt.Errorf(
			"%s: row %d: field index %d contains invalid UTF-8",
			path,
			rowNumber,
			fieldIndex,
		)
	}
	return nil
}
