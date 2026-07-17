package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublisherBuildsAndAtomicallyPublishesNormalizedCurrentState(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-visible",
		canonicalKey:      "doi:10.1000/publisher-visible",
		title:             "Evidence-Grounded Agents",
		topicNames:        []string{"Agent Systems"},
		methodNames:       []string{"Causal Inference"},
		withCode:          true,
		withData:          true,
		withBenchmark:     true,
		paperType:         "research_article",
		jcrDecision:       "accepted",
		jcrMatchedRules:   `["jif_at_least_10"]`,
		jcrEvidence:       `{"venue_type":"journal","jif":12.4,"quartiles":["Q1"]}`,
		publishedAt:       time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	publisher := mustPublisher(t, pool)
	generatedAt := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)

	generation, err := publisher.PublishCurrent(context.Background(), PublishInput{
		FormulaVersion: "public-catalog/v1",
		GeneratedAt:    generatedAt,
	})
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	if generation.ID == uuid.Nil ||
		generation.GeneratedAt != generatedAt ||
		generation.FormulaVersion != "public-catalog/v1" {
		t.Fatalf("published generation = %#v, want requested immutable generation", generation)
	}
	if len(generation.SourceRevision) != 64 {
		t.Fatalf("source revision = %q, want SHA-256", generation.SourceRevision)
	}

	repository := mustRepository(t, pool)
	page, err := repository.Papers(context.Background(), PaperListQuery{Limit: 20})
	if err != nil {
		t.Fatalf("Papers() after publish error = %v", err)
	}
	assertPaperIDs(t, page.Items, fixture.workID)

	detail, err := repository.Paper(context.Background(), fixture.workID)
	if err != nil {
		t.Fatalf("Paper() after publish error = %v", err)
	}
	assertNestedJSONValue(t, detail.Payload, []string{"type", "state"}, "known")
	assertNestedJSONValue(
		t,
		detail.Payload,
		[]string{"type", "value"},
		"research_article",
	)
	assertNestedJSONValue(t, detail.Payload, []string{"has_code", "value"}, true)
	assertNestedJSONValue(t, detail.Payload, []string{"has_data", "value"}, true)
	assertNestedJSONValue(t, detail.Payload, []string{"has_benchmark", "value"}, true)
	assertNestedJSONValue(t, detail.Payload, []string{"curation", "state"}, "known")
	assertNestedJSONValue(
		t,
		detail.Payload,
		[]string{"curation", "value", "decision"},
		"accepted",
	)
	assertJSONArrayLength(t, detail.Payload, "source_provenance", 1)

	topics, err := repository.Topics(context.Background(), PageQuery{Limit: 20})
	if err != nil {
		t.Fatalf("Topics() after publish error = %v", err)
	}
	if len(topics.Items) != 1 {
		t.Fatalf("topic count = %d, want 1", len(topics.Items))
	}
	assertJSONField(t, topics.Items[0], "slug", "agent-systems")

	methods, err := repository.Methods(context.Background(), PageQuery{Limit: 20})
	if err != nil {
		t.Fatalf("Methods() after publish error = %v", err)
	}
	if len(methods.Items) != 1 {
		t.Fatalf("method count = %d, want 1", len(methods.Items))
	}
	assertJSONField(t, methods.Items[0], "slug", "causal-inference")

	stats, err := repository.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() after publish error = %v", err)
	}
	assertNestedJSONValue(t, stats.Payload, []string{"papers_total", "value"}, float64(1))
	assertNestedJSONValue(t, stats.Payload, []string{"with_code_ratio", "value"}, float64(1))
}

func TestPublisherLoadsNormalizedPayloadByBoundAssertionID(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-normalized-binding",
		canonicalKey:      "doi:10.1000/publisher-normalized-binding",
		title:             "Bound normalized payload",
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
	})
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO ingestion_normalized_records (
			raw_event_id, source_record_uuid, normalization_policy_version,
			payload_schema_version, normalized_payload
		) VALUES (
			$1, $2, 'normalize/v1', 'normalized-record/v3',
			'{
				"source":"openalex",
				"source_record_id":"openalex:publisher-normalized-binding",
				"canonical_key":"doi:10.1000/publisher-normalized-binding",
				"title":"Unbound decoy payload"
			}'
		)
	`, fixture.rawEventID, fixture.sourceRecord); err != nil {
		t.Fatalf("insert unbound normalized assertion: %v", err)
	}

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin normalized binding read: %v", err)
	}
	defer tx.Rollback(context.Background())
	states, err := loadCurrentSourceStates(context.Background(), tx)
	if err != nil {
		t.Fatalf("loadCurrentSourceStates() error = %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("current source states = %d, want one bound assertion", len(states))
	}
	payload, ok := states[0].normalizedPayload.(map[string]any)
	if !ok || payload["title"] != "Bound normalized payload" {
		t.Fatalf(
			"bound normalized payload = %#v, want the source-state assertion",
			states[0].normalizedPayload,
		)
	}
}

func TestPublisherPreservesKnownUnknownAndMissingWithoutInventingValues(t *testing.T) {
	pool := openCatalogTestPool(t)
	known := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-known",
		canonicalKey:      "doi:10.1000/publisher-known",
		title:             "Known Curation",
		paperType:         "review",
		jcrDecision:       "accepted",
		jcrMatchedRules:   `["jcr_q1"]`,
		jcrEvidence:       `{"venue_type":"journal","quartiles":["Q1"]}`,
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		publishedAt:       time.Date(2026, time.July, 14, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 14, 9, 0, 0, 0, time.UTC),
	})
	unknown := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-unknown",
		canonicalKey:      "doi:10.1000/publisher-unknown",
		title:             "Unknown Curation",
		jcrDecision:       "unknown",
		jcrMatchedRules:   `[]`,
		jcrEvidence:       `{"venue_type":"journal","reason":"metric_unknown"}`,
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC),
	})
	missing := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-missing",
		canonicalKey:      "doi:10.1000/publisher-missing",
		title:             "Missing Curation",
		noJCRAssessment:   true,
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 14, 11, 0, 0, 0, time.UTC),
	})

	publisher := mustPublisher(t, pool)
	if _, err := publisher.PublishCurrent(context.Background(), PublishInput{
		FormulaVersion: "public-catalog/v1",
		GeneratedAt:    time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	repository := mustRepository(t, pool)

	for _, test := range []struct {
		name  string
		id    uuid.UUID
		state string
	}{
		{"known", known.workID, "known"},
		{"unknown", unknown.workID, "unknown"},
		{"missing", missing.workID, "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, err := repository.Paper(context.Background(), test.id)
			if err != nil {
				t.Fatalf("Paper(%s) error = %v", test.id, err)
			}
			assertNestedJSONValue(
				t,
				document.Payload,
				[]string{"curation", "state"},
				test.state,
			)
		})
	}

	missingDocument, err := repository.Paper(context.Background(), missing.workID)
	if err != nil {
		t.Fatalf("Paper(missing evidence) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		missingDocument.Payload,
		[]string{"published_at", "state"},
		"missing",
	)
	assertNestedJSONValue(
		t,
		missingDocument.Payload,
		[]string{"has_code", "state"},
		"unknown",
	)
}

func TestPublisherReturnsEmptyDomainWithoutWritingGeneration(t *testing.T) {
	pool := openCatalogTestPool(t)
	insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-excluded",
		canonicalKey:      "doi:10.1000/publisher-excluded",
		title:             "Excluded Work",
		scopeStatus:       "excluded",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
	})
	publisher := mustPublisher(t, pool)

	_, err := publisher.PublishCurrent(context.Background(), PublishInput{
		FormulaVersion: "public-catalog/v1",
		GeneratedAt:    time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, ErrEmptyDomain) {
		t.Fatalf("PublishCurrent() error = %v, want ErrEmptyDomain", err)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherReturnsNotReadyForPendingOrBrokenIncludedStateWithoutWrites(t *testing.T) {
	tests := []struct {
		name    string
		options publisherWorkOptions
	}{
		{
			name: "pending current source state",
			options: publisherWorkOptions{
				eventKey:          "openalex:publisher-pending",
				canonicalKey:      "doi:10.1000/publisher-pending",
				title:             "Pending Work",
				scopeStatus:       "pending",
				includeWorkLink:   true,
				includeWorkID:     true,
				includeNormalized: true,
				sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "included state without work ID",
			options: publisherWorkOptions{
				eventKey:          "openalex:publisher-no-work",
				canonicalKey:      "doi:10.1000/publisher-no-work",
				title:             "No Work Link",
				scopeStatus:       "included",
				includeWorkLink:   false,
				includeWorkID:     false,
				includeNormalized: true,
				sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := openCatalogTestPool(t)
			insertPublisherVisibleWork(t, pool, test.options)
			publisher := mustPublisher(t, pool)

			_, err := publisher.PublishCurrent(context.Background(), PublishInput{
				FormulaVersion: "public-catalog/v1",
				GeneratedAt:    time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
			})
			if !errors.Is(err, ErrCatalogNotReady) {
				t.Fatalf("PublishCurrent() error = %v, want ErrCatalogNotReady", err)
			}
			assertNoCatalogWrites(t, pool)
		})
	}
}

func TestPublisherRejectsDeterministicSlugCollisionsWithoutPartialPublication(t *testing.T) {
	pool := openCatalogTestPool(t)
	for index, topic := range []string{"Agent Systems", "Agent-Systems"} {
		insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          fmt.Sprintf("openalex:publisher-collision-%d", index),
			canonicalKey:      fmt.Sprintf("doi:10.1000/publisher-collision-%d", index),
			title:             fmt.Sprintf("Collision Work %d", index),
			topicNames:        []string{topic},
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
			sourceTime:        time.Date(2026, time.July, 15, 9+index, 0, 0, 0, time.UTC),
		})
	}
	publisher := mustPublisher(t, pool)

	_, err := publisher.PublishCurrent(context.Background(), PublishInput{
		FormulaVersion: "public-catalog/v1",
		GeneratedAt:    time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, ErrCatalogNotReady) {
		t.Fatalf("PublishCurrent() error = %v, want ErrCatalogNotReady", err)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRetryReusesIdenticalImmutableGeneration(t *testing.T) {
	pool := openCatalogTestPool(t)
	insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-retry",
		canonicalKey:      "doi:10.1000/publisher-retry",
		title:             "Retry Work",
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
	})
	publisher := mustPublisher(t, pool)

	first, err := publisher.PublishCurrent(context.Background(), PublishInput{
		FormulaVersion: "public-catalog/v1",
		GeneratedAt:    time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("PublishCurrent(first) error = %v", err)
	}
	second, err := publisher.PublishCurrent(context.Background(), PublishInput{
		FormulaVersion: "public-catalog/v1",
		GeneratedAt:    time.Date(2026, time.July, 15, 13, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("PublishCurrent(second) error = %v", err)
	}
	if second.ID != first.ID || second.SourceRevision != first.SourceRevision {
		t.Fatalf("retry generation = %#v, want original %#v", second, first)
	}

	var generations, publications int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM public_catalog_generations),
			(SELECT count(*) FROM public_catalog_publications)
	`).Scan(&generations, &publications); err != nil {
		t.Fatalf("count idempotent publication rows: %v", err)
	}
	if generations != 1 || publications != 1 {
		t.Fatalf(
			"idempotent publication rows = generations %d publications %d, want 1/1",
			generations,
			publications,
		)
	}
}

type publisherWorkOptions struct {
	eventKey          string
	canonicalKey      string
	title             string
	topicNames        []string
	methodNames       []string
	withCode          bool
	withData          bool
	withBenchmark     bool
	paperType         string
	jcrDecision       string
	jcrMatchedRules   string
	jcrEvidence       string
	noJCRAssessment   bool
	publishedAt       time.Time
	sourceTime        time.Time
	scopeStatus       string
	includeWorkLink   bool
	includeWorkID     bool
	includeNormalized bool
}

type publisherWorkFixture struct {
	workID                uuid.UUID
	sourceRecord          uuid.UUID
	rawEventID            uuid.UUID
	normalizedAssertionID uuid.UUID
}

func insertPublisherVisibleWork(
	t *testing.T,
	pool *pgxpool.Pool,
	options publisherWorkOptions,
) publisherWorkFixture {
	t.Helper()

	if options.eventKey == "" {
		options.eventKey = "openalex:" + uuid.NewString()
	}
	if options.canonicalKey == "" {
		options.canonicalKey = "openalex:W" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if options.title == "" {
		options.title = "Publisher Work"
	}
	if options.sourceTime.IsZero() {
		options.sourceTime = time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC)
	}
	if options.scopeStatus == "" {
		options.scopeStatus = "included"
	}

	fixture := publisherWorkFixture{
		workID:       uuid.New(),
		sourceRecord: uuid.New(),
		rawEventID:   uuid.New(),
	}
	jobID := uuid.New()
	venueID := uuid.New()
	ctx := context.Background()
	hash := sha256.Sum256([]byte(options.eventKey))
	contentHash := hex.EncodeToString(hash[:])

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin publisher fixture transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_jobs (
			id, source, job_type, idempotency_key, status, payload, max_attempts,
			batch_key, stage
		) VALUES (
			$1, 'openalex', 'sync', $2, 'succeeded', '{}', 1, $2, 'project'
		)
	`, jobID, "publisher:"+options.eventKey); err != nil {
		t.Fatalf("insert publisher ingestion job: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_raw_events (
			id, job_id, logical_source, event_key, event_kind, source_record_id,
			source_time, tie_break_key, position, content_hash, raw_format, raw_payload
		) VALUES (
			$1, $2, 'openalex', $3, 'upsert', $4, $5, 'publisher-fixture', 1,
			$6, 'json', $7
		)
	`,
		fixture.rawEventID,
		jobID,
		options.eventKey,
		options.eventKey,
		options.sourceTime,
		contentHash,
		[]byte(`{"fixture":"publisher"}`),
	); err != nil {
		t.Fatalf("insert publisher raw event: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_records (
			id, source, source_record_id, source_identity, source_time,
			content_hash, raw_payload
		) VALUES (
			$1, 'openalex', $2, $3::jsonb, $4, $5, '{"fixture":"publisher"}'
		)
	`,
		fixture.sourceRecord,
		options.eventKey,
		fmt.Sprintf(`{"canonical_key":%q}`, options.canonicalKey),
		options.sourceTime,
		contentHash,
	); err != nil {
		t.Fatalf("insert publisher source record: %v", err)
	}
	if options.includeNormalized {
		normalizedPayload := fmt.Sprintf(
			`{"source":"openalex","source_record_id":%q,"canonical_key":%q,"title":%q}`,
			options.eventKey,
			options.canonicalKey,
			options.title,
		)
		if err := tx.QueryRow(ctx, `
			INSERT INTO ingestion_normalized_records (
				raw_event_id, source_record_uuid, normalization_policy_version,
				payload_schema_version, normalized_payload
			) VALUES ($1, $2, 'normalize/v1', 'normalized-record/v2', $3::jsonb)
			RETURNING id
		`, fixture.rawEventID, fixture.sourceRecord, normalizedPayload).Scan(
			&fixture.normalizedAssertionID,
		); err != nil {
			t.Fatalf("insert publisher normalized record: %v", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO venues (
			id, venue_type, display_title, source_scheme, source_identifier
		) VALUES ($1, 'journal', $2, 'openalex', $3)
	`,
		venueID,
		"Publisher Journal "+options.eventKey,
		"V"+strings.ReplaceAll(fixture.workID.String(), "-", ""),
	); err != nil {
		t.Fatalf("insert publisher venue: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO works (
			id, canonical_key, status, title, abstract, published_at, venue_id
		) VALUES (
			$1, $2, 'active', $3, 'Source-backed abstract',
			NULLIF($4, '0001-01-01 00:00:00+00'::timestamptz), $5
		)
	`,
		fixture.workID,
		options.canonicalKey,
		options.title,
		options.publishedAt,
		venueID,
	); err != nil {
		t.Fatalf("insert publisher work: %v", err)
	}
	if options.includeWorkLink {
		if _, err := tx.Exec(ctx, `
			INSERT INTO source_record_works (source_record_id, work_id)
			VALUES ($1, $2)
		`, fixture.sourceRecord, fixture.workID); err != nil {
			t.Fatalf("insert publisher source/work association: %v", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_source_states (
			logical_source, event_key, raw_event_id, source_record_uuid, work_id,
			source_time, tie_break_key, position, scope_status,
			scope_policy_version, projection_policy_version,
			normalized_assertion_id, is_deleted
		) VALUES (
			'openalex', $1, $2, $3, NULLIF($4, '')::uuid, $5,
			'publisher-fixture', 1, $6, 'scope/v1', 'projection/v1',
			NULLIF($7, '')::uuid, false
		)
	`,
		options.eventKey,
		fixture.rawEventID,
		fixture.sourceRecord,
		optionalUUID(options.includeWorkID, fixture.workID),
		options.sourceTime,
		options.scopeStatus,
		optionalUUID(options.includeNormalized, fixture.normalizedAssertionID),
	); err != nil {
		t.Fatalf("insert publisher source state: %v", err)
	}

	if options.includeWorkLink {
		assertions := map[string]map[string]any{
			"published_at": {
				"state": stateForTime(options.publishedAt),
				"value": optionalTimeValue(options.publishedAt),
			},
			"abstract": {
				"state": "known",
				"value": "Source-backed abstract",
			},
			"code_urls": {
				"state": boolState(options.withCode),
				"value": optionalStringArray(
					options.withCode,
					"https://github.com/example/project",
				),
			},
		}
		if options.paperType != "" {
			assertions["paper_type"] = map[string]any{
				"state": "known",
				"value": options.paperType,
			}
		}
		for name, value := range assertions {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("encode publisher assertion %s: %v", name, err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO field_assertions (
					work_id, source_record_id, field_name, asserted_value, asserted_at
				) VALUES ($1, $2, $3, $4, $5)
			`,
				fixture.workID,
				fixture.sourceRecord,
				name,
				encoded,
				options.sourceTime,
			); err != nil {
				t.Fatalf("insert publisher assertion %s: %v", name, err)
			}
		}
	}

	authorID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO authors (id, display_name) VALUES ($1, 'Ada Lovelace')
	`, authorID); err != nil {
		t.Fatalf("insert publisher author: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_authors (work_id, author_id, author_position)
		VALUES ($1, $2, 1)
	`, fixture.workID, authorID); err != nil {
		t.Fatalf("associate publisher author: %v", err)
	}
	for _, name := range options.topicNames {
		topicID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO topics (id, name) VALUES ($1, $2)
		`, topicID, name); err != nil {
			t.Fatalf("insert publisher topic %q: %v", name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_topics (work_id, topic_id, source_record_id)
			VALUES ($1, $2, $3)
		`, fixture.workID, topicID, fixture.sourceRecord); err != nil {
			t.Fatalf("associate publisher topic %q: %v", name, err)
		}
	}
	for _, name := range options.methodNames {
		methodID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO methods (id, name) VALUES ($1, $2)
		`, methodID, name); err != nil {
			t.Fatalf("insert publisher method %q: %v", name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_methods (work_id, method_id, source_record_id)
			VALUES ($1, $2, $3)
		`, fixture.workID, methodID, fixture.sourceRecord); err != nil {
			t.Fatalf("associate publisher method %q: %v", name, err)
		}
	}
	if options.withCode {
		repositoryID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO code_repositories (
				id, canonical_url, host, owner_name, repository_name
			) VALUES ($1, $2, 'github.com', 'example', $3)
		`,
			repositoryID,
			"https://github.com/example/"+fixture.workID.String(),
			fixture.workID.String(),
		); err != nil {
			t.Fatalf("insert publisher code repository: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_code_repositories (work_id, repository_id, relation_type)
			VALUES ($1, $2, 'official')
		`, fixture.workID, repositoryID); err != nil {
			t.Fatalf("associate publisher code repository: %v", err)
		}
	}
	if options.withData {
		datasetID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO datasets (id, name) VALUES ($1, $2)
		`, datasetID, "Dataset "+fixture.workID.String()); err != nil {
			t.Fatalf("insert publisher dataset: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_datasets (work_id, dataset_id, relation_type)
			VALUES ($1, $2, 'used')
		`, fixture.workID, datasetID); err != nil {
			t.Fatalf("associate publisher dataset: %v", err)
		}
	}
	if options.withBenchmark {
		benchmarkID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO benchmarks (id, name) VALUES ($1, $2)
		`, benchmarkID, "Benchmark "+fixture.workID.String()); err != nil {
			t.Fatalf("insert publisher benchmark: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_benchmarks (work_id, benchmark_id, relation_type)
			VALUES ($1, $2, 'evaluated')
		`, fixture.workID, benchmarkID); err != nil {
			t.Fatalf("associate publisher benchmark: %v", err)
		}
	}

	if !options.noJCRAssessment {
		policyID := uuid.New()
		decision := options.jcrDecision
		if decision == "" {
			decision = "accepted"
		}
		matchedRules := options.jcrMatchedRules
		if matchedRules == "" {
			matchedRules = `["jcr_q1"]`
		}
		evidence := options.jcrEvidence
		if evidence == "" {
			evidence = `{"venue_type":"journal","quartiles":["Q1"]}`
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO venue_policy_versions (
				id, policy_name, version_number, definition, effective_at
			) VALUES ($1, $2, 1, '{"kind":"publisher-fixture"}', $3)
		`,
			policyID,
			"publisher-policy-"+fixture.workID.String(),
			options.sourceTime,
		); err != nil {
			t.Fatalf("insert publisher JCR policy: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO venue_policy_assessments (
				venue_id, policy_version_id, metric_year, decision,
				matched_rules, evidence, assessed_at
			) VALUES ($1, $2, 2026, $3, $4::jsonb, $5::jsonb, $6)
		`,
			venueID,
			policyID,
			decision,
			matchedRules,
			evidence,
			options.sourceTime,
		); err != nil {
			t.Fatalf("insert publisher JCR assessment: %v", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit publisher fixture: %v", err)
	}
	return fixture
}

func mustPublisher(t *testing.T, pool *pgxpool.Pool) *Publisher {
	t.Helper()

	publisher, err := NewPublisher(pool)
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}
	return publisher
}

func assertNoCatalogWrites(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	var generations, publications, current int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM public_catalog_generations),
			(SELECT count(*) FROM public_catalog_publications),
			(SELECT count(*) FROM public_catalog_current)
	`).Scan(&generations, &publications, &current); err != nil {
		t.Fatalf("count public catalog writes: %v", err)
	}
	if generations != 0 || publications != 0 || current != 0 {
		t.Fatalf(
			"catalog writes = generations %d publications %d current %d, want 0/0/0",
			generations,
			publications,
			current,
		)
	}
}

func assertNestedJSONValue(
	t *testing.T,
	payload json.RawMessage,
	path []string,
	want any,
) {
	t.Helper()

	var current any
	if err := json.Unmarshal(payload, &current); err != nil {
		t.Fatalf("decode JSON payload: %v", err)
	}
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("JSON path %v reached non-object %#v", path, current)
		}
		current, ok = object[segment]
		if !ok {
			t.Fatalf("JSON path %v is missing segment %q in %s", path, segment, payload)
		}
	}
	if fmt.Sprint(current) != fmt.Sprint(want) {
		t.Fatalf("JSON path %v = %#v, want %#v; payload=%s", path, current, want, payload)
	}
}

func assertJSONArrayLength(
	t *testing.T,
	payload json.RawMessage,
	field string,
	want int,
) {
	t.Helper()

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode JSON payload: %v", err)
	}
	value, ok := decoded[field].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want DataValue object", field, decoded[field])
	}
	items, ok := value["value"].([]any)
	if !ok {
		t.Fatalf("%s.value = %#v, want array", field, value["value"])
	}
	if len(items) != want {
		t.Fatalf("%s.value length = %d, want %d", field, len(items), want)
	}
}

func optionalUUID(include bool, value uuid.UUID) string {
	if include {
		return value.String()
	}
	return ""
}

func stateForTime(value time.Time) string {
	if value.IsZero() {
		return "missing"
	}
	return "known"
}

func optionalTimeValue(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.Format(time.RFC3339Nano)
}

func boolState(value bool) string {
	if value {
		return "known"
	}
	return "unknown"
}

func optionalStringArray(include bool, value string) []string {
	if !include {
		return nil
	}
	return []string{value}
}
