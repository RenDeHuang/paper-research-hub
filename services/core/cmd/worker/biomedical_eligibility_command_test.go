package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
)

func TestParseWorkerCommandRequiresExplicitBiomedicalEligibilityInputs(t *testing.T) {
	t.Parallel()

	valid := biomedicalEligibilityCommandArgs()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing metric year",
			args: removeBiomedicalEligibilityFlag(valid, "--metric-year"),
			want: "--metric-year",
		},
		{
			name: "metric year below range",
			args: replaceBiomedicalEligibilityFlag(
				valid,
				"--metric-year",
				"1899",
			),
			want: "1900 and 3000",
		},
		{
			name: "metric year above range",
			args: replaceBiomedicalEligibilityFlag(
				valid,
				"--metric-year",
				"3001",
			),
			want: "1900 and 3000",
		},
		{
			name: "missing Subject version",
			args: removeBiomedicalEligibilityFlag(valid, "--subject-version"),
			want: "--subject-version",
		},
		{
			name: "untrimmed Subject version",
			args: replaceBiomedicalEligibilityFlag(
				valid,
				"--subject-version",
				" biomedical-jcr-subjects/v1",
			),
			want: "trimmed",
		},
		{
			name: "research domain Registry requires channel admission",
			args: replaceBiomedicalEligibilityFlag(
				valid,
				"--subject-version",
				"research-domains-jcr-subjects/v2",
			),
			want: "channel admission",
		},
		{
			name: "missing policy version",
			args: removeBiomedicalEligibilityFlag(valid, "--policy-version"),
			want: "--policy-version",
		},
		{
			name: "unsupported policy version",
			args: replaceBiomedicalEligibilityFlag(
				valid,
				"--policy-version",
				"biomedical-public-eligibility/v2",
			),
			want: biomed.BiomedicalPublicEligibilityPolicyVersion,
		},
		{
			name: "untrimmed policy version",
			args: replaceBiomedicalEligibilityFlag(
				valid,
				"--policy-version",
				"biomedical-public-eligibility/v1 ",
			),
			want: "trimmed",
		},
		{
			name: "missing assessed at",
			args: removeBiomedicalEligibilityFlag(valid, "--assessed-at"),
			want: "--assessed-at",
		},
		{
			name: "untrimmed assessed at",
			args: replaceBiomedicalEligibilityFlag(
				valid,
				"--assessed-at",
				" 2026-07-17T15:30:00.123456789Z",
			),
			want: "trimmed",
		},
		{
			name: "invalid assessed at",
			args: replaceBiomedicalEligibilityFlag(
				valid,
				"--assessed-at",
				"2026-07-17 15:30:00",
			),
			want: "RFC3339Nano",
		},
		{
			name: "zero assessed at",
			args: replaceBiomedicalEligibilityFlag(
				valid,
				"--assessed-at",
				"0001-01-01T00:00:00Z",
			),
			want: "non-zero",
		},
		{
			name: "unexpected positional argument",
			args: append(
				append([]string(nil), valid...),
				"unexpected",
			),
			want: "unexpected",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := parseWorkerCommand(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"parseWorkerCommand(%#v) error = %v, want containing %q",
					test.args,
					err,
					test.want,
				)
			}
		})
	}
}

func TestRealMainParsesBiomedicalEligibilityAndOutputsExactSummary(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var received workerCommand

	code := realMain(
		context.Background(),
		biomedicalEligibilityCommandArgs(),
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
			if cfg.Database.URL == "" {
				t.Fatal("biomedical eligibility did not load database-only configuration")
			}
			return biomedicalEligibilityResult(
				command,
				biomed.PublicEligibilityBatchSummary{
					Total:    9,
					Accepted: 4,
					Rejected: 3,
					Missing:  2,
				},
			), nil
		},
	)

	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	wantAssessedAt := time.Date(
		2026,
		time.July,
		17,
		15,
		30,
		0,
		123456789,
		time.UTC,
	)
	if received.Kind != commandAssessBiomedicalEligibility ||
		received.MetricYear != 2025 ||
		received.SubjectVersion != "biomedical-jcr-subjects/v1" ||
		received.PolicyVersion !=
			biomed.BiomedicalPublicEligibilityPolicyVersion ||
		!received.AssessedAt.Equal(wantAssessedAt) {
		t.Fatalf("received biomedical eligibility command = %#v", received)
	}

	var output struct {
		MetricYear     int    `json:"metric_year"`
		SubjectVersion string `json:"subject_version"`
		PolicyVersion  string `json:"policy_version"`
		AssessedAt     string `json:"assessed_at"`
		Total          int    `json:"total"`
		Accepted       int    `json:"accepted"`
		Rejected       int    `json:"rejected"`
		Missing        int    `json:"missing"`
	}
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		t.Fatalf(
			"decode biomedical eligibility result: %v; stdout=%s",
			err,
			stdout.String(),
		)
	}
	if output.MetricYear != 2025 ||
		output.SubjectVersion != "biomedical-jcr-subjects/v1" ||
		output.PolicyVersion !=
			biomed.BiomedicalPublicEligibilityPolicyVersion ||
		output.AssessedAt != wantAssessedAt.Format(time.RFC3339Nano) ||
		output.Total != 9 ||
		output.Accepted != 4 ||
		output.Rejected != 3 ||
		output.Missing != 2 {
		t.Fatalf("biomedical eligibility output = %#v", output)
	}
}

func biomedicalEligibilityCommandArgs() []string {
	return []string{
		"assess",
		"biomedical-eligibility",
		"--metric-year",
		"2025",
		"--subject-version",
		"biomedical-jcr-subjects/v1",
		"--policy-version",
		"biomedical-public-eligibility/v1",
		"--assessed-at",
		"2026-07-17T15:30:00.123456789Z",
	}
}

func removeBiomedicalEligibilityFlag(args []string, name string) []string {
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

func replaceBiomedicalEligibilityFlag(
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
