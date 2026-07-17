package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
)

func TestParseWorkerCommandRequiresExplicitVenueAssessmentInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing metric year",
			args: []string{
				"assess", "venues",
				"--policy-version", "journal-jif-or-q1/v1",
				"--assessed-at", "2026-07-17T09:00:00Z",
				"--jcr-receipt", "00000000-0000-0000-0000-000000000501",
			},
			want: "--metric-year",
		},
		{
			name: "unsupported policy version",
			args: []string{
				"assess", "venues",
				"--metric-year", "2025",
				"--policy-version", "journal-jif-or-q1/v2",
				"--assessed-at", "2026-07-17T09:00:00Z",
				"--jcr-receipt", "00000000-0000-0000-0000-000000000501",
			},
			want: "journal-jif-or-q1/v1",
		},
		{
			name: "missing assessed at",
			args: []string{
				"assess", "venues",
				"--metric-year", "2025",
				"--policy-version", "journal-jif-or-q1/v1",
				"--jcr-receipt", "00000000-0000-0000-0000-000000000501",
			},
			want: "--assessed-at",
		},
		{
			name: "invalid assessed at",
			args: []string{
				"assess", "venues",
				"--metric-year", "2025",
				"--policy-version", "journal-jif-or-q1/v1",
				"--assessed-at", "2026-07-17",
				"--jcr-receipt", "00000000-0000-0000-0000-000000000501",
			},
			want: "RFC3339Nano",
		},
		{
			name: "missing receipt",
			args: []string{
				"assess", "venues",
				"--metric-year", "2025",
				"--policy-version", "journal-jif-or-q1/v1",
				"--assessed-at", "2026-07-17T09:00:00Z",
			},
			want: "--jcr-receipt",
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

func TestRealMainParsesVenueAssessmentCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var received workerCommand
	code := realMain(
		context.Background(),
		[]string{
			"assess",
			"venues",
			"--metric-year",
			"2025",
			"--policy-version",
			"journal-jif-or-q1/v1",
			"--assessed-at",
			"2026-07-17T09:00:00.123456789Z",
			"--jcr-receipt",
			"00000000-0000-0000-0000-000000000501",
		},
		&stdout,
		&stderr,
		func(key string) (string, bool) {
			if key == "DATABASE_URL" {
				return "postgres://paper:secret@localhost/papers", true
			}
			return "", false
		},
		func(_ context.Context, cfg config.Config, command workerCommand) (map[string]any, error) {
			received = command
			if cfg.Database.URL == "" {
				t.Fatal("Venue assessment did not load database-only configuration")
			}
			return map[string]any{
				"total":          5,
				"accepted":       2,
				"rejected":       1,
				"unknown":        1,
				"not_applicable": 1,
			}, nil
		},
	)
	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.Kind != commandAssessVenues ||
		received.MetricYear != 2025 ||
		received.PolicyVersion != "journal-jif-or-q1/v1" ||
		received.JCRReceipt != "00000000-0000-0000-0000-000000000501" ||
		!received.AssessedAt.Equal(
			time.Date(2026, time.July, 17, 9, 0, 0, 123456789, time.UTC),
		) {
		t.Fatalf("received Venue assessment command = %#v", received)
	}
	for _, fragment := range []string{
		`"total":5`,
		`"accepted":2`,
		`"not_applicable":1`,
	} {
		if !strings.Contains(stdout.String(), fragment) {
			t.Fatalf("stdout = %q, want containing %q", stdout.String(), fragment)
		}
	}
}
