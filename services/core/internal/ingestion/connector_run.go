package ingestion

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrWatermarkConflict = errors.New("connector watermark compare-and-swap conflict")

type WatermarkKind string

const WatermarkTimestamp WatermarkKind = "timestamp"

type ConnectorRunStatus string

const (
	ConnectorRunRunning   ConnectorRunStatus = "running"
	ConnectorRunSucceeded ConnectorRunStatus = "succeeded"
	ConnectorRunFailed    ConnectorRunStatus = "failed"
)

type ConnectorClaim struct {
	Source         string
	Stream         string
	WatermarkKind  WatermarkKind
	From           time.Time
	Until          time.Time
	IdempotencyKey string
}

func NewConnectorClaim(
	source string,
	stream string,
	watermarkKind WatermarkKind,
	from time.Time,
	until time.Time,
	idempotencyKey string,
) (ConnectorClaim, error) {
	claim := ConnectorClaim{
		Source:         strings.TrimSpace(source),
		Stream:         strings.TrimSpace(stream),
		WatermarkKind:  watermarkKind,
		From:           from.UTC(),
		Until:          until.UTC(),
		IdempotencyKey: strings.TrimSpace(idempotencyKey),
	}
	if err := claim.Validate(); err != nil {
		return ConnectorClaim{}, err
	}
	return claim, nil
}

func (claim ConnectorClaim) Validate() error {
	if claim.Source == "" {
		return errors.New("connector claim source is required")
	}
	if claim.Source != strings.TrimSpace(claim.Source) {
		return errors.New("connector claim source must be trimmed")
	}
	if claim.Stream == "" {
		return errors.New("connector claim stream is required")
	}
	if claim.Stream != strings.TrimSpace(claim.Stream) {
		return errors.New("connector claim stream must be trimmed")
	}
	if claim.WatermarkKind != WatermarkTimestamp {
		return fmt.Errorf(
			"connector claim watermark kind %q is unsupported",
			claim.WatermarkKind,
		)
	}
	if claim.From.IsZero() {
		return errors.New("connector claim from watermark is required")
	}
	if claim.Until.IsZero() || !claim.Until.After(claim.From) {
		return errors.New("connector claim until watermark must follow from")
	}
	if claim.IdempotencyKey == "" {
		return errors.New("connector claim idempotency key is required")
	}
	if claim.IdempotencyKey != strings.TrimSpace(claim.IdempotencyKey) {
		return errors.New("connector claim idempotency key must be trimmed")
	}
	return nil
}

type ConnectorRun struct {
	ID                       string
	Claim                    ConnectorClaim
	ExpectedWatermarkVersion int64
	Status                   ConnectorRunStatus
	FailureStage             string
	FailureCode              string
}

func RestoreConnectorRun(
	id string,
	claim ConnectorClaim,
	expectedWatermarkVersion int64,
	status ConnectorRunStatus,
) (ConnectorRun, error) {
	run := ConnectorRun{
		ID:                       strings.TrimSpace(id),
		Claim:                    claim,
		ExpectedWatermarkVersion: expectedWatermarkVersion,
		Status:                   status,
	}
	if err := run.Validate(); err != nil {
		return ConnectorRun{}, err
	}
	return run, nil
}

func (run ConnectorRun) Validate() error {
	if _, err := uuid.Parse(run.ID); err != nil {
		return errors.New("connector run ID must be a UUID")
	}
	if err := run.Claim.Validate(); err != nil {
		return err
	}
	if run.ExpectedWatermarkVersion < 0 {
		return errors.New("connector run expected watermark version must not be negative")
	}
	switch run.Status {
	case ConnectorRunRunning, ConnectorRunSucceeded:
		if run.FailureStage != "" || run.FailureCode != "" {
			return errors.New("non-failed connector run cannot contain failure metadata")
		}
	case ConnectorRunFailed:
		if strings.TrimSpace(run.FailureStage) == "" ||
			strings.TrimSpace(run.FailureCode) == "" {
			return errors.New("failed connector run requires stage and code")
		}
	default:
		return fmt.Errorf("invalid connector run status %q", run.Status)
	}
	return nil
}

func (run ConnectorRun) Succeed() (ConnectorRun, error) {
	if err := run.Validate(); err != nil {
		return ConnectorRun{}, err
	}
	if run.Status != ConnectorRunRunning {
		return ConnectorRun{}, fmt.Errorf(
			"connector run cannot succeed from status %q",
			run.Status,
		)
	}
	run.Status = ConnectorRunSucceeded
	return run, nil
}

func (run ConnectorRun) Fail(
	stage string,
	code string,
) (ConnectorRun, error) {
	if err := run.Validate(); err != nil {
		return ConnectorRun{}, err
	}
	if run.Status != ConnectorRunRunning {
		return ConnectorRun{}, fmt.Errorf(
			"connector run cannot fail from status %q",
			run.Status,
		)
	}
	run.Status = ConnectorRunFailed
	run.FailureStage = strings.TrimSpace(stage)
	run.FailureCode = strings.TrimSpace(code)
	if err := run.Validate(); err != nil {
		return ConnectorRun{}, err
	}
	return run, nil
}

type ConnectorPageReceipt struct {
	RunID         string
	PageOrdinal   int
	CursorIn      string
	CursorOut     string
	ContentSHA256 string
	RecordCount   int
}

func NewConnectorPageReceipt(
	runID string,
	pageOrdinal int,
	cursorIn string,
	cursorOut string,
	contentSHA256 string,
	recordCount int,
) (ConnectorPageReceipt, error) {
	receipt := ConnectorPageReceipt{
		RunID:         strings.TrimSpace(runID),
		PageOrdinal:   pageOrdinal,
		CursorIn:      cursorIn,
		CursorOut:     cursorOut,
		ContentSHA256: strings.TrimSpace(contentSHA256),
		RecordCount:   recordCount,
	}
	if err := receipt.Validate(); err != nil {
		return ConnectorPageReceipt{}, err
	}
	return receipt, nil
}

func (receipt ConnectorPageReceipt) Validate() error {
	if _, err := uuid.Parse(receipt.RunID); err != nil {
		return errors.New("connector page receipt run ID must be a UUID")
	}
	if receipt.PageOrdinal < 1 {
		return errors.New("connector page receipt ordinal must be positive")
	}
	if receipt.CursorIn == "" {
		return errors.New("connector page receipt cursor-in is required")
	}
	if !lowerSHA256Pattern.MatchString(receipt.ContentSHA256) {
		return errors.New("connector page receipt requires a lowercase SHA256")
	}
	if receipt.RecordCount < 0 {
		return errors.New("connector page receipt record count must not be negative")
	}
	return nil
}
