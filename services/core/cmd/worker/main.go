package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/catalog"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ingestion"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/crossref"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/openalex"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
)

type commandKind string

const (
	commandSyncOpenAlex   commandKind = "sync_openalex"
	commandSyncPubMed     commandKind = "sync_pubmed"
	commandSyncCrossref   commandKind = "sync_crossref"
	commandImportJCR      commandKind = "import_jcr"
	commandPublishCatalog commandKind = "publish_catalog"
	maxSyncResults                    = 1000
)

type workerCommand struct {
	Kind           commandKind
	Query          string
	Filter         string
	FromDate       time.Time
	ToDate         time.Time
	ISSNs          []string
	MaxResults     int
	File           string
	FormulaVersion string
	GeneratedAt    time.Time
}

type commandRunner func(
	context.Context,
	config.Config,
	workerCommand,
) (map[string]any, error)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	os.Exit(realMain(
		ctx,
		os.Args[1:],
		os.Stdout,
		os.Stderr,
		os.LookupEnv,
		runCommand,
	))
}

func realMain(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookup config.LookupEnv,
	runner commandRunner,
) int {
	command, role, err := parseWorkerCommand(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg, err := config.LoadFrom(role, lookup)
	if err != nil {
		fmt.Fprintf(stderr, "load worker configuration: %v\n", err)
		return 1
	}
	result, err := runner(ctx, cfg, command)
	if err != nil {
		fmt.Fprintf(stderr, "run worker command: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(stderr, "encode worker result: %v\n", err)
		return 1
	}
	return 0
}

func parseWorkerCommand(args []string) (workerCommand, config.Role, error) {
	if len(args) < 2 {
		return workerCommand{}, "", errors.New(
			"usage: paper-hub-worker <sync|import|publish> <source> [flags]",
		)
	}
	if args[0] == "publish" {
		if args[1] != "catalog" {
			return workerCommand{}, "", fmt.Errorf(
				"unsupported publish target %q; expected catalog",
				args[1],
			)
		}
		command, err := parseCatalogPublishCommand(args[2:])
		return command, config.RoleCatalogPublish, err
	}
	if args[0] == "import" {
		if args[1] != "jcr" {
			return workerCommand{}, "", fmt.Errorf(
				"unsupported import source %q; expected jcr",
				args[1],
			)
		}
		command, err := parseJCRImportCommand(args[2:])
		return command, config.RoleJCRImport, err
	}
	if args[0] != "sync" {
		return workerCommand{}, "", errors.New(
			"usage: paper-hub-worker <sync|import|publish> <source> [flags]",
		)
	}
	switch args[1] {
	case "openalex":
		command, err := parseOpenAlexCommand(args[2:])
		return command, config.RoleOpenAlexSync, err
	case "pubmed":
		command, err := parsePubMedCommand(args[2:])
		return command, config.RolePubMedSync, err
	case "crossref":
		command, err := parseCrossrefCommand(args[2:])
		return command, config.RoleCrossrefSync, err
	default:
		return workerCommand{}, "", fmt.Errorf(
			"unsupported sync source %q; expected openalex, pubmed, or crossref",
			args[1],
		)
	}
}

func parseCatalogPublishCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("publish catalog", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	var generatedAt string
	set.StringVar(
		&command.FormulaVersion,
		"formula-version",
		"",
		"catalog formula version",
	)
	set.StringVar(
		&generatedAt,
		"generated-at",
		"",
		"explicit RFC3339Nano generation time",
	)
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse catalog publish flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected catalog publish arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}

	command.Kind = commandPublishCatalog
	if command.FormulaVersion == "" {
		return workerCommand{}, errors.New(
			"catalog publish requires an explicit --formula-version",
		)
	}
	if command.FormulaVersion != strings.TrimSpace(command.FormulaVersion) {
		return workerCommand{}, errors.New("catalog formula-version must be trimmed")
	}
	if generatedAt == "" {
		return workerCommand{}, errors.New(
			"catalog publish requires an explicit --generated-at",
		)
	}
	if generatedAt != strings.TrimSpace(generatedAt) {
		return workerCommand{}, errors.New("catalog generated-at must be trimmed")
	}
	parsed, err := time.Parse(time.RFC3339Nano, generatedAt)
	if err != nil {
		return workerCommand{}, fmt.Errorf(
			"catalog generated-at must use RFC3339Nano: %w",
			err,
		)
	}
	if parsed.IsZero() {
		return workerCommand{}, errors.New("catalog generated-at must be non-zero")
	}
	command.GeneratedAt = parsed
	return command, nil
}

func parseJCRImportCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("import jcr", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	set.StringVar(&command.File, "file", "", "authorized JCR CSV export")
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse JCR import flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf(
			"unexpected JCR import arguments: %s",
			strings.Join(set.Args(), " "),
		)
	}
	command.Kind = commandImportJCR
	command.File = strings.TrimSpace(command.File)
	if command.File == "" {
		return workerCommand{}, errors.New("JCR import requires an explicit --file")
	}
	if strings.ContainsAny(command.File, "?#") ||
		!strings.EqualFold(filepath.Ext(command.File), ".csv") {
		return workerCommand{}, errors.New("JCR import file must be an explicit .csv file")
	}
	return command, nil
}

func parseOpenAlexCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("sync openalex", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	set.StringVar(&command.Query, "query", "", "OpenAlex search query")
	set.StringVar(&command.Filter, "filter", "", "OpenAlex exact filter expression")
	set.IntVar(&command.MaxResults, "max-results", 0, "maximum records")
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse OpenAlex flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf("unexpected OpenAlex arguments: %s", strings.Join(set.Args(), " "))
	}
	command.Kind = commandSyncOpenAlex
	command.Query = strings.TrimSpace(command.Query)
	command.Filter = strings.TrimSpace(command.Filter)
	if command.Query == "" && command.Filter == "" {
		return workerCommand{}, errors.New("OpenAlex sync requires a query or filter")
	}
	if err := validateMaxResults(command.MaxResults); err != nil {
		return workerCommand{}, err
	}
	return command, nil
}

func parsePubMedCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("sync pubmed", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	var fromDate, toDate string
	var issns stringListFlag
	set.StringVar(&command.Query, "query", "", "PubMed Entrez term")
	set.StringVar(&fromDate, "from-date", "", "inclusive YYYY-MM-DD")
	set.StringVar(&toDate, "to-date", "", "inclusive YYYY-MM-DD")
	set.Var(&issns, "issn", "exact journal ISSN; repeatable")
	set.IntVar(&command.MaxResults, "max-results", 0, "maximum records")
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse PubMed flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf("unexpected PubMed arguments: %s", strings.Join(set.Args(), " "))
	}
	if (fromDate == "") != (toDate == "") {
		return workerCommand{}, errors.New("PubMed sync requires both from-date and to-date")
	}
	if fromDate == "" {
		return workerCommand{}, errors.New("PubMed sync requires both from-date and to-date")
	}
	var err error
	command.FromDate, err = parseDateFlag("from-date", fromDate)
	if err != nil {
		return workerCommand{}, err
	}
	command.ToDate, err = parseDateFlag("to-date", toDate)
	if err != nil {
		return workerCommand{}, err
	}
	if command.FromDate.After(command.ToDate) {
		return workerCommand{}, errors.New("PubMed from-date must not follow to-date")
	}
	command.Kind = commandSyncPubMed
	command.Query = strings.TrimSpace(command.Query)
	command.ISSNs = issns.values()
	if command.Query == "" && len(command.ISSNs) == 0 {
		return workerCommand{}, errors.New("PubMed sync requires a query or exact ISSN")
	}
	if err := validateMaxResults(command.MaxResults); err != nil {
		return workerCommand{}, err
	}
	return command, nil
}

func parseCrossrefCommand(args []string) (workerCommand, error) {
	set := flag.NewFlagSet("sync crossref", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var command workerCommand
	var fromDate, toDate string
	var issns stringListFlag
	set.StringVar(&fromDate, "from-date", "", "inclusive indexed YYYY-MM-DD")
	set.StringVar(&toDate, "to-date", "", "inclusive indexed YYYY-MM-DD")
	set.Var(&issns, "issn", "exact journal ISSN; repeatable")
	set.IntVar(&command.MaxResults, "max-results", 0, "maximum records")
	if err := set.Parse(args); err != nil {
		return workerCommand{}, fmt.Errorf("parse Crossref flags: %w", err)
	}
	if set.NArg() != 0 {
		return workerCommand{}, fmt.Errorf("unexpected Crossref arguments: %s", strings.Join(set.Args(), " "))
	}
	if (fromDate == "") != (toDate == "") {
		return workerCommand{}, errors.New("Crossref sync requires both from-date and to-date")
	}
	var err error
	if fromDate != "" {
		command.FromDate, err = parseDateFlag("from-date", fromDate)
		if err != nil {
			return workerCommand{}, err
		}
		command.ToDate, err = parseDateFlag("to-date", toDate)
		if err != nil {
			return workerCommand{}, err
		}
		if command.FromDate.After(command.ToDate) {
			return workerCommand{}, errors.New("Crossref from-date must not follow to-date")
		}
	}
	command.Kind = commandSyncCrossref
	command.ISSNs = issns.values()
	if command.FromDate.IsZero() && len(command.ISSNs) == 0 {
		return workerCommand{}, errors.New("Crossref sync requires a date window or ISSN")
	}
	if err := validateMaxResults(command.MaxResults); err != nil {
		return workerCommand{}, err
	}
	return command, nil
}

func validateMaxResults(value int) error {
	if value < 1 || value > maxSyncResults {
		return fmt.Errorf("max-results must be between 1 and %d", maxSyncResults)
	}
	return nil
}

func parseDateFlag(name string, value string) (time.Time, error) {
	parsed, err := time.Parse(time.DateOnly, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must use YYYY-MM-DD: %w", name, err)
	}
	return parsed.UTC(), nil
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if normalized == "" {
		return errors.New("ISSN must not be empty")
	}
	*values = append(*values, normalized)
	return nil
}

func (values stringListFlag) values() []string {
	result := append([]string(nil), values...)
	slices.Sort(result)
	result = slices.Compact(result)
	return result
}

func runCommand(
	ctx context.Context,
	cfg config.Config,
	command workerCommand,
) (map[string]any, error) {
	pool, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("open worker database: %w", err)
	}
	defer pool.Close()
	switch command.Kind {
	case commandPublishCatalog:
		return runCatalogPublish(ctx, pool, command)
	case commandImportJCR:
		return runJCRImport(ctx, pool, cfg, command)
	default:
		return runSync(ctx, pool, cfg, command)
	}
}

func runCatalogPublish(
	ctx context.Context,
	pool *pgxpool.Pool,
	command workerCommand,
) (map[string]any, error) {
	publisher, err := catalog.NewPublisher(pool)
	if err != nil {
		return nil, fmt.Errorf("create catalog publisher: %w", err)
	}
	generation, err := publisher.PublishCurrent(ctx, catalog.PublishInput{
		FormulaVersion: command.FormulaVersion,
		GeneratedAt:    command.GeneratedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("publish current catalog: %w", err)
	}
	return catalogGenerationResult(generation), nil
}

func catalogGenerationResult(generation catalog.Generation) map[string]any {
	return map[string]any{
		"id":              generation.ID.String(),
		"source_revision": generation.SourceRevision,
		"formula_version": generation.FormulaVersion,
		"generated_at":    generation.GeneratedAt.UTC().Format(time.RFC3339Nano),
		"published_at":    generation.PublishedAt.UTC().Format(time.RFC3339Nano),
	}
}

func runSync(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg config.Config,
	command workerCommand,
) (map[string]any, error) {
	repository, err := ingestion.NewPostgresRepository(pool)
	if err != nil {
		return nil, err
	}
	scopePolicy := ingestion.NewControlledIdentityScopePolicy(
		"scope/controlled-canonical-identity/v1",
	)
	projectionPolicy := ingestion.NewDeterministicProjectionPolicy(
		"projection/latest-source-revision/v1",
	)
	service, err := ingestion.NewService(
		repository,
		repository,
		repository,
		repository,
		scopePolicy,
		projectionPolicy,
	)
	if err != nil {
		return nil, err
	}
	records, logicalSource, err := fetchRecords(ctx, cfg, command)
	if err != nil {
		return nil, err
	}
	payload := commandPayload(command)
	idempotencyKey, err := commandIdempotencyKey(logicalSource, payload)
	if err != nil {
		return nil, err
	}
	job, err := ingestion.NewJob(
		"sync/"+logicalSource+"/"+idempotencyKey,
		logicalSource,
		idempotencyKey,
		payload,
	)
	if err != nil {
		return nil, err
	}
	summary, err := service.Run(ctx, job, recordEvents(logicalSource, records))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"job_id":       summary.JobID,
		"status":       summary.Status,
		"raw_inserted": summary.RawInserted,
		"raw_reused":   summary.RawReused,
		"projected":    summary.Projected,
		"excluded":     summary.Excluded,
		"deleted":      summary.Deleted,
		"unchanged":    summary.Unchanged,
		"failed":       summary.Failed,
	}, nil
}

func runJCRImport(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg config.Config,
	command workerCommand,
) (map[string]any, error) {
	if command.File != cfg.Venues.JCRImportPath {
		return nil, fmt.Errorf(
			"JCR --file %q must exactly match JCR_IMPORT_PATH",
			command.File,
		)
	}
	file, err := os.Open(command.File)
	if err != nil {
		return nil, fmt.Errorf("open JCR import file: %w", err)
	}
	defer file.Close()

	store, err := venue.NewPostgresJCRStore(
		pool,
		venue.PostgresJCRStoreConfig{
			SourceLicense: cfg.Venues.JCRSourceLicense,
		},
	)
	if err != nil {
		return nil, err
	}
	importer, err := venue.NewJCRImporter(store, store, time.Now)
	if err != nil {
		return nil, err
	}
	receipt, err := importer.Import(ctx, file)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"file_sha256":    receipt.FileSHA256(),
		"source":         receipt.Source(),
		"imported_at":    receipt.ImportedAt(),
		"input_rows":     receipt.InputRows(),
		"inserted_rows":  receipt.InsertedRows(),
		"unchanged_rows": receipt.UnchangedRows(),
	}, nil
}

func fetchRecords(
	ctx context.Context,
	cfg config.Config,
	command workerCommand,
) (source.ClientSequence, string, error) {
	switch command.Kind {
	case commandSyncOpenAlex:
		client, err := openalex.NewClient(
			http.DefaultClient,
			openalex.Config{
				BaseURL:          cfg.OpenAlex.Request.BaseURL,
				APIKey:           cfg.OpenAlex.APIKey,
				ContactEmail:     cfg.OpenAlex.ContactEmail,
				UserAgent:        "paper-research-hub/0.1 (+mailto:" + cfg.OpenAlex.ContactEmail + ")",
				PerPage:          min(cfg.OpenAlex.Request.BatchSize, 100),
				Timeout:          cfg.OpenAlex.Request.Timeout,
				RateLimit:        httpclient.RateLimit{Requests: 10, Interval: time.Second},
				MaxRetries:       cfg.OpenAlex.Request.MaxRetries,
				MaxWait:          cfg.OpenAlex.Request.MaxWait,
				InitialBackoff:   500 * time.Millisecond,
				MaxBackoff:       10 * time.Second,
				MaxResponseBytes: 32 << 20,
			},
			httpclient.Dependencies{},
		)
		if err != nil {
			return nil, "", err
		}
		return client.Fetch(ctx, source.Query{
			Search:     command.Query,
			Filter:     command.Filter,
			MaxResults: command.MaxResults,
		}), source.OpenAlex, nil

	case commandSyncPubMed:
		client, err := pubmed.NewClient(
			http.DefaultClient,
			pubmed.Config{
				BaseURL:          cfg.PubMed.Request.BaseURL,
				Tool:             cfg.PubMed.Tool,
				Email:            cfg.PubMed.Email,
				APIKey:           cfg.PubMed.APIKey,
				UserAgent:        "paper-research-hub/0.1 (+mailto:" + cfg.PubMed.Email + ")",
				BatchSize:        min(cfg.PubMed.Request.BatchSize, 10_000),
				Timeout:          cfg.PubMed.Request.Timeout,
				MaxRetries:       cfg.PubMed.Request.MaxRetries,
				MaxWait:          cfg.PubMed.Request.MaxWait,
				InitialBackoff:   500 * time.Millisecond,
				MaxBackoff:       10 * time.Second,
				MaxResponseBytes: 64 << 20,
			},
			httpclient.Dependencies{},
		)
		if err != nil {
			return nil, "", err
		}
		history, err := client.Search(ctx, pubmed.SearchQuery{
			Term:         command.Query,
			JournalISSNs: command.ISSNs,
			DateWindow: pubmed.DateWindow{
				From: command.FromDate,
				To:   command.ToDate,
			},
			MaxResults: command.MaxResults,
		})
		if err != nil {
			return nil, "", err
		}
		return client.Fetch(ctx, history), source.PubMed, nil

	case commandSyncCrossref:
		client, err := crossref.NewClient(
			http.DefaultClient,
			crossref.Config{
				BaseURL:          cfg.Crossref.Request.BaseURL,
				ContactEmail:     cfg.Crossref.ContactEmail,
				UserAgent:        "paper-research-hub/0.1 (+mailto:" + cfg.Crossref.ContactEmail + ")",
				BatchSize:        min(cfg.Crossref.Request.BatchSize, 1000),
				Timeout:          cfg.Crossref.Request.Timeout,
				RateLimit:        httpclient.RateLimit{Requests: 10, Interval: time.Second},
				MaxRetries:       cfg.Crossref.Request.MaxRetries,
				MaxWait:          cfg.Crossref.Request.MaxWait,
				InitialBackoff:   500 * time.Millisecond,
				MaxBackoff:       10 * time.Second,
				MaxResponseBytes: 64 << 20,
			},
			httpclient.Dependencies{},
		)
		if err != nil {
			return nil, "", err
		}
		query := crossref.Query{
			ISSNs:      command.ISSNs,
			MaxResults: command.MaxResults,
		}
		if !command.FromDate.IsZero() {
			query.IndexedDateWindow = crossref.DateWindow{
				From: command.FromDate,
				To:   command.ToDate,
			}
		}
		return client.Fetch(ctx, query), source.Crossref, nil

	default:
		return nil, "", fmt.Errorf("unsupported worker command kind %q", command.Kind)
	}
}

func recordEvents(
	logicalSource string,
	records source.ClientSequence,
) ingestion.EventSequence {
	return func(yield func(ingestion.Event, error) bool) {
		if records == nil {
			yield(nil, errors.New("source record sequence is required"))
			return
		}
		position := int64(0)
		for record, err := range records {
			if err != nil {
				yield(nil, err)
				return
			}
			position++
			if strings.TrimSpace(record.Source) != logicalSource {
				yield(nil, fmt.Errorf(
					"record source %q conflicts with worker source %q",
					record.Source,
					logicalSource,
				))
				return
			}
			sourceTime, ok := recordRevisionTime(record)
			if !ok {
				yield(nil, fmt.Errorf(
					"record %s:%s has no deterministic source revision time",
					logicalSource,
					record.SourceRecordID,
				))
				return
			}
			envelope, err := ingestion.NewEnvelope(
				logicalSource,
				logicalSource+":"+record.SourceRecordID,
				sourceTime,
				record.Raw.SHA256,
				position,
				record,
				record.Raw,
			)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(envelope, nil) {
				return
			}
		}
	}
}

func recordRevisionTime(record source.Record) (time.Time, bool) {
	for _, value := range []*time.Time{
		record.UpdatedAt,
		record.RevisedAt,
		record.CreatedAt,
		record.ElectronicPublishedAt,
		record.PublishedAt,
	} {
		if value != nil && !value.IsZero() {
			return value.UTC(), true
		}
	}
	return time.Time{}, false
}

func commandPayload(command workerCommand) map[string]any {
	payload := map[string]any{
		"kind":        command.Kind,
		"max_results": command.MaxResults,
	}
	if command.Query != "" {
		payload["query"] = command.Query
	}
	if command.Filter != "" {
		payload["filter"] = command.Filter
	}
	if !command.FromDate.IsZero() {
		payload["from_date"] = command.FromDate.Format(time.DateOnly)
		payload["to_date"] = command.ToDate.Format(time.DateOnly)
	}
	if len(command.ISSNs) > 0 {
		payload["issns"] = append([]string(nil), command.ISSNs...)
	}
	return payload
}

func commandIdempotencyKey(
	logicalSource string,
	payload map[string]any,
) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode worker idempotency payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return logicalSource + ":" + hex.EncodeToString(digest[:]), nil
}

var _ iter.Seq2[ingestion.Event, error] = recordEvents("", nil)
