package scope

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRegistryChannelImportsAreAuditableIdempotentAndConflictSafe(t *testing.T) {
	pool := openScopeTestPool(t)
	importedAt := time.Date(2026, time.July, 18, 4, 30, 0, 0, time.UTC)
	importer, err := NewPostgresChannelRegistryImporter(
		pool,
		func() time.Time { return importedAt },
	)
	if err != nil {
		t.Fatalf("NewPostgresChannelRegistryImporter() error = %v", err)
	}
	preprints := preprintRegistryFixture()
	conferences := conferenceRegistryFixture()

	firstPreprint, err := importer.ImportPreprints(
		context.Background(),
		bytes.NewReader(preprints),
	)
	if err != nil {
		t.Fatalf("ImportPreprints(first) error = %v", err)
	}
	secondPreprint, err := importer.ImportPreprints(
		context.Background(),
		bytes.NewReader(preprints),
	)
	if err != nil {
		t.Fatalf("ImportPreprints(idempotent) error = %v", err)
	}
	if firstPreprint != secondPreprint ||
		firstPreprint.RegistryVersion() != PreprintRegistryVersion ||
		firstPreprint.EntryCount() != 3 ||
		firstPreprint.RuleCount() != 4 {
		t.Fatalf("preprint receipts = %#v / %#v", firstPreprint, secondPreprint)
	}

	firstConference, err := importer.ImportConferences(
		context.Background(),
		bytes.NewReader(conferences),
	)
	if err != nil {
		t.Fatalf("ImportConferences(first) error = %v", err)
	}
	secondConference, err := importer.ImportConferences(
		context.Background(),
		bytes.NewReader(conferences),
	)
	if err != nil {
		t.Fatalf("ImportConferences(idempotent) error = %v", err)
	}
	if firstConference != secondConference ||
		firstConference.RegistryVersion() != ConferenceRegistryVersion ||
		firstConference.EntryCount() != 3 ||
		firstConference.RuleCount() != 4 {
		t.Fatalf("conference receipts = %#v / %#v", firstConference, secondConference)
	}

	ctx := scopeTestContext(t)
	var (
		channelCount,
		preprintVersionCount,
		preprintSourceCount,
		conferenceVersionCount,
		seriesCount,
		eventCount,
		entryCount,
		identifierCount,
		hostCount int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM content_channels),
			(SELECT count(*) FROM preprint_source_versions),
			(SELECT count(*) FROM trusted_preprint_sources),
			(SELECT count(*) FROM conference_registry_versions),
			(SELECT count(*) FROM conference_series),
			(SELECT count(*) FROM conference_events),
			(SELECT count(*) FROM conference_registry_entries),
			(SELECT count(*) FROM conference_identifiers),
			(SELECT count(*) FROM conference_official_hosts)
	`).Scan(
		&channelCount,
		&preprintVersionCount,
		&preprintSourceCount,
		&conferenceVersionCount,
		&seriesCount,
		&eventCount,
		&entryCount,
		&identifierCount,
		&hostCount,
	); err != nil {
		t.Fatalf("query channel Registry receipt chains: %v", err)
	}
	if channelCount != 4 ||
		preprintVersionCount != 1 ||
		preprintSourceCount != 3 ||
		conferenceVersionCount != 1 ||
		seriesCount != 3 ||
		eventCount != 3 ||
		entryCount != 3 ||
		identifierCount != 3 ||
		hostCount != 3 {
		t.Fatalf(
			"channel Registry counts = channels %d preprints %d/%d conferences %d/%d/%d/%d/%d/%d",
			channelCount,
			preprintVersionCount,
			preprintSourceCount,
			conferenceVersionCount,
			seriesCount,
			eventCount,
			entryCount,
			identifierCount,
			hostCount,
		)
	}

	store, err := NewPostgresChannelRegistryStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresChannelRegistryStore() error = %v", err)
	}
	preprint, found, err := store.FindPreprintSource(
		ctx,
		PreprintRegistryVersion,
		"arxiv",
		ResearchDomainComputerScience,
	)
	if err != nil || !found ||
		preprint.OfficialHost() != "arxiv.org" ||
		preprint.IdentifierScheme() != "arxiv" {
		t.Fatalf("FindPreprintSource() = %#v, %t, %v", preprint, found, err)
	}
	if _, found, err := store.FindPreprintSource(
		ctx,
		PreprintRegistryVersion,
		"ArXiv",
		ResearchDomainComputerScience,
	); err != nil || found {
		t.Fatalf("FindPreprintSource(normalized) found=%t error=%v", found, err)
	}
	conference, found, err := store.FindConferenceEntry(
		ctx,
		ConferenceRegistryVersion,
		"reviewed_allowlist",
		"recomb",
		"recomb-2026",
		ResearchDomainBiology,
	)
	if err != nil || !found || !conference.Reviewed() {
		t.Fatalf("FindConferenceEntry() = %#v, %t, %v", conference, found, err)
	}

	conflictingPreprints := bytes.ReplaceAll(
		preprints,
		[]byte("arxiv.org"),
		[]byte("export.arxiv.org"),
	)
	if _, err := importer.ImportPreprints(
		context.Background(),
		bytes.NewReader(conflictingPreprints),
	); !errors.Is(err, ErrConflictingPreprintRegistry) {
		t.Fatalf("ImportPreprints(conflict) error = %v", err)
	}
	conflictingConferences := bytes.ReplaceAll(
		conferences,
		[]byte("recomb.org"),
		[]byte("www.recomb.org"),
	)
	if _, err := importer.ImportConferences(
		context.Background(),
		bytes.NewReader(conflictingConferences),
	); !errors.Is(err, ErrConflictingConferenceRegistry) {
		t.Fatalf("ImportConferences(conflict) error = %v", err)
	}

	for _, table := range []string{
		"content_channels",
		"preprint_source_versions",
		"trusted_preprint_sources",
		"conference_registry_versions",
		"conference_series",
		"conference_events",
		"conference_registry_entries",
		"conference_identifiers",
		"conference_official_hosts",
	} {
		if _, err := pool.Exec(
			ctx,
			"DELETE FROM "+table,
		); err == nil {
			t.Fatalf("immutable %s DELETE error = nil", table)
		}
	}
}

func TestRegistryAssertionAndProjectionTablesPreserveSourcePathsAndVersions(t *testing.T) {
	pool := openScopeTestPool(t)
	fixture := insertScopeProjectionFixture(t, pool, "immutable")
	ctx := scopeTestContext(t)

	var channelAssertionID, lifecycleAssertionID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO work_channel_assertions (
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			work_id,
			channel,
			source_path,
			asserted_at
		) VALUES ($1, $2, $3, $4, 'preprint', '$.server', $5)
		RETURNING id::text
	`,
		fixture.projectionAssertionID,
		fixture.normalizedAssertionID,
		fixture.sourceRecordID,
		fixture.workID,
		fixture.assertedAt,
	).Scan(&channelAssertionID); err != nil {
		t.Fatalf("insert Work channel assertion: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO work_lifecycle_assertions (
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			work_id,
			lifecycle_fact,
			source_path,
			asserted_at
		) VALUES ($1, $2, $3, $4, 'posted', '$.posted', $5)
		RETURNING id::text
	`,
		fixture.projectionAssertionID,
		fixture.normalizedAssertionID,
		fixture.sourceRecordID,
		fixture.workID,
		fixture.assertedAt,
	).Scan(&lifecycleAssertionID); err != nil {
		t.Fatalf("insert Work lifecycle assertion: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_channel_decisions (
			work_id, channel, state, source_record_id, source_path,
			policy_version, evidence, decided_at
		) VALUES (
			$1, 'preprint', 'resolved', $2, '$.server',
			'channel-projection/v1',
			jsonb_build_object('assertion_id', $3::text),
			$4
		)
	`,
		fixture.workID,
		fixture.sourceRecordID,
		channelAssertionID,
		fixture.assertedAt.Add(time.Minute),
	); err != nil {
		t.Fatalf("insert Work channel decision: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_lifecycle_states (
			work_id, channel, lifecycle_state, source_record_id, source_path,
			policy_version, evidence, decided_at
		) VALUES (
			$1, 'preprint', 'preprint_active', $2, '$.posted',
			'lifecycle-projection/v1',
			jsonb_build_object('assertion_id', $3::text),
			$4
		)
	`,
		fixture.workID,
		fixture.sourceRecordID,
		lifecycleAssertionID,
		fixture.assertedAt.Add(time.Minute),
	); err != nil {
		t.Fatalf("insert Work lifecycle state: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_channel_admission_decisions (
			work_id, channel, decision, reason, source_record_id, source_path,
			policy_version, registry_version, evidence, decided_at
		) VALUES (
			$1, 'preprint', 'accepted', 'eligible', $2, '$',
			'channel-admission/v1', 'preprint-sources/v1',
			jsonb_build_object(
				'channel_assertion_id', $3::text,
				'lifecycle_assertion_id', $4::text
			),
			$5
		)
	`,
		fixture.workID,
		fixture.sourceRecordID,
		channelAssertionID,
		lifecycleAssertionID,
		fixture.assertedAt.Add(2*time.Minute),
	); err != nil {
		t.Fatalf("insert Work admission decision: %v", err)
	}

	var channelPath, channelPolicy, lifecyclePath, lifecyclePolicy, registryVersion string
	if err := pool.QueryRow(ctx, `
		SELECT
			channel.source_path,
			channel.policy_version,
			lifecycle.source_path,
			lifecycle.policy_version,
			admission.registry_version
		FROM work_channel_decisions AS channel
		JOIN work_lifecycle_states AS lifecycle
		  ON lifecycle.work_id = channel.work_id
		JOIN work_channel_admission_decisions AS admission
		  ON admission.work_id = channel.work_id
		WHERE channel.work_id = $1
	`, fixture.workID).Scan(
		&channelPath,
		&channelPolicy,
		&lifecyclePath,
		&lifecyclePolicy,
		&registryVersion,
	); err != nil {
		t.Fatalf("query versioned scope projections: %v", err)
	}
	if channelPath != "$.server" ||
		channelPolicy != "channel-projection/v1" ||
		lifecyclePath != "$.posted" ||
		lifecyclePolicy != "lifecycle-projection/v1" ||
		registryVersion != PreprintRegistryVersion {
		t.Fatalf(
			"projection provenance = %q/%q %q/%q %q",
			channelPath,
			channelPolicy,
			lifecyclePath,
			lifecyclePolicy,
			registryVersion,
		)
	}

	for _, table := range []string{
		"work_channel_assertions",
		"work_lifecycle_assertions",
		"work_channel_decisions",
		"work_lifecycle_states",
		"work_channel_admission_decisions",
	} {
		if _, err := pool.Exec(
			ctx,
			"UPDATE "+table+" SET source_path = '$.mutated'",
		); err == nil {
			t.Fatalf("immutable %s UPDATE error = nil", table)
		}
		if _, err := pool.Exec(
			ctx,
			"DELETE FROM "+table,
		); err == nil {
			t.Fatalf("immutable %s DELETE error = nil", table)
		}
	}
}

func preprintRegistryFixture() []byte {
	return []byte(
		"registry_name,registry_version,source_key,display_name,identifier_scheme,official_host,allowed_domain,lifecycle\n" +
			"medpaperhub-trusted-preprints,preprint-sources/v1,biorxiv,bioRxiv,biorxiv,www.biorxiv.org,biology,active\n" +
			"medpaperhub-trusted-preprints,preprint-sources/v1,medrxiv,medRxiv,medrxiv,www.medrxiv.org,medicine,active\n" +
			"medpaperhub-trusted-preprints,preprint-sources/v1,arxiv,arXiv,arxiv,arxiv.org,biology,active\n" +
			"medpaperhub-trusted-preprints,preprint-sources/v1,arxiv,arXiv,arxiv,arxiv.org,computer_science,active\n",
	)
}

func conferenceRegistryFixture() []byte {
	return []byte(
		"registry_name,registry_version,provider,series_key,series_name,event_key,event_name,event_year,identifier_scheme,identifier_value,official_host,allowed_domain,lifecycle,reviewed\n" +
			"medpaperhub-conferences,conference-venues/v1,ieee,cvpr,IEEE/CVF Conference on Computer Vision and Pattern Recognition,cvpr-2026,CVPR 2026,2026,doi_prefix,10.1109,ieeexplore.ieee.org,computer_science,active,false\n" +
			"medpaperhub-conferences,conference-venues/v1,acm,sigkdd,ACM SIGKDD Conference on Knowledge Discovery and Data Mining,kdd-2026,KDD 2026,2026,doi_prefix,10.1145,dl.acm.org,computer_science,active,false\n" +
			"medpaperhub-conferences,conference-venues/v1,reviewed_allowlist,recomb,Research in Computational Molecular Biology,recomb-2026,RECOMB 2026,2026,series_issn,1528-4305,recomb.org,biology,active,true\n" +
			"medpaperhub-conferences,conference-venues/v1,reviewed_allowlist,recomb,Research in Computational Molecular Biology,recomb-2026,RECOMB 2026,2026,series_issn,1528-4305,recomb.org,computer_science,active,true\n",
	)
}

type scopeProjectionFixture struct {
	workID                string
	sourceRecordID        string
	normalizedAssertionID string
	projectionAssertionID string
	assertedAt            time.Time
}

func insertScopeProjectionFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	suffix string,
) scopeProjectionFixture {
	t.Helper()
	ctx := scopeTestContext(t)
	fixture := scopeProjectionFixture{
		assertedAt: time.Date(2026, time.July, 18, 5, 0, 0, 0, time.UTC),
	}
	var jobID, rawEventID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO works (canonical_key, status, title)
		VALUES ($1, 'active', 'Scope fixture')
		RETURNING id::text
	`, "openreview:scope-"+suffix).Scan(&fixture.workID); err != nil {
		t.Fatalf("insert scope Work: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO source_records (
			source, source_record_id, source_identity,
			source_time, content_hash, raw_payload
		) VALUES (
			'scope-fixture', $1,
			jsonb_build_object('id', $1::text),
			$2, $3,
			jsonb_build_object('fixture', $1::text)
		)
		RETURNING id::text
	`,
		"scope-record-"+suffix,
		fixture.assertedAt,
		strings.Repeat("a", 63)+"1",
	).Scan(&fixture.sourceRecordID); err != nil {
		t.Fatalf("insert scope SourceRecord: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, fixture.sourceRecordID, fixture.workID); err != nil {
		t.Fatalf("insert scope SourceRecord Work link: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_jobs (
			source, job_type, idempotency_key, status, payload,
			max_attempts, batch_key, stage
		) VALUES (
			'scope-fixture', 'projection', $1, 'succeeded', '{}',
			1, $1, 'project'
		)
		RETURNING id::text
	`, "scope-job-"+suffix).Scan(&jobID); err != nil {
		t.Fatalf("insert scope ingestion job: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_raw_events (
			job_id, logical_source, event_key, event_kind, source_record_id,
			source_time, tie_break_key, position, content_hash, raw_format, raw_payload
		) VALUES (
			$1, 'scope-fixture', $2, 'upsert', $2,
			$3, $2, 0, $4, 'json', convert_to('{}', 'UTF8')
		)
		RETURNING id::text
	`,
		jobID,
		"scope-event-"+suffix,
		fixture.assertedAt,
		strings.Repeat("b", 64),
	).Scan(&rawEventID); err != nil {
		t.Fatalf("insert scope raw event: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id, source_record_uuid, normalization_policy_version,
			payload_schema_version, normalized_payload
		) VALUES (
			$1, $2, 'scope-normalization/v1',
			'normalized-record/v2', '{"scope":true}'
		)
		RETURNING id::text
	`, rawEventID, fixture.sourceRecordID).Scan(
		&fixture.normalizedAssertionID,
	); err != nil {
		t.Fatalf("insert scope normalized assertion: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_projection_assertions (
			normalized_assertion_id, raw_event_id, source_record_uuid,
			work_id, job_id, scope_policy_version,
			projection_policy_version, record_payload
		) VALUES (
			$1, $2, $3, $4, $5,
			'scope-policy/v1', 'scope-projection/v1', '{"scope":true}'
		)
		RETURNING id::text
	`,
		fixture.normalizedAssertionID,
		rawEventID,
		fixture.sourceRecordID,
		fixture.workID,
		jobID,
	).Scan(&fixture.projectionAssertionID); err != nil {
		t.Fatalf("insert scope projection assertion: %v", err)
	}
	return fixture
}
