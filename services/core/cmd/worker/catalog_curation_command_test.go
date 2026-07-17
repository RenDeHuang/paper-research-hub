package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

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
		"journal-jif-or-q1",
		"--venue-policy-version",
		"1",
		"--subject-version",
		"biomedical-jcr-subjects/v1",
		"--jcr-import-receipt",
		"00000000-0000-0000-0000-000000000501",
	}
	tests := []struct {
		name string
		flag string
		want string
	}{
		{name: "JCR metric year", flag: "--jcr-metric-year", want: "--jcr-metric-year"},
		{name: "Venue policy name", flag: "--venue-policy-name", want: "--venue-policy-name"},
		{name: "Venue policy version", flag: "--venue-policy-version", want: "--venue-policy-version"},
		{name: "Subject version", flag: "--subject-version", want: "--subject-version"},
		{name: "JCR receipt", flag: "--jcr-import-receipt", want: "--jcr-import-receipt"},
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
			"journal-jif-or-q1",
			"--venue-policy-version",
			"1",
			"--subject-version",
			"biomedical-jcr-subjects/v1",
			"--jcr-import-receipt",
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
		func(_ context.Context, _ config.Config, command workerCommand) (map[string]any, error) {
			received = command
			return map[string]any{"status": "accepted-only"}, nil
		},
	)
	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.MetricYear != 2025 ||
		received.VenuePolicyName != "journal-jif-or-q1" ||
		received.VenuePolicyVersion != 1 ||
		received.SubjectVersion != "biomedical-jcr-subjects/v1" ||
		received.JCRReceipt != "00000000-0000-0000-0000-000000000501" {
		t.Fatalf("received Catalog curation command = %#v", received)
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
