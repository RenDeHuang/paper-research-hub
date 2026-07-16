package ingestion

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

type Service struct {
	locker                  BatchLocker
	jobs                    JobRepository
	raw                     RawRepository
	projections             ProjectionRepository
	scopePolicy             ScopePolicy
	projectionPolicy        ProjectionPolicy
	scopePolicyVersion      string
	projectionPolicyVersion string
	hooks                   []SideEffectHook
}

func NewService(
	locker BatchLocker,
	jobs JobRepository,
	raw RawRepository,
	projections ProjectionRepository,
	scopePolicy ScopePolicy,
	projectionPolicy ProjectionPolicy,
	hooks ...SideEffectHook,
) (*Service, error) {
	if interfaceIsNil(locker) {
		return nil, errors.New("ingestion batch locker is required")
	}
	if interfaceIsNil(jobs) {
		return nil, errors.New("ingestion job repository is required")
	}
	if interfaceIsNil(raw) {
		return nil, errors.New("ingestion raw repository is required")
	}
	if interfaceIsNil(projections) {
		return nil, errors.New("ingestion projection repository is required")
	}
	if interfaceIsNil(scopePolicy) {
		return nil, errors.New("ingestion scope policy is required")
	}
	if interfaceIsNil(projectionPolicy) {
		return nil, errors.New("ingestion projection policy is required")
	}

	scopePolicyVersion := strings.TrimSpace(scopePolicy.Version())
	if scopePolicyVersion == "" {
		return nil, errors.New("ingestion scope policy version is required")
	}
	projectionPolicyVersion := strings.TrimSpace(projectionPolicy.Version())
	if projectionPolicyVersion == "" {
		return nil, errors.New("ingestion projection policy version is required")
	}

	clonedHooks := make([]SideEffectHook, len(hooks))
	names := make(map[string]struct{}, len(hooks))
	for index, hook := range hooks {
		if interfaceIsNil(hook) {
			return nil, fmt.Errorf("ingestion side-effect hook %d is nil", index)
		}
		name := strings.TrimSpace(hook.Name())
		if name == "" {
			return nil, fmt.Errorf("ingestion side-effect hook %d requires a name", index)
		}
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("duplicate ingestion side-effect hook %q", name)
		}
		names[name] = struct{}{}
		clonedHooks[index] = hook
	}

	return &Service{
		locker:                  locker,
		jobs:                    jobs,
		raw:                     raw,
		projections:             projections,
		scopePolicy:             scopePolicy,
		projectionPolicy:        projectionPolicy,
		scopePolicyVersion:      scopePolicyVersion,
		projectionPolicyVersion: projectionPolicyVersion,
		hooks:                   clonedHooks,
	}, nil
}

func (service *Service) Run(
	ctx context.Context,
	pending Job,
	events EventSequence,
) (JobSummary, error) {
	if service == nil {
		return JobSummary{}, errors.New("ingestion service is nil")
	}
	if ctx == nil {
		return JobSummary{}, errors.New("ingestion context is required")
	}
	if events == nil {
		return JobSummary{}, errors.New("ingestion event sequence is required")
	}
	if err := ctx.Err(); err != nil {
		return JobSummary{}, err
	}
	if err := pending.Validate(); err != nil {
		return JobSummary{}, err
	}
	if pending.Status != JobStatusPending {
		return JobSummary{}, fmt.Errorf(
			"ingestion service requires a pending job, got %q",
			pending.Status,
		)
	}

	lease, err := service.locker.Acquire(ctx, pending.BatchKey)
	if err != nil {
		return JobSummary{}, err
	}
	if interfaceIsNil(lease) {
		return JobSummary{}, errors.New("ingestion batch locker returned a nil lease")
	}

	running, err := service.jobs.Start(ctx, pending)
	if err != nil {
		return JobSummary{}, errors.Join(
			err,
			lease.Release(context.WithoutCancel(ctx)),
		)
	}
	if err := validateStartedJob(pending, running); err != nil {
		return JobSummary{}, errors.Join(
			err,
			lease.Release(context.WithoutCancel(ctx)),
		)
	}

	current := running
	var sideEffectErr error
	for event, eventErr := range events {
		if err := ctx.Err(); err != nil {
			return service.finishFailure(
				ctx,
				current,
				lease,
				err,
				sideEffectErr,
			)
		}
		if eventErr != nil {
			batchJob, stageErr := current.AtStage(StageBatch)
			if stageErr == nil {
				current = batchJob
			}
			return service.finishFailure(
				ctx,
				current,
				lease,
				eventErr,
				sideEffectErr,
			)
		}

		clonedEvent, err := validateAndCloneEvent(event)
		if err != nil {
			return service.finishFailure(
				ctx,
				current,
				lease,
				err,
				sideEffectErr,
			)
		}
		if eventLogicalSource(clonedEvent) != running.LogicalSource {
			return service.finishFailure(
				ctx,
				current,
				lease,
				fmt.Errorf(
					"event logical source %q conflicts with job logical source %q",
					eventLogicalSource(clonedEvent),
					running.LogicalSource,
				),
				sideEffectErr,
			)
		}

		switch value := clonedEvent.(type) {
		case Envelope:
			var itemSideEffectErr error
			current, itemSideEffectErr, err = service.processUpsert(
				ctx,
				current,
				value,
			)
			sideEffectErr = errors.Join(sideEffectErr, itemSideEffectErr)
		case DeletionEnvelope:
			current, err = service.processDeletion(ctx, current, value)
		default:
			err = fmt.Errorf("unsupported ingestion event type %T", clonedEvent)
		}
		if err != nil {
			return service.finishFailure(
				ctx,
				current,
				lease,
				err,
				sideEffectErr,
			)
		}
	}

	if err := ctx.Err(); err != nil {
		return service.finishFailure(ctx, current, lease, err, sideEffectErr)
	}
	succeeded, err := current.Succeed()
	if err != nil {
		return service.finishFailure(ctx, current, lease, err, sideEffectErr)
	}
	if err := service.jobs.Complete(ctx, succeeded); err != nil {
		return service.finishFailure(ctx, current, lease, err, sideEffectErr)
	}

	cleanupCtx := context.WithoutCancel(ctx)
	summary, summaryErr := service.jobs.Summary(cleanupCtx, running.ID)
	summaryValidationErr := validateRepositorySummary(summary, running.ID)
	releaseErr := lease.Release(cleanupCtx)
	return summary, errors.Join(
		sideEffectErr,
		summaryErr,
		summaryValidationErr,
		releaseErr,
	)
}

func (service *Service) processUpsert(
	ctx context.Context,
	job Job,
	envelope Envelope,
) (Job, error, error) {
	current, err := service.setStage(ctx, job, StageRaw)
	if err != nil {
		return current, nil, err
	}
	persisted, err := service.raw.PersistRaw(ctx, current.ID, envelope.Clone())
	if err != nil {
		return current, nil, err
	}
	if err := validatePersistedRaw(persisted, current.ID, envelope); err != nil {
		return current, nil, err
	}
	if err := ctx.Err(); err != nil {
		return current, nil, err
	}

	current, err = service.setStage(ctx, current, StageNormalize)
	if err != nil {
		return current, nil, err
	}
	normalized, err := service.projections.Normalize(
		ctx,
		current.ID,
		persisted.Clone(),
		service.scopePolicyVersion,
	)
	if err != nil {
		return current, nil, err
	}
	if err := validateNormalizedRecord(normalized, persisted); err != nil {
		return current, nil, err
	}
	if err := ctx.Err(); err != nil {
		return current, nil, err
	}

	current, err = service.setStage(ctx, current, StagePolicy)
	if err != nil {
		return current, nil, err
	}
	decision, err := service.scopePolicy.Evaluate(ctx, normalized.Clone())
	if err != nil {
		return current, nil, err
	}
	if err := validateScopeDecision(decision); err != nil {
		return current, nil, err
	}
	if err := ctx.Err(); err != nil {
		return current, nil, err
	}

	if decision.Status == source.ScopeExcluded {
		current, err = service.setStage(ctx, current, StageProject)
		if err != nil {
			return current, nil, err
		}
		result, excludeErr := service.projections.Exclude(
			ctx,
			current.ID,
			normalized.Clone(),
			cloneScopeDecision(decision),
			service.scopePolicyVersion,
		)
		if excludeErr != nil {
			return current, nil, excludeErr
		}
		if err := validateProjectionResult(
			result,
			envelope.EventKey,
			ProjectionStatusExcluded,
			ProjectionStatusUnchanged,
		); err != nil {
			return current, nil, err
		}
		return current, nil, ctx.Err()
	}

	candidate, err := service.projectionPolicy.Prepare(
		ctx,
		normalized.Clone(),
		cloneScopeDecision(decision),
	)
	if err != nil {
		return current, nil, err
	}
	if err := validateProjectionCandidate(candidate, normalized); err != nil {
		return current, nil, err
	}
	if err := ctx.Err(); err != nil {
		return current, nil, err
	}

	current, err = service.setStage(ctx, current, StageProject)
	if err != nil {
		return current, nil, err
	}
	result, err := service.projections.Project(
		ctx,
		current.ID,
		candidate.Clone(),
		service.scopePolicyVersion,
		service.projectionPolicyVersion,
	)
	if err != nil {
		return current, nil, err
	}
	if err := validateProjectionResult(
		result,
		envelope.EventKey,
		ProjectionStatusProjected,
		ProjectionStatusUnchanged,
	); err != nil {
		return current, nil, err
	}
	if err := ctx.Err(); err != nil {
		return current, nil, err
	}

	return current, service.runHooks(ctx, current, envelope, result), nil
}

func (service *Service) processDeletion(
	ctx context.Context,
	job Job,
	envelope DeletionEnvelope,
) (Job, error) {
	current, err := service.setStage(ctx, job, StageRaw)
	if err != nil {
		return current, err
	}
	persisted, err := service.raw.PersistDeletion(
		ctx,
		current.ID,
		envelope.Clone(),
	)
	if err != nil {
		return current, err
	}
	if err := validatePersistedDeletion(persisted, current.ID, envelope); err != nil {
		return current, err
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}

	current, err = service.setStage(ctx, current, StageProject)
	if err != nil {
		return current, err
	}
	result, err := service.projections.ApplyDeletion(
		ctx,
		current.ID,
		persisted.Clone(),
		service.scopePolicyVersion,
		service.projectionPolicyVersion,
	)
	if err != nil {
		return current, err
	}
	if err := validateProjectionResult(
		result,
		envelope.EventKey,
		ProjectionStatusDeleted,
		ProjectionStatusUnchanged,
	); err != nil {
		return current, err
	}
	return current, ctx.Err()
}

func (service *Service) setStage(
	ctx context.Context,
	job Job,
	stage Stage,
) (Job, error) {
	if err := ctx.Err(); err != nil {
		return job, err
	}
	advanced, err := job.AtStage(stage)
	if err != nil {
		return job, err
	}
	if err := service.jobs.SetStage(ctx, advanced); err != nil {
		return advanced, err
	}
	return advanced, nil
}

func (service *Service) runHooks(
	ctx context.Context,
	job Job,
	envelope Envelope,
	result ProjectionResult,
) error {
	var joined error
	for _, hook := range service.hooks {
		if err := ctx.Err(); err != nil {
			return errors.Join(joined, err)
		}
		hookErr := hook.Run(ctx, CoreResult{
			JobID:         job.ID,
			Kind:          EventKindUpsert,
			LogicalSource: envelope.LogicalSource,
			EventKey:      envelope.EventKey,
			Projection:    result,
		})
		if hookErr == nil {
			continue
		}
		sideEffectErr := &SideEffectError{
			Hook:     strings.TrimSpace(hook.Name()),
			EventKey: envelope.EventKey,
			Cause:    hookErr,
		}
		failure := SideEffectFailure{
			Hook:          strings.TrimSpace(hook.Name()),
			LogicalSource: envelope.LogicalSource,
			EventKey:      envelope.EventKey,
			Stage:         StageProject,
			Message:       hookErr.Error(),
		}
		recordErr := failure.Validate()
		if recordErr == nil {
			recordErr = service.jobs.RecordSideEffectFailure(
				context.WithoutCancel(ctx),
				job.ID,
				failure,
			)
		}
		joined = errors.Join(joined, sideEffectErr, recordErr)
	}
	return joined
}

func (service *Service) finishFailure(
	ctx context.Context,
	running Job,
	lease BatchLease,
	primaryErr error,
	sideEffectErr error,
) (JobSummary, error) {
	cleanupCtx := context.WithoutCancel(ctx)
	terminal := Job{}
	transitionErr := error(nil)
	if errors.Is(primaryErr, context.Canceled) ||
		errors.Is(primaryErr, context.DeadlineExceeded) {
		terminal, transitionErr = running.Cancel()
	} else {
		terminal, transitionErr = running.Fail()
	}

	var failErr error
	if transitionErr == nil {
		failErr = service.jobs.Fail(cleanupCtx, terminal, primaryErr)
	}
	summary, summaryErr := service.jobs.Summary(cleanupCtx, running.ID)
	summaryValidationErr := validateRepositorySummary(summary, running.ID)
	releaseErr := lease.Release(cleanupCtx)
	return summary, errors.Join(
		primaryErr,
		sideEffectErr,
		transitionErr,
		failErr,
		summaryErr,
		summaryValidationErr,
		releaseErr,
	)
}

func validateStartedJob(pending, running Job) error {
	if err := running.Validate(); err != nil {
		return fmt.Errorf("job repository returned invalid running job: %w", err)
	}
	if running.Status != JobStatusRunning {
		return fmt.Errorf(
			"job repository returned status %q, want running",
			running.Status,
		)
	}
	if running.BatchKey != pending.BatchKey ||
		running.LogicalSource != pending.LogicalSource ||
		running.IdempotencyKey != pending.IdempotencyKey {
		return errors.New("job repository changed immutable job identity")
	}
	return nil
}

func validateAndCloneEvent(event Event) (Event, error) {
	if interfaceIsNil(event) {
		return nil, errors.New("ingestion event is nil")
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	switch value := event.cloneEvent().(type) {
	case Envelope:
		return value, nil
	case DeletionEnvelope:
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported ingestion event type %T", event)
	}
}

func eventLogicalSource(event Event) string {
	switch value := event.(type) {
	case Envelope:
		return value.LogicalSource
	case DeletionEnvelope:
		return value.LogicalSource
	default:
		return ""
	}
}

func validatePersistedRaw(
	persisted PersistedRaw,
	jobID string,
	envelope Envelope,
) error {
	if err := persisted.Validate(); err != nil {
		return fmt.Errorf("raw repository returned invalid persistence result: %w", err)
	}
	if persisted.JobID != jobID ||
		persisted.Envelope.LogicalSource != envelope.LogicalSource ||
		persisted.Envelope.EventKey != envelope.EventKey ||
		!persisted.Envelope.SourceTime.Equal(envelope.SourceTime) ||
		persisted.Envelope.TieBreakKey != envelope.TieBreakKey ||
		persisted.Envelope.Position != envelope.Position {
		return errors.New("raw repository changed immutable event identity")
	}
	return nil
}

func validatePersistedDeletion(
	persisted PersistedDeletion,
	jobID string,
	envelope DeletionEnvelope,
) error {
	if err := persisted.Validate(); err != nil {
		return fmt.Errorf("raw repository returned invalid deletion result: %w", err)
	}
	if persisted.JobID != jobID ||
		persisted.Envelope.LogicalSource != envelope.LogicalSource ||
		persisted.Envelope.EventKey != envelope.EventKey ||
		!persisted.Envelope.SourceTime.Equal(envelope.SourceTime) ||
		persisted.Envelope.TieBreakKey != envelope.TieBreakKey ||
		persisted.Envelope.Position != envelope.Position {
		return errors.New("raw repository changed immutable deletion identity")
	}
	return nil
}

func validateNormalizedRecord(
	normalized NormalizedRecord,
	persisted PersistedRaw,
) error {
	if err := normalized.Validate(); err != nil {
		return fmt.Errorf("projection repository returned invalid normalization: %w", err)
	}
	if normalized.RawID != persisted.ID ||
		normalized.JobID != persisted.JobID ||
		normalized.LogicalSource != persisted.Envelope.LogicalSource ||
		normalized.EventKey != persisted.Envelope.EventKey ||
		!normalized.SourceTime.Equal(persisted.Envelope.SourceTime) ||
		normalized.TieBreakKey != persisted.Envelope.TieBreakKey ||
		normalized.Position != persisted.Envelope.Position {
		return errors.New("normalization changed immutable event identity")
	}
	return nil
}

func validateScopeDecision(decision source.ScopeDecision) error {
	switch decision.Status {
	case source.ScopeIncluded, source.ScopeExcluded:
	default:
		return fmt.Errorf(
			"ingestion scope policy returned nonterminal status %q",
			decision.Status,
		)
	}
	if strings.TrimSpace(decision.Reason) == "" {
		return errors.New("ingestion scope policy decision requires a reason")
	}
	return nil
}

func validateProjectionCandidate(
	candidate ProjectionCandidate,
	normalized NormalizedRecord,
) error {
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("projection policy returned invalid candidate: %w", err)
	}
	if candidate.RawID != normalized.RawID ||
		candidate.JobID != normalized.JobID ||
		candidate.LogicalSource != normalized.LogicalSource ||
		candidate.EventKey != normalized.EventKey ||
		!candidate.SourceTime.Equal(normalized.SourceTime) ||
		candidate.TieBreakKey != normalized.TieBreakKey ||
		candidate.Position != normalized.Position {
		return errors.New("projection policy changed immutable event identity")
	}
	return nil
}

func validateProjectionResult(
	result ProjectionResult,
	eventKey string,
	allowed ...ProjectionStatus,
) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("projection repository returned invalid result: %w", err)
	}
	if result.EventKey != eventKey {
		return fmt.Errorf(
			"projection result event key %q conflicts with %q",
			result.EventKey,
			eventKey,
		)
	}
	for _, status := range allowed {
		if result.Status == status {
			return nil
		}
	}
	return fmt.Errorf(
		"projection result status %q is invalid for event %q",
		result.Status,
		eventKey,
	)
}

func validateRepositorySummary(summary JobSummary, jobID string) error {
	if err := summary.Validate(); err != nil {
		return fmt.Errorf("job repository returned invalid summary: %w", err)
	}
	if summary.JobID != jobID {
		return fmt.Errorf(
			"job repository summary ID %q conflicts with job %q",
			summary.JobID,
			jobID,
		)
	}
	return nil
}

func cloneScopeDecision(decision source.ScopeDecision) source.ScopeDecision {
	return source.ScopeDecision{
		Status:   decision.Status,
		Reason:   decision.Reason,
		Evidence: append([]source.FieldEvidence(nil), decision.Evidence...),
	}
}

func interfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
