package scope

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const channelRegistryImportAdvisoryNamespace int32 = 0x43484e4c

var (
	ErrConflictingPreprintRegistry = errors.New(
		"conflicting preprint Registry import",
	)
	ErrConflictingConferenceRegistry = errors.New(
		"conflicting conference Registry import",
	)
)

type ChannelRegistryReceipt struct {
	registryName    string
	registryVersion string
	fileSHA256      string
	entryCount      int
	ruleCount       int
	importedAt      time.Time
}

func (receipt ChannelRegistryReceipt) RegistryName() string {
	return receipt.registryName
}

func (receipt ChannelRegistryReceipt) RegistryVersion() string {
	return receipt.registryVersion
}

func (receipt ChannelRegistryReceipt) FileSHA256() string {
	return receipt.fileSHA256
}

func (receipt ChannelRegistryReceipt) EntryCount() int {
	return receipt.entryCount
}

func (receipt ChannelRegistryReceipt) RuleCount() int {
	return receipt.ruleCount
}

func (receipt ChannelRegistryReceipt) ImportedAt() time.Time {
	return receipt.importedAt
}

type PostgresChannelRegistryImporter struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func NewPostgresChannelRegistryImporter(
	pool *pgxpool.Pool,
	clock func() time.Time,
) (*PostgresChannelRegistryImporter, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	if clock == nil {
		return nil, errors.New("channel Registry import clock is required")
	}
	return &PostgresChannelRegistryImporter{pool: pool, clock: clock}, nil
}

func (importer *PostgresChannelRegistryImporter) ImportPreprints(
	ctx context.Context,
	source io.Reader,
) (ChannelRegistryReceipt, error) {
	if ctx == nil {
		return ChannelRegistryReceipt{}, errors.New(
			"preprint Registry import context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return ChannelRegistryReceipt{}, err
	}
	if importer == nil || importer.pool == nil || importer.clock == nil {
		return ChannelRegistryReceipt{}, errors.New(
			"PostgresChannelRegistryImporter is not initialized",
		)
	}
	registry, err := ParsePreprintRegistry(source)
	if err != nil {
		return ChannelRegistryReceipt{}, err
	}
	importedAt, err := importer.importedAt()
	if err != nil {
		return ChannelRegistryReceipt{}, err
	}
	receipt := ChannelRegistryReceipt{
		registryName:    registry.Name(),
		registryVersion: registry.Version(),
		fileSHA256:      registry.FileSHA256(),
		entryCount:      registry.SourceCount(),
		ruleCount:       registry.RuleCount(),
		importedAt:      importedAt,
	}
	tx, err := importer.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ChannelRegistryReceipt{}, fmt.Errorf(
			"begin preprint Registry import transaction: %w",
			err,
		)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1::integer, $2::integer)",
		channelRegistryImportAdvisoryNamespace,
		registryImportLockKey("preprint:"+registry.Name(), registry.Version()),
	); err != nil {
		return ChannelRegistryReceipt{}, fmt.Errorf(
			"acquire preprint Registry import lock: %w",
			err,
		)
	}
	existing, found, err := findPreprintReceipt(ctx, tx, registry.Version())
	if err != nil {
		return ChannelRegistryReceipt{}, err
	}
	if found {
		if !channelReceiptMatches(existing, receipt) {
			return ChannelRegistryReceipt{}, fmt.Errorf(
				"%w: version %q already has SHA-256 %s",
				ErrConflictingPreprintRegistry,
				registry.Version(),
				existing.FileSHA256(),
			)
		}
		if err := tx.Commit(ctx); err != nil {
			return ChannelRegistryReceipt{}, fmt.Errorf(
				"commit idempotent preprint Registry import: %w",
				err,
			)
		}
		return existing, nil
	}
	var versionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO preprint_source_versions (
			registry_name,
			registry_version,
			file_sha256,
			source_count,
			rule_count,
			imported_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text
	`,
		receipt.RegistryName(),
		receipt.RegistryVersion(),
		receipt.FileSHA256(),
		receipt.EntryCount(),
		receipt.RuleCount(),
		receipt.ImportedAt(),
	).Scan(&versionID); err != nil {
		return ChannelRegistryReceipt{}, mapChannelRegistryImportError(
			"insert preprint Registry receipt",
			ErrConflictingPreprintRegistry,
			err,
		)
	}
	for _, entry := range registry.Sources() {
		if _, err := tx.Exec(ctx, `
			INSERT INTO trusted_preprint_sources (
				preprint_source_version_id,
				source_key,
				display_name,
				identifier_scheme,
				official_host,
				allowed_domains,
				lifecycle
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
		`,
			versionID,
			entry.SourceKey(),
			entry.DisplayName(),
			entry.IdentifierScheme(),
			entry.OfficialHost(),
			researchDomainStrings(entry.AllowedDomains()),
			entry.Lifecycle(),
		); err != nil {
			return ChannelRegistryReceipt{}, mapChannelRegistryImportError(
				fmt.Sprintf("insert trusted preprint source %q", entry.SourceKey()),
				ErrConflictingPreprintRegistry,
				err,
			)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ChannelRegistryReceipt{}, mapChannelRegistryImportError(
			"commit preprint Registry import",
			ErrConflictingPreprintRegistry,
			err,
		)
	}
	return receipt, nil
}

func (importer *PostgresChannelRegistryImporter) ImportConferences(
	ctx context.Context,
	source io.Reader,
) (ChannelRegistryReceipt, error) {
	if ctx == nil {
		return ChannelRegistryReceipt{}, errors.New(
			"conference Registry import context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return ChannelRegistryReceipt{}, err
	}
	if importer == nil || importer.pool == nil || importer.clock == nil {
		return ChannelRegistryReceipt{}, errors.New(
			"PostgresChannelRegistryImporter is not initialized",
		)
	}
	registry, err := ParseConferenceRegistry(source)
	if err != nil {
		return ChannelRegistryReceipt{}, err
	}
	importedAt, err := importer.importedAt()
	if err != nil {
		return ChannelRegistryReceipt{}, err
	}
	receipt := ChannelRegistryReceipt{
		registryName:    registry.Name(),
		registryVersion: registry.Version(),
		fileSHA256:      registry.FileSHA256(),
		entryCount:      registry.EventCount(),
		ruleCount:       registry.RuleCount(),
		importedAt:      importedAt,
	}
	tx, err := importer.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ChannelRegistryReceipt{}, fmt.Errorf(
			"begin conference Registry import transaction: %w",
			err,
		)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1::integer, $2::integer)",
		channelRegistryImportAdvisoryNamespace,
		registryImportLockKey("conference:"+registry.Name(), registry.Version()),
	); err != nil {
		return ChannelRegistryReceipt{}, fmt.Errorf(
			"acquire conference Registry import lock: %w",
			err,
		)
	}
	existing, found, err := findConferenceReceipt(ctx, tx, registry.Version())
	if err != nil {
		return ChannelRegistryReceipt{}, err
	}
	if found {
		if !channelReceiptMatches(existing, receipt) {
			return ChannelRegistryReceipt{}, fmt.Errorf(
				"%w: version %q already has SHA-256 %s",
				ErrConflictingConferenceRegistry,
				registry.Version(),
				existing.FileSHA256(),
			)
		}
		if err := tx.Commit(ctx); err != nil {
			return ChannelRegistryReceipt{}, fmt.Errorf(
				"commit idempotent conference Registry import: %w",
				err,
			)
		}
		return existing, nil
	}
	var versionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO conference_registry_versions (
			registry_name,
			registry_version,
			file_sha256,
			series_count,
			event_count,
			rule_count,
			imported_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id::text
	`,
		receipt.RegistryName(),
		receipt.RegistryVersion(),
		receipt.FileSHA256(),
		registry.SeriesCount(),
		registry.EventCount(),
		receipt.RuleCount(),
		receipt.ImportedAt(),
	).Scan(&versionID); err != nil {
		return ChannelRegistryReceipt{}, mapChannelRegistryImportError(
			"insert conference Registry receipt",
			ErrConflictingConferenceRegistry,
			err,
		)
	}
	seriesIDs := make(map[string]string)
	for _, entry := range registry.Entries() {
		seriesIdentity := entry.Provider() + "\x00" + entry.SeriesKey()
		seriesID, found := seriesIDs[seriesIdentity]
		if !found {
			if err := tx.QueryRow(ctx, `
				INSERT INTO conference_series (
					conference_registry_version_id,
					provider,
					series_key,
					series_name
				) VALUES ($1, $2, $3, $4)
				RETURNING id::text
			`,
				versionID,
				entry.Provider(),
				entry.SeriesKey(),
				entry.SeriesName(),
			).Scan(&seriesID); err != nil {
				return ChannelRegistryReceipt{}, mapChannelRegistryImportError(
					fmt.Sprintf("insert conference series %q", entry.SeriesKey()),
					ErrConflictingConferenceRegistry,
					err,
				)
			}
			seriesIDs[seriesIdentity] = seriesID
		}
		var eventID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO conference_events (
				conference_series_id,
				event_key,
				event_name,
				event_year
			) VALUES ($1, $2, $3, $4)
			RETURNING id::text
		`,
			seriesID,
			entry.EventKey(),
			entry.EventName(),
			entry.EventYear(),
		).Scan(&eventID); err != nil {
			return ChannelRegistryReceipt{}, mapChannelRegistryImportError(
				fmt.Sprintf("insert conference event %q", entry.EventKey()),
				ErrConflictingConferenceRegistry,
				err,
			)
		}
		var registryEntryID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO conference_registry_entries (
				conference_registry_version_id,
				conference_event_id,
				allowed_domains,
				lifecycle,
				reviewed
			) VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text
		`,
			versionID,
			eventID,
			researchDomainStrings(entry.AllowedDomains()),
			entry.Lifecycle(),
			entry.Reviewed(),
		).Scan(&registryEntryID); err != nil {
			return ChannelRegistryReceipt{}, mapChannelRegistryImportError(
				fmt.Sprintf("insert conference entry %q", entry.EventKey()),
				ErrConflictingConferenceRegistry,
				err,
			)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO conference_identifiers (
				conference_registry_entry_id,
				identifier_scheme,
				identifier_value
			) VALUES ($1, $2, $3)
		`,
			registryEntryID,
			entry.IdentifierScheme(),
			entry.IdentifierValue(),
		); err != nil {
			return ChannelRegistryReceipt{}, mapChannelRegistryImportError(
				fmt.Sprintf("insert conference identifier %q", entry.EventKey()),
				ErrConflictingConferenceRegistry,
				err,
			)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO conference_official_hosts (
				conference_registry_entry_id,
				official_host
			) VALUES ($1, $2)
		`, registryEntryID, entry.OfficialHost()); err != nil {
			return ChannelRegistryReceipt{}, mapChannelRegistryImportError(
				fmt.Sprintf("insert conference official host %q", entry.EventKey()),
				ErrConflictingConferenceRegistry,
				err,
			)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ChannelRegistryReceipt{}, mapChannelRegistryImportError(
			"commit conference Registry import",
			ErrConflictingConferenceRegistry,
			err,
		)
	}
	return receipt, nil
}

func (importer *PostgresChannelRegistryImporter) importedAt() (time.Time, error) {
	value := importer.clock()
	if value.IsZero() {
		return time.Time{}, errors.New(
			"channel Registry import clock returned a zero timestamp",
		)
	}
	return value.UTC(), nil
}

func channelReceiptMatches(
	left ChannelRegistryReceipt,
	right ChannelRegistryReceipt,
) bool {
	return left.RegistryName() == right.RegistryName() &&
		left.RegistryVersion() == right.RegistryVersion() &&
		left.FileSHA256() == right.FileSHA256() &&
		left.EntryCount() == right.EntryCount() &&
		left.RuleCount() == right.RuleCount()
}

type channelReceiptQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func findPreprintReceipt(
	ctx context.Context,
	querier channelReceiptQuerier,
	version string,
) (ChannelRegistryReceipt, bool, error) {
	var receipt ChannelRegistryReceipt
	err := querier.QueryRow(ctx, `
		SELECT
			registry_name,
			registry_version,
			file_sha256,
			source_count,
			rule_count,
			imported_at
		FROM preprint_source_versions
		WHERE registry_version = $1
	`, version).Scan(
		&receipt.registryName,
		&receipt.registryVersion,
		&receipt.fileSHA256,
		&receipt.entryCount,
		&receipt.ruleCount,
		&receipt.importedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelRegistryReceipt{}, false, nil
	}
	if err != nil {
		return ChannelRegistryReceipt{}, false, fmt.Errorf(
			"find preprint Registry receipt: %w",
			err,
		)
	}
	receipt.importedAt = receipt.importedAt.UTC()
	return receipt, true, nil
}

func findConferenceReceipt(
	ctx context.Context,
	querier channelReceiptQuerier,
	version string,
) (ChannelRegistryReceipt, bool, error) {
	var receipt ChannelRegistryReceipt
	err := querier.QueryRow(ctx, `
		SELECT
			registry_name,
			registry_version,
			file_sha256,
			event_count,
			rule_count,
			imported_at
		FROM conference_registry_versions
		WHERE registry_version = $1
	`, version).Scan(
		&receipt.registryName,
		&receipt.registryVersion,
		&receipt.fileSHA256,
		&receipt.entryCount,
		&receipt.ruleCount,
		&receipt.importedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelRegistryReceipt{}, false, nil
	}
	if err != nil {
		return ChannelRegistryReceipt{}, false, fmt.Errorf(
			"find conference Registry receipt: %w",
			err,
		)
	}
	receipt.importedAt = receipt.importedAt.UTC()
	return receipt, true, nil
}

func mapChannelRegistryImportError(
	operation string,
	sentinel error,
	err error,
) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) &&
		(postgresError.Code == "23505" || postgresError.Code == "23514") {
		return fmt.Errorf("%s: %w: %w", operation, sentinel, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func researchDomainStrings(domains []ResearchDomain) []string {
	result := make([]string, len(domains))
	for index, domain := range domains {
		result[index] = string(domain)
	}
	return result
}

type PostgresChannelRegistryStore struct {
	pool *pgxpool.Pool
}

func NewPostgresChannelRegistryStore(
	pool *pgxpool.Pool,
) (*PostgresChannelRegistryStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &PostgresChannelRegistryStore{pool: pool}, nil
}

func (store *PostgresChannelRegistryStore) FindPreprintSource(
	ctx context.Context,
	registryVersion string,
	sourceKey string,
	domain ResearchDomain,
) (TrustedPreprintSource, bool, error) {
	if ctx == nil {
		return TrustedPreprintSource{}, false, errors.New(
			"preprint Registry lookup context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return TrustedPreprintSource{}, false, err
	}
	if store == nil || store.pool == nil {
		return TrustedPreprintSource{}, false, errors.New(
			"PostgresChannelRegistryStore is not initialized",
		)
	}
	if registryVersion != PreprintRegistryVersion {
		return TrustedPreprintSource{}, false, fmt.Errorf(
			"unsupported preprint Registry version %q",
			registryVersion,
		)
	}
	if _, err := ParseResearchDomain(string(domain)); err != nil {
		return TrustedPreprintSource{}, false, err
	}
	var entry TrustedPreprintSource
	var rawDomains []string
	err := store.pool.QueryRow(ctx, `
		SELECT
			source.source_key,
			source.display_name,
			source.identifier_scheme,
			source.official_host,
			source.allowed_domains,
			source.lifecycle
		FROM trusted_preprint_sources AS source
		JOIN preprint_source_versions AS version
		  ON version.id = source.preprint_source_version_id
		WHERE version.registry_version = $1
		  AND source.source_key = $2
		  AND source.lifecycle = 'active'
		  AND $3 = ANY(source.allowed_domains)
	`, registryVersion, sourceKey, domain).Scan(
		&entry.sourceKey,
		&entry.displayName,
		&entry.identifierScheme,
		&entry.officialHost,
		&rawDomains,
		&entry.lifecycle,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return TrustedPreprintSource{}, false, nil
	}
	if err != nil {
		return TrustedPreprintSource{}, false, fmt.Errorf(
			"find exact trusted preprint source: %w",
			err,
		)
	}
	entry.allowedDomains, err = parseStoredDomains(rawDomains)
	if err != nil {
		return TrustedPreprintSource{}, false, err
	}
	return entry, true, nil
}

func (store *PostgresChannelRegistryStore) FindConferenceEntry(
	ctx context.Context,
	registryVersion string,
	provider string,
	seriesKey string,
	eventKey string,
	domain ResearchDomain,
) (ConferenceEntry, bool, error) {
	if ctx == nil {
		return ConferenceEntry{}, false, errors.New(
			"conference Registry lookup context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return ConferenceEntry{}, false, err
	}
	if store == nil || store.pool == nil {
		return ConferenceEntry{}, false, errors.New(
			"PostgresChannelRegistryStore is not initialized",
		)
	}
	if registryVersion != ConferenceRegistryVersion {
		return ConferenceEntry{}, false, fmt.Errorf(
			"unsupported conference Registry version %q",
			registryVersion,
		)
	}
	if _, err := ParseResearchDomain(string(domain)); err != nil {
		return ConferenceEntry{}, false, err
	}
	var entry ConferenceEntry
	var rawDomains []string
	err := store.pool.QueryRow(ctx, `
		SELECT
			series.provider,
			series.series_key,
			series.series_name,
			event.event_key,
			event.event_name,
			event.event_year,
			identifier.identifier_scheme,
			identifier.identifier_value,
			host.official_host,
			registry_entry.allowed_domains,
			registry_entry.lifecycle,
			registry_entry.reviewed
		FROM conference_registry_entries AS registry_entry
		JOIN conference_registry_versions AS version
		  ON version.id = registry_entry.conference_registry_version_id
		JOIN conference_events AS event
		  ON event.id = registry_entry.conference_event_id
		JOIN conference_series AS series
		  ON series.id = event.conference_series_id
		JOIN conference_identifiers AS identifier
		  ON identifier.conference_registry_entry_id = registry_entry.id
		JOIN conference_official_hosts AS host
		  ON host.conference_registry_entry_id = registry_entry.id
		WHERE version.registry_version = $1
		  AND series.provider = $2
		  AND series.series_key = $3
		  AND event.event_key = $4
		  AND registry_entry.lifecycle = 'active'
		  AND $5 = ANY(registry_entry.allowed_domains)
	`,
		registryVersion,
		provider,
		seriesKey,
		eventKey,
		domain,
	).Scan(
		&entry.provider,
		&entry.seriesKey,
		&entry.seriesName,
		&entry.eventKey,
		&entry.eventName,
		&entry.eventYear,
		&entry.identifierScheme,
		&entry.identifierValue,
		&entry.officialHost,
		&rawDomains,
		&entry.lifecycle,
		&entry.reviewed,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConferenceEntry{}, false, nil
	}
	if err != nil {
		return ConferenceEntry{}, false, fmt.Errorf(
			"find exact conference Registry entry: %w",
			err,
		)
	}
	entry.allowedDomains, err = parseStoredDomains(rawDomains)
	if err != nil {
		return ConferenceEntry{}, false, err
	}
	return entry, true, nil
}

func parseStoredDomains(values []string) ([]ResearchDomain, error) {
	result := make([]ResearchDomain, 0, len(values))
	for _, value := range values {
		domain, err := ParseResearchDomain(value)
		if err != nil {
			return nil, fmt.Errorf("restore stored Registry domain: %w", err)
		}
		result = append(result, domain)
	}
	return slices.Clone(result), nil
}
