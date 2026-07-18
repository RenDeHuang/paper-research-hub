package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/abstractanalysis"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
)

func TestParseAbstractRouteAnalysisRequiresFrozenVersionsAndBoundary(
	t *testing.T,
) {
	t.Parallel()

	command, role, err := parseWorkerCommand(abstractAnalysisCommandArgs())
	if err != nil {
		t.Fatalf("parseWorkerCommand() error = %v", err)
	}
	if role != config.RoleAbstractAnalysis {
		t.Fatalf(
			"parseWorkerCommand() role = %q, want %q",
			role,
			config.RoleAbstractAnalysis,
		)
	}
	if command.Kind != commandAnalyzeAbstractRoutes ||
		command.PromptVersion != abstractanalysis.PromptVersion ||
		command.SchemaVersion != abstractanalysis.SchemaVersion ||
		!command.AnalysisCutoff.Equal(time.Date(
			2026,
			time.July,
			18,
			8,
			30,
			0,
			123456789,
			time.UTC,
		)) ||
		command.Limit != 25 {
		t.Fatalf("parsed abstract analysis command = %#v", command)
	}
}

func TestParseAbstractRouteAnalysisRejectsInvalidContractBeforeExecution(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing prompt version",
			args: removeWorkerFlag(
				abstractAnalysisCommandArgs(),
				"--prompt-version",
			),
			want: "--prompt-version",
		},
		{
			name: "unsupported prompt version",
			args: replaceWorkerFlag(
				abstractAnalysisCommandArgs(),
				"--prompt-version",
				"abstract-route-prompt/v2",
			),
			want: abstractanalysis.PromptVersion,
		},
		{
			name: "unsupported schema version",
			args: replaceWorkerFlag(
				abstractAnalysisCommandArgs(),
				"--schema-version",
				"abstract-route/v2",
			),
			want: abstractanalysis.SchemaVersion,
		},
		{
			name: "missing cutoff",
			args: removeWorkerFlag(
				abstractAnalysisCommandArgs(),
				"--analysis-cutoff",
			),
			want: "--analysis-cutoff",
		},
		{
			name: "untrimmed cutoff",
			args: replaceWorkerFlag(
				abstractAnalysisCommandArgs(),
				"--analysis-cutoff",
				" 2026-07-18T08:30:00Z",
			),
			want: "cutoff must be trimmed",
		},
		{
			name: "zero limit",
			args: replaceWorkerFlag(
				abstractAnalysisCommandArgs(),
				"--limit",
				"0",
			),
			want: "between 1 and 1000",
		},
		{
			name: "limit above maximum",
			args: replaceWorkerFlag(
				abstractAnalysisCommandArgs(),
				"--limit",
				"1001",
			),
			want: "between 1 and 1000",
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

func TestRealMainLoadsOpenAIConfigurationForAbstractRouteAnalysis(
	t *testing.T,
) {
	t.Parallel()

	const secret = "openai-secret-that-must-not-leak"
	var stdout, stderr bytes.Buffer
	var received workerCommand
	code := realMain(
		context.Background(),
		abstractAnalysisCommandArgs(),
		&stdout,
		&stderr,
		func(key string) (string, bool) {
			values := map[string]string{
				"DATABASE_URL":    "postgres://paper:secret@localhost/papers",
				"OPENAI_BASE_URL": "https://gateway.example.test/openai/v1",
				"OPENAI_API_MODE": "chat_completions",
				"OPENAI_API_KEY":  secret,
				"OPENAI_MODEL":    "pinned-model-2026-07-18",
			}
			value, ok := values[key]
			return value, ok
		},
		func(
			_ context.Context,
			cfg config.Config,
			command workerCommand,
		) (map[string]any, error) {
			received = command
			if cfg.OpenAI.BaseURL !=
				"https://gateway.example.test/openai/v1" ||
				cfg.OpenAI.APIMode != "chat_completions" ||
				cfg.OpenAI.APIKey != secret ||
				cfg.OpenAI.Model != "pinned-model-2026-07-18" {
				t.Fatalf("OpenAI config = %#v", cfg.Redacted().OpenAI)
			}
			return map[string]any{
				"selected":  2,
				"succeeded": 2,
				"failed":    0,
			}, nil
		},
	)
	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.Kind != commandAnalyzeAbstractRoutes {
		t.Fatalf("received command kind = %q", received.Kind)
	}
	if !strings.Contains(stdout.String(), `"succeeded":2`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Fatal("worker output leaked OPENAI_API_KEY")
	}
}

func TestAbstractAnalysisInputAndResultPreserveAuditableBoundary(t *testing.T) {
	t.Parallel()

	command, _, err := parseWorkerCommand(abstractAnalysisCommandArgs())
	if err != nil {
		t.Fatalf("parseWorkerCommand() error = %v", err)
	}
	input := abstractAnalysisInput(command)
	if input.PromptVersion != abstractanalysis.PromptVersion ||
		input.SchemaVersion != abstractanalysis.SchemaVersion ||
		!input.Cutoff.Equal(command.AnalysisCutoff) ||
		input.Limit != command.Limit {
		t.Fatalf("abstractAnalysisInput() = %#v", input)
	}

	result := abstractAnalysisResult(
		command,
		"chat_completions",
		"pinned-model-2026-07-18",
		abstractanalysis.Summary{
			Selected:  3,
			Succeeded: 2,
			Failed:    1,
		},
	)
	if result["prompt_version"] != abstractanalysis.PromptVersion ||
		result["schema_version"] != abstractanalysis.SchemaVersion ||
		result["analysis_cutoff"] !=
			command.AnalysisCutoff.Format(time.RFC3339Nano) ||
		result["api_mode"] != "chat_completions" ||
		result["requested_model"] != "pinned-model-2026-07-18" ||
		result["selected"] != 3 ||
		result["succeeded"] != 2 ||
		result["failed"] != 1 {
		t.Fatalf("abstractAnalysisResult() = %#v", result)
	}
}

func abstractAnalysisCommandArgs() []string {
	return []string{
		"analyze",
		"abstract-routes",
		"--prompt-version",
		"abstract-route-prompt/v1",
		"--schema-version",
		"abstract-route/v1",
		"--analysis-cutoff",
		"2026-07-18T08:30:00.123456789Z",
		"--limit",
		"25",
	}
}
