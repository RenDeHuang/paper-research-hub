package ingestion

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
)

type Stage string

const (
	StageBatch     Stage = "batch"
	StageRaw       Stage = "raw"
	StageNormalize Stage = "normalize"
	StageProject   Stage = "project"
	StagePolicy    Stage = "policy"
	StageRanking   Stage = "ranking"
)

func (stage Stage) Valid() bool {
	switch stage {
	case StageBatch,
		StageRaw,
		StageNormalize,
		StageProject,
		StagePolicy,
		StageRanking:
		return true
	default:
		return false
	}
}

func (stage Stage) String() string {
	return string(stage)
}

type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusSucceeded JobStatus = "succeeded"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

func (status JobStatus) Valid() bool {
	switch status {
	case JobStatusPending,
		JobStatusRunning,
		JobStatusSucceeded,
		JobStatusFailed,
		JobStatusCancelled:
		return true
	default:
		return false
	}
}

func (status JobStatus) String() string {
	return string(status)
}

func (status JobStatus) terminal() bool {
	switch status {
	case JobStatusSucceeded, JobStatusFailed, JobStatusCancelled:
		return true
	default:
		return false
	}
}

type Job struct {
	ID             string
	BatchKey       string
	LogicalSource  string
	IdempotencyKey string
	Stage          Stage
	Status         JobStatus
	Payload        map[string]any
}

type JobState = Job

func NewJob(
	batchKey string,
	logicalSource string,
	idempotencyKey string,
	payload map[string]any,
) (Job, error) {
	clonedPayload, err := clonePayload(payload)
	if err != nil {
		return Job{}, err
	}
	job := Job{
		BatchKey:       strings.TrimSpace(batchKey),
		LogicalSource:  strings.TrimSpace(logicalSource),
		IdempotencyKey: strings.TrimSpace(idempotencyKey),
		Stage:          StageBatch,
		Status:         JobStatusPending,
		Payload:        clonedPayload,
	}
	if err := job.Validate(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func RestoreJob(state JobState) (Job, error) {
	payload, err := clonePayload(state.Payload)
	if err != nil {
		return Job{}, err
	}
	job := Job{
		ID:             strings.TrimSpace(state.ID),
		BatchKey:       strings.TrimSpace(state.BatchKey),
		LogicalSource:  strings.TrimSpace(state.LogicalSource),
		IdempotencyKey: strings.TrimSpace(state.IdempotencyKey),
		Stage:          state.Stage,
		Status:         state.Status,
		Payload:        payload,
	}
	if err := job.Validate(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (job Job) Validate() error {
	if strings.TrimSpace(job.BatchKey) == "" {
		return errors.New("ingestion job batch key is required")
	}
	if strings.TrimSpace(job.LogicalSource) == "" {
		return errors.New("ingestion job logical source is required")
	}
	if strings.TrimSpace(job.IdempotencyKey) == "" {
		return errors.New("ingestion job idempotency key is required")
	}
	if !job.Stage.Valid() {
		return fmt.Errorf("invalid ingestion job stage %q", job.Stage)
	}
	if !job.Status.Valid() {
		return fmt.Errorf("invalid ingestion job status %q", job.Status)
	}
	if job.Status != JobStatusPending && strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("ingestion job with status %q requires an ID", job.Status)
	}
	if _, err := clonePayload(job.Payload); err != nil {
		return err
	}
	return nil
}

func (job Job) Start(id string) (Job, error) {
	if err := job.Validate(); err != nil {
		return Job{}, err
	}
	if job.Status != JobStatusPending {
		return Job{}, fmt.Errorf("ingestion job cannot start from status %q", job.Status)
	}
	normalizedID := strings.TrimSpace(id)
	if normalizedID == "" {
		return Job{}, errors.New("started ingestion job requires an ID")
	}
	if job.ID != "" && job.ID != normalizedID {
		return Job{}, fmt.Errorf(
			"persisted ingestion job ID %q conflicts with start ID %q",
			job.ID,
			normalizedID,
		)
	}
	started, err := cloneJob(job)
	if err != nil {
		return Job{}, err
	}
	started.ID = normalizedID
	started.Status = JobStatusRunning
	return started, nil
}

func (job Job) AtStage(stage Stage) (Job, error) {
	if err := job.Validate(); err != nil {
		return Job{}, err
	}
	if job.Status != JobStatusRunning {
		return Job{}, fmt.Errorf(
			"ingestion job cannot change stage from status %q",
			job.Status,
		)
	}
	if !stage.Valid() {
		return Job{}, fmt.Errorf("invalid ingestion job stage %q", stage)
	}
	advanced, err := cloneJob(job)
	if err != nil {
		return Job{}, err
	}
	advanced.Stage = stage
	return advanced, nil
}

func (job Job) Succeed() (Job, error) {
	return job.finish(JobStatusSucceeded)
}

func (job Job) Fail() (Job, error) {
	return job.finish(JobStatusFailed)
}

func (job Job) Cancel() (Job, error) {
	return job.finish(JobStatusCancelled)
}

func (job Job) finish(status JobStatus) (Job, error) {
	if err := job.Validate(); err != nil {
		return Job{}, err
	}
	if job.Status != JobStatusRunning {
		return Job{}, fmt.Errorf(
			"ingestion job cannot transition from status %q to %q",
			job.Status,
			status,
		)
	}
	if !status.terminal() {
		return Job{}, fmt.Errorf("ingestion job terminal status %q is invalid", status)
	}
	finished, err := cloneJob(job)
	if err != nil {
		return Job{}, err
	}
	finished.Status = status
	return finished, nil
}

func cloneJob(job Job) (Job, error) {
	payload, err := clonePayload(job.Payload)
	if err != nil {
		return Job{}, err
	}
	job.Payload = payload
	return job, nil
}

type JobSummaryCounts struct {
	RawInserted int64
	RawReused   int64
	Projected   int64
	Excluded    int64
	Deleted     int64
	Unchanged   int64
	Failed      int64
}

type JobSummary struct {
	JobID       string
	Status      JobStatus
	RawInserted int64
	RawReused   int64
	Projected   int64
	Excluded    int64
	Deleted     int64
	Unchanged   int64
	Failed      int64
}

func NewJobSummary(
	jobID string,
	status JobStatus,
	counts JobSummaryCounts,
) (JobSummary, error) {
	summary := JobSummary{
		JobID:       strings.TrimSpace(jobID),
		Status:      status,
		RawInserted: counts.RawInserted,
		RawReused:   counts.RawReused,
		Projected:   counts.Projected,
		Excluded:    counts.Excluded,
		Deleted:     counts.Deleted,
		Unchanged:   counts.Unchanged,
		Failed:      counts.Failed,
	}
	if err := summary.Validate(); err != nil {
		return JobSummary{}, err
	}
	return summary, nil
}

func (summary JobSummary) Validate() error {
	if strings.TrimSpace(summary.JobID) == "" {
		return errors.New("ingestion job summary requires a job ID")
	}
	if !summary.Status.Valid() {
		return fmt.Errorf("invalid ingestion job summary status %q", summary.Status)
	}
	counts := []struct {
		name  string
		value int64
	}{
		{name: "RawInserted", value: summary.RawInserted},
		{name: "RawReused", value: summary.RawReused},
		{name: "Projected", value: summary.Projected},
		{name: "Excluded", value: summary.Excluded},
		{name: "Deleted", value: summary.Deleted},
		{name: "Unchanged", value: summary.Unchanged},
		{name: "Failed", value: summary.Failed},
	}
	for _, count := range counts {
		if count.value < 0 {
			return fmt.Errorf(
				"ingestion job summary %s must not be negative",
				count.name,
			)
		}
	}
	return nil
}

func clonePayload(payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, errors.New("ingestion job payload must be a JSON object")
	}
	cloned, err := cloneJSONValue(reflect.ValueOf(payload), make(map[jsonVisit]struct{}))
	if err != nil {
		return nil, fmt.Errorf("clone ingestion job payload: %w", err)
	}
	result, ok := cloned.Interface().(map[string]any)
	if !ok {
		return nil, errors.New("ingestion job payload must be a JSON object")
	}
	return result, nil
}

type jsonVisit struct {
	kind reflect.Kind
	ptr  uintptr
}

func cloneJSONValue(
	value reflect.Value,
	active map[jsonVisit]struct{},
) (reflect.Value, error) {
	if !value.IsValid() {
		return reflect.Value{}, nil
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneJSONValue(value.Elem(), active)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result, nil

	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf(
				"map type %s must use string keys",
				value.Type(),
			)
		}
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		visit := jsonVisit{kind: value.Kind(), ptr: value.Pointer()}
		if _, exists := active[visit]; exists {
			return reflect.Value{}, errors.New("cyclic map is not valid JSON")
		}
		active[visit] = struct{}{}
		defer delete(active, visit)

		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			item, err := cloneJSONValue(iterator.Value(), active)
			if err != nil {
				return reflect.Value{}, err
			}
			cloned.SetMapIndex(iterator.Key(), item)
		}
		return cloned, nil

	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		visit := jsonVisit{kind: value.Kind(), ptr: value.Pointer()}
		if _, exists := active[visit]; exists {
			return reflect.Value{}, errors.New("cyclic slice is not valid JSON")
		}
		active[visit] = struct{}{}
		defer delete(active, visit)

		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := range value.Len() {
			item, err := cloneJSONValue(value.Index(index), active)
			if err != nil {
				return reflect.Value{}, err
			}
			cloned.Index(index).Set(item)
		}
		return cloned, nil

	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for index := range value.Len() {
			item, err := cloneJSONValue(value.Index(index), active)
			if err != nil {
				return reflect.Value{}, err
			}
			cloned.Index(index).Set(item)
		}
		return cloned, nil

	case reflect.Bool,
		reflect.String,
		reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64,
		reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64:
		return value, nil

	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return reflect.Value{}, errors.New("non-finite number is not valid JSON")
		}
		return value, nil

	default:
		return reflect.Value{}, fmt.Errorf(
			"value of type %s is not valid ingestion job JSON",
			value.Type(),
		)
	}
}
