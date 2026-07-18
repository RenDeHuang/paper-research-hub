package scope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConflictingAdmissionDecision = errors.New(
	"conflicting immutable admission decision",
)

type AdmissionRecord struct {
	ID             string
	Decision       AdmissionDecision
	SourceRecordID string
	SourcePath     string
}

type admissionRecordEvidence struct {
	SourcePaths []string `json:"source_paths"`
}

type PostgresAdmissionStore struct {
	pool *pgxpool.Pool
}

func NewPostgresAdmissionStore(
	pool *pgxpool.Pool,
) (*PostgresAdmissionStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &PostgresAdmissionStore{pool: pool}, nil
}

func (store *PostgresAdmissionStore) Persist(
	ctx context.Context,
	record AdmissionRecord,
) (AdmissionRecord, error) {
	if ctx == nil {
		return AdmissionRecord{}, errors.New(
			"admission persistence context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return AdmissionRecord{}, err
	}
	if store == nil || store.pool == nil {
		return AdmissionRecord{}, errors.New(
			"PostgresAdmissionStore is not initialized",
		)
	}
	if record.ID != "" {
		return AdmissionRecord{}, errors.New(
			"new admission record cannot provide a persisted ID",
		)
	}
	record.Decision.DecidedAt = normalizeAdmissionDecisionTimestamp(
		record.Decision.DecidedAt,
	)
	if err := validateAdmissionRecord(record); err != nil {
		return AdmissionRecord{}, err
	}
	rawEvidence, err := json.Marshal(admissionRecordEvidence{
		SourcePaths: slices.Clone(record.Decision.SourcePaths),
	})
	if err != nil {
		return AdmissionRecord{}, fmt.Errorf(
			"encode admission decision evidence: %w",
			err,
		)
	}
	err = store.pool.QueryRow(ctx, `
		INSERT INTO work_channel_admission_decisions (
			work_id,
			channel,
			decision,
			reason,
			source_record_id,
			source_path,
			admission_policy_version,
			domain_registry_version,
			journal_policy_version,
			channel_registry_version,
			evidence,
			decided_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12
		)
		ON CONFLICT ON CONSTRAINT
			work_channel_admission_decisions_identity_key
		DO NOTHING
		RETURNING id::text
	`,
		record.Decision.WorkID,
		nullableAdmissionString(string(record.Decision.Channel)),
		record.Decision.Decision,
		record.Decision.Reason,
		record.SourceRecordID,
		record.SourcePath,
		record.Decision.AdmissionPolicyVersion,
		record.Decision.DomainRegistryVersion,
		nullableAdmissionString(record.Decision.JournalPolicyVersion),
		nullableAdmissionString(record.Decision.ChannelRegistryVersion),
		rawEvidence,
		record.Decision.DecidedAt,
	).Scan(&record.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, found, findErr := store.findByIdentity(
			ctx,
			record.Decision.WorkID,
			record.Decision.Channel,
			record.Decision.AdmissionPolicyVersion,
			record.SourceRecordID,
		)
		if findErr != nil {
			return AdmissionRecord{}, findErr
		}
		if !found || !admissionRecordsEquivalent(existing, record) {
			return AdmissionRecord{}, ErrConflictingAdmissionDecision
		}
		return existing, nil
	}
	if err != nil {
		return AdmissionRecord{}, fmt.Errorf(
			"persist admission decision: %w",
			err,
		)
	}
	return record, nil
}

func (store *PostgresAdmissionStore) Find(
	ctx context.Context,
	recordID string,
) (AdmissionRecord, bool, error) {
	if ctx == nil {
		return AdmissionRecord{}, false, errors.New(
			"admission lookup context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return AdmissionRecord{}, false, err
	}
	if store == nil || store.pool == nil {
		return AdmissionRecord{}, false, errors.New(
			"PostgresAdmissionStore is not initialized",
		)
	}
	if recordID == "" || recordID != strings.TrimSpace(recordID) {
		return AdmissionRecord{}, false, errors.New(
			"admission lookup requires an exact record ID",
		)
	}
	record, found, err := scanAdmissionRecord(store.pool.QueryRow(ctx, `
		SELECT
			id::text,
			work_id::text,
			channel,
			decision,
			reason,
			source_record_id::text,
			source_path,
			admission_policy_version,
			domain_registry_version,
			journal_policy_version,
			channel_registry_version,
			evidence,
			decided_at
		FROM work_channel_admission_decisions
		WHERE id = $1
	`, recordID))
	if err != nil {
		return AdmissionRecord{}, false, fmt.Errorf(
			"find admission decision: %w",
			err,
		)
	}
	return record, found, nil
}

func (store *PostgresAdmissionStore) findByIdentity(
	ctx context.Context,
	workID string,
	channel ContentChannel,
	admissionPolicyVersion string,
	sourceRecordID string,
) (AdmissionRecord, bool, error) {
	record, found, err := scanAdmissionRecord(store.pool.QueryRow(ctx, `
		SELECT
			id::text,
			work_id::text,
			channel,
			decision,
			reason,
			source_record_id::text,
			source_path,
			admission_policy_version,
			domain_registry_version,
			journal_policy_version,
			channel_registry_version,
			evidence,
			decided_at
		FROM work_channel_admission_decisions
		WHERE work_id = $1
		  AND channel IS NOT DISTINCT FROM $2
		  AND admission_policy_version = $3
		  AND source_record_id = $4
	`, workID, nullableAdmissionString(string(channel)),
		admissionPolicyVersion, sourceRecordID))
	if err != nil {
		return AdmissionRecord{}, false, fmt.Errorf(
			"find admission decision identity: %w",
			err,
		)
	}
	return record, found, nil
}

type admissionRow interface {
	Scan(...any) error
}

func scanAdmissionRecord(
	row admissionRow,
) (AdmissionRecord, bool, error) {
	var (
		record                 AdmissionRecord
		channel                pgtype.Text
		journalPolicyVersion   pgtype.Text
		channelRegistryVersion pgtype.Text
		rawEvidence            []byte
	)
	err := row.Scan(
		&record.ID,
		&record.Decision.WorkID,
		&channel,
		&record.Decision.Decision,
		&record.Decision.Reason,
		&record.SourceRecordID,
		&record.SourcePath,
		&record.Decision.AdmissionPolicyVersion,
		&record.Decision.DomainRegistryVersion,
		&journalPolicyVersion,
		&channelRegistryVersion,
		&rawEvidence,
		&record.Decision.DecidedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdmissionRecord{}, false, nil
	}
	if err != nil {
		return AdmissionRecord{}, false, err
	}
	if channel.Valid {
		record.Decision.Channel = ContentChannel(channel.String)
	}
	if journalPolicyVersion.Valid {
		record.Decision.JournalPolicyVersion = journalPolicyVersion.String
	}
	if channelRegistryVersion.Valid {
		record.Decision.ChannelRegistryVersion = channelRegistryVersion.String
	}
	var evidence admissionRecordEvidence
	if err := json.Unmarshal(rawEvidence, &evidence); err != nil {
		return AdmissionRecord{}, false, fmt.Errorf(
			"decode admission decision evidence: %w",
			err,
		)
	}
	record.Decision.SourcePaths = slices.Clone(evidence.SourcePaths)
	record.Decision.DecidedAt = normalizeAdmissionDecisionTimestamp(
		record.Decision.DecidedAt,
	)
	if err := validateAdmissionRecord(record); err != nil {
		return AdmissionRecord{}, false, fmt.Errorf(
			"restore admission decision: %w",
			err,
		)
	}
	return record, true, nil
}

func validateAdmissionRecord(record AdmissionRecord) error {
	if err := record.Decision.Validate(); err != nil {
		return err
	}
	if record.SourceRecordID == "" ||
		record.SourceRecordID != strings.TrimSpace(record.SourceRecordID) {
		return errors.New("admission record requires an exact source record ID")
	}
	if record.SourcePath == "" ||
		record.SourcePath != strings.TrimSpace(record.SourcePath) {
		return errors.New("admission record requires an exact source path")
	}
	return nil
}

func admissionRecordsEquivalent(
	left AdmissionRecord,
	right AdmissionRecord,
) bool {
	return left.Decision.WorkID == right.Decision.WorkID &&
		left.Decision.Channel == right.Decision.Channel &&
		left.Decision.Decision == right.Decision.Decision &&
		left.Decision.Reason == right.Decision.Reason &&
		left.Decision.AdmissionPolicyVersion ==
			right.Decision.AdmissionPolicyVersion &&
		left.Decision.DomainRegistryVersion ==
			right.Decision.DomainRegistryVersion &&
		left.Decision.JournalPolicyVersion ==
			right.Decision.JournalPolicyVersion &&
		left.Decision.ChannelRegistryVersion ==
			right.Decision.ChannelRegistryVersion &&
		slices.Equal(left.Decision.SourcePaths, right.Decision.SourcePaths) &&
		left.Decision.DecidedAt.Equal(right.Decision.DecidedAt) &&
		left.SourceRecordID == right.SourceRecordID &&
		left.SourcePath == right.SourcePath
}

func nullableAdmissionString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
