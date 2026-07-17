package main

import (
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/analysis"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
	"github.com/google/uuid"
)

func TestParseBiomedicalAnalysisCommandsRequireExactScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		kind commandKind
	}{
		{
			name: "publication trends",
			args: trendAnalysisCommandArgs(),
			kind: commandAnalyzeTrends,
		},
		{
			name: "journal editorial patterns",
			args: journalAnalysisCommandArgs(),
			kind: commandAnalyzeJournals,
		},
		{
			name: "research opportunities",
			args: opportunityAnalysisCommandArgs(),
			kind: commandAnalyzeOpportunities,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			command, role, err := parseWorkerCommand(test.args)
			if err != nil {
				t.Fatalf("parseWorkerCommand() error = %v", err)
			}
			if role != config.RoleBiomedicalAnalysis {
				t.Fatalf(
					"parseWorkerCommand() role = %q, want %q",
					role,
					config.RoleBiomedicalAnalysis,
				)
			}
			if command.Kind != test.kind ||
				!command.AsOf.Equal(time.Date(
					2026,
					time.July,
					17,
					12,
					0,
					0,
					0,
					time.UTC,
				)) ||
				command.SubjectVersion != "biomedical-jcr-subjects/v1" ||
				command.EligibilityPolicyVersion !=
					"biomedical-public-eligibility/v1" ||
				command.MetricYear != 2025 ||
				command.JCRReceipt !=
					"00000000-0000-0000-0000-000000000501" ||
				command.VenuePolicyName != "journal-jif-or-q1" ||
				command.VenuePolicyVersion != 1 {
				t.Fatalf("parsed biomedical analysis command = %#v", command)
			}
		})
	}
}

func TestParseTrendAnalysisRequiresPredeclaredStatisticalBoundaries(
	t *testing.T,
) {
	t.Parallel()

	command, _, err := parseWorkerCommand(trendAnalysisCommandArgs())
	if err != nil {
		t.Fatalf("parseWorkerCommand() error = %v", err)
	}
	if command.RecentWindowDays != 56 ||
		command.BaselineWindowDays != 364 ||
		command.MinimumPaperCount != 20 ||
		command.MinimumIndependentJournalCount != 3 ||
		command.MinimumIndependentTeamCount != 3 ||
		command.TrendModelSelectionRule !=
			"predeclared_dispersion_threshold" ||
		command.TrendDispersionThreshold != 1.5 ||
		command.FormulaVersion != "biomedical-trends/v1" {
		t.Fatalf("parsed trend analysis command = %#v", command)
	}
}

func TestParseJournalAnalysisRequiresMinimumSupportBoundaries(t *testing.T) {
	t.Parallel()

	command, _, err := parseWorkerCommand(journalAnalysisCommandArgs())
	if err != nil {
		t.Fatalf("parseWorkerCommand() error = %v", err)
	}
	if command.WindowDays != 364 ||
		command.MinimumSupportCount != 10 ||
		command.MinimumFieldBaselineCount != 40 ||
		command.FormulaVersion != "biomedical-journal-patterns/v1" {
		t.Fatalf("parsed journal analysis command = %#v", command)
	}
}

func TestParseOpportunityAnalysisRequiresExactUpstreamRuns(t *testing.T) {
	t.Parallel()

	command, _, err := parseWorkerCommand(opportunityAnalysisCommandArgs())
	if err != nil {
		t.Fatalf("parseWorkerCommand() error = %v", err)
	}
	if command.RuleSetVersion != "biomedical-opportunity-rules/v1" ||
		command.FormulaVersion != "biomedical-opportunities/v1" ||
		command.CitationAnalysisRunID !=
			"00000000-0000-0000-0000-000000000701" ||
		command.TrendAnalysisRunID !=
			"00000000-0000-0000-0000-000000000702" ||
		command.JournalAnalysisRunID !=
			"00000000-0000-0000-0000-000000000703" {
		t.Fatalf("parsed opportunity analysis command = %#v", command)
	}
}

func TestBiomedicalAnalysisInputMappingPreservesExactScope(t *testing.T) {
	t.Parallel()

	trendCommand, _, err := parseWorkerCommand(
		trendAnalysisCommandArgs(),
	)
	if err != nil {
		t.Fatalf("parse trend command: %v", err)
	}
	trend := trendAnalysisInput(trendCommand)
	if trend.FormulaVersion != analysis.PublicationTrendFormulaVersion ||
		trend.VenuePolicyName != "journal-jif-or-q1" ||
		trend.VenuePolicyVersion != 1 ||
		trend.MinimumPaperCount != 20 ||
		trend.MinimumIndependentJournalCount != 3 ||
		trend.MinimumIndependentTeamCount != 3 {
		t.Fatalf("trendAnalysisInput() = %#v", trend)
	}

	journalCommand, _, err := parseWorkerCommand(
		journalAnalysisCommandArgs(),
	)
	if err != nil {
		t.Fatalf("parse journal command: %v", err)
	}
	journal := journalAnalysisInput(journalCommand)
	if journal.FormulaVersion != analysis.JournalPatternFormulaVersion ||
		journal.VenuePolicyName != "journal-jif-or-q1" ||
		journal.VenuePolicyVersion != 1 ||
		journal.WindowDays != 364 ||
		journal.MinimumSupportCount != 10 ||
		journal.MinimumFieldBaselineCount != 40 {
		t.Fatalf("journalAnalysisInput() = %#v", journal)
	}

	opportunityCommand, _, err := parseWorkerCommand(
		opportunityAnalysisCommandArgs(),
	)
	if err != nil {
		t.Fatalf("parse opportunity command: %v", err)
	}
	opportunity := opportunityAnalysisInput(opportunityCommand)
	if opportunity.FormulaVersion != analysis.OpportunityFormulaVersion ||
		opportunity.RuleSetVersion != analysis.OpportunityRuleSetVersion ||
		opportunity.VenuePolicyName != "journal-jif-or-q1" ||
		opportunity.VenuePolicyVersion != 1 ||
		opportunity.CitationAnalysisRunID != uuid.MustParse(
			"00000000-0000-0000-0000-000000000701",
		) ||
		opportunity.TrendAnalysisRunID != uuid.MustParse(
			"00000000-0000-0000-0000-000000000702",
		) ||
		opportunity.JournalAnalysisRunID != uuid.MustParse(
			"00000000-0000-0000-0000-000000000703",
		) {
		t.Fatalf("opportunityAnalysisInput() = %#v", opportunity)
	}
}

func TestParseBiomedicalAnalysisRejectsMissingOrInvalidBoundaries(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "trend recent window is zero",
			args: replaceWorkerFlag(
				trendAnalysisCommandArgs(),
				"--recent-window-days",
				"0",
			),
			want: "recent-window-days",
		},
		{
			name: "trend baseline does not exceed recent window",
			args: replaceWorkerFlag(
				trendAnalysisCommandArgs(),
				"--baseline-window-days",
				"56",
			),
			want: "greater than recent-window-days",
		},
		{
			name: "trend dispersion threshold missing for declared selection",
			args: replaceWorkerFlag(
				trendAnalysisCommandArgs(),
				"--dispersion-threshold",
				"0",
			),
			want: "dispersion-threshold",
		},
		{
			name: "journal minimum support is zero",
			args: replaceWorkerFlag(
				journalAnalysisCommandArgs(),
				"--minimum-support-count",
				"0",
			),
			want: "minimum-support-count",
		},
		{
			name: "analysis venue policy is missing",
			args: removeWorkerFlag(
				trendAnalysisCommandArgs(),
				"--venue-policy-name",
			),
			want: "--venue-policy-name",
		},
		{
			name: "analysis venue policy version is zero",
			args: replaceWorkerFlag(
				trendAnalysisCommandArgs(),
				"--venue-policy-version",
				"0",
			),
			want: "--venue-policy-version",
		},
		{
			name: "opportunity missing trend run",
			args: removeWorkerFlag(
				opportunityAnalysisCommandArgs(),
				"--trend-analysis-run-id",
			),
			want: "--trend-analysis-run-id",
		},
		{
			name: "opportunity invalid journal run",
			args: replaceWorkerFlag(
				opportunityAnalysisCommandArgs(),
				"--journal-analysis-run-id",
				"not-a-uuid",
			),
			want: "journal-analysis-run-id must be a UUID",
		},
		{
			name: "unsupported trend formula",
			args: replaceWorkerFlag(
				trendAnalysisCommandArgs(),
				"--formula-version",
				"biomedical-trends/v2",
			),
			want: "biomedical-trends/v1",
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

func trendAnalysisCommandArgs() []string {
	return append(
		[]string{
			"analyze",
			"trends",
			"--recent-window-days",
			"56",
			"--baseline-window-days",
			"364",
			"--minimum-paper-count",
			"20",
			"--minimum-independent-journal-count",
			"3",
			"--minimum-independent-team-count",
			"3",
			"--model-selection",
			"predeclared_dispersion_threshold",
			"--dispersion-threshold",
			"1.5",
			"--formula-version",
			"biomedical-trends/v1",
		},
		biomedicalAnalysisScopeArgs()...,
	)
}

func journalAnalysisCommandArgs() []string {
	return append(
		[]string{
			"analyze",
			"journals",
			"--window-days",
			"364",
			"--minimum-support-count",
			"10",
			"--minimum-field-baseline-count",
			"40",
			"--formula-version",
			"biomedical-journal-patterns/v1",
		},
		biomedicalAnalysisScopeArgs()...,
	)
}

func opportunityAnalysisCommandArgs() []string {
	return append(
		[]string{
			"analyze",
			"opportunities",
			"--rule-set-version",
			"biomedical-opportunity-rules/v1",
			"--formula-version",
			"biomedical-opportunities/v1",
			"--citation-analysis-run-id",
			"00000000-0000-0000-0000-000000000701",
			"--trend-analysis-run-id",
			"00000000-0000-0000-0000-000000000702",
			"--journal-analysis-run-id",
			"00000000-0000-0000-0000-000000000703",
		},
		biomedicalAnalysisScopeArgs()...,
	)
}

func biomedicalAnalysisScopeArgs() []string {
	return []string{
		"--as-of",
		"2026-07-17T12:00:00Z",
		"--subject-version",
		"biomedical-jcr-subjects/v1",
		"--eligibility-policy-version",
		"biomedical-public-eligibility/v1",
		"--jcr-metric-year",
		"2025",
		"--jcr-import-receipt",
		"00000000-0000-0000-0000-000000000501",
		"--venue-policy-name",
		"journal-jif-or-q1",
		"--venue-policy-version",
		"1",
	}
}
