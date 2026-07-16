package ingestion

import (
	"reflect"
	"testing"
)

func TestStageValidationIsClosed(t *testing.T) {
	t.Parallel()

	valid := []Stage{
		StageBatch,
		StageRaw,
		StageNormalize,
		StageProject,
		StagePolicy,
		StageRanking,
	}
	for _, stage := range valid {
		if !stage.Valid() {
			t.Fatalf("stage %q is invalid", stage)
		}
		if stage.String() != string(stage) {
			t.Fatalf("Stage.String() = %q, want %q", stage.String(), stage)
		}
	}
	for _, stage := range []Stage{"", "fetch", "parse", "publish"} {
		if stage.Valid() {
			t.Fatalf("undefined stage %q was accepted", stage)
		}
	}
}

func TestJobStatusValidationIsClosed(t *testing.T) {
	t.Parallel()

	valid := []JobStatus{
		JobStatusPending,
		JobStatusRunning,
		JobStatusSucceeded,
		JobStatusFailed,
		JobStatusCancelled,
	}
	for _, status := range valid {
		if !status.Valid() {
			t.Fatalf("status %q is invalid", status)
		}
		if status.String() != string(status) {
			t.Fatalf("JobStatus.String() = %q, want %q", status.String(), status)
		}
	}
	for _, status := range []JobStatus{"", "complete", "retrying"} {
		if status.Valid() {
			t.Fatalf("undefined status %q was accepted", status)
		}
	}
}

func TestNewJobCreatesPendingBatchAndDeepCopiesPayload(t *testing.T) {
	t.Parallel()

	payload := map[string]any{
		"query": "agent systems",
		"filters": map[string]any{
			"venues": []any{"S1", "S2"},
		},
		"pages": []any{
			map[string]any{"cursor": "*"},
		},
		"raw": []byte{1, 2, 3},
	}

	job, err := NewJob(
		"sync/openalex/agent-systems",
		"openalex",
		"openalex:query:agent-systems:2026-07-16",
		payload,
	)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	if job.ID != "" ||
		job.BatchKey != "sync/openalex/agent-systems" ||
		job.LogicalSource != "openalex" ||
		job.IdempotencyKey != "openalex:query:agent-systems:2026-07-16" ||
		job.Stage != StageBatch ||
		job.Status != JobStatusPending {
		t.Fatalf("Job = %#v", job)
	}

	payload["query"] = "changed"
	payload["filters"].(map[string]any)["venues"].([]any)[0] = "changed"
	payload["pages"].([]any)[0].(map[string]any)["cursor"] = "changed"
	payload["raw"].([]byte)[0] = 9

	if job.Payload["query"] != "agent systems" ||
		job.Payload["filters"].(map[string]any)["venues"].([]any)[0] != "S1" ||
		job.Payload["pages"].([]any)[0].(map[string]any)["cursor"] != "*" ||
		job.Payload["raw"].([]byte)[0] != 1 {
		t.Fatal("NewJob retained a mutable alias to payload data")
	}
	if err := job.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestNewJobRejectsInvalidIdentityOrNonJSONPayload(t *testing.T) {
	t.Parallel()

	validPayload := map[string]any{}
	tests := []struct {
		name           string
		batchKey       string
		logicalSource  string
		idempotencyKey string
		payload        map[string]any
	}{
		{
			name:           "missing batch key",
			logicalSource:  "openalex",
			idempotencyKey: "key",
			payload:        validPayload,
		},
		{
			name:           "missing logical source",
			batchKey:       "batch",
			idempotencyKey: "key",
			payload:        validPayload,
		},
		{
			name:          "missing idempotency key",
			batchKey:      "batch",
			logicalSource: "openalex",
			payload:       validPayload,
		},
		{
			name:           "nil payload",
			batchKey:       "batch",
			logicalSource:  "openalex",
			idempotencyKey: "key",
		},
		{
			name:           "unsupported payload",
			batchKey:       "batch",
			logicalSource:  "openalex",
			idempotencyKey: "key",
			payload:        map[string]any{"channel": make(chan struct{})},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := NewJob(
				tt.batchKey,
				tt.logicalSource,
				tt.idempotencyKey,
				tt.payload,
			); err == nil {
				t.Fatalf("NewJob() = %#v, want error", got)
			}
		})
	}
}

func TestJobTransitionsStrictlyThroughRunningToTerminalState(t *testing.T) {
	t.Parallel()

	pending, err := NewJob("batch", "openalex", "key", map[string]any{"query": "agents"})
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	running, err := pending.Start("job-1")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if running.ID != "job-1" || running.Status != JobStatusRunning || running.Stage != StageBatch {
		t.Fatalf("running Job = %#v", running)
	}

	normalizing, err := running.AtStage(StageNormalize)
	if err != nil {
		t.Fatalf("AtStage(normalize) error = %v", err)
	}
	if normalizing.Stage != StageNormalize || normalizing.Status != JobStatusRunning {
		t.Fatalf("normalizing Job = %#v", normalizing)
	}
	if running.Stage != StageBatch {
		t.Fatal("AtStage mutated its receiver")
	}

	succeeded, err := normalizing.Succeed()
	if err != nil {
		t.Fatalf("Succeed() error = %v", err)
	}
	if succeeded.Status != JobStatusSucceeded || succeeded.Stage != StageNormalize {
		t.Fatalf("succeeded Job = %#v", succeeded)
	}
	if _, err := succeeded.AtStage(StageProject); err == nil {
		t.Fatal("terminal job advanced to another stage")
	}
	if _, err := succeeded.Fail(); err == nil {
		t.Fatal("succeeded job transitioned to failed")
	}

	failed, err := running.Fail()
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if failed.Status != JobStatusFailed {
		t.Fatalf("failed status = %q", failed.Status)
	}

	cancelled, err := running.Cancel()
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if cancelled.Status != JobStatusCancelled {
		t.Fatalf("cancelled status = %q", cancelled.Status)
	}
}

func TestJobTransitionsRejectInvalidOriginsIDsAndStages(t *testing.T) {
	t.Parallel()

	pending, err := NewJob("batch", "openalex", "key", map[string]any{})
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	if _, err := pending.Start(" "); err == nil {
		t.Fatal("Start() accepted an empty job ID")
	}
	if _, err := pending.AtStage(StageRaw); err == nil {
		t.Fatal("pending job advanced to raw stage")
	}
	if _, err := pending.Succeed(); err == nil {
		t.Fatal("pending job succeeded without running")
	}
	if _, err := pending.Fail(); err == nil {
		t.Fatal("pending job failed without running")
	}
	if _, err := pending.Cancel(); err == nil {
		t.Fatal("pending job cancelled without running")
	}

	running, err := pending.Start("job-1")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := running.Start("job-2"); err == nil {
		t.Fatal("running job started twice")
	}
	if _, err := running.AtStage(Stage("fetch")); err == nil {
		t.Fatal("running job accepted an undefined stage")
	}
}

func TestRestoreJobValidatesPersistedStateAndClonesPayload(t *testing.T) {
	t.Parallel()

	payload := map[string]any{"nested": map[string]any{"key": "value"}}
	job, err := RestoreJob(JobState{
		ID:             "job-1",
		BatchKey:       "batch",
		LogicalSource:  "openalex",
		IdempotencyKey: "key",
		Stage:          StageProject,
		Status:         JobStatusRunning,
		Payload:        payload,
	})
	if err != nil {
		t.Fatalf("RestoreJob() error = %v", err)
	}
	payload["nested"].(map[string]any)["key"] = "changed"
	if got := job.Payload["nested"].(map[string]any)["key"]; got != "value" {
		t.Fatalf("restored payload value = %v, want value", got)
	}

	invalid := []JobState{
		{
			BatchKey:       "batch",
			LogicalSource:  "openalex",
			IdempotencyKey: "key",
			Stage:          StageBatch,
			Status:         JobStatusRunning,
			Payload:        map[string]any{},
		},
		{
			ID:             "job-1",
			BatchKey:       "batch",
			LogicalSource:  "openalex",
			IdempotencyKey: "key",
			Stage:          Stage("fetch"),
			Status:         JobStatusRunning,
			Payload:        map[string]any{},
		},
		{
			ID:             "job-1",
			BatchKey:       "batch",
			LogicalSource:  "openalex",
			IdempotencyKey: "key",
			Stage:          StageBatch,
			Status:         JobStatus("complete"),
			Payload:        map[string]any{},
		},
	}
	for _, state := range invalid {
		if got, restoreErr := RestoreJob(state); restoreErr == nil {
			t.Fatalf("RestoreJob(%#v) = %#v, want error", state, got)
		}
	}
}

func TestNewJobSummaryRequiresDurableIdentityStatusAndNonnegativeCounts(t *testing.T) {
	t.Parallel()

	counts := JobSummaryCounts{
		RawInserted: 3,
		RawReused:   2,
		Projected:   2,
		Excluded:    1,
		Deleted:     1,
		Unchanged:   1,
		Failed:      0,
	}
	summary, err := NewJobSummary("job-1", JobStatusSucceeded, counts)
	if err != nil {
		t.Fatalf("NewJobSummary() error = %v", err)
	}
	if summary.JobID != "job-1" ||
		summary.Status != JobStatusSucceeded ||
		summary.RawInserted != 3 ||
		summary.RawReused != 2 ||
		summary.Projected != 2 ||
		summary.Excluded != 1 ||
		summary.Deleted != 1 ||
		summary.Unchanged != 1 ||
		summary.Failed != 0 {
		t.Fatalf("JobSummary = %#v", summary)
	}
	if err := summary.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for _, tt := range []struct {
		name   string
		jobID  string
		status JobStatus
		counts JobSummaryCounts
	}{
		{name: "missing job ID", status: JobStatusSucceeded, counts: counts},
		{name: "invalid status", jobID: "job-1", status: JobStatus("complete"), counts: counts},
		{
			name:   "negative raw inserted",
			jobID:  "job-1",
			status: JobStatusFailed,
			counts: JobSummaryCounts{RawInserted: -1},
		},
		{
			name:   "negative failed",
			jobID:  "job-1",
			status: JobStatusFailed,
			counts: JobSummaryCounts{Failed: -1},
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, summaryErr := NewJobSummary(tt.jobID, tt.status, tt.counts); summaryErr == nil {
				t.Fatalf("NewJobSummary() = %#v, want error", got)
			}
		})
	}
}

func TestJobPayloadClonePreservesSupportedJSONShape(t *testing.T) {
	t.Parallel()

	payload := map[string]any{
		"nil":     nil,
		"bool":    true,
		"string":  "value",
		"integer": int64(42),
		"float":   1.25,
		"array":   []any{"a", int64(2)},
		"object":  map[string]any{"x": false},
		"bytes":   []byte{1, 2},
	}
	job, err := NewJob("batch", "openalex", "key", payload)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	if !reflect.DeepEqual(job.Payload, payload) {
		t.Fatalf("Payload = %#v, want %#v", job.Payload, payload)
	}
}
