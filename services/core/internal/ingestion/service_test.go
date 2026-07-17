package ingestion

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

func TestServiceProcessesUpsertInStrictCommittedStageOrder(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	envelope := testEnvelope(t, source.ScopeIncluded)

	summary, err := fixture.service.Run(
		context.Background(),
		testJob(t),
		eventSequence(eventItem{event: envelope}),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Status != JobStatusSucceeded ||
		summary.RawInserted != 1 ||
		summary.Projected != 1 ||
		summary.Failed != 0 {
		t.Fatalf("summary = %#v", summary)
	}

	want := []string{
		"lock:sync/openalex/agents",
		"job:start",
		"job:stage:raw",
		"raw:persist:openalex:W1",
		"job:stage:normalize",
		"projection:normalize:scope/v2",
		"job:stage:policy",
		"scope:evaluate",
		"projection-policy:prepare",
		"job:stage:project",
		"projection:project:scope/v2:projection/v3",
		"job:complete",
		"job:summary",
		"lease:release",
	}
	if !slices.Equal(fixture.state.calls, want) {
		t.Fatalf("calls =\n%s\nwant\n%s", strings.Join(fixture.state.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestServicePreservesRawCommitWhenProjectionFailsAndReturnsDurableSummary(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	projectErr := errors.New("projection transaction failed")
	fixture.projections.projectErr = projectErr

	summary, err := fixture.service.Run(
		context.Background(),
		testJob(t),
		eventSequence(eventItem{event: testEnvelope(t, source.ScopeIncluded)}),
	)
	if !errors.Is(err, projectErr) {
		t.Fatalf("Run() error = %v, want projection error", err)
	}
	if summary.Status != JobStatusFailed ||
		summary.RawInserted != 1 ||
		summary.Projected != 0 ||
		summary.Failed != 1 {
		t.Fatalf("summary = %#v, want durable raw commit and failed job", summary)
	}
	if fixture.raw.rollbackCalls != 0 || fixture.projections.rollbackCalls != 0 {
		t.Fatal("service attempted to roll back an earlier committed stage")
	}

	wantSuffix := []string{
		"job:stage:project",
		"projection:project:scope/v2:projection/v3",
		"job:fail:project",
		"job:summary",
		"lease:release",
	}
	if !slices.Equal(fixture.state.calls[len(fixture.state.calls)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("call suffix = %#v, want %#v", fixture.state.calls, wantSuffix)
	}
}

func TestServiceKeepsEarlierDurableItemsWhenIteratorLaterFails(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	iteratorErr := errors.New("page 2 malformed after record 1")
	fixture.jobs.summaryOverride = &JobSummary{
		JobID:       "job-1",
		Status:      JobStatusFailed,
		RawInserted: 41,
		RawReused:   2,
		Projected:   37,
		Excluded:    3,
		Deleted:     1,
		Unchanged:   2,
		Failed:      1,
	}

	summary, err := fixture.service.Run(
		context.Background(),
		testJob(t),
		eventSequence(
			eventItem{event: testEnvelope(t, source.ScopeIncluded)},
			eventItem{err: iteratorErr},
		),
	)
	if !errors.Is(err, iteratorErr) {
		t.Fatalf("Run() error = %v, want iterator error", err)
	}
	if summary != *fixture.jobs.summaryOverride {
		t.Fatalf("summary = %#v, want repository summary %#v", summary, *fixture.jobs.summaryOverride)
	}
	if fixture.jobs.summaryCalls != 1 {
		t.Fatalf("Summary() calls = %d, want 1", fixture.jobs.summaryCalls)
	}
	if !slices.Contains(fixture.state.calls, "projection:project:scope/v2:projection/v3") {
		t.Fatal("first item did not reach its durable projection before iterator failure")
	}
}

func TestServiceRecordsExcludedWithoutCallingProject(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	envelope := testEnvelope(t, source.ScopeExcluded)

	summary, err := fixture.service.Run(
		context.Background(),
		testJob(t),
		eventSequence(eventItem{event: envelope}),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Excluded != 1 || summary.Projected != 0 {
		t.Fatalf("summary = %#v, want one exclusion and no projection", summary)
	}
	if fixture.projections.projectCalls != 0 {
		t.Fatalf("Project() calls = %d, want 0", fixture.projections.projectCalls)
	}
	if fixture.projectionPolicy.calls != 0 {
		t.Fatalf("ProjectionPolicy.Prepare() calls = %d, want 0", fixture.projectionPolicy.calls)
	}
	if fixture.projections.excludeCalls != 1 {
		t.Fatalf("Exclude() calls = %d, want 1", fixture.projections.excludeCalls)
	}
}

func TestServiceAllowsRepositoryToCanonicalizeIdentifierOnlyRecord(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	fixture.projections.projectCanonicalKey = "pmid:12345678"
	raw := mustRawRecord(t, `{"pmid":"12345678"}`)
	record := source.Record{
		Source:         source.PubMed,
		SourceRecordID: "12345678",
		Identifiers: []source.Identifier{{
			Scheme: source.IdentifierPMID,
			Value:  "12345678",
		}},
		Raw:   raw,
		Title: "Identifier-only PubMed record",
		Scope: source.ScopeDecision{
			Status: source.ScopeIncluded,
			Reason: "fixture",
		},
	}
	envelope, err := NewEnvelope(
		source.PubMed,
		"pubmed:12345678",
		time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC),
		"history/00000001",
		1,
		record,
		raw,
	)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}

	summary, err := fixture.service.Run(
		context.Background(),
		testJobForSource(t, source.PubMed),
		eventSequence(eventItem{event: envelope}),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Projected != 1 || fixture.projections.projectCalls != 1 {
		t.Fatalf("summary = %#v, Project() calls = %d", summary, fixture.projections.projectCalls)
	}
}

func TestServiceDeletionSkipsNormalizeAndUsesPersistDeletionThenApplyDeletion(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	deletion := testDeletionEnvelope(t)

	summary, err := fixture.service.Run(
		context.Background(),
		testJobForSource(t, source.PubMed),
		eventSequence(eventItem{event: deletion}),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.RawInserted != 1 || summary.Deleted != 1 || summary.Projected != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if fixture.projections.normalizeCalls != 0 ||
		fixture.scope.calls != 0 ||
		fixture.projectionPolicy.calls != 0 ||
		fixture.projections.projectCalls != 0 {
		t.Fatal("delete event entered normalize, policy, or project path")
	}

	wantCore := []string{
		"job:stage:raw",
		"raw:persist-deletion:pubmed:12345678",
		"job:stage:project",
		"projection:delete:scope/v2:projection/v3",
	}
	if !containsOrdered(fixture.state.calls, wantCore) {
		t.Fatalf("calls = %#v, missing ordered delete flow %#v", fixture.state.calls, wantCore)
	}
}

func TestServiceStopsAfterContextCancellationWithoutStartingNextStage(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.raw.afterPersist = cancel

	summary, err := fixture.service.Run(
		ctx,
		testJob(t),
		eventSequence(eventItem{event: testEnvelope(t, source.ScopeIncluded)}),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if fixture.projections.normalizeCalls != 0 {
		t.Fatalf("Normalize() calls = %d after cancellation, want 0", fixture.projections.normalizeCalls)
	}
	if summary.RawInserted != 1 || summary.Failed != 1 || summary.Status != JobStatusCancelled {
		t.Fatalf("summary = %#v, want durable raw and cancelled job", summary)
	}
	if !slices.Contains(fixture.state.calls, "job:fail:raw") ||
		fixture.state.calls[len(fixture.state.calls)-1] != "lease:release" {
		t.Fatalf("cleanup calls = %#v", fixture.state.calls)
	}
}

func TestServiceJoinsPrimaryFailAndReleaseErrors(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	primaryErr := errors.New("raw persistence failed")
	failErr := errors.New("job failure write failed")
	releaseErr := errors.New("lease release failed")
	fixture.raw.persistErr = primaryErr
	fixture.jobs.failErr = failErr
	fixture.lease.releaseErr = releaseErr

	_, err := fixture.service.Run(
		context.Background(),
		testJob(t),
		eventSequence(eventItem{event: testEnvelope(t, source.ScopeIncluded)}),
	)
	for _, target := range []error{primaryErr, failErr, releaseErr} {
		if !errors.Is(err, target) {
			t.Fatalf("Run() error = %v, want joined target %v", err, target)
		}
	}
	if !strings.HasPrefix(err.Error(), primaryErr.Error()) {
		t.Fatalf("joined error = %q, want primary error first", err)
	}
	if fixture.jobs.summaryCalls != 1 {
		t.Fatalf("Summary() calls = %d, want durable read despite fail error", fixture.jobs.summaryCalls)
	}
}

func TestServicePreservesTypedIdempotencyAndCanonicalConflicts(t *testing.T) {
	t.Parallel()

	t.Run("idempotency", func(t *testing.T) {
		t.Parallel()

		fixture := newServiceFixture(t)
		conflict := &ErrIdempotencyConflict{
			IdempotencyKey: "openalex:query:agents:2026-07-16",
			ExistingJobID:  "job-existing",
		}
		fixture.jobs.startErr = conflict

		_, err := fixture.service.Run(
			context.Background(),
			testJob(t),
			eventSequence(eventItem{event: testEnvelope(t, source.ScopeIncluded)}),
		)
		var got *ErrIdempotencyConflict
		if !errors.As(err, &got) || got != conflict {
			t.Fatalf("Run() error = %v, want original ErrIdempotencyConflict", err)
		}
	})

	t.Run("canonical", func(t *testing.T) {
		t.Parallel()

		fixture := newServiceFixture(t)
		conflict := &CanonicalConflictError{
			LogicalSource:        source.OpenAlex,
			EventKey:             "openalex:W1",
			ExistingCanonicalKey: "doi:10.1000/existing",
			IncomingCanonicalKey: "doi:10.1000/incoming",
		}
		fixture.projections.projectErr = conflict

		_, err := fixture.service.Run(
			context.Background(),
			testJob(t),
			eventSequence(eventItem{event: testEnvelope(t, source.ScopeIncluded)}),
		)
		var got *CanonicalConflictError
		if !errors.As(err, &got) || got != conflict {
			t.Fatalf("Run() error = %v, want original CanonicalConflictError", err)
		}
	})
}

func TestServiceRecordsHookFailureSeparatelyWithoutFailingCoreJob(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	hookErr := errors.New("publisher unavailable")
	hook := &fakeHook{name: "publisher", err: hookErr, state: fixture.state}
	service, err := NewService(
		fixture.locker,
		fixture.jobs,
		fixture.raw,
		fixture.projections,
		fixture.scope,
		fixture.projectionPolicy,
		hook,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	summary, runErr := service.Run(
		context.Background(),
		testJob(t),
		eventSequence(eventItem{event: testEnvelope(t, source.ScopeIncluded)}),
	)
	var sideEffectErr *SideEffectError
	if !errors.As(runErr, &sideEffectErr) || !errors.Is(runErr, hookErr) {
		t.Fatalf("Run() error = %v, want SideEffectError wrapping hook failure", runErr)
	}
	if summary.Status != JobStatusSucceeded ||
		summary.Projected != 1 ||
		summary.Failed != 0 {
		t.Fatalf("summary = %#v, hook failure masqueraded as core failure", summary)
	}
	if len(fixture.jobs.sideEffectFailures) != 1 {
		t.Fatalf("recorded side-effect failures = %#v", fixture.jobs.sideEffectFailures)
	}
	failure := fixture.jobs.sideEffectFailures[0]
	if failure.Hook != "publisher" ||
		failure.EventKey != "openalex:W1" ||
		failure.Stage != StageProject ||
		failure.Message != hookErr.Error() {
		t.Fatalf("side-effect failure = %#v", failure)
	}
	if !containsOrdered(fixture.state.calls, []string{
		"projection:project:scope/v2:projection/v3",
		"hook:publisher",
		"job:side-effect-failure:publisher",
		"job:complete",
	}) {
		t.Fatalf("calls = %#v, hook failure did not remain outside core transaction", fixture.state.calls)
	}
}

func TestServiceReceivesConstructorIsolatedInput(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	raw := mustRawRecord(t, `{"id":"W1","title":"original"}`)
	record := source.Record{
		Source:         source.OpenAlex,
		SourceRecordID: "W1",
		Raw:            raw,
		Title:          "original",
		CodeURLs:       []string{"https://code.invalid/original"},
		Scope: source.ScopeDecision{
			Status: source.ScopeIncluded,
			Reason: "fixture",
		},
	}
	identity, err := paper.NewIdentifier(paper.SchemeOpenAlex, "W1")
	if err != nil {
		t.Fatalf("paper.NewIdentifier() error = %v", err)
	}
	record.Identity = identity
	envelope, err := NewEnvelope(
		source.OpenAlex,
		"openalex:W1",
		time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC),
		"1",
		1,
		record,
		raw,
	)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}

	raw.Payload[0] = '['
	record.Raw.Payload[1] = '!'
	record.Title = "changed"
	record.CodeURLs[0] = "https://code.invalid/changed"

	_, runErr := fixture.service.Run(
		context.Background(),
		testJob(t),
		eventSequence(eventItem{event: envelope}),
	)
	if runErr != nil {
		t.Fatalf("Run() error = %v", runErr)
	}
	got := fixture.raw.persisted[0]
	if got.Record.Title != "original" ||
		got.Record.CodeURLs[0] != "https://code.invalid/original" ||
		got.Raw.Payload[0] != '{' ||
		got.Record.Raw.Payload[1] != '"' {
		t.Fatalf("PersistRaw input = %#v, retained caller aliases", got)
	}
}

func TestNewServiceRejectsMissingDependenciesAndBlankPolicyVersions(t *testing.T) {
	t.Parallel()

	fixture := newServiceFixture(t)
	tests := []struct {
		name       string
		locker     BatchLocker
		jobs       JobRepository
		raw        RawRepository
		projection ProjectionRepository
		scope      ScopePolicy
		policy     ProjectionPolicy
	}{
		{
			name:       "missing locker",
			jobs:       fixture.jobs,
			raw:        fixture.raw,
			projection: fixture.projections,
			scope:      fixture.scope,
			policy:     fixture.projectionPolicy,
		},
		{
			name:       "missing jobs",
			locker:     fixture.locker,
			raw:        fixture.raw,
			projection: fixture.projections,
			scope:      fixture.scope,
			policy:     fixture.projectionPolicy,
		},
		{
			name:       "missing raw",
			locker:     fixture.locker,
			jobs:       fixture.jobs,
			projection: fixture.projections,
			scope:      fixture.scope,
			policy:     fixture.projectionPolicy,
		},
		{
			name:   "missing projection",
			locker: fixture.locker,
			jobs:   fixture.jobs,
			raw:    fixture.raw,
			scope:  fixture.scope,
			policy: fixture.projectionPolicy,
		},
		{
			name:       "missing scope policy",
			locker:     fixture.locker,
			jobs:       fixture.jobs,
			raw:        fixture.raw,
			projection: fixture.projections,
			policy:     fixture.projectionPolicy,
		},
		{
			name:       "missing projection policy",
			locker:     fixture.locker,
			jobs:       fixture.jobs,
			raw:        fixture.raw,
			projection: fixture.projections,
			scope:      fixture.scope,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := NewService(
				tt.locker,
				tt.jobs,
				tt.raw,
				tt.projection,
				tt.scope,
				tt.policy,
			); err == nil {
				t.Fatalf("NewService() = %#v, want error", got)
			}
		})
	}

	fixture.scope.version = " "
	if got, err := NewService(
		fixture.locker,
		fixture.jobs,
		fixture.raw,
		fixture.projections,
		fixture.scope,
		fixture.projectionPolicy,
	); err == nil {
		t.Fatalf("NewService() = %#v, want blank scope version error", got)
	}
}

type eventItem struct {
	event Event
	err   error
}

func eventSequence(items ...eventItem) EventSequence {
	return iter.Seq2[Event, error](func(yield func(Event, error) bool) {
		for _, item := range items {
			if !yield(item.event, item.err) {
				return
			}
		}
	})
}

func testJob(t *testing.T) Job {
	t.Helper()

	return testJobForSource(t, source.OpenAlex)
}

func testJobForSource(t *testing.T, logicalSource string) Job {
	t.Helper()

	job, err := NewJob(
		"sync/"+logicalSource+"/agents",
		logicalSource,
		logicalSource+":query:agents:2026-07-16",
		map[string]any{"query": "agents"},
	)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	return job
}

func testEnvelope(t *testing.T, scopeStatus source.ScopeStatus) Envelope {
	t.Helper()

	raw := mustRawRecord(t, `{"id":"W1","doi":"10.1000/agents"}`)
	identity, err := paper.NewIdentifier(paper.SchemeDOI, "10.1000/agents")
	if err != nil {
		t.Fatalf("paper.NewIdentifier() error = %v", err)
	}
	record := source.Record{
		Source:         source.OpenAlex,
		SourceRecordID: "W1",
		Identity:       identity,
		Raw:            raw,
		Title:          "Agent systems",
		Scope: source.ScopeDecision{
			Status: scopeStatus,
			Reason: "fixture",
		},
	}
	envelope, err := NewEnvelope(
		source.OpenAlex,
		"openalex:W1",
		time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC),
		"page-1/item-1",
		1,
		record,
		raw,
	)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	return envelope
}

func testDeletionEnvelope(t *testing.T) DeletionEnvelope {
	t.Helper()

	envelope, err := NewDeletionEnvelope(
		source.PubMed,
		"pubmed:12345678",
		time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC),
		"pubmed26n0002.xml.gz/00000002",
		2,
		mustRawRecord(t, `{"delete":"12345678"}`),
	)
	if err != nil {
		t.Fatalf("NewDeletionEnvelope() error = %v", err)
	}
	return envelope
}

type serviceFixture struct {
	state            *fakeState
	locker           *fakeBatchLocker
	lease            *fakeBatchLease
	jobs             *fakeJobRepository
	raw              *fakeRawRepository
	projections      *fakeProjectionRepository
	scope            *fakeScopePolicy
	projectionPolicy *fakeProjectionPolicy
	service          *Service
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()

	state := &fakeState{}
	lease := &fakeBatchLease{state: state}
	locker := &fakeBatchLocker{state: state, lease: lease}
	jobs := &fakeJobRepository{state: state}
	raw := &fakeRawRepository{state: state, jobs: jobs}
	projections := &fakeProjectionRepository{state: state, jobs: jobs}
	scope := &fakeScopePolicy{state: state, version: "scope/v2"}
	projectionPolicy := &fakeProjectionPolicy{state: state, version: "projection/v3"}
	service, err := NewService(
		locker,
		jobs,
		raw,
		projections,
		scope,
		projectionPolicy,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return &serviceFixture{
		state:            state,
		locker:           locker,
		lease:            lease,
		jobs:             jobs,
		raw:              raw,
		projections:      projections,
		scope:            scope,
		projectionPolicy: projectionPolicy,
		service:          service,
	}
}

type fakeState struct {
	calls []string
}

func (state *fakeState) add(call string) {
	state.calls = append(state.calls, call)
}

type fakeBatchLocker struct {
	state *fakeState
	lease *fakeBatchLease
	err   error
}

func (locker *fakeBatchLocker) Acquire(
	_ context.Context,
	batchKey string,
) (BatchLease, error) {
	locker.state.add("lock:" + batchKey)
	if locker.err != nil {
		return nil, locker.err
	}
	return locker.lease, nil
}

type fakeBatchLease struct {
	state      *fakeState
	releaseErr error
}

func (lease *fakeBatchLease) Release(context.Context) error {
	lease.state.add("lease:release")
	return lease.releaseErr
}

type fakeJobRepository struct {
	state              *fakeState
	counts             JobSummaryCounts
	status             JobStatus
	startErr           error
	failErr            error
	completeErr        error
	summaryErr         error
	summaryOverride    *JobSummary
	summaryCalls       int
	sideEffectFailures []SideEffectFailure
	sideEffectErr      error
}

func (repository *fakeJobRepository) Start(_ context.Context, pending Job) (Job, error) {
	repository.state.add("job:start")
	if repository.startErr != nil {
		return Job{}, repository.startErr
	}
	running, err := pending.Start("job-1")
	if err != nil {
		return Job{}, err
	}
	repository.status = JobStatusRunning
	return running, nil
}

func (repository *fakeJobRepository) SetStage(_ context.Context, job Job) error {
	repository.state.add("job:stage:" + job.Stage.String())
	return nil
}

func (repository *fakeJobRepository) Complete(_ context.Context, job Job) error {
	repository.state.add("job:complete")
	if job.Status != JobStatusSucceeded {
		return fmt.Errorf("Complete() status = %q", job.Status)
	}
	if repository.completeErr != nil {
		return repository.completeErr
	}
	repository.status = JobStatusSucceeded
	return nil
}

func (repository *fakeJobRepository) Fail(_ context.Context, job Job, _ error) error {
	repository.state.add("job:fail:" + job.Stage.String())
	if job.Status != JobStatusFailed && job.Status != JobStatusCancelled {
		return fmt.Errorf("Fail() status = %q", job.Status)
	}
	repository.status = job.Status
	repository.counts.Failed++
	return repository.failErr
}

func (repository *fakeJobRepository) Summary(
	_ context.Context,
	jobID string,
) (JobSummary, error) {
	repository.state.add("job:summary")
	repository.summaryCalls++
	if repository.summaryOverride != nil {
		return *repository.summaryOverride, repository.summaryErr
	}
	summary, err := NewJobSummary(jobID, repository.status, repository.counts)
	if err != nil {
		return JobSummary{}, err
	}
	return summary, repository.summaryErr
}

func (repository *fakeJobRepository) RecordSideEffectFailure(
	_ context.Context,
	jobID string,
	failure SideEffectFailure,
) error {
	repository.state.add("job:side-effect-failure:" + failure.Hook)
	if jobID == "" {
		return errors.New("missing job ID")
	}
	repository.sideEffectFailures = append(repository.sideEffectFailures, failure)
	return repository.sideEffectErr
}

type fakeRawRepository struct {
	state         *fakeState
	jobs          *fakeJobRepository
	persistErr    error
	deletionErr   error
	afterPersist  func()
	persisted     []Envelope
	deletions     []DeletionEnvelope
	rollbackCalls int
}

func (repository *fakeRawRepository) PersistRaw(
	_ context.Context,
	jobID string,
	envelope Envelope,
) (PersistedRaw, error) {
	repository.state.add("raw:persist:" + envelope.EventKey)
	repository.persisted = append(repository.persisted, envelope.Clone())
	if repository.persistErr != nil {
		return PersistedRaw{}, repository.persistErr
	}
	repository.jobs.counts.RawInserted++
	if repository.afterPersist != nil {
		repository.afterPersist()
	}
	return NewPersistedRaw("raw-1", jobID, envelope, RawDispositionInserted)
}

func (repository *fakeRawRepository) PersistDeletion(
	_ context.Context,
	jobID string,
	envelope DeletionEnvelope,
) (PersistedDeletion, error) {
	repository.state.add("raw:persist-deletion:" + envelope.EventKey)
	repository.deletions = append(repository.deletions, envelope.Clone())
	if repository.deletionErr != nil {
		return PersistedDeletion{}, repository.deletionErr
	}
	repository.jobs.counts.RawInserted++
	return NewPersistedDeletion("deletion-1", jobID, envelope, RawDispositionInserted)
}

type fakeProjectionRepository struct {
	state               *fakeState
	jobs                *fakeJobRepository
	normalizeErr        error
	excludeErr          error
	projectErr          error
	deleteErr           error
	projectCanonicalKey string
	normalizeCalls      int
	excludeCalls        int
	projectCalls        int
	deleteCalls         int
	rollbackCalls       int
}

func (repository *fakeProjectionRepository) Normalize(
	_ context.Context,
	_ string,
	raw PersistedRaw,
	scopePolicyVersion string,
) (NormalizedRecord, error) {
	repository.state.add("projection:normalize:" + scopePolicyVersion)
	repository.normalizeCalls++
	if repository.normalizeErr != nil {
		return NormalizedRecord{}, repository.normalizeErr
	}
	return NewNormalizedRecord(
		raw,
		raw.Envelope.Record,
		"normalized-assertion-"+raw.ID,
		normalizedPayloadSchemaVersion,
	)
}

func (repository *fakeProjectionRepository) Exclude(
	_ context.Context,
	_ string,
	record NormalizedRecord,
	decision source.ScopeDecision,
	scopePolicyVersion string,
) (ProjectionResult, error) {
	repository.state.add("projection:exclude:" + scopePolicyVersion)
	repository.excludeCalls++
	if repository.excludeErr != nil {
		return ProjectionResult{}, repository.excludeErr
	}
	repository.jobs.counts.Excluded++
	return NewProjectionResult(record.EventKey, ProjectionStatusExcluded, "")
}

func (repository *fakeProjectionRepository) Project(
	_ context.Context,
	_ string,
	candidate ProjectionCandidate,
	scopePolicyVersion string,
	projectionPolicyVersion string,
) (ProjectionResult, error) {
	repository.state.add(
		"projection:project:" + scopePolicyVersion + ":" + projectionPolicyVersion,
	)
	repository.projectCalls++
	if repository.projectErr != nil {
		return ProjectionResult{}, repository.projectErr
	}
	repository.jobs.counts.Projected++
	canonicalKey := repository.projectCanonicalKey
	if canonicalKey == "" {
		canonicalKey = candidate.Record.Identity.CanonicalKey()
	}
	return NewProjectionResult(
		candidate.EventKey,
		ProjectionStatusProjected,
		canonicalKey,
	)
}

func (repository *fakeProjectionRepository) ApplyDeletion(
	_ context.Context,
	_ string,
	deletion PersistedDeletion,
	scopePolicyVersion string,
	projectionPolicyVersion string,
) (ProjectionResult, error) {
	repository.state.add(
		"projection:delete:" + scopePolicyVersion + ":" + projectionPolicyVersion,
	)
	repository.deleteCalls++
	if repository.deleteErr != nil {
		return ProjectionResult{}, repository.deleteErr
	}
	repository.jobs.counts.Deleted++
	return NewProjectionResult(deletion.Envelope.EventKey, ProjectionStatusDeleted, "")
}

type fakeScopePolicy struct {
	state   *fakeState
	version string
	err     error
	calls   int
}

func (policy *fakeScopePolicy) Version() string {
	return policy.version
}

func (policy *fakeScopePolicy) Evaluate(
	_ context.Context,
	record NormalizedRecord,
) (source.ScopeDecision, error) {
	policy.state.add("scope:evaluate")
	policy.calls++
	if policy.err != nil {
		return source.ScopeDecision{}, policy.err
	}
	return record.Record.Scope, nil
}

type fakeProjectionPolicy struct {
	state   *fakeState
	version string
	err     error
	calls   int
}

func (policy *fakeProjectionPolicy) Version() string {
	return policy.version
}

func (policy *fakeProjectionPolicy) Prepare(
	_ context.Context,
	record NormalizedRecord,
	decision source.ScopeDecision,
) (ProjectionCandidate, error) {
	policy.state.add("projection-policy:prepare")
	policy.calls++
	if policy.err != nil {
		return ProjectionCandidate{}, policy.err
	}
	return NewProjectionCandidate(record, decision)
}

type fakeHook struct {
	name  string
	err   error
	state *fakeState
}

func (hook *fakeHook) Name() string {
	return hook.name
}

func (hook *fakeHook) Run(_ context.Context, _ CoreResult) error {
	hook.state.add("hook:" + hook.name)
	return hook.err
}

func containsOrdered(haystack, needles []string) bool {
	index := 0
	for _, value := range haystack {
		if index < len(needles) && value == needles[index] {
			index++
		}
	}
	return index == len(needles)
}
