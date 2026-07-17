package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/citation"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
	"github.com/google/uuid"
)

func TestParseCitationAnalysisRequiresExactDeterministicScope(t *testing.T) {
	t.Parallel()

	command, role, err := parseWorkerCommand(citationAnalysisCommandArgs())
	if err != nil {
		t.Fatalf("parseWorkerCommand() error = %v", err)
	}
	if role != config.RoleCitationAnalysis {
		t.Fatalf("parseWorkerCommand() role = %q, want %q", role, config.RoleCitationAnalysis)
	}
	if command.Kind != commandAnalyzeCitations ||
		command.CitationSource != "openalex" ||
		!command.AsOf.Equal(time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)) ||
		command.VelocityWindowDays != 30 ||
		command.MinimumCohortSize != 20 ||
		command.FormulaVersion != "citation-intelligence/v1" ||
		command.SubjectVersion != "biomedical-jcr-subjects/v1" ||
		command.EligibilityPolicyVersion != "biomedical-public-eligibility/v1" ||
		command.MetricYear != 2025 ||
		command.JCRReceipt != "00000000-0000-0000-0000-000000000501" {
		t.Fatalf("parsed citation analysis command = %#v", command)
	}
}

func TestParseCitationAnalysisRejectsMissingOrInvalidScopeBeforeExecution(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing as of",
			args: removeWorkerFlag(
				citationAnalysisCommandArgs(),
				"--as-of",
			),
			want: "--as-of",
		},
		{
			name: "untrimmed source",
			args: replaceWorkerFlag(
				citationAnalysisCommandArgs(),
				"--source",
				" openalex",
			),
			want: "source must be trimmed",
		},
		{
			name: "zero velocity window",
			args: replaceWorkerFlag(
				citationAnalysisCommandArgs(),
				"--velocity-window-days",
				"0",
			),
			want: "between 1 and 3650",
		},
		{
			name: "minimum cohort below two",
			args: replaceWorkerFlag(
				citationAnalysisCommandArgs(),
				"--minimum-cohort-size",
				"1",
			),
			want: "between 2 and 1000000",
		},
		{
			name: "unsupported eligibility policy",
			args: replaceWorkerFlag(
				citationAnalysisCommandArgs(),
				"--eligibility-policy-version",
				"biomedical-public-eligibility/v2",
			),
			want: "biomedical-public-eligibility/v1",
		},
		{
			name: "invalid JCR receipt",
			args: replaceWorkerFlag(
				citationAnalysisCommandArgs(),
				"--jcr-import-receipt",
				"not-a-uuid",
			),
			want: "must be a UUID",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := parseWorkerCommand(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"parseWorkerCommand() error = %v, want containing %q",
					err,
					test.want,
				)
			}
		})
	}
}

func TestRealMainParsesCitationAnalysisBeforeRunning(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	var received workerCommand
	code := realMain(
		context.Background(),
		citationAnalysisCommandArgs(),
		&stdout,
		&stderr,
		func(key string) (string, bool) {
			if key == "DATABASE_URL" {
				return "postgres://paper:secret@localhost/papers", true
			}
			return "", false
		},
		func(
			_ context.Context,
			cfg config.Config,
			command workerCommand,
		) (map[string]any, error) {
			received = command
			if cfg.OpenAlex.APIKey != "" {
				t.Fatal("citation analysis unexpectedly required source credentials")
			}
			return map[string]any{
				"analysis_run_id": "00000000-0000-0000-0000-000000000701",
			}, nil
		},
	)
	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.Kind != commandAnalyzeCitations {
		t.Fatalf("received command kind = %q", received.Kind)
	}
	if !strings.Contains(
		stdout.String(),
		`"analysis_run_id":"00000000-0000-0000-0000-000000000701"`,
	) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestCitationAnalysisCommandMapsAndReportsRunIdentity(t *testing.T) {
	t.Parallel()

	command, _, err := parseWorkerCommand(citationAnalysisCommandArgs())
	if err != nil {
		t.Fatalf("parseWorkerCommand() error = %v", err)
	}
	input := citationAnalysisInput(command)
	if input != (citation.AnalysisInput{
		AsOf: time.Date(
			2026,
			time.July,
			17,
			12,
			0,
			0,
			0,
			time.UTC,
		),
		Source:                   "openalex",
		VelocityWindowDays:       30,
		MinimumCohortSize:        20,
		FormulaVersion:           citation.CitationIntelligenceFormulaVersion,
		SubjectVersion:           "biomedical-jcr-subjects/v1",
		EligibilityPolicyVersion: "biomedical-public-eligibility/v1",
		JCRMetricYear:            2025,
		JCRImportReceipt: uuid.MustParse(
			"00000000-0000-0000-0000-000000000501",
		),
	}) {
		t.Fatalf("citationAnalysisInput() = %#v", input)
	}

	runID := uuid.MustParse("00000000-0000-0000-0000-000000000701")
	result := citationAnalysisResult(
		command,
		citation.AnalysisSummary{
			RunID:                  runID,
			TotalWorks:             100,
			KnownCitationCounts:    90,
			KnownVelocities:        40,
			InsufficientVelocity:   60,
			PercentileRows:         180,
			KnownPercentiles:       120,
			InsufficientPercentile: 60,
		},
	)
	if result["analysis_run_id"] != runID.String() ||
		result["source"] != "openalex" ||
		result["total_works"] != 100 ||
		result["known_percentiles"] != 120 {
		t.Fatalf("citationAnalysisResult() = %#v", result)
	}
}

func citationAnalysisCommandArgs() []string {
	return []string{
		"analyze",
		"citations",
		"--as-of",
		"2026-07-17T12:00:00Z",
		"--source",
		"openalex",
		"--velocity-window-days",
		"30",
		"--minimum-cohort-size",
		"20",
		"--formula-version",
		"citation-intelligence/v1",
		"--subject-version",
		"biomedical-jcr-subjects/v1",
		"--eligibility-policy-version",
		"biomedical-public-eligibility/v1",
		"--jcr-metric-year",
		"2025",
		"--jcr-import-receipt",
		"00000000-0000-0000-0000-000000000501",
	}
}

func replaceWorkerFlag(args []string, name string, value string) []string {
	result := append([]string(nil), args...)
	for index := range result {
		if result[index] == name {
			result[index+1] = value
			return result
		}
	}
	panic("worker flag not found: " + name)
}
