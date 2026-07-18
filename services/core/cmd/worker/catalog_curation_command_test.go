package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/catalog"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
)

func TestParseCatalogPublishRequiresExplicitBiomedicalCurationInputs(t *testing.T) {
	t.Parallel()

	base := []string{
		"publish",
		"catalog",
		"--formula-version",
		"public-catalog/biomedical-v1",
		"--generated-at",
		"2026-07-17T12:00:00Z",
		"--jcr-metric-year",
		"2025",
		"--venue-policy-name",
		"journal-all-q1",
		"--venue-policy-version",
		"2",
		"--eligibility-policy-version",
		"biomedical-public-eligibility/v1",
		"--subject-version",
		"biomedical-jcr-subjects/v1",
		"--jcr-import-receipt",
		"00000000-0000-0000-0000-000000000501",
		"--citation-source",
		"openalex",
		"--citation-analysis-run-id",
		"00000000-0000-0000-0000-000000000701",
		"--trend-analysis-run-id",
		"00000000-0000-0000-0000-000000000702",
		"--journal-analysis-run-id",
		"00000000-0000-0000-0000-000000000703",
		"--opportunity-analysis-run-id",
		"00000000-0000-0000-0000-000000000704",
	}
	tests := []struct {
		name string
		flag string
		want string
	}{
		{name: "JCR metric year", flag: "--jcr-metric-year", want: "--jcr-metric-year"},
		{name: "Venue policy name", flag: "--venue-policy-name", want: "--venue-policy-name"},
		{name: "Venue policy version", flag: "--venue-policy-version", want: "--venue-policy-version"},
		{
			name: "Eligibility policy version",
			flag: "--eligibility-policy-version",
			want: "--eligibility-policy-version",
		},
		{name: "Subject version", flag: "--subject-version", want: "--subject-version"},
		{name: "JCR receipt", flag: "--jcr-import-receipt", want: "--jcr-import-receipt"},
		{name: "Citation source", flag: "--citation-source", want: "--citation-source"},
		{
			name: "Citation analysis run",
			flag: "--citation-analysis-run-id",
			want: "--citation-analysis-run-id",
		},
		{
			name: "Trend analysis run",
			flag: "--trend-analysis-run-id",
			want: "--trend-analysis-run-id",
		},
		{
			name: "Journal analysis run",
			flag: "--journal-analysis-run-id",
			want: "--journal-analysis-run-id",
		},
		{
			name: "Opportunity analysis run",
			flag: "--opportunity-analysis-run-id",
			want: "--opportunity-analysis-run-id",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			args := removeWorkerFlag(base, test.flag)
			_, _, err := parseWorkerCommand(args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseWorkerCommand() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseCatalogPublishRequiresExactEligibilityPolicyVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "unsupported version",
			value: "biomedical-public-eligibility/v2",
			want:  biomed.BiomedicalPublicEligibilityPolicyVersion,
		},
		{
			name:  "untrimmed version",
			value: " biomedical-public-eligibility/v1",
			want:  "trimmed",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			args := replaceCatalogPublishFlag(
				catalogPublishCommandArgs(),
				"--eligibility-policy-version",
				test.value,
			)
			_, _, err := parseWorkerCommand(args)
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

func TestRealMainParsesCatalogBiomedicalCurationInputs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var received workerCommand
	code := realMain(
		context.Background(),
		[]string{
			"publish",
			"catalog",
			"--formula-version",
			"public-catalog/biomedical-v1",
			"--generated-at",
			"2026-07-17T12:00:00Z",
			"--jcr-metric-year",
			"2025",
			"--venue-policy-name",
			"journal-all-q1",
			"--venue-policy-version",
			"2",
			"--eligibility-policy-version",
			"biomedical-public-eligibility/v1",
			"--subject-version",
			"biomedical-jcr-subjects/v1",
			"--jcr-import-receipt",
			"00000000-0000-0000-0000-000000000501",
			"--citation-source",
			"openalex",
			"--citation-analysis-run-id",
			"00000000-0000-0000-0000-000000000701",
			"--trend-analysis-run-id",
			"00000000-0000-0000-0000-000000000702",
			"--journal-analysis-run-id",
			"00000000-0000-0000-0000-000000000703",
			"--opportunity-analysis-run-id",
			"00000000-0000-0000-0000-000000000704",
		},
		&stdout,
		&stderr,
		func(key string) (string, bool) {
			if key == "DATABASE_URL" {
				return "postgres://paper:secret@localhost/papers", true
			}
			return "", false
		},
		func(_ context.Context, _ config.Config, command workerCommand) (map[string]any, error) {
			received = command
			return map[string]any{"status": "accepted-only"}, nil
		},
	)
	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.MetricYear != 2025 ||
		received.VenuePolicyName != "journal-all-q1" ||
		received.VenuePolicyVersion != 2 ||
		received.EligibilityPolicyVersion !=
			biomed.BiomedicalPublicEligibilityPolicyVersion ||
		received.SubjectVersion != "biomedical-jcr-subjects/v1" ||
		received.JCRReceipt != "00000000-0000-0000-0000-000000000501" ||
		received.CitationSource != "openalex" ||
		received.CitationAnalysisRunID !=
			"00000000-0000-0000-0000-000000000701" ||
		received.TrendAnalysisRunID !=
			"00000000-0000-0000-0000-000000000702" ||
		received.JournalAnalysisRunID !=
			"00000000-0000-0000-0000-000000000703" ||
		received.OpportunityAnalysisRunID !=
			"00000000-0000-0000-0000-000000000704" {
		t.Fatalf("received Catalog curation command = %#v", received)
	}
}

func TestCatalogPublishMapsAndReportsEligibilityPolicyVersion(t *testing.T) {
	t.Parallel()

	command, _, err := parseWorkerCommand(catalogPublishCommandArgs())
	if err != nil {
		t.Fatalf("parseWorkerCommand() error = %v", err)
	}
	input := catalogPublishInput(command)
	if input.EligibilityPolicyVersion !=
		biomed.BiomedicalPublicEligibilityPolicyVersion {
		t.Fatalf(
			"PublishInput.EligibilityPolicyVersion = %q",
			input.EligibilityPolicyVersion,
		)
	}
	if input.CitationSource != "openalex" {
		t.Fatalf("PublishInput.CitationSource = %q", input.CitationSource)
	}
	if input.CitationAnalysisRunID != uuid.MustParse(
		"00000000-0000-0000-0000-000000000701",
	) {
		t.Fatalf(
			"PublishInput.CitationAnalysisRunID = %s",
			input.CitationAnalysisRunID,
		)
	}
	if input.TrendAnalysisRunID != uuid.MustParse(
		"00000000-0000-0000-0000-000000000702",
	) || input.JournalAnalysisRunID != uuid.MustParse(
		"00000000-0000-0000-0000-000000000703",
	) || input.OpportunityAnalysisRunID != uuid.MustParse(
		"00000000-0000-0000-0000-000000000704",
	) {
		t.Fatalf("PublishInput analysis run IDs = %#v", input)
	}

	generation := catalog.Generation{
		ID: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
	}
	result := catalogPublishResult(generation, command)
	if result["eligibility_policy_version"] !=
		biomed.BiomedicalPublicEligibilityPolicyVersion {
		t.Fatalf(
			"catalog publish result eligibility policy = %#v",
			result["eligibility_policy_version"],
		)
	}
	if result["citation_source"] != "openalex" {
		t.Fatalf(
			"catalog publish result citation source = %#v",
			result["citation_source"],
		)
	}
	if result["citation_analysis_run_id"] !=
		"00000000-0000-0000-0000-000000000701" {
		t.Fatalf(
			"catalog publish result citation analysis run = %#v",
			result["citation_analysis_run_id"],
		)
	}
	if result["trend_analysis_run_id"] !=
		"00000000-0000-0000-0000-000000000702" ||
		result["journal_analysis_run_id"] !=
			"00000000-0000-0000-0000-000000000703" ||
		result["opportunity_analysis_run_id"] !=
			"00000000-0000-0000-0000-000000000704" {
		t.Fatalf("catalog publish result analysis runs = %#v", result)
	}
}

func catalogPublishCommandArgs() []string {
	return []string{
		"publish",
		"catalog",
		"--formula-version",
		"public-catalog/biomedical-v1",
		"--generated-at",
		time.Date(
			2026,
			time.July,
			17,
			12,
			0,
			0,
			0,
			time.UTC,
		).Format(time.RFC3339Nano),
		"--jcr-metric-year",
		"2025",
		"--venue-policy-name",
		"journal-all-q1",
		"--venue-policy-version",
		"2",
		"--eligibility-policy-version",
		biomed.BiomedicalPublicEligibilityPolicyVersion,
		"--subject-version",
		"biomedical-jcr-subjects/v1",
		"--jcr-import-receipt",
		"00000000-0000-0000-0000-000000000501",
		"--citation-source",
		"openalex",
		"--citation-analysis-run-id",
		"00000000-0000-0000-0000-000000000701",
		"--trend-analysis-run-id",
		"00000000-0000-0000-0000-000000000702",
		"--journal-analysis-run-id",
		"00000000-0000-0000-0000-000000000703",
		"--opportunity-analysis-run-id",
		"00000000-0000-0000-0000-000000000704",
	}
}

func removeWorkerFlag(args []string, name string) []string {
	result := make([]string, 0, len(args)-2)
	for index := 0; index < len(args); index++ {
		if args[index] == name {
			index++
			continue
		}
		result = append(result, args[index])
	}
	return result
}

func replaceCatalogPublishFlag(
	args []string,
	name string,
	value string,
) []string {
	result := append([]string(nil), args...)
	for index := 0; index < len(result); index++ {
		if result[index] == name {
			result[index+1] = value
			return result
		}
	}
	panic("test flag not found: " + name)
}
