package abstractanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/openairesponses"
)

const (
	abstractAnalysisPostgresImage    = "postgres:18-alpine"
	abstractAnalysisPostgresUser     = "paper_hub_abstract_analysis_test"
	abstractAnalysisPostgresPassword = "paper-hub-abstract-analysis-test-password"
	abstractAnalysisPostgresDatabase = "postgres"
)

var (
	abstractAnalysisPostgresURL string
	abstractAnalysisDatabaseID  atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(
		ctx,
		abstractAnalysisPostgresImage,
		postgres.WithDatabase(abstractAnalysisPostgresDatabase),
		postgres.WithUsername(abstractAnalysisPostgresUser),
		postgres.WithPassword(abstractAnalysisPostgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(
			os.Stderr,
			"start required %s Testcontainer: %v\n",
			abstractAnalysisPostgresImage,
			err,
		)
		os.Exit(1)
	}
	abstractAnalysisPostgresURL, err = container.ConnectionString(
		ctx,
		"sslmode=disable",
	)
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		fmt.Fprintf(os.Stderr, "resolve PostgreSQL test URL: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := testcontainers.TerminateContainer(container); err != nil {
		fmt.Fprintf(os.Stderr, "terminate PostgreSQL Testcontainer: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func TestPostgresStorePersistsAuditableSuccessAndExcludesExactRevision(
	t *testing.T,
) {
	pool := openAbstractAnalysisTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	fixture := insertAbstractAnalysisCandidate(t, pool)
	ctx := context.Background()
	selection := Selection{
		Cutoff:         fixture.SourceTime.Add(time.Hour),
		Limit:          10,
		PromptVersion:  PromptVersion,
		SchemaVersion:  SchemaVersion,
		APIMode:        openairesponses.ModeChatCompletions,
		RequestedModel: "requested-model",
	}
	candidates, err := store.Candidates(ctx, selection)
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0] != fixture.Candidate {
		t.Fatalf("candidates = %#v, want %#v", candidates, fixture.Candidate)
	}
	descriptor, err := newRunDescriptor(
		candidates[0],
		openairesponses.ModeChatCompletions,
		"requested-model",
		fixture.SourceTime.Add(2*time.Hour),
	)
	if err != nil {
		t.Fatalf("newRunDescriptor() error = %v", err)
	}
	run, err := store.Start(ctx, descriptor)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	result := notReportedResult()
	result.Domain = EvidenceField{
		State:    StateSupported,
		Value:    "oncology",
		Evidence: []string{"Patients with advanced cancer"},
	}
	output, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal(result) error = %v", err)
	}
	success := Success{
		ResponseID:  "resp_postgres_success",
		ActualModel: "actual-model-2026-07-18",
		Usage: openairesponses.Usage{
			InputTokens:  100,
			OutputTokens: 200,
			TotalTokens:  300,
			Raw: json.RawMessage(
				`{"input_tokens":100,"output_tokens":200,"total_tokens":300}`,
			),
		},
		OutputJSON:  output,
		Result:      result,
		CompletedAt: descriptor.StartedAt.Add(time.Minute),
	}
	if err := store.Succeed(ctx, run, success); err != nil {
		t.Fatalf("Succeed() error = %v", err)
	}

	var (
		status                                         string
		apiMode                                        string
		requestedModel, actualModel, responseID        string
		promptVersion, schemaVersion                   string
		title, abstract                                string
		titleHash, abstractHash, schemaHash, inputHash string
		schemaJSON, usageJSON, outputJSON              []byte
		inputTokens, outputTokens, totalTokens         int64
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			status,
			api_mode,
			requested_model,
			actual_model,
			response_id,
			prompt_version,
			schema_version,
			schema_json,
			input_title,
			input_abstract,
			title_sha256,
			abstract_sha256,
			schema_sha256,
			input_sha256,
			usage,
			output_payload,
			input_tokens,
			output_tokens,
			total_tokens
		FROM abstract_route_analysis_runs
		WHERE id = $1
	`, run.ID).Scan(
		&status,
		&apiMode,
		&requestedModel,
		&actualModel,
		&responseID,
		&promptVersion,
		&schemaVersion,
		&schemaJSON,
		&title,
		&abstract,
		&titleHash,
		&abstractHash,
		&schemaHash,
		&inputHash,
		&usageJSON,
		&outputJSON,
		&inputTokens,
		&outputTokens,
		&totalTokens,
	); err != nil {
		t.Fatalf("query persisted abstract route run: %v", err)
	}
	if status != "succeeded" ||
		apiMode != string(openairesponses.ModeChatCompletions) ||
		requestedModel != "requested-model" ||
		actualModel != success.ActualModel ||
		responseID != success.ResponseID ||
		promptVersion != PromptVersion ||
		schemaVersion != SchemaVersion ||
		title != fixture.Candidate.Title ||
		abstract != fixture.Candidate.Abstract ||
		len(titleHash) != 64 ||
		len(abstractHash) != 64 ||
		len(schemaHash) != 64 ||
		len(inputHash) != 64 ||
		inputTokens != 100 ||
		outputTokens != 200 ||
		totalTokens != 300 {
		t.Fatalf(
			"persisted audit = status=%q mode=%q requested=%q actual=%q response=%q tokens=%d/%d/%d",
			status,
			apiMode,
			requestedModel,
			actualModel,
			responseID,
			inputTokens,
			outputTokens,
			totalTokens,
		)
	}
	assertCanonicalJSONEqual(t, schemaJSON, SchemaJSON())
	assertCanonicalJSONEqual(t, usageJSON, success.Usage.Raw)
	assertCanonicalJSONEqual(t, outputJSON, success.OutputJSON)

	var persistedResult Result
	if err := json.Unmarshal(outputJSON, &persistedResult); err != nil {
		t.Fatalf("decode persisted abstract route result: %v", err)
	}
	if persistedResult.Domain.State != StateSupported ||
		len(persistedResult.Domain.Evidence) != 1 ||
		persistedResult.Domain.Evidence[0] !=
			"Patients with advanced cancer" {
		t.Fatalf("persisted evidence fields = %#v", persistedResult.Domain)
	}

	candidates, err = store.Candidates(ctx, selection)
	if err != nil {
		t.Fatalf("Candidates(after success) error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("Candidates(after success) = %#v", candidates)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE abstract_route_analysis_runs
		SET response_id = 'mutated'
		WHERE id = $1
	`, run.ID); postgresCode(err) != "55000" {
		t.Fatalf("terminal mutation error = %v", err)
	}
}

func TestPostgresStorePersistsFailureMetadataAndAllowsRetry(t *testing.T) {
	pool := openAbstractAnalysisTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	fixture := insertAbstractAnalysisCandidate(t, pool)
	ctx := context.Background()
	selection := Selection{
		Cutoff:         fixture.SourceTime.Add(time.Hour),
		Limit:          1,
		PromptVersion:  PromptVersion,
		SchemaVersion:  SchemaVersion,
		APIMode:        openairesponses.ModeChatCompletions,
		RequestedModel: "requested-model",
	}
	candidates, err := store.Candidates(ctx, selection)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Candidates() = %#v, %v", candidates, err)
	}
	descriptor, err := newRunDescriptor(
		candidates[0],
		openairesponses.ModeChatCompletions,
		"requested-model",
		fixture.SourceTime.Add(2*time.Hour),
	)
	if err != nil {
		t.Fatalf("newRunDescriptor() error = %v", err)
	}
	run, err := store.Start(ctx, descriptor)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	failure := Failure{
		Code:        FailureRefusal,
		ResponseID:  "resp_refusal",
		ActualModel: "actual-model",
		Usage: openairesponses.Usage{
			InputTokens:  10,
			OutputTokens: 2,
			TotalTokens:  12,
			Raw: json.RawMessage(
				`{"input_tokens":10,"output_tokens":2,"total_tokens":12}`,
			),
		},
		CompletedAt: descriptor.StartedAt.Add(time.Minute),
	}
	if err := store.Fail(ctx, run, failure); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	var status, code, responseID, actualModel string
	var usage []byte
	if err := pool.QueryRow(ctx, `
		SELECT status, failure_code, response_id, actual_model, usage
		FROM abstract_route_analysis_runs
		WHERE id = $1
	`, run.ID).Scan(
		&status,
		&code,
		&responseID,
		&actualModel,
		&usage,
	); err != nil {
		t.Fatalf("query failed abstract route run: %v", err)
	}
	if status != "failed" ||
		code != FailureRefusal ||
		responseID != failure.ResponseID ||
		actualModel != failure.ActualModel {
		t.Fatalf(
			"failed audit = %q/%q/%q/%q",
			status,
			code,
			responseID,
			actualModel,
		)
	}
	assertCanonicalJSONEqual(t, usage, failure.Usage.Raw)
	candidates, err = store.Candidates(ctx, selection)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Candidates(after failure) = %#v, %v; want retry", candidates, err)
	}
}

func TestPostgresStoreReclaimsExpiredRunningLeaseAndKeepsActiveLease(
	t *testing.T,
) {
	pool := openAbstractAnalysisTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	fixture := insertAbstractAnalysisCandidate(t, pool)
	ctx := context.Background()
	selection := Selection{
		Cutoff:         fixture.SourceTime.Add(time.Hour),
		Limit:          1,
		PromptVersion:  PromptVersion,
		SchemaVersion:  SchemaVersion,
		APIMode:        openairesponses.ModeChatCompletions,
		RequestedModel: "requested-model",
	}
	candidates, err := store.Candidates(ctx, selection)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Candidates() = %#v, %v", candidates, err)
	}

	activeDescriptor, err := newRunDescriptor(
		candidates[0],
		openairesponses.ModeChatCompletions,
		"requested-model",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("newRunDescriptor(active) error = %v", err)
	}
	activeRun, err := store.Start(ctx, activeDescriptor)
	if err != nil {
		t.Fatalf("Start(active) error = %v", err)
	}
	candidates, err = store.Candidates(ctx, selection)
	if err != nil {
		t.Fatalf("Candidates(active) error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("Candidates(active) = %#v, want none", candidates)
	}
	if err := store.Fail(ctx, activeRun, Failure{
		Code:        FailureRequestCancelled,
		CompletedAt: activeDescriptor.StartedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("Fail(active) error = %v", err)
	}

	candidates, err = store.Candidates(ctx, selection)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Candidates(after active failure) = %#v, %v", candidates, err)
	}
	expiredDescriptor, err := newRunDescriptor(
		candidates[0],
		openairesponses.ModeChatCompletions,
		"requested-model",
		time.Now().UTC().Add(-RunLeaseDuration-time.Minute),
	)
	if err != nil {
		t.Fatalf("newRunDescriptor(expired) error = %v", err)
	}
	expiredRun, err := store.Start(ctx, expiredDescriptor)
	if err != nil {
		t.Fatalf("Start(expired) error = %v", err)
	}
	candidates, err = store.Candidates(ctx, selection)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Candidates(expired) = %#v, %v; want reclaimed", candidates, err)
	}

	var status, failureCode string
	if err := pool.QueryRow(ctx, `
		SELECT status, failure_code
		FROM abstract_route_analysis_runs
		WHERE id = $1
	`, expiredRun.ID).Scan(&status, &failureCode); err != nil {
		t.Fatalf("query reclaimed run: %v", err)
	}
	if status != "failed" || failureCode != FailureLeaseExpired {
		t.Fatalf(
			"reclaimed run = status %q failure %q",
			status,
			failureCode,
		)
	}
}

type abstractAnalysisFixture struct {
	Candidate  Candidate
	SourceTime time.Time
}

func insertAbstractAnalysisCandidate(
	t *testing.T,
	pool *pgxpool.Pool,
) abstractAnalysisFixture {
	t.Helper()

	ctx := context.Background()
	sourceTime := time.Date(2026, time.July, 18, 6, 0, 0, 0, time.UTC)
	var workID, jobID, rawID, sourceRecordID, normalizedID, projectionID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO works (canonical_key, status, title, published_at)
		VALUES (
			'doi:10.1000/abstract-analysis',
			'active',
			'External validation of an oncology model',
			$1
		)
		RETURNING id::text
	`, sourceTime).Scan(&workID); err != nil {
		t.Fatalf("insert Work: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_jobs (
			source,
			job_type,
			idempotency_key,
			status,
			payload,
			attempts,
			max_attempts,
			started_at,
			finished_at,
			batch_key,
			stage
		) VALUES (
			'pubmed',
			'sync',
			$1,
			'succeeded',
			'{}',
			1,
			1,
			$2,
			$2,
			$1,
			'project'
		)
		RETURNING id::text
	`, "abstract-analysis-"+workID, sourceTime).Scan(&jobID); err != nil {
		t.Fatalf("insert ingestion job: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_raw_events (
			job_id,
			logical_source,
			event_key,
			event_kind,
			source_record_id,
			source_time,
			tie_break_key,
			position,
			content_hash,
			raw_format,
			raw_payload
		) VALUES (
			$1,
			'pubmed',
			'pubmed:12345678',
			'upsert',
			'12345678',
			$2,
			$3,
			1,
			$4,
			'json',
			'{"PMID":"12345678"}'
		)
		RETURNING id::text
	`,
		jobID,
		sourceTime,
		"pubmed-page-1-item-1",
		strings.Repeat("a", 64),
	).Scan(&rawID); err != nil {
		t.Fatalf("insert raw event: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO source_records (
			source,
			source_record_id,
			source_identity,
			source_time,
			content_hash,
			raw_payload
		) VALUES (
			'pubmed',
			'12345678',
			'{"canonical_key":"doi:10.1000/abstract-analysis"}',
			$1,
			$2,
			'{"PMID":"12345678"}'
		)
		RETURNING id::text
	`, sourceTime, strings.Repeat("a", 64)).Scan(&sourceRecordID); err != nil {
		t.Fatalf("insert SourceRecord: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, sourceRecordID, workID); err != nil {
		t.Fatalf("link SourceRecord Work: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id,
			source_record_uuid,
			normalization_policy_version,
			payload_schema_version,
			normalized_payload
		) VALUES (
			$1,
			$2,
			'normalization/pubmed-v1',
			'normalized-record/v4',
			$3
		)
		RETURNING id::text
	`, rawID, sourceRecordID, json.RawMessage(`{
		"source":"pubmed",
		"source_record_id":"12345678",
		"parser_version":"pubmed/pubmed-article-v1",
		"title":"External validation of an oncology model",
		"abstract":"Patients with advanced cancer were evaluated in an external validation cohort.",
		"identifiers":[],
		"abstract_sections":[],
		"publication_model":"",
		"publication_status":"",
		"publication_history":[],
		"authors":[],
		"mesh_headings":[],
		"publication_types":[],
		"relations":[],
		"topics":[],
		"keywords":[],
		"code_urls":[],
		"evidence":[]
	}`)).Scan(&normalizedID); err != nil {
		t.Fatalf("insert normalized assertion: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_projection_assertions (
			raw_event_id,
			source_record_uuid,
			work_id,
			job_id,
			scope_policy_version,
			projection_policy_version,
			record_payload,
			normalized_assertion_id
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			'scope/v1',
			'projection/v1',
			'{"title":"External validation of an oncology model"}',
			$5
		)
		RETURNING id::text
	`, rawID, sourceRecordID, workID, jobID, normalizedID).Scan(
		&projectionID,
	); err != nil {
		t.Fatalf("insert projection assertion: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_projection_states (
			work_id,
			raw_event_id,
			source_record_uuid,
			source_time,
			tie_break_key,
			position,
			scope_policy_version,
			projection_policy_version,
			normalized_assertion_id
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			$5,
			1,
			'scope/v1',
			'projection/v1',
			$6
		)
	`, workID, rawID, sourceRecordID, sourceTime, strings.Repeat("a", 64), normalizedID); err != nil {
		t.Fatalf("insert work projection state: %v", err)
	}
	return abstractAnalysisFixture{
		SourceTime: sourceTime,
		Candidate: Candidate{
			WorkID:                 workID,
			ProjectionAssertionID:  projectionID,
			NormalizedAssertionID:  normalizedID,
			SourceRecordID:         sourceRecordID,
			Source:                 "pubmed",
			SourceRecordExternalID: "12345678",
			ParserVersion:          "pubmed/pubmed-article-v1",
			SourceTime:             sourceTime,
			Title:                  "External validation of an oncology model",
			Abstract:               "Patients with advanced cancer were evaluated in an external validation cohort.",
		},
	}
}

func openAbstractAnalysisTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	parsed, err := url.Parse(abstractAnalysisPostgresURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL URL: %v", err)
	}
	databaseName := fmt.Sprintf(
		"abstract_analysis_%d",
		abstractAnalysisDatabaseID.Add(1),
	)
	admin, err := pgxpool.New(context.Background(), abstractAnalysisPostgresURL)
	if err != nil {
		t.Fatalf("open PostgreSQL admin pool: %v", err)
	}
	if _, err := admin.Exec(
		context.Background(),
		"CREATE DATABASE "+databaseName,
	); err != nil {
		admin.Close()
		t.Fatalf("create PostgreSQL database: %v", err)
	}
	admin.Close()
	parsed.Path = "/" + databaseName
	pool, err := pgxpool.New(context.Background(), parsed.String())
	if err != nil {
		t.Fatalf("open PostgreSQL test pool: %v", err)
	}
	if err := database.Up(context.Background(), pool); err != nil {
		pool.Close()
		t.Fatalf("migrate PostgreSQL test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func assertCanonicalJSONEqual(t *testing.T, got []byte, want []byte) {
	t.Helper()

	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v", err)
	}
	gotCanonical, _ := json.Marshal(gotValue)
	wantCanonical, _ := json.Marshal(wantValue)
	if string(gotCanonical) != string(wantCanonical) {
		t.Fatalf("JSON = %s, want %s", gotCanonical, wantCanonical)
	}
}

func postgresCode(err error) string {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code
	}
	return ""
}
