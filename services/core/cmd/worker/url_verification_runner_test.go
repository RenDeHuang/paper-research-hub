package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/urlverify"
)

func TestExecuteURLVerificationBatchImportsCurrentCandidatesAndProjectsOnlyVerifiedLinks(
	t *testing.T,
) {
	t.Parallel()

	now := time.Date(2026, time.July, 19, 8, 30, 0, 0, time.UTC)
	verifiedCandidate := workerURLCandidate(
		"candidate-verified",
		"work-verified",
		"10.1000/verified",
		"https://publisher.example.test/verified",
	)
	failedCandidate := workerURLCandidate(
		"candidate-failed",
		"work-failed",
		"10.1000/failed",
		"https://publisher.example.test/failed",
	)
	store := &fakeURLVerificationStore{
		references: []urlverify.CurrentProjectionRef{{
			WorkID:                "work-import",
			NormalizedAssertionID: "normalized-import",
		}},
		hostPolicy: workerURLHostPolicy(
			t,
			"official-url/v1",
			"publisher.example.test",
		),
		candidates: []urlverify.Candidate{
			verifiedCandidate,
			failedCandidate,
		},
	}
	observer := &fakeURLObserver{
		observations: map[string]urlverify.HTTPObservation{
			verifiedCandidate.ID: workerURLObservation(
				verifiedCandidate,
				verifiedCandidate.Identifier,
			),
			failedCandidate.ID: workerURLObservation(
				failedCandidate,
				urlverify.StableIdentifier{
					Scheme: "doi",
					Value:  "10.1000/different",
				},
			),
		},
	}

	summary, err := executeURLVerificationBatch(
		context.Background(),
		store,
		observer,
		workerCommand{
			Kind:          commandVerifyURLs,
			PolicyVersion: "official-url/v1",
			Limit:         25,
		},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("executeURLVerificationBatch() error = %v", err)
	}
	if summary != (urlVerificationSummary{
		Selected:  2,
		Verified:  1,
		Failed:    1,
		Projected: 1,
	}) {
		t.Fatalf("summary = %#v", summary)
	}
	if !slices.Equal(
		store.calls,
		[]string{
			"list-current:25",
			"import:work-import:normalized-import",
			"list-verification:official-url/v1:25",
			"load-host-policy:candidate-verified:official-url/v1",
			"persist:candidate-verified:verified",
			"project:verification-1",
			"load-host-policy:candidate-failed:official-url/v1",
			"persist:candidate-failed:failed",
		},
	) {
		t.Fatalf("store calls = %#v", store.calls)
	}
	if !slices.Equal(
		observer.candidateIDs,
		[]string{"candidate-verified", "candidate-failed"},
	) {
		t.Fatalf("observed candidate IDs = %#v", observer.candidateIDs)
	}
	if !slices.Equal(
		observer.policyVersions,
		[]string{"official-url/v1", "official-url/v1"},
	) {
		t.Fatalf("observed policy versions = %#v", observer.policyVersions)
	}
	for _, verification := range store.persisted {
		if verification.CheckedAt != now ||
			verification.PolicyVersion != "official-url/v1" ||
			verification.VerifierVersion != officialURLVerifierVersion ||
			!verification.ExpiresAt.Equal(now.Add(officialURLVerificationTTL)) {
			t.Fatalf("persisted verification = %#v", verification)
		}
	}
}

func TestExecuteURLVerificationBatchAggregatesCandidateErrorsAndContinues(
	t *testing.T,
) {
	t.Parallel()

	now := time.Date(2026, time.July, 19, 9, 0, 0, 0, time.UTC)
	candidates := []urlverify.Candidate{
		workerURLCandidate(
			"candidate-observe-error",
			"work-observe-error",
			"10.1000/observe-error",
			"https://publisher.example.test/observe-error",
		),
		workerURLCandidate(
			"candidate-persist-error",
			"work-persist-error",
			"10.1000/persist-error",
			"https://publisher.example.test/persist-error",
		),
		workerURLCandidate(
			"candidate-project-error",
			"work-project-error",
			"10.1000/project-error",
			"https://publisher.example.test/project-error",
		),
		workerURLCandidate(
			"candidate-success",
			"work-success",
			"10.1000/success",
			"https://publisher.example.test/success",
		),
	}
	observeErr := errors.New("observe unavailable")
	persistErr := errors.New("persist unavailable")
	projectErr := errors.New("project unavailable")
	store := &fakeURLVerificationStore{
		candidates: candidates,
		hostPolicy: workerURLHostPolicy(
			t,
			"official-url/v1",
			"publisher.example.test",
		),
		persistErrors: map[string]error{
			candidates[1].ID: persistErr,
		},
		projectErrors: map[string]error{
			"verification-1": projectErr,
		},
	}
	observer := &fakeURLObserver{
		observations: map[string]urlverify.HTTPObservation{
			candidates[1].ID: workerURLObservation(
				candidates[1],
				candidates[1].Identifier,
			),
			candidates[2].ID: workerURLObservation(
				candidates[2],
				candidates[2].Identifier,
			),
			candidates[3].ID: workerURLObservation(
				candidates[3],
				candidates[3].Identifier,
			),
		},
		errors: map[string]error{
			candidates[0].ID: observeErr,
		},
	}

	summary, err := executeURLVerificationBatch(
		context.Background(),
		store,
		observer,
		workerCommand{
			Kind:          commandVerifyURLs,
			PolicyVersion: "official-url/v1",
			Limit:         4,
		},
		func() time.Time { return now },
	)
	if !errors.Is(err, observeErr) ||
		!errors.Is(err, persistErr) ||
		!errors.Is(err, projectErr) {
		t.Fatalf("executeURLVerificationBatch() error = %v", err)
	}
	if summary != (urlVerificationSummary{
		Selected:  4,
		Verified:  2,
		Projected: 1,
	}) {
		t.Fatalf("summary = %#v", summary)
	}
	if !slices.Equal(
		observer.candidateIDs,
		[]string{
			"candidate-observe-error",
			"candidate-persist-error",
			"candidate-project-error",
			"candidate-success",
		},
	) {
		t.Fatalf("observed candidate IDs = %#v", observer.candidateIDs)
	}
	if !slices.Equal(
		store.calls,
		[]string{
			"list-current:4",
			"list-verification:official-url/v1:4",
			"load-host-policy:candidate-observe-error:official-url/v1",
			"load-host-policy:candidate-persist-error:official-url/v1",
			"persist:candidate-persist-error:verified",
			"load-host-policy:candidate-project-error:official-url/v1",
			"persist:candidate-project-error:verified",
			"project:verification-1",
			"load-host-policy:candidate-success:official-url/v1",
			"persist:candidate-success:verified",
			"project:verification-2",
		},
	) {
		t.Fatalf("store calls = %#v", store.calls)
	}
}

func TestExecuteURLVerificationBatchContinuesAfterReferenceImportErrors(
	t *testing.T,
) {
	t.Parallel()

	now := time.Date(2026, time.July, 19, 9, 30, 0, 0, time.UTC)
	mixedGoodCandidate := workerURLCandidate(
		"candidate-mixed-good",
		"work-mixed",
		"10.1000/mixed-good",
		"https://publisher.example.test/mixed-good",
	)
	laterGoodCandidate := workerURLCandidate(
		"candidate-later-good",
		"work-later",
		"10.1000/later-good",
		"https://publisher.example.test/later-good",
	)
	mixedReference := urlverify.CurrentProjectionRef{
		WorkID:                "work-mixed",
		NormalizedAssertionID: "normalized-mixed",
	}
	laterReference := urlverify.CurrentProjectionRef{
		WorkID:                "work-later",
		NormalizedAssertionID: "normalized-later",
	}
	importErr := fmt.Errorf(
		"%w: source_path is required at index 1",
		urlverify.ErrInvalidNormalizedURLCandidate,
	)
	store := &fakeURLVerificationStore{
		references: []urlverify.CurrentProjectionRef{
			mixedReference,
			laterReference,
		},
		hostPolicy: workerURLHostPolicy(
			t,
			"official-url/v1",
			"publisher.example.test",
		),
		candidates: []urlverify.Candidate{
			mixedGoodCandidate,
			laterGoodCandidate,
		},
		importErrors: map[string]error{
			urlVerificationReferenceKey(mixedReference): importErr,
		},
	}
	observer := &fakeURLObserver{
		observations: map[string]urlverify.HTTPObservation{
			mixedGoodCandidate.ID: workerURLObservation(
				mixedGoodCandidate,
				mixedGoodCandidate.Identifier,
			),
			laterGoodCandidate.ID: workerURLObservation(
				laterGoodCandidate,
				laterGoodCandidate.Identifier,
			),
		},
	}

	summary, err := executeURLVerificationBatch(
		context.Background(),
		store,
		observer,
		workerCommand{
			Kind:          commandVerifyURLs,
			PolicyVersion: "official-url/v1",
			Limit:         2,
		},
		func() time.Time { return now },
	)
	if !errors.Is(err, importErr) ||
		!errors.Is(err, urlverify.ErrInvalidNormalizedURLCandidate) {
		t.Fatalf("executeURLVerificationBatch() error = %v", err)
	}
	if summary != (urlVerificationSummary{
		Selected:  2,
		Verified:  2,
		Projected: 2,
	}) {
		t.Fatalf("summary = %#v", summary)
	}
	if !slices.Equal(
		store.calls,
		[]string{
			"list-current:2",
			"import:work-mixed:normalized-mixed",
			"import:work-later:normalized-later",
			"list-verification:official-url/v1:2",
			"load-host-policy:candidate-mixed-good:official-url/v1",
			"persist:candidate-mixed-good:verified",
			"project:verification-1",
			"load-host-policy:candidate-later-good:official-url/v1",
			"persist:candidate-later-good:verified",
			"project:verification-2",
		},
	) {
		t.Fatalf("store calls = %#v", store.calls)
	}
	if !slices.Equal(
		observer.candidateIDs,
		[]string{"candidate-mixed-good", "candidate-later-good"},
	) {
		t.Fatalf("observed candidate IDs = %#v", observer.candidateIDs)
	}
}

func TestExecuteURLVerificationBatchPersistsObserverFailureForCooldown(
	t *testing.T,
) {
	t.Parallel()

	now := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	candidate := workerURLCandidate(
		"candidate-network-failure",
		"work-network-failure",
		"10.1000/network-failure",
		"https://publisher.example.test/network-failure",
	)
	store := &fakeURLVerificationStore{
		candidates: []urlverify.Candidate{candidate},
		hostPolicy: workerURLHostPolicy(
			t,
			"official-url/v1",
			"publisher.example.test",
		),
	}
	observer := &fakeURLObserver{
		observations: map[string]urlverify.HTTPObservation{
			candidate.ID: {
				FinalURL:    candidate.URL,
				FailureCode: urlverify.FailureNetwork,
			},
		},
	}

	summary, err := executeURLVerificationBatch(
		context.Background(),
		store,
		observer,
		workerCommand{
			Kind:          commandVerifyURLs,
			PolicyVersion: "official-url/v1",
			Limit:         1,
		},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("executeURLVerificationBatch() error = %v", err)
	}
	if summary != (urlVerificationSummary{
		Selected: 1,
		Failed:   1,
	}) {
		t.Fatalf("summary = %#v", summary)
	}
	if !slices.Equal(
		store.calls,
		[]string{
			"list-current:1",
			"list-verification:official-url/v1:1",
			"load-host-policy:candidate-network-failure:official-url/v1",
			"persist:candidate-network-failure:failed",
		},
	) {
		t.Fatalf("store calls = %#v", store.calls)
	}
	if len(store.persisted) != 1 ||
		store.persisted[0].FailureCode != urlverify.FailureNetwork ||
		store.persisted[0].HTTPStatus != 0 ||
		len(store.persisted[0].RedirectChain) != 0 ||
		!store.persisted[0].ExpiresAt.Equal(
			now.Add(officialURLVerificationTTL),
		) {
		t.Fatalf("persisted verification = %#v", store.persisted)
	}
}

func TestURLVerificationResultUsesStableStructuredCounters(t *testing.T) {
	t.Parallel()

	result := urlVerificationResult(urlVerificationSummary{
		Selected:  7,
		Verified:  5,
		Failed:    2,
		Projected: 5,
	})
	if len(result) != 4 ||
		result["selected"] != 7 ||
		result["verified"] != 5 ||
		result["failed"] != 2 ||
		result["projected"] != 5 {
		t.Fatalf("urlVerificationResult() = %#v", result)
	}
}

type fakeURLVerificationStore struct {
	references []urlverify.CurrentProjectionRef
	candidates []urlverify.Candidate
	calls      []string
	persisted  []urlverify.Verification
	hostPolicy urlverify.HostPolicy

	importErrors     map[string]error
	hostPolicyErrors map[string]error
	persistErrors    map[string]error
	projectErrors    map[string]error
}

func (store *fakeURLVerificationStore) ListCurrentProjectionRefs(
	_ context.Context,
	limit int,
) ([]urlverify.CurrentProjectionRef, error) {
	store.calls = append(store.calls, fmt.Sprintf("list-current:%d", limit))
	return slices.Clone(store.references), nil
}

func (store *fakeURLVerificationStore) ImportCurrentProjectionCandidates(
	_ context.Context,
	reference urlverify.CurrentProjectionRef,
) ([]urlverify.Candidate, error) {
	store.calls = append(
		store.calls,
		fmt.Sprintf(
			"import:%s:%s",
			reference.WorkID,
			reference.NormalizedAssertionID,
		),
	)
	if err := store.importErrors[urlVerificationReferenceKey(reference)]; err != nil {
		return nil, err
	}
	return nil, nil
}

func (store *fakeURLVerificationStore) ListVerificationCandidates(
	_ context.Context,
	selection urlverify.CandidateSelection,
) ([]urlverify.Candidate, error) {
	store.calls = append(
		store.calls,
		fmt.Sprintf(
			"list-verification:%s:%d",
			selection.PolicyVersion,
			selection.Limit,
		),
	)
	return slices.Clone(store.candidates), nil
}

func (store *fakeURLVerificationStore) LoadHostPolicy(
	_ context.Context,
	candidate urlverify.Candidate,
	policyVersion string,
) (urlverify.HostPolicy, error) {
	store.calls = append(
		store.calls,
		fmt.Sprintf(
			"load-host-policy:%s:%s",
			candidate.ID,
			policyVersion,
		),
	)
	if err := store.hostPolicyErrors[candidate.ID]; err != nil {
		return urlverify.HostPolicy{}, err
	}
	return store.hostPolicy, nil
}

func (store *fakeURLVerificationStore) PersistVerification(
	_ context.Context,
	verification urlverify.Verification,
) (urlverify.Verification, error) {
	store.calls = append(
		store.calls,
		fmt.Sprintf(
			"persist:%s:%s",
			verification.CandidateID,
			verification.State,
		),
	)
	if err := store.persistErrors[verification.CandidateID]; err != nil {
		return urlverify.Verification{}, err
	}
	store.persisted = append(store.persisted, verification)
	verification.ID = fmt.Sprintf(
		"verification-%d",
		len(store.persisted),
	)
	return verification, nil
}

func (store *fakeURLVerificationStore) ProjectCurrent(
	_ context.Context,
	verificationID string,
	_ time.Time,
) (urlverify.OfficialLink, error) {
	store.calls = append(store.calls, "project:"+verificationID)
	if err := store.projectErrors[verificationID]; err != nil {
		return urlverify.OfficialLink{}, err
	}
	return urlverify.OfficialLink{}, nil
}

func urlVerificationReferenceKey(
	reference urlverify.CurrentProjectionRef,
) string {
	return reference.WorkID + "\x00" + reference.NormalizedAssertionID
}

type fakeURLObserver struct {
	observations   map[string]urlverify.HTTPObservation
	errors         map[string]error
	candidateIDs   []string
	policyVersions []string
}

func (observer *fakeURLObserver) Observe(
	_ context.Context,
	candidate urlverify.Candidate,
	hostPolicy urlverify.HostPolicy,
) (urlverify.HTTPObservation, error) {
	observer.candidateIDs = append(observer.candidateIDs, candidate.ID)
	observer.policyVersions = append(
		observer.policyVersions,
		hostPolicy.PolicyVersion(),
	)
	if err := observer.errors[candidate.ID]; err != nil {
		return urlverify.HTTPObservation{}, err
	}
	observation, found := observer.observations[candidate.ID]
	if !found {
		return urlverify.HTTPObservation{}, fmt.Errorf(
			"missing observation for %s",
			candidate.ID,
		)
	}
	return observation, nil
}

func workerURLHostPolicy(
	t *testing.T,
	policyVersion string,
	allowedHosts ...string,
) urlverify.HostPolicy {
	t.Helper()
	policy, err := urlverify.NewHostPolicy(policyVersion, allowedHosts)
	if err != nil {
		t.Fatalf("NewHostPolicy() error = %v", err)
	}
	return policy
}

func workerURLCandidate(
	id string,
	workID string,
	doi string,
	rawURL string,
) urlverify.Candidate {
	return urlverify.Candidate{
		ID:                    id,
		WorkID:                workID,
		ProjectionAssertionID: "projection-" + id,
		NormalizedAssertionID: "normalized-" + id,
		SourceRecordID:        "source-" + id,
		SourcePath:            "$.message.URL",
		Channel:               scope.ContentChannelJournalPublished,
		LinkRole:              urlverify.LinkRoleOfficialArticle,
		URL:                   rawURL,
		ParserVersion:         "crossref/crossref-work-v4",
		Identifier: urlverify.StableIdentifier{
			Scheme: "doi",
			Value:  doi,
		},
		AssertedAt: time.Date(
			2026,
			time.July,
			19,
			8,
			0,
			0,
			0,
			time.UTC,
		),
	}
}

func workerURLObservation(
	candidate urlverify.Candidate,
	observed urlverify.StableIdentifier,
) urlverify.HTTPObservation {
	return urlverify.HTTPObservation{
		FinalURL:   candidate.URL,
		HTTPStatus: 200,
		RedirectChain: []urlverify.RedirectHop{{
			URL:        candidate.URL,
			StatusCode: 200,
			Metadata: urlverify.ResponseMetadata{
				ContentType: "text/html; charset=utf-8",
			},
		}},
		IdentifierEvidence: []urlverify.IdentifierEvidence{{
			Identifier: observed,
			SourceKind: urlverify.IdentifierSourceHTMLMeta,
			SourcePath: `meta[name="citation_doi"]@content`,
			RawValue:   observed.Value,
		}},
		ResponseMetadata: urlverify.ResponseMetadata{
			ContentType: "text/html; charset=utf-8",
		},
	}
}
