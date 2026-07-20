package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/httpclient"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venueenrich"
)

var journalPubMedRegistryHeader = []string{
	"domain",
	"source_order",
	"source_journal_name",
	"impact_factor",
	"jcr_value",
	"cass_value",
	"issn_l",
	"print_issn",
	"eissn",
	"all_issns",
	"crossref_publisher",
	"resolution_status",
	"pubmed_supported",
	"pubmed_record_count",
	"pubmed_checked_at",
	"source_url",
	"verification_status",
}

const (
	journalRegistryHashOne = "1111111111111111111111111111111111111111111111111111111111111111"
	journalRegistryHashTwo = "2222222222222222222222222222222222222222222222222222222222222222"
)

func TestJournalPubMedRegistryCLIRequiresExactlyThreeInputsAndExplicitPaths(
	t *testing.T,
) {
	t.Parallel()

	valid := []string{
		"--input", "medicine.csv",
		"--input", "biology.csv",
		"--input", "computer.csv",
		"--cache-dir", "cache",
		"--output", "registry.csv",
		"--report", "registry.report.json",
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no inputs", args: valid[6:], want: "--input must be provided exactly 3 times"},
		{
			name: "two inputs",
			args: append(
				[]string{"--input", "one.csv", "--input", "two.csv"},
				valid[6:]...,
			),
			want: "--input must be provided exactly 3 times",
		},
		{
			name: "four inputs",
			args: append(
				[]string{
					"--input", "one.csv",
					"--input", "two.csv",
					"--input", "three.csv",
					"--input", "four.csv",
				},
				valid[6:]...,
			),
			want: "--input must be provided exactly 3 times",
		},
		{
			name: "missing cache",
			args: []string{
				"--input", "one.csv",
				"--input", "two.csv",
				"--input", "three.csv",
				"--output", "registry.csv",
				"--report", "registry.report.json",
			},
			want: "--cache-dir must be explicit and non-empty",
		},
		{
			name: "blank output",
			args: []string{
				"--input", "one.csv",
				"--input", "two.csv",
				"--input", "three.csv",
				"--cache-dir", "cache",
				"--output", " ",
				"--report", "registry.report.json",
			},
			want: "--output must be explicit and non-empty",
		},
		{
			name: "missing report",
			args: []string{
				"--input", "one.csv",
				"--input", "two.csv",
				"--input", "three.csv",
				"--cache-dir", "cache",
				"--output", "registry.csv",
			},
			want: "--report must be explicit and non-empty",
		},
		{
			name: "positional argument",
			args: append(append([]string(nil), valid...), "unexpected"),
			want: "unexpected arguments",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			lookedUp := false
			ran := false
			code := realMain(
				context.Background(),
				test.args,
				&stdout,
				&stderr,
				func(string) (string, bool) {
					lookedUp = true
					return "", false
				},
				func(context.Context, registryCommand) (registryResult, error) {
					ran = true
					return registryResult{}, nil
				},
			)
			if code != 2 {
				t.Fatalf("realMain() code = %d, want parsing exit 2", code)
			}
			if lookedUp {
				t.Fatal("environment was read before argument validation")
			}
			if ran {
				t.Fatal("registry run was called for invalid arguments")
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestJournalPubMedRegistrySkipsCoverageAuditByDefault(t *testing.T) {
	t.Parallel()

	t.Run("flag and source configuration", func(t *testing.T) {
		defaultCommand, err := parseRegistryCommand(validJournalRegistryArgs())
		if err != nil {
			t.Fatalf("parseRegistryCommand(default) error = %v", err)
		}
		if defaultCommand.AuditPubMedCoverage {
			t.Fatal("default command unexpectedly enables PubMed coverage audit")
		}

		auditArgs := append(
			slices.Clone(validJournalRegistryArgs()),
			"--audit-pubmed-coverage",
		)
		auditCommand, err := parseRegistryCommand(auditArgs)
		if err != nil {
			t.Fatalf("parseRegistryCommand(audit) error = %v", err)
		}
		if !auditCommand.AuditPubMedCoverage {
			t.Fatal("explicit audit flag did not enable PubMed coverage audit")
		}

		var stdout, stderr bytes.Buffer
		var received registryCommand
		ncbiLookups := 0
		ran := false
		code := realMain(
			context.Background(),
			validJournalRegistryArgs(),
			&stdout,
			&stderr,
			func(key string) (string, bool) {
				if strings.HasPrefix(key, "NCBI_") {
					ncbiLookups++
				}
				return "", false
			},
			func(
				_ context.Context,
				command registryCommand,
			) (registryResult, error) {
				ran = true
				received = command
				return registryResult{
					Rows:          journalPubMedRegistryRows,
					PubMedUnknown: journalPubMedRegistryRows,
				}, nil
			},
		)
		if code != 0 {
			t.Fatalf(
				"realMain(default) code = %d, stderr = %s",
				code,
				stderr.String(),
			)
		}
		if ncbiLookups != 0 {
			t.Fatalf("default configuration read %d NCBI variables", ncbiLookups)
		}
		if !ran {
			t.Fatal("default configuration did not call the registry runner")
		}
		if received.AuditPubMedCoverage ||
			received.NCBITool != "" ||
			received.NCBIEmail != "" ||
			received.NCBIAPIKey != "" ||
			received.CrossrefContactEmail != "" {
			t.Fatalf("default source configuration = %#v", received)
		}
	})

	t.Run("runner writes complete unknown overlay", func(t *testing.T) {
		inputs := writeJournalRegistryInputs(t, []journalRegistryInputSpec{
			{domain: "medicine", rows: 1546},
			{domain: "biology", rows: 302},
			{domain: "computer_science", rows: 184},
		})
		directory := t.TempDir()
		outputPath := filepath.Join(directory, "registry.csv")
		reportPath := filepath.Join(directory, "registry.report.json")
		generatedAt := time.Date(
			2026,
			time.July,
			20,
			1,
			2,
			3,
			4,
			time.UTC,
		)
		dependencies := defaultRegistryDependencies()
		dependencies.now = func() time.Time { return generatedAt }
		dependencies.pubMedBaseURL = ""
		dependencies.newPubMedCounter = nil
		dependencies.fetchCrossref = func(
			context.Context,
			*http.Client,
			venueenrich.CrossrefCatalogConfig,
			httpclient.Dependencies,
		) (venueenrich.CrossrefCatalog, error) {
			return journalRegistryCatalog(generatedAt), nil
		}
		dependencies.matchCrossref = func(
			sources []venueenrich.SourceRow,
			_ venueenrich.CrossrefCatalog,
		) ([]venueenrich.RegistryRow, error) {
			rows := journalRegistryMatches(sources)
			rows[0].PubMedSupported = venueenrich.SupportStatusYes
			rows[0].PubMedRecordCount = 99
			rows[1].PubMedSupported = venueenrich.SupportStatusNo
			return rows, nil
		}

		result, err := (registryRunner{dependencies: dependencies}).run(
			context.Background(),
			registryCommand{
				Inputs:     inputs,
				CacheDir:   filepath.Join(directory, "cache"),
				OutputPath: outputPath,
				ReportPath: reportPath,
			},
		)
		if err != nil {
			t.Fatalf("registryRunner.run(default) error = %v", err)
		}
		if result.Rows != journalPubMedRegistryRows ||
			result.PubMedUnknown != journalPubMedRegistryRows {
			t.Fatalf("default registry result = %#v", result)
		}

		outputBytes, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("ReadFile(output) error = %v", err)
		}
		records, err := csv.NewReader(bytes.NewReader(outputBytes)).ReadAll()
		if err != nil {
			t.Fatalf("decode default CSV: %v", err)
		}
		if len(records) != journalPubMedRegistryRows+1 {
			t.Fatalf("default CSV records = %d", len(records))
		}
		for index, record := range records[1:] {
			if record[12] != "unknown" ||
				record[13] != "0" ||
				record[14] != "" {
				t.Fatalf(
					"default CSV row %d PubMed fields = %q/%q/%q",
					index+1,
					record[12],
					record[13],
					record[14],
				)
			}
		}

		reportBytes, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatalf("ReadFile(report) error = %v", err)
		}
		var report registryReport
		decodeSingleJSONValue(t, bytes.NewReader(reportBytes), &report)
		if report.Counts.Match.Resolved != 3 ||
			report.Counts.Probes.Eligible != 3 ||
			report.Counts.Probes.Attempted != 0 ||
			report.Counts.PubMed.Unknown != journalPubMedRegistryRows {
			t.Fatalf("default report counts = %#v", report.Counts)
		}
		if len(report.Probes) != journalPubMedRegistryRows {
			t.Fatalf("default report probes = %d", len(report.Probes))
		}
		for index, probe := range report.Probes {
			if probe.Attempted ||
				probe.CheckedAt != "" ||
				probe.ResponseSHA256 != "" ||
				probe.Error != "" ||
				probe.Status != venueenrich.SupportStatusUnknown ||
				probe.RecordCount != 0 {
				t.Fatalf("default probe %d = %#v", index+1, probe)
			}
		}
		if !slices.Equal(
			report.Probes[0].ISSNs,
			[]string{"0028-0836", "2049-3630"},
		) {
			t.Fatalf(
				"default resolved ISSNs = %v, want canonical exact set",
				report.Probes[0].ISSNs,
			)
		}
		if err := reconcileRegistryArtifacts(outputBytes, reportBytes); err != nil {
			t.Fatalf("reconcileRegistryArtifacts(default) error = %v", err)
		}
	})
}

func TestJournalPubMedRegistryAuditRequiresNCBIConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "missing tool",
			env:  map[string]string{},
			want: "NCBI_TOOL",
		},
		{
			name: "missing email",
			env: map[string]string{
				"NCBI_TOOL": "paper-hub-registry",
			},
			want: "NCBI_EMAIL",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			ran := false
			code := realMain(
				context.Background(),
				validJournalRegistryAuditArgs(),
				&stdout,
				&stderr,
				journalRegistryLookup(test.env),
				func(
					context.Context,
					registryCommand,
				) (registryResult, error) {
					ran = true
					return registryResult{}, nil
				},
			)
			if code != 1 {
				t.Fatalf("realMain(audit) code = %d, want 1", code)
			}
			if ran {
				t.Fatal("audit runner called without required NCBI configuration")
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestJournalPubMedRegistryValidatesDependenciesByCoverageMode(t *testing.T) {
	t.Parallel()

	dependencies := defaultRegistryDependencies()
	dependencies.pubMedBaseURL = ""
	dependencies.newPubMedCounter = nil

	if _, err := dependencies.validated(false); err != nil {
		t.Fatalf("validated(default) error = %v", err)
	}

	t.Run("audit requires base URL", func(t *testing.T) {
		auditDependencies := defaultRegistryDependencies()
		auditDependencies.pubMedBaseURL = ""
		if _, err := auditDependencies.validated(true); err == nil ||
			!strings.Contains(err.Error(), "base URL") {
			t.Fatalf(
				"validated(audit missing URL) error = %v",
				err,
			)
		}
	})

	t.Run("audit requires counter constructor", func(t *testing.T) {
		auditDependencies := defaultRegistryDependencies()
		auditDependencies.newPubMedCounter = nil
		if _, err := auditDependencies.validated(true); err == nil ||
			!strings.Contains(err.Error(), "counter") {
			t.Fatalf(
				"validated(audit missing counter) error = %v",
				err,
			)
		}
	})
}

func TestJournalPubMedRegistryCLIUsesOnlySourceConfigurationWithoutDatabase(
	t *testing.T,
) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	var received registryCommand
	code := realMain(
		context.Background(),
		validJournalRegistryAuditArgs(),
		&stdout,
		&stderr,
		journalRegistryLookup(map[string]string{
			"NCBI_TOOL":                "paper-hub-registry",
			"NCBI_EMAIL":               "research@example.test",
			"NCBI_API_KEY":             "ncbi-registry-secret",
			"DATABASE_URL":             "not-a-postgres-url-and-must-be-ignored",
			"UNRELATED_OPENAI_API_KEY": "must-not-be-read",
		}),
		func(_ context.Context, command registryCommand) (registryResult, error) {
			received = command
			return registryResult{
				Rows:          journalPubMedRegistryRows,
				PubMedUnknown: 7,
			}, nil
		},
	)
	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.NCBITool != "paper-hub-registry" ||
		received.NCBIEmail != "research@example.test" ||
		received.NCBIAPIKey != "ncbi-registry-secret" ||
		!received.AuditPubMedCoverage ||
		received.CrossrefContactEmail != "research@example.test" {
		t.Fatalf("source-only command configuration = %#v", received)
	}
	if !slices.Equal(
		received.Inputs,
		[]string{"medicine.csv", "biology.csv", "computer.csv"},
	) {
		t.Fatalf("Inputs = %v, want exact flag order", received.Inputs)
	}
	if received.CacheDir != "cache" ||
		received.OutputPath != "registry.csv" ||
		received.ReportPath != "registry.report.json" {
		t.Fatalf("explicit paths = %#v", received)
	}
	if !strings.Contains(stdout.String(), "2032") ||
		!strings.Contains(stdout.String(), "unknown=7") {
		t.Fatalf("stdout = %q, want complete summary including explicit unknown", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "ncbi-registry-secret") {
		t.Fatal("CLI output leaked NCBI_API_KEY")
	}
}

func TestJournalPubMedRegistryCLIValidatesBareEmailsAndRedactsAPIKey(
	t *testing.T,
) {
	t.Parallel()

	t.Run("rejects display name email", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		ran := false
		code := realMain(
			context.Background(),
			validJournalRegistryAuditArgs(),
			&stdout,
			&stderr,
			journalRegistryLookup(map[string]string{
				"NCBI_TOOL":  "paper-hub-registry",
				"NCBI_EMAIL": "Researcher <research@example.test>",
			}),
			func(context.Context, registryCommand) (registryResult, error) {
				ran = true
				return registryResult{}, nil
			},
		)
		if code != 1 {
			t.Fatalf("realMain() code = %d, want configuration exit 1", code)
		}
		if ran {
			t.Fatal("registry run called after invalid NCBI_EMAIL")
		}
		if !strings.Contains(stderr.String(), "NCBI_EMAIL") ||
			!strings.Contains(stderr.String(), "bare email") {
			t.Fatalf("stderr = %q, want bare NCBI_EMAIL error", stderr.String())
		}
	})

	t.Run("redacts run error", func(t *testing.T) {
		apiKey := "ncbi+/registry-secret"
		var stdout, stderr bytes.Buffer
		code := realMain(
			context.Background(),
			validJournalRegistryAuditArgs(),
			&stdout,
			&stderr,
			journalRegistryLookup(map[string]string{
				"NCBI_TOOL":    "paper-hub-registry",
				"NCBI_EMAIL":   "research@example.test",
				"NCBI_API_KEY": apiKey,
			}),
			func(context.Context, registryCommand) (registryResult, error) {
				return registryResult{}, fmt.Errorf(
					"request failed with raw %s and escaped %s",
					apiKey,
					url.QueryEscape(apiKey),
				)
			},
		)
		if code != 1 {
			t.Fatalf("realMain() code = %d, want run exit 1", code)
		}
		combined := stdout.String() + stderr.String()
		if strings.Contains(combined, apiKey) ||
			strings.Contains(combined, url.QueryEscape(apiKey)) {
			t.Fatalf("CLI output leaked NCBI_API_KEY: %s", combined)
		}
		if !strings.Contains(stderr.String(), "[REDACTED]") {
			t.Fatalf("stderr = %q, want explicit redaction marker", stderr.String())
		}
	})
}

func TestJournalPubMedRegistryGeneratesFixedCSVReportCountsAndHashes(t *testing.T) {
	t.Parallel()

	inputs := writeJournalRegistryInputs(t, []journalRegistryInputSpec{
		{domain: "medicine", rows: 1546},
		{domain: "biology", rows: 302},
		{domain: "computer_science", rows: 184},
	})
	directory := t.TempDir()
	outputPath := filepath.Join(directory, "journal-pubmed-registry.v1.csv")
	reportPath := filepath.Join(directory, "journal-pubmed-registry.v1.report.json")
	generatedAt := time.Date(
		2026,
		time.July,
		19,
		9,
		10,
		11,
		123456789,
		time.FixedZone("source", 8*60*60),
	)
	catalog := journalRegistryCatalog(generatedAt.Add(-time.Hour))
	counter := &journalRegistryCounter{
		steps: []journalRegistryCounterStep{
			{
				result: pubmed.CoverageResult{
					Count:          12,
					ResponseSHA256: journalRegistryHashOne,
				},
			},
			{
				result: pubmed.CoverageResult{
					Count:          0,
					ResponseSHA256: journalRegistryHashTwo,
				},
			},
			{err: errors.New("one journal probe unavailable")},
		},
	}

	dependencies := defaultRegistryDependencies()
	fetchCalls := 0
	matchCalls := 0
	clientConstructions := 0
	dependencies.now = func() time.Time { return generatedAt }
	dependencies.fetchCrossref = func(
		_ context.Context,
		_ *http.Client,
		config venueenrich.CrossrefCatalogConfig,
		_ httpclient.Dependencies,
	) (venueenrich.CrossrefCatalog, error) {
		fetchCalls++
		if config.CacheDir != filepath.Join(directory, "cache") ||
			config.ContactEmail != "crossref@example.test" {
			t.Fatalf("Crossref config = %#v", config)
		}
		return catalog, nil
	}
	dependencies.matchCrossref = func(
		sources []venueenrich.SourceRow,
		gotCatalog venueenrich.CrossrefCatalog,
	) ([]venueenrich.RegistryRow, error) {
		matchCalls++
		if gotCatalog.Manifest.CatalogSHA256 != catalog.Manifest.CatalogSHA256 {
			t.Fatalf("match catalog = %#v, want fetched catalog", gotCatalog)
		}
		return journalRegistryMatches(sources), nil
	}
	dependencies.newPubMedCounter = func(
		_ *http.Client,
		config pubmed.Config,
		_ httpclient.Dependencies,
	) (venueenrich.PubMedCoverageCounter, error) {
		clientConstructions++
		if config.Tool != "paper-hub-registry" ||
			config.Email != "research@example.test" ||
			config.APIKey != "ncbi-registry-secret" ||
			config.BaseURL != dependencies.pubMedBaseURL {
			t.Fatalf("PubMed config = %#v", config)
		}
		return counter, nil
	}

	result, err := (registryRunner{dependencies: dependencies}).run(
		context.Background(),
		registryCommand{
			Inputs:               inputs,
			CacheDir:             filepath.Join(directory, "cache"),
			OutputPath:           outputPath,
			ReportPath:           reportPath,
			AuditPubMedCoverage:  true,
			NCBITool:             "paper-hub-registry",
			NCBIEmail:            "research@example.test",
			NCBIAPIKey:           "ncbi-registry-secret",
			CrossrefContactEmail: "crossref@example.test",
		},
	)
	if err != nil {
		t.Fatalf("registryRunner.run() error = %v", err)
	}
	if result.Rows != journalPubMedRegistryRows ||
		result.PubMedUnknown != journalPubMedRegistryRows-2 {
		t.Fatalf("registry result = %#v", result)
	}
	if fetchCalls != 1 || matchCalls != 1 || clientConstructions != 1 {
		t.Fatalf(
			"pipeline calls = fetch %d, match %d, PubMed clients %d; want one each",
			fetchCalls,
			matchCalls,
			clientConstructions,
		)
	}
	if len(counter.calls) != 3 {
		t.Fatalf("shared counter calls = %d, want three sequential eligible rows", len(counter.calls))
	}
	if !slices.Equal(
		counter.calls[0].JournalISSNs,
		[]string{"0028-0836", "2049-3630"},
	) {
		t.Fatalf("first exact OR ISSNs = %v", counter.calls[0].JournalISSNs)
	}

	outputBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(output) error = %v", err)
	}
	records, err := csv.NewReader(bytes.NewReader(outputBytes)).ReadAll()
	if err != nil {
		t.Fatalf("read generated CSV: %v", err)
	}
	if len(records) != journalPubMedRegistryRows+1 {
		t.Fatalf("CSV records = %d, want header + 2032 rows", len(records))
	}
	if !slices.Equal(records[0], journalPubMedRegistryHeader) {
		t.Fatalf("CSV header = %v, want %v", records[0], journalPubMedRegistryHeader)
	}
	if records[1][0] != "medicine" ||
		records[1][1] != "1" ||
		records[1546][1] != "1546" ||
		records[1547][0] != "biology" ||
		records[1849][0] != "computer_science" ||
		records[2032][1] != "184" {
		t.Fatalf("CSV order boundaries were not preserved")
	}
	if records[1][9] != `["0028-0836","2049-3630"]` ||
		records[1][12] != "yes" ||
		records[1][13] != "12" ||
		records[1][14] != generatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("first CSV row = %#v", records[1])
	}
	if records[2][12] != "no" || records[2][13] != "0" {
		t.Fatalf("second CSV row = %#v", records[2])
	}
	if records[3][12] != "unknown" ||
		records[3][13] != "0" ||
		records[3][14] == "" {
		t.Fatalf("attempted-error CSV row = %#v", records[3])
	}
	if records[4][12] != "unknown" ||
		records[4][13] != "0" ||
		records[4][14] != "" {
		t.Fatalf("unattempted CSV row = %#v", records[4])
	}
	for rowIndex := 1; rowIndex < len(records); rowIndex++ {
		if records[rowIndex][6] != "" {
			t.Fatalf("CSV row %d inferred unsupported issn_l %q", rowIndex, records[rowIndex][6])
		}
		if records[rowIndex][16] != venueenrich.VerificationStatusPendingClarivate {
			t.Fatalf("CSV row %d verification_status = %q", rowIndex, records[rowIndex][16])
		}
	}

	reportBytes, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("ReadFile(report) error = %v", err)
	}
	var report registryReport
	decoder := json.NewDecoder(bytes.NewReader(reportBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("decode generated report: %v", err)
	}
	if report.SchemaVersion != journalPubMedRegistryReportSchemaVersion ||
		report.GeneratedAt != generatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("report identity = %#v", report)
	}
	if len(report.Inputs) != 3 {
		t.Fatalf("report inputs = %d, want 3", len(report.Inputs))
	}
	for index, input := range report.Inputs {
		payload, err := os.ReadFile(inputs[index])
		if err != nil {
			t.Fatalf("ReadFile(input %d): %v", index, err)
		}
		hash := sha256.Sum256(payload)
		if input.Ordinal != index+1 ||
			input.Path != inputs[index] ||
			input.Rows != []int{1546, 302, 184}[index] ||
			input.Bytes != int64(len(payload)) ||
			input.SHA256 != hex.EncodeToString(hash[:]) {
			t.Fatalf("report input %d = %#v", index, input)
		}
	}
	if report.CrossrefCatalog.ManifestVersion != venueenrich.CrossrefCatalogSchemaVersion ||
		report.CrossrefCatalog.Source != catalog.Manifest.SourceURL ||
		report.CrossrefCatalog.FetchedAt != catalog.Manifest.FetchedAt.UTC().Format(time.RFC3339Nano) ||
		report.CrossrefCatalog.Hash != catalog.Manifest.CatalogSHA256 ||
		report.CrossrefCatalog.Bytes != catalog.Manifest.CatalogBytes ||
		report.CrossrefCatalog.Records != catalog.Manifest.RecordCount ||
		!report.CrossrefCatalog.Replayed ||
		report.CrossrefCatalog.Resumed {
		t.Fatalf("report Crossref catalog = %#v", report.CrossrefCatalog)
	}
	if report.Counts.InputRows != journalPubMedRegistryRows ||
		report.Counts.OutputRows != journalPubMedRegistryRows ||
		report.Counts.Match.Resolved != 3 ||
		report.Counts.Match.Ambiguous+report.Counts.Match.Unresolved !=
			journalPubMedRegistryRows-3 ||
		report.Counts.Probes.Eligible != 3 ||
		report.Counts.Probes.Attempted != 3 ||
		report.Counts.PubMed.Yes != 1 ||
		report.Counts.PubMed.No != 1 ||
		report.Counts.PubMed.Unknown != journalPubMedRegistryRows-2 {
		t.Fatalf("report counts = %#v", report.Counts)
	}
	if len(report.ByDomain) != 3 ||
		report.ByDomain[0].Domain != "medicine" ||
		report.ByDomain[1].Domain != "biology" ||
		report.ByDomain[2].Domain != "computer_science" {
		t.Fatalf("report by_domain = %#v", report.ByDomain)
	}
	assertJournalRegistryCountsReconcile(t, report)
	if len(report.Probes) != journalPubMedRegistryRows ||
		report.Probes[0].ResponseSHA256 != journalRegistryHashOne ||
		report.Probes[1].ResponseSHA256 != journalRegistryHashTwo ||
		report.Probes[2].Error != "one journal probe unavailable" ||
		report.Probes[3].Attempted {
		t.Fatalf("report probes = first entries %#v", report.Probes[:4])
	}
	outputHash := sha256.Sum256(outputBytes)
	if report.OutputCSV.Path != outputPath ||
		report.OutputCSV.Rows != journalPubMedRegistryRows ||
		report.OutputCSV.Bytes != int64(len(outputBytes)) ||
		report.OutputCSV.SHA256 != hex.EncodeToString(outputHash[:]) {
		t.Fatalf("report output_csv = %#v", report.OutputCSV)
	}
	if bytes.Contains(reportBytes, []byte("report_sha256")) ||
		bytes.Contains(reportBytes, []byte("ncbi-registry-secret")) {
		t.Fatalf("report contains self hash or API key: %s", reportBytes)
	}
}

func TestJournalPubMedRegistryRequiresExactly2032LogicalRowsBeforeNetwork(
	t *testing.T,
) {
	t.Parallel()

	inputs := writeJournalRegistryInputs(t, []journalRegistryInputSpec{
		{domain: "medicine", rows: 1545},
		{domain: "biology", rows: 302},
		{domain: "computer_science", rows: 184},
	})
	directory := t.TempDir()
	dependencies := defaultRegistryDependencies()
	fetched := false
	dependencies.fetchCrossref = func(
		context.Context,
		*http.Client,
		venueenrich.CrossrefCatalogConfig,
		httpclient.Dependencies,
	) (venueenrich.CrossrefCatalog, error) {
		fetched = true
		return venueenrich.CrossrefCatalog{}, nil
	}

	_, err := (registryRunner{dependencies: dependencies}).run(
		context.Background(),
		registryCommand{
			Inputs:               inputs,
			CacheDir:             filepath.Join(directory, "cache"),
			OutputPath:           filepath.Join(directory, "registry.csv"),
			ReportPath:           filepath.Join(directory, "registry.json"),
			NCBITool:             "paper-hub-registry",
			NCBIEmail:            "research@example.test",
			CrossrefContactEmail: "research@example.test",
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "2031") ||
		!strings.Contains(err.Error(), "2032") {
		t.Fatalf("registryRunner.run() error = %v, want exact logical row count", err)
	}
	if fetched {
		t.Fatal("Crossref fetch started before 2032-row assertion")
	}
	for _, path := range []string{
		filepath.Join(directory, "registry.csv"),
		filepath.Join(directory, "registry.json"),
	} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unexpected final %q after count failure: %v", path, statErr)
		}
	}
}

func TestJournalPubMedRegistryRedactsAPIKeyFromProbeReport(t *testing.T) {
	t.Parallel()

	apiKey := "ncbi-live-registry-secret"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Query().Get("api_key") != apiKey {
			t.Errorf("api_key was not supplied to PubMed")
		}
		http.Error(writer, "upstream echoed "+apiKey, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	inputs := writeJournalRegistryInputs(t, []journalRegistryInputSpec{
		{domain: "medicine", rows: 1546},
		{domain: "biology", rows: 302},
		{domain: "computer_science", rows: 184},
	})
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "registry.report.json")
	dependencies := defaultRegistryDependencies()
	dependencies.pubMedBaseURL = server.URL
	dependencies.now = func() time.Time {
		return time.Date(2026, time.July, 19, 1, 2, 3, 0, time.UTC)
	}
	dependencies.httpDependencies = httpclient.Dependencies{
		Now: dependencies.now,
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
	}
	dependencies.fetchCrossref = func(
		context.Context,
		*http.Client,
		venueenrich.CrossrefCatalogConfig,
		httpclient.Dependencies,
	) (venueenrich.CrossrefCatalog, error) {
		return journalRegistryCatalog(dependencies.now()), nil
	}
	dependencies.matchCrossref = func(
		sources []venueenrich.SourceRow,
		_ venueenrich.CrossrefCatalog,
	) ([]venueenrich.RegistryRow, error) {
		rows := journalRegistryMatches(sources)
		for index := 1; index < len(rows); index++ {
			rows[index] = journalRegistryUnresolvedRow(
				sources[index],
				venueenrich.MatchStatusUnresolved,
			)
		}
		return rows, nil
	}

	result, err := (registryRunner{dependencies: dependencies}).run(
		context.Background(),
		registryCommand{
			Inputs:               inputs,
			CacheDir:             filepath.Join(directory, "cache"),
			OutputPath:           filepath.Join(directory, "registry.csv"),
			ReportPath:           reportPath,
			AuditPubMedCoverage:  true,
			NCBITool:             "paper-hub-registry",
			NCBIEmail:            "research@example.test",
			NCBIAPIKey:           apiKey,
			CrossrefContactEmail: "research@example.test",
		},
	)
	if err != nil {
		t.Fatalf("registryRunner.run() error = %v", err)
	}
	if result.PubMedUnknown != journalPubMedRegistryRows {
		t.Fatalf("result = %#v, want all explicit unknown after bounded failure", result)
	}
	if requests != pubMedRegistryMaxRetries+1 {
		t.Fatalf(
			"PubMed requests = %d, want initial plus %d bounded retries",
			requests,
			pubMedRegistryMaxRetries,
		)
	}
	reportBytes, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("ReadFile(report) error = %v", err)
	}
	if bytes.Contains(reportBytes, []byte(apiKey)) {
		t.Fatalf("report leaked NCBI_API_KEY: %s", reportBytes)
	}
	if !bytes.Contains(reportBytes, []byte("[REDACTED]")) {
		t.Fatalf("report lacks explicit redaction marker: %s", reportBytes)
	}
}

func TestJournalPubMedRegistryOutputFailureCleansTempsAndNewFinals(t *testing.T) {
	t.Parallel()

	inputs := writeJournalRegistryInputs(t, []journalRegistryInputSpec{
		{domain: "medicine", rows: 1546},
		{domain: "biology", rows: 302},
		{domain: "computer_science", rows: 184},
	})
	directory := t.TempDir()
	outputPath := filepath.Join(directory, "registry.csv")
	reportPath := filepath.Join(directory, "registry.report.json")
	dependencies := journalRegistryFixtureDependencies(t)
	dependencies.fileOps.before = func(stage string) error {
		if stage == registryPublishReport {
			return errors.New("injected report publish failure")
		}
		return nil
	}

	_, err := (registryRunner{dependencies: dependencies}).run(
		context.Background(),
		registryCommand{
			Inputs:               inputs,
			CacheDir:             filepath.Join(directory, "cache"),
			OutputPath:           outputPath,
			ReportPath:           reportPath,
			AuditPubMedCoverage:  true,
			NCBITool:             "paper-hub-registry",
			NCBIEmail:            "research@example.test",
			CrossrefContactEmail: "research@example.test",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "injected report publish failure") {
		t.Fatalf("registryRunner.run() error = %v, want injected publish failure", err)
	}
	for _, path := range []string{outputPath, reportPath} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("new final %q remained after controlled failure: %v", path, statErr)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file remained after failure: %s", entry.Name())
		}
	}
}

func TestJournalPubMedRegistryConcurrentFinalCreationNeverOverwrites(
	t *testing.T,
) {
	t.Parallel()

	t.Run("output final", func(t *testing.T) {
		directory := t.TempDir()
		outputPath := filepath.Join(directory, "registry.csv")
		reportPath := filepath.Join(directory, "registry.report.json")
		ops := defaultRegistryFileOps()
		ops.before = func(stage string) error {
			if stage != registryPublishOutput {
				return nil
			}
			return os.WriteFile(
				outputPath,
				[]byte("concurrent output"),
				0o600,
			)
		}

		err := publishRegistryArtifacts(
			outputPath,
			[]byte("csv"),
			reportPath,
			[]byte("report"),
			ops,
		)
		if err == nil {
			t.Fatal("publishRegistryArtifacts() error = nil, want no-replace failure")
		}
		if got, readErr := os.ReadFile(outputPath); readErr != nil {
			t.Fatalf("ReadFile(output) error = %v", readErr)
		} else if string(got) != "concurrent output" {
			t.Fatalf("output contents = %q, want concurrent output preserved", got)
		}
		if _, statErr := os.Lstat(reportPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("report final unexpectedly published: %v", statErr)
		}
		assertRegistryTempsAbsent(t, directory)
	})

	t.Run("report final rolls back output", func(t *testing.T) {
		directory := t.TempDir()
		outputPath := filepath.Join(directory, "registry.csv")
		reportPath := filepath.Join(directory, "registry.report.json")
		ops := defaultRegistryFileOps()
		ops.before = func(stage string) error {
			if stage != registryPublishReport {
				return nil
			}
			return os.WriteFile(
				reportPath,
				[]byte("concurrent report"),
				0o600,
			)
		}

		err := publishRegistryArtifacts(
			outputPath,
			[]byte("csv"),
			reportPath,
			[]byte("report"),
			ops,
		)
		if err == nil {
			t.Fatal("publishRegistryArtifacts() error = nil, want no-replace failure")
		}
		if _, statErr := os.Lstat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("output final was not rolled back: %v", statErr)
		}
		if got, readErr := os.ReadFile(reportPath); readErr != nil {
			t.Fatalf("ReadFile(report) error = %v", readErr)
		} else if string(got) != "concurrent report" {
			t.Fatalf("report contents = %q, want concurrent report preserved", got)
		}
		assertRegistryTempsAbsent(t, directory)
	})
}

func TestJournalPubMedRegistryRollbackPreservesReplacedOutputInode(
	t *testing.T,
) {
	t.Parallel()

	directory := t.TempDir()
	outputPath := filepath.Join(directory, "registry.csv")
	reportPath := filepath.Join(directory, "registry.report.json")
	var publishedInfo os.FileInfo
	var replacementInfo os.FileInfo
	ops := defaultRegistryFileOps()
	ops.before = func(stage string) error {
		if stage != registryPublishReport {
			return nil
		}
		var err error
		publishedInfo, err = os.Stat(outputPath)
		if err != nil {
			return fmt.Errorf("stat published output before replacement: %w", err)
		}
		if err := os.Remove(outputPath); err != nil {
			return fmt.Errorf("remove published output for replacement: %w", err)
		}
		if err := os.WriteFile(
			outputPath,
			[]byte("external replacement"),
			0o600,
		); err != nil {
			return fmt.Errorf("write external output replacement: %w", err)
		}
		replacementInfo, err = os.Stat(outputPath)
		if err != nil {
			return fmt.Errorf("stat external output replacement: %w", err)
		}
		return nil
	}
	originalLink := ops.link
	ops.link = func(tempPath, finalPath string) error {
		if finalPath == reportPath {
			return errors.New("injected report publish failure")
		}
		return originalLink(tempPath, finalPath)
	}

	err := publishRegistryArtifacts(
		outputPath,
		[]byte("csv"),
		reportPath,
		[]byte("report"),
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), "injected report publish failure") {
		t.Fatalf("publishRegistryArtifacts() error = %v, want injected failure", err)
	}
	if !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("publishRegistryArtifacts() error = %v, want rollback conflict", err)
	}
	if publishedInfo == nil || replacementInfo == nil ||
		os.SameFile(publishedInfo, replacementInfo) {
		t.Fatalf("output replacement did not create a new inode")
	}
	if got, readErr := os.ReadFile(outputPath); readErr != nil {
		t.Fatalf("ReadFile(output) error = %v", readErr)
	} else if string(got) != "external replacement" {
		t.Fatalf("output contents = %q, want external replacement preserved", got)
	}
	if _, statErr := os.Lstat(reportPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("report final unexpectedly remained: %v", statErr)
	}
	assertRegistryTempsAbsent(t, directory)
}

func TestJournalPubMedRegistryRejectsFinalReplacedInsideLink(
	t *testing.T,
) {
	t.Parallel()

	directory := t.TempDir()
	outputPath := filepath.Join(directory, "registry.csv")
	reportPath := filepath.Join(directory, "registry.report.json")
	externalPayload := []byte("external during link")
	originalLink := defaultRegistryFileOps().link
	ops := defaultRegistryFileOps()
	ops.link = func(tempPath, finalPath string) error {
		if finalPath != outputPath {
			return errors.New("report link unexpectedly attempted")
		}
		if err := originalLink(tempPath, finalPath); err != nil {
			return err
		}
		if err := os.Remove(finalPath); err != nil {
			return fmt.Errorf("replace linked output: remove: %w", err)
		}
		if err := os.WriteFile(finalPath, externalPayload, 0o600); err != nil {
			return fmt.Errorf("replace linked output: write: %w", err)
		}
		return nil
	}

	err := publishRegistryArtifacts(
		outputPath,
		[]byte("csv"),
		reportPath,
		[]byte("report"),
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), "output") ||
		!strings.Contains(err.Error(), "identity") {
		t.Fatalf(
			"publishRegistryArtifacts() error = %v, want output identity rejection",
			err,
		)
	}
	if strings.Contains(err.Error(), "report link unexpectedly attempted") {
		t.Fatalf("publishRegistryArtifacts() reached report link: %v", err)
	}
	if got, readErr := os.ReadFile(outputPath); readErr != nil {
		t.Fatalf("ReadFile(output) error = %v", readErr)
	} else if string(got) != string(externalPayload) {
		t.Fatalf("output contents = %q, want external payload preserved", got)
	}
	if _, statErr := os.Lstat(reportPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("report final unexpectedly remained: %v", statErr)
	}
	assertRegistryTempsAbsent(t, directory)
}

func TestJournalPubMedRegistryReportsTemporaryCleanupFailure(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	outputPath := filepath.Join(directory, "registry.csv")
	reportPath := filepath.Join(
		directory,
		"missing",
		"registry.report.json",
	)
	ops := defaultRegistryFileOps()
	ops.remove = func(string) error {
		return errors.New("injected temporary cleanup failure")
	}

	err := publishRegistryArtifacts(
		outputPath,
		[]byte("csv"),
		reportPath,
		[]byte("report"),
		ops,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "injected temporary cleanup failure") {
		t.Fatalf(
			"publishRegistryArtifacts() error = %v, want cleanup failure",
			err,
		)
	}
}

func TestJournalPubMedRegistryRejectsEquivalentFinalPathsBeforePublishing(
	t *testing.T,
) {
	t.Parallel()

	t.Run("absolute clean aliases", func(t *testing.T) {
		directory := t.TempDir()
		outputPath := filepath.Join(directory, "registry")
		reportPath := directory + string(filepath.Separator) + "." +
			string(filepath.Separator) + "registry"

		err := publishRegistryArtifacts(
			outputPath,
			[]byte("csv"),
			reportPath,
			[]byte("report"),
			defaultRegistryFileOps(),
		)
		if err == nil || !strings.Contains(err.Error(), "same final") {
			t.Fatalf("publishRegistryArtifacts() error = %v, want same-final rejection", err)
		}
		assertRegistryFinalsAbsent(t, outputPath, reportPath)
		assertRegistryTempsAbsent(t, directory)
	})

	t.Run("symlinked parent aliases", func(t *testing.T) {
		directory := t.TempDir()
		realDirectory := filepath.Join(directory, "real")
		aliasDirectory := filepath.Join(directory, "alias")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatalf("Mkdir(real) error = %v", err)
		}
		if err := os.Symlink(realDirectory, aliasDirectory); err != nil {
			t.Fatalf("Symlink(alias) error = %v", err)
		}
		outputPath := filepath.Join(realDirectory, "registry")
		reportPath := filepath.Join(aliasDirectory, "registry")

		err := publishRegistryArtifacts(
			outputPath,
			[]byte("csv"),
			reportPath,
			[]byte("report"),
			defaultRegistryFileOps(),
		)
		if err == nil || !strings.Contains(err.Error(), "same final") {
			t.Fatalf("publishRegistryArtifacts() error = %v, want symlink-alias rejection", err)
		}
		assertRegistryFinalsAbsent(t, outputPath, reportPath)
		assertRegistryTempsAbsent(t, realDirectory)
	})

	t.Run("missing parent through symlink aliases", func(t *testing.T) {
		directory := t.TempDir()
		realDirectory := filepath.Join(directory, "real")
		aliasDirectory := filepath.Join(directory, "alias")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatalf("Mkdir(real) error = %v", err)
		}
		if err := os.Symlink(realDirectory, aliasDirectory); err != nil {
			t.Fatalf("Symlink(alias) error = %v", err)
		}
		outputPath := filepath.Join(realDirectory, "missing", "registry")
		reportPath := filepath.Join(aliasDirectory, "missing", "registry")

		err := publishRegistryArtifacts(
			outputPath,
			[]byte("csv"),
			reportPath,
			[]byte("report"),
			defaultRegistryFileOps(),
		)
		if err == nil || !strings.Contains(err.Error(), "same final") {
			t.Fatalf("publishRegistryArtifacts() error = %v, want missing-parent alias rejection", err)
		}
		assertRegistryFinalsAbsent(t, outputPath, reportPath)
		assertRegistryTempsAbsent(t, realDirectory)
	})
}

func TestJournalPubMedRegistryCaseOnlyMissingFinalAliasesFollowFilesystem(
	t *testing.T,
) {
	t.Parallel()

	directory := t.TempDir()
	caseInsensitive := journalRegistryFilesystemIsCaseInsensitive(
		t,
		directory,
	)
	outputPath := filepath.Join(directory, "Registry")
	reportPath := filepath.Join(directory, "registry")

	err := publishRegistryArtifacts(
		outputPath,
		[]byte("csv"),
		reportPath,
		[]byte("report"),
		defaultRegistryFileOps(),
	)
	if caseInsensitive {
		if err == nil || !strings.Contains(err.Error(), "same final") {
			t.Fatalf(
				"publishRegistryArtifacts() error = %v, want case-alias rejection",
				err,
			)
		}
		assertRegistryFinalsAbsent(t, outputPath, reportPath)
		assertRegistryTempsAbsent(t, directory)
		assertRegistryCaseProbesAbsent(t, directory)
		return
	}

	if err != nil {
		t.Fatalf(
			"publishRegistryArtifacts() error = %v on case-sensitive filesystem",
			err,
		)
	}
	if got, readErr := os.ReadFile(outputPath); readErr != nil {
		t.Fatalf("ReadFile(output) error = %v", readErr)
	} else if string(got) != "csv" {
		t.Fatalf("output contents = %q, want csv", got)
	}
	if got, readErr := os.ReadFile(reportPath); readErr != nil {
		t.Fatalf("ReadFile(report) error = %v", readErr)
	} else if string(got) != "report" {
		t.Fatalf("report contents = %q, want report", got)
	}
	assertRegistryTempsAbsent(t, directory)
	assertRegistryCaseProbesAbsent(t, directory)
}

func TestJournalPubMedRegistryCaseOnlyParentAndFinalAliasesFollowFilesystem(
	t *testing.T,
) {
	t.Parallel()

	directory := t.TempDir()
	caseInsensitive := journalRegistryFilesystemIsCaseInsensitive(
		t,
		directory,
	)
	upperParent := filepath.Join(directory, "Parent")
	lowerParent := filepath.Join(directory, "parent")
	if err := os.Mkdir(upperParent, 0o700); err != nil {
		t.Fatalf("Mkdir(upper parent) error = %v", err)
	}
	if !caseInsensitive {
		if err := os.Mkdir(lowerParent, 0o700); err != nil {
			t.Fatalf("Mkdir(lower parent) error = %v", err)
		}
	}
	outputPath := filepath.Join(upperParent, "Registry")
	reportPath := filepath.Join(lowerParent, "registry")

	err := publishRegistryArtifacts(
		outputPath,
		[]byte("csv"),
		reportPath,
		[]byte("report"),
		defaultRegistryFileOps(),
	)
	if caseInsensitive {
		if err == nil || !strings.Contains(err.Error(), "same final") {
			t.Fatalf(
				"publishRegistryArtifacts() error = %v, want full-path case-alias rejection",
				err,
			)
		}
		assertRegistryFinalsAbsent(t, outputPath, reportPath)
		assertRegistryTempsAbsent(t, upperParent)
		assertRegistryCaseProbesAbsent(t, directory)
		return
	}

	if err != nil {
		t.Fatalf(
			"publishRegistryArtifacts() error = %v on case-sensitive filesystem",
			err,
		)
	}
	if got, readErr := os.ReadFile(outputPath); readErr != nil {
		t.Fatalf("ReadFile(output) error = %v", readErr)
	} else if string(got) != "csv" {
		t.Fatalf("output contents = %q, want csv", got)
	}
	if got, readErr := os.ReadFile(reportPath); readErr != nil {
		t.Fatalf("ReadFile(report) error = %v", readErr)
	} else if string(got) != "report" {
		t.Fatalf("report contents = %q, want report", got)
	}
	assertRegistryTempsAbsent(t, upperParent)
	assertRegistryTempsAbsent(t, lowerParent)
	assertRegistryCaseProbesAbsent(t, directory)
}

func TestJournalPubMedRegistryRejectsExistingSymlinkAndHardlinkFinalAliases(
	t *testing.T,
) {
	t.Parallel()

	t.Run("symlink final", func(t *testing.T) {
		directory := t.TempDir()
		targetPath := filepath.Join(directory, "target")
		outputPath := filepath.Join(directory, "output")
		if err := os.WriteFile(targetPath, []byte("existing"), 0o600); err != nil {
			t.Fatalf("WriteFile(target) error = %v", err)
		}
		if err := os.Symlink(targetPath, outputPath); err != nil {
			t.Fatalf("Symlink(output) error = %v", err)
		}

		err := publishRegistryArtifacts(
			outputPath,
			[]byte("replacement"),
			targetPath,
			[]byte("report"),
			defaultRegistryFileOps(),
		)
		if err == nil || !strings.Contains(err.Error(), "same final") {
			t.Fatalf("publishRegistryArtifacts() error = %v, want symlink conflict", err)
		}
		if got, readErr := os.ReadFile(targetPath); readErr != nil {
			t.Fatalf("ReadFile(target) error = %v", readErr)
		} else if string(got) != "existing" {
			t.Fatalf("target contents = %q, want unchanged existing content", got)
		}
	})

	t.Run("hardlink finals", func(t *testing.T) {
		directory := t.TempDir()
		outputPath := filepath.Join(directory, "output")
		reportPath := filepath.Join(directory, "report")
		if err := os.WriteFile(outputPath, []byte("existing"), 0o600); err != nil {
			t.Fatalf("WriteFile(output) error = %v", err)
		}
		if err := os.Link(outputPath, reportPath); err != nil {
			t.Fatalf("Link(report) error = %v", err)
		}

		err := publishRegistryArtifacts(
			outputPath,
			[]byte("replacement"),
			reportPath,
			[]byte("report"),
			defaultRegistryFileOps(),
		)
		if err == nil || !strings.Contains(err.Error(), "same final") {
			t.Fatalf("publishRegistryArtifacts() error = %v, want hardlink conflict", err)
		}
		if got, readErr := os.ReadFile(outputPath); readErr != nil {
			t.Fatalf("ReadFile(output) error = %v", readErr)
		} else if string(got) != "existing" {
			t.Fatalf("output contents = %q, want unchanged existing content", got)
		}
	})
}

func TestJournalPubMedRegistryReportValidationRequires2032RowsAndValidProbes(
	t *testing.T,
) {
	t.Parallel()

	t.Run("fixed row count", func(t *testing.T) {
		report := validJournalRegistryReportForValidation()
		report.Inputs[0].Rows--
		report.Counts.InputRows--
		report.Counts.OutputRows--
		report.Counts.Match.Unresolved--
		report.Counts.PubMed.Unknown--
		report.ByDomain[0].Counts = report.Counts
		report.Probes = report.Probes[:len(report.Probes)-1]
		report.OutputCSV.Rows--

		err := validateRegistryReport(report)
		if err == nil || !strings.Contains(err.Error(), "require exactly 2032") {
			t.Fatalf("validateRegistryReport() error = %v, want fixed 2032-row invariant", err)
		}
	})

	t.Run("probe reconciliation", func(t *testing.T) {
		report := validJournalRegistryReportForValidation()
		report.Probes[0].Attempted = false
		report.Probes[0].Status = venueenrich.SupportStatusYes
		report.Probes[0].RecordCount = 1

		err := validateRegistryReport(report)
		if err == nil || !strings.Contains(err.Error(), "probe") {
			t.Fatalf("validateRegistryReport() error = %v, want invalid probe receipt", err)
		}
	})
}

func TestJournalPubMedRegistryAcceptsCompleteSkippedCoverageOverlay(
	t *testing.T,
) {
	t.Parallel()

	report := validJournalRegistryReportForValidation()
	report.Counts.Match.Resolved = 1
	report.Counts.Match.Unresolved--
	report.Counts.Probes.Eligible = 1
	report.Counts.Probes.Attempted = 1
	report.Counts.PubMed.Unknown--
	report.Counts.PubMed.Yes = 1
	report.ByDomain[0].Counts = report.Counts
	report.Probes[0] = venueenrich.PubMedProbeReceipt{
		Domain:         "all",
		SourceOrder:    1,
		ISSNs:          []string{"0028-0836"},
		Attempted:      true,
		CheckedAt:      "2026-07-19T01:02:03Z",
		Status:         venueenrich.SupportStatusYes,
		RecordCount:    1,
		ResponseSHA256: journalRegistryHashOne,
	}

	// Overlay the report as if the resolved probe was silently left unprobed.
	report.Probes[0].Attempted = false
	report.Probes[0].CheckedAt = ""
	report.Probes[0].Status = venueenrich.SupportStatusUnknown
	report.Probes[0].RecordCount = 0
	report.Probes[0].ResponseSHA256 = ""
	report.Counts.Probes.Attempted = 0
	report.Counts.PubMed.Yes = 0
	report.Counts.PubMed.Unknown++
	report.ByDomain[0].Counts = report.Counts

	if err := validateRegistryReport(report); err != nil {
		t.Fatalf(
			"validateRegistryReport(complete skip) error = %v",
			err,
		)
	}
}

func TestJournalPubMedRegistryRejectsPartialCoverageAudit(t *testing.T) {
	t.Parallel()

	t.Run("report", func(t *testing.T) {
		report := validJournalRegistryReportForValidation()
		report.Counts.Match.Resolved = 2
		report.Counts.Match.Unresolved -= 2
		report.Counts.Probes.Eligible = 2
		report.Counts.Probes.Attempted = 1
		report.Counts.PubMed.Unknown--
		report.Counts.PubMed.Yes = 1
		report.ByDomain[0].Counts = report.Counts
		report.Probes[0] = venueenrich.PubMedProbeReceipt{
			Domain:         "all",
			SourceOrder:    1,
			ISSNs:          []string{"0028-0836"},
			Attempted:      true,
			CheckedAt:      "2026-07-19T01:02:03Z",
			Status:         venueenrich.SupportStatusYes,
			RecordCount:    1,
			ResponseSHA256: journalRegistryHashOne,
		}

		err := validateRegistryReport(report)
		if err == nil || !strings.Contains(err.Error(), "probe counts") {
			t.Fatalf(
				"validateRegistryReport(partial audit) error = %v",
				err,
			)
		}
	})

	t.Run("by domain", func(t *testing.T) {
		counts := registryCounts{
			InputRows:  journalPubMedRegistryRows,
			OutputRows: journalPubMedRegistryRows,
			Match: registryMatchCounts{
				Resolved:   2,
				Unresolved: journalPubMedRegistryRows - 2,
			},
			Probes: registryProbeCounts{
				Eligible:  2,
				Attempted: 2,
			},
			PubMed: registrySupportCounts{
				Yes:     2,
				Unknown: journalPubMedRegistryRows - 2,
			},
		}
		byDomain := []registryDomainCounts{
			{
				Domain: "medicine",
				Counts: registryCounts{
					InputRows:  1,
					OutputRows: 1,
					Match: registryMatchCounts{
						Resolved: 1,
					},
					Probes: registryProbeCounts{
						Eligible: 1,
					},
					PubMed: registrySupportCounts{
						Unknown: 1,
					},
				},
			},
			{
				Domain: "biology",
				Counts: registryCounts{
					InputRows:  journalPubMedRegistryRows - 1,
					OutputRows: journalPubMedRegistryRows - 1,
					Match: registryMatchCounts{
						Resolved:   1,
						Unresolved: journalPubMedRegistryRows - 2,
					},
					Probes: registryProbeCounts{
						Eligible:  1,
						Attempted: 2,
					},
					PubMed: registrySupportCounts{
						Yes:     2,
						Unknown: journalPubMedRegistryRows - 3,
					},
				},
			},
		}

		err := validateRegistryCounts(counts, byDomain)
		if err == nil || !strings.Contains(err.Error(), "by_domain") {
			t.Fatalf(
				"validateRegistryCounts(partial domain audit) error = %v",
				err,
			)
		}
	})
}

func TestJournalPubMedRegistryAcceptsResolvedRowWithStrictSkippedReceipt(
	t *testing.T,
) {
	t.Parallel()

	source := venueenrich.SourceRow{
		Domain:            "medicine",
		SourceOrder:       1,
		SourceJournalName: "Resolved Journal",
		SourceURL:         "https://example.test/medicine/1",
	}
	row := journalRegistryResolvedRow(
		source,
		[]string{"0028-0836", "2049-3630"},
	)
	probe := venueenrich.PubMedProbeReceipt{
		Domain:      row.Domain,
		SourceOrder: row.SourceOrder,
		ISSNs:       slices.Clone(row.AllISSNs),
		Status:      venueenrich.SupportStatusUnknown,
	}

	if err := validateProbeForRow(row, probe); err != nil {
		t.Fatalf("validateProbeForRow(strict skip) error = %v", err)
	}
}

func TestJournalPubMedRegistryInputRowsAndReceiptUseOneSnapshot(t *testing.T) {
	t.Parallel()

	snapshotA := journalRegistrySourcePayload(
		t,
		"medicine",
		1,
		"Snapshot A Journal",
	)
	snapshotB := journalRegistrySourcePayload(
		t,
		"medicine",
		1,
		"Snapshot B Journal",
	)
	biology := journalRegistrySourcePayload(
		t,
		"biology",
		1,
		"Biology Journal",
	)
	computer := journalRegistrySourcePayload(
		t,
		"computer_science",
		1,
		"Computer Journal",
	)
	paths := []string{"changing.csv", "biology.csv", "computer.csv"}
	snapshots := map[string][][]byte{
		paths[0]: {snapshotA, snapshotB, snapshotA},
		paths[1]: {biology, biology, biology},
		paths[2]: {computer, computer, computer},
	}
	readCounts := make(map[string]int)
	readFile := func(path string) ([]byte, error) {
		index := readCounts[path]
		readCounts[path]++
		pathSnapshots := snapshots[path]
		if index >= len(pathSnapshots) {
			return nil, fmt.Errorf("unexpected read %d for %q", index+1, path)
		}
		return slices.Clone(pathSnapshots[index]), nil
	}

	rows, receipts, err := loadRegistryInputs(paths, readFile)
	if err != nil {
		t.Fatalf("loadRegistryInputs() error = %v", err)
	}
	if len(rows) != 3 || len(receipts) != 3 {
		t.Fatalf(
			"loadRegistryInputs() rows=%d receipts=%d, want 3 each",
			len(rows),
			len(receipts),
		)
	}
	if rows[0].SourceJournalName != "Snapshot A Journal" {
		t.Fatalf(
			"rows[0].SourceJournalName = %q, want receipt snapshot A",
			rows[0].SourceJournalName,
		)
	}
	if receipts[0].Bytes != int64(len(snapshotA)) ||
		receipts[0].SHA256 != sha256Hex(snapshotA) ||
		receipts[0].Rows != 1 {
		t.Fatalf("receipt[0] = %#v, want snapshot A metadata", receipts[0])
	}
	for _, path := range paths {
		if readCounts[path] != 1 {
			t.Fatalf("read count for %q = %d, want exactly 1", path, readCounts[path])
		}
	}
}

type journalRegistryInputSpec struct {
	domain string
	rows   int
}

func writeJournalRegistryInputs(
	t *testing.T,
	specs []journalRegistryInputSpec,
) []string {
	t.Helper()

	directory := t.TempDir()
	paths := make([]string, 0, len(specs))
	for ordinal, spec := range specs {
		path := filepath.Join(directory, fmt.Sprintf("%d-%s.csv", ordinal+1, spec.domain))
		file, err := os.Create(path)
		if err != nil {
			t.Fatalf("Create(%q) error = %v", path, err)
		}
		writer := csv.NewWriter(file)
		if err := writer.Write([]string{
			"domain",
			"source_order",
			"journal_name",
			"impact_factor",
			"jcr_value",
			"cass_value",
			"source_url",
			"verification_status",
		}); err != nil {
			_ = file.Close()
			t.Fatalf("Write(header) error = %v", err)
		}
		for sourceOrder := 1; sourceOrder <= spec.rows; sourceOrder++ {
			if err := writer.Write([]string{
				spec.domain,
				strconv.Itoa(sourceOrder),
				fmt.Sprintf("%s Journal %04d", spec.domain, sourceOrder),
				"12.3",
				"Category-1区",
				"1区",
				fmt.Sprintf(
					"https://example.test/%s/%d",
					spec.domain,
					sourceOrder,
				),
				venueenrich.VerificationStatusPendingClarivate,
			}); err != nil {
				_ = file.Close()
				t.Fatalf("Write(row) error = %v", err)
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			_ = file.Close()
			t.Fatalf("csv.Writer error = %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("Close(%q) error = %v", path, err)
		}
		paths = append(paths, path)
	}
	return paths
}

func journalRegistrySourcePayload(
	t *testing.T,
	domain string,
	sourceOrder int,
	journalName string,
) []byte {
	t.Helper()

	var payload bytes.Buffer
	writer := csv.NewWriter(&payload)
	if err := writer.Write([]string{
		"domain",
		"source_order",
		"journal_name",
		"impact_factor",
		"jcr_value",
		"cass_value",
		"source_url",
		"verification_status",
	}); err != nil {
		t.Fatalf("Write(source payload header) error = %v", err)
	}
	if err := writer.Write([]string{
		domain,
		strconv.Itoa(sourceOrder),
		journalName,
		"12.3",
		"Category-1区",
		"1区",
		fmt.Sprintf(
			"https://example.test/%s/%d",
			domain,
			sourceOrder,
		),
		venueenrich.VerificationStatusPendingClarivate,
	}); err != nil {
		t.Fatalf("Write(source payload row) error = %v", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatalf("source payload csv.Writer error = %v", err)
	}
	return payload.Bytes()
}

func journalRegistryCatalog(fetchedAt time.Time) venueenrich.CrossrefCatalog {
	return venueenrich.CrossrefCatalog{
		Manifest: venueenrich.CrossrefCatalogManifest{
			SchemaVersion: venueenrich.CrossrefCatalogSchemaVersion,
			SourceURL:     "https://api.crossref.test/journals",
			FetchedAt:     fetchedAt.UTC(),
			CheckpointAt:  fetchedAt.UTC(),
			Rows:          venueenrich.CrossrefCatalogRows,
			CatalogSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			CatalogBytes:  987654,
			RecordCount:   12345,
			Complete:      true,
		},
		CatalogPath:  "/cache/crossref-journals.v1.jsonl",
		ManifestPath: "/cache/crossref-journals.v1.manifest.json",
		Replayed:     true,
		Resumed:      false,
	}
}

func journalRegistryMatches(
	sources []venueenrich.SourceRow,
) []venueenrich.RegistryRow {
	rows := make([]venueenrich.RegistryRow, len(sources))
	for index, source := range sources {
		switch index {
		case 0:
			rows[index] = journalRegistryResolvedRow(
				source,
				[]string{"2049-3630", "0028-0836", "2049-3630"},
			)
		case 1:
			rows[index] = journalRegistryResolvedRow(
				source,
				[]string{"3141-592X"},
			)
		case 2:
			rows[index] = journalRegistryResolvedRow(
				source,
				[]string{"1234-5679"},
			)
		default:
			status := venueenrich.MatchStatusAmbiguous
			if index%2 == 0 {
				status = venueenrich.MatchStatusUnresolved
			}
			rows[index] = journalRegistryUnresolvedRow(source, status)
		}
	}
	return rows
}

func journalRegistryResolvedRow(
	source venueenrich.SourceRow,
	issns []string,
) venueenrich.RegistryRow {
	return venueenrich.RegistryRow{
		SourceRow:         source,
		PrintISSN:         issns[0],
		AllISSNs:          slices.Clone(issns),
		CrossrefTitle:     source.SourceJournalName,
		CrossrefPublisher: "Fixture Publisher",
		CrossrefTotalDOIs: 10,
		CrossrefSupported: venueenrich.SupportStatusYes,
		OpenAlexSupported: venueenrich.SupportStatusUnknown,
		PubMedSupported:   venueenrich.SupportStatusUnknown,
		MatchStatus:       venueenrich.MatchStatusResolved,
	}
}

func journalRegistryUnresolvedRow(
	source venueenrich.SourceRow,
	status venueenrich.MatchStatus,
) venueenrich.RegistryRow {
	return venueenrich.RegistryRow{
		SourceRow:         source,
		CrossrefSupported: venueenrich.SupportStatusUnknown,
		OpenAlexSupported: venueenrich.SupportStatusUnknown,
		PubMedSupported:   venueenrich.SupportStatusUnknown,
		MatchStatus:       status,
	}
}

type journalRegistryCounterStep struct {
	result pubmed.CoverageResult
	err    error
}

type journalRegistryCounter struct {
	calls []pubmed.CoverageQuery
	steps []journalRegistryCounterStep
}

func (counter *journalRegistryCounter) CountCoverage(
	_ context.Context,
	query pubmed.CoverageQuery,
) (pubmed.CoverageResult, error) {
	counter.calls = append(counter.calls, pubmed.CoverageQuery{
		JournalISSNs: slices.Clone(query.JournalISSNs),
	})
	index := len(counter.calls) - 1
	if index >= len(counter.steps) {
		return pubmed.CoverageResult{}, errors.New("unexpected PubMed coverage call")
	}
	return counter.steps[index].result, counter.steps[index].err
}

func journalRegistryFixtureDependencies(t *testing.T) registryDependencies {
	t.Helper()

	dependencies := defaultRegistryDependencies()
	dependencies.now = func() time.Time {
		return time.Date(2026, time.July, 19, 1, 2, 3, 4, time.UTC)
	}
	dependencies.fetchCrossref = func(
		context.Context,
		*http.Client,
		venueenrich.CrossrefCatalogConfig,
		httpclient.Dependencies,
	) (venueenrich.CrossrefCatalog, error) {
		return journalRegistryCatalog(dependencies.now()), nil
	}
	dependencies.matchCrossref = func(
		sources []venueenrich.SourceRow,
		_ venueenrich.CrossrefCatalog,
	) ([]venueenrich.RegistryRow, error) {
		rows := make([]venueenrich.RegistryRow, len(sources))
		for index, source := range sources {
			rows[index] = journalRegistryUnresolvedRow(
				source,
				venueenrich.MatchStatusUnresolved,
			)
		}
		return rows, nil
	}
	dependencies.newPubMedCounter = func(
		*http.Client,
		pubmed.Config,
		httpclient.Dependencies,
	) (venueenrich.PubMedCoverageCounter, error) {
		return &journalRegistryCounter{}, nil
	}
	return dependencies
}

func assertJournalRegistryCountsReconcile(t *testing.T, report registryReport) {
	t.Helper()

	counts := report.Counts
	completeProbeMode := counts.Probes.Attempted == 0 ||
		counts.Probes.Attempted == counts.Probes.Eligible
	if counts.InputRows != counts.OutputRows ||
		counts.Match.Resolved+counts.Match.Ambiguous+counts.Match.Unresolved != counts.InputRows ||
		counts.Probes.Eligible != counts.Match.Resolved ||
		!completeProbeMode ||
		counts.PubMed.Yes+counts.PubMed.No+counts.PubMed.Unknown != counts.OutputRows {
		t.Fatalf("top-level counts do not reconcile: %#v", counts)
	}
	if counts.Probes.Attempted == 0 &&
		(counts.PubMed.Yes != 0 ||
			counts.PubMed.No != 0 ||
			counts.PubMed.Unknown != counts.OutputRows) {
		t.Fatalf("skipped probe counts are contradictory: %#v", counts)
	}

	var aggregate registryCounts
	for _, domain := range report.ByDomain {
		expectedAttempts := domain.Counts.Probes.Eligible
		if counts.Probes.Attempted == 0 {
			expectedAttempts = 0
		}
		if domain.Counts.Probes.Attempted != expectedAttempts {
			t.Fatalf("by_domain probe mode is partial: %#v", domain)
		}
		aggregate.InputRows += domain.Counts.InputRows
		aggregate.OutputRows += domain.Counts.OutputRows
		aggregate.Match.Resolved += domain.Counts.Match.Resolved
		aggregate.Match.Ambiguous += domain.Counts.Match.Ambiguous
		aggregate.Match.Unresolved += domain.Counts.Match.Unresolved
		aggregate.Probes.Eligible += domain.Counts.Probes.Eligible
		aggregate.Probes.Attempted += domain.Counts.Probes.Attempted
		aggregate.PubMed.Yes += domain.Counts.PubMed.Yes
		aggregate.PubMed.No += domain.Counts.PubMed.No
		aggregate.PubMed.Unknown += domain.Counts.PubMed.Unknown
	}
	if aggregate != counts {
		t.Fatalf("by_domain aggregate = %#v, top-level = %#v", aggregate, counts)
	}
}

func validJournalRegistryReportForValidation() registryReport {
	probes := make([]venueenrich.PubMedProbeReceipt, journalPubMedRegistryRows)
	for index := range probes {
		probes[index] = venueenrich.PubMedProbeReceipt{
			Domain:      "all",
			SourceOrder: index + 1,
			ISSNs:       []string{},
			Status:      venueenrich.SupportStatusUnknown,
		}
	}
	counts := registryCounts{
		InputRows:  journalPubMedRegistryRows,
		OutputRows: journalPubMedRegistryRows,
		Match: registryMatchCounts{
			Unresolved: journalPubMedRegistryRows,
		},
		PubMed: registrySupportCounts{
			Unknown: journalPubMedRegistryRows,
		},
	}
	return registryReport{
		SchemaVersion: journalPubMedRegistryReportSchemaVersion,
		GeneratedAt:   "2026-07-19T01:02:03Z",
		Inputs: []registryInputReceipt{
			{
				Ordinal: 1,
				Path:    "medicine.csv",
				Rows:    1546,
				Bytes:   1,
				SHA256:  journalRegistryHashOne,
			},
			{
				Ordinal: 2,
				Path:    "biology.csv",
				Rows:    302,
				Bytes:   1,
				SHA256:  journalRegistryHashOne,
			},
			{
				Ordinal: 3,
				Path:    "computer.csv",
				Rows:    184,
				Bytes:   1,
				SHA256:  journalRegistryHashOne,
			},
		},
		CrossrefCatalog: registryCrossrefReceipt{
			ManifestVersion: venueenrich.CrossrefCatalogSchemaVersion,
			Source:          "https://api.crossref.test/journals",
			FetchedAt:       "2026-07-19T00:00:00Z",
			Hash:            journalRegistryHashTwo,
			Bytes:           1,
			Records:         1,
		},
		Counts: counts,
		ByDomain: []registryDomainCounts{
			{
				Domain: "all",
				Counts: counts,
			},
		},
		Probes: probes,
		OutputCSV: registryOutputReceipt{
			Path:   "registry.csv",
			Rows:   journalPubMedRegistryRows,
			Bytes:  1,
			SHA256: journalRegistryHashTwo,
		},
	}
}

func assertRegistryFinalsAbsent(t *testing.T, paths ...string) {
	t.Helper()

	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected final path %q after rejection: %v", path, err)
		}
	}
}

func assertRegistryTempsAbsent(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", directory, err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary path remained in %q: %s", directory, entry.Name())
		}
	}
}

func assertRegistryCaseProbesAbsent(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", directory, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(
			entry.Name(),
			".journal-registry-case-probe-",
		) {
			t.Fatalf(
				"case-sensitivity probe remained in %q: %s",
				directory,
				entry.Name(),
			)
		}
	}
}

func journalRegistryFilesystemIsCaseInsensitive(
	t *testing.T,
	directory string,
) bool {
	t.Helper()

	probePath := filepath.Join(directory, "CaseSensitivityProbe")
	aliasPath := filepath.Join(directory, "casesensitivityprobe")
	if err := os.WriteFile(probePath, []byte("probe"), 0o600); err != nil {
		t.Fatalf("WriteFile(case probe) error = %v", err)
	}
	defer func() {
		if err := os.Remove(probePath); err != nil {
			t.Errorf("Remove(case probe) error = %v", err)
		}
	}()
	probeInfo, err := os.Lstat(probePath)
	if err != nil {
		t.Fatalf("Lstat(case probe) error = %v", err)
	}
	aliasInfo, err := os.Lstat(aliasPath)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("Lstat(case probe alias) error = %v", err)
	}
	if !os.SameFile(probeInfo, aliasInfo) {
		t.Fatalf(
			"case probe alias %q resolved to a different inode",
			aliasPath,
		)
	}
	return true
}

func validJournalRegistryArgs() []string {
	return []string{
		"--input", "medicine.csv",
		"--input", "biology.csv",
		"--input", "computer.csv",
		"--cache-dir", "cache",
		"--output", "registry.csv",
		"--report", "registry.report.json",
	}
}

func validJournalRegistryAuditArgs() []string {
	return append(
		validJournalRegistryArgs(),
		"--audit-pubmed-coverage",
	)
}

func journalRegistryLookup(values map[string]string) envLookup {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func decodeSingleJSONValue(t *testing.T, reader io.Reader, destination any) {
	t.Helper()

	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("JSON contains trailing value: %v", err)
	}
}
