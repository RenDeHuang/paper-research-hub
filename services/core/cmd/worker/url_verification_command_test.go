package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
)

func TestParseVerifyURLsRequiresExactPolicyAndBoundedLimit(t *testing.T) {
	t.Parallel()

	command, role, err := parseWorkerCommand(verifyURLsCommandArgs())
	if err != nil {
		t.Fatalf("parseWorkerCommand() error = %v", err)
	}
	if role != config.RoleMigrate {
		t.Fatalf("parseWorkerCommand() role = %q, want %q", role, config.RoleMigrate)
	}
	if command.Kind != commandVerifyURLs ||
		command.PolicyVersion != "official-url/v1" ||
		command.Limit != 25 {
		t.Fatalf("parsed verify URLs command = %#v", command)
	}
}

func TestParseVerifyURLsRejectsInvalidContractBeforeExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing policy version",
			args: removeWorkerFlag(
				verifyURLsCommandArgs(),
				"--policy-version",
			),
			want: "--policy-version",
		},
		{
			name: "unsupported policy version",
			args: replaceWorkerFlag(
				verifyURLsCommandArgs(),
				"--policy-version",
				"official-url/v2",
			),
			want: "official-url/v1",
		},
		{
			name: "untrimmed policy version",
			args: replaceWorkerFlag(
				verifyURLsCommandArgs(),
				"--policy-version",
				" official-url/v1",
			),
			want: "trimmed",
		},
		{
			name: "zero limit",
			args: replaceWorkerFlag(
				verifyURLsCommandArgs(),
				"--limit",
				"0",
			),
			want: "between 1 and 1000",
		},
		{
			name: "limit above maximum",
			args: replaceWorkerFlag(
				verifyURLsCommandArgs(),
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

func TestRealMainLoadsDatabaseConfigurationForURLVerification(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	var received workerCommand
	code := realMain(
		context.Background(),
		verifyURLsCommandArgs(),
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
			if cfg.Database.URL !=
				"postgres://paper:secret@localhost/papers" {
				t.Fatalf("database config was not loaded")
			}
			return map[string]any{
				"selected":  3,
				"verified":  2,
				"failed":    1,
				"projected": 2,
			}, nil
		},
	)
	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.Kind != commandVerifyURLs {
		t.Fatalf("received command kind = %q", received.Kind)
	}
	if !strings.Contains(stdout.String(), `"projected":2`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "secret") {
		t.Fatal("worker output leaked DATABASE_URL credentials")
	}
}

func verifyURLsCommandArgs() []string {
	return []string{
		"verify",
		"urls",
		"--policy-version",
		"official-url/v1",
		"--limit",
		"25",
	}
}
