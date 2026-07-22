package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/urlverify"
)

const (
	officialURLVerifierVersion   = "official-url-verifier/v1"
	officialURLVerificationTTL   = 24 * time.Hour
	officialURLObserverTimeout   = 20 * time.Second
	officialURLObserverBodyLimit = 2 << 20
)

type urlVerificationSummary struct {
	Selected  int
	Verified  int
	Failed    int
	Projected int
}

type urlVerificationStore interface {
	ListCurrentProjectionRefs(
		context.Context,
		int,
	) ([]urlverify.CurrentProjectionRef, error)
	ImportCurrentProjectionCandidates(
		context.Context,
		urlverify.CurrentProjectionRef,
	) ([]urlverify.Candidate, error)
	ListVerificationCandidates(
		context.Context,
		urlverify.CandidateSelection,
	) ([]urlverify.Candidate, error)
	LoadHostPolicy(
		context.Context,
		urlverify.Candidate,
		string,
	) (urlverify.HostPolicy, error)
	PersistVerification(
		context.Context,
		urlverify.Verification,
	) (urlverify.Verification, error)
	ProjectCurrent(
		context.Context,
		string,
		time.Time,
	) (urlverify.OfficialLink, error)
}

type urlObserver interface {
	Observe(
		context.Context,
		urlverify.Candidate,
		urlverify.HostPolicy,
	) (urlverify.HTTPObservation, error)
}

func runURLVerification(
	ctx context.Context,
	pool *pgxpool.Pool,
	command workerCommand,
) (map[string]any, error) {
	store, err := urlverify.NewPostgresStore(pool)
	if err != nil {
		return nil, fmt.Errorf(
			"create official URL verification store: %w",
			err,
		)
	}
	observer, err := urlverify.NewHTTPObserver(
		&http.Client{Transport: http.DefaultTransport},
		urlverify.HTTPObserverConfig{
			Timeout:      officialURLObserverTimeout,
			MaxBodyBytes: officialURLObserverBodyLimit,
			MaxRedirects: urlverify.MaximumRedirects,
			Resolver:     net.DefaultResolver,
			Dialer: &net.Dialer{
				Timeout: officialURLObserverTimeout,
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create official URL HTTP observer: %w",
			err,
		)
	}
	summary, err := executeURLVerificationBatch(
		ctx,
		store,
		observer,
		command,
		time.Now,
	)
	if err != nil {
		return nil, err
	}
	return urlVerificationResult(summary), nil
}

func urlVerificationResult(
	summary urlVerificationSummary,
) map[string]any {
	return map[string]any{
		"selected":  summary.Selected,
		"verified":  summary.Verified,
		"failed":    summary.Failed,
		"projected": summary.Projected,
	}
}

func executeURLVerificationBatch(
	ctx context.Context,
	store urlVerificationStore,
	observer urlObserver,
	command workerCommand,
	now func() time.Time,
) (urlVerificationSummary, error) {
	if ctx == nil {
		return urlVerificationSummary{}, errors.New(
			"official URL verification context is required",
		)
	}
	if store == nil {
		return urlVerificationSummary{}, errors.New(
			"official URL verification store is required",
		)
	}
	if observer == nil {
		return urlVerificationSummary{}, errors.New(
			"official URL observer is required",
		)
	}
	if now == nil {
		return urlVerificationSummary{}, errors.New(
			"official URL verification clock is required",
		)
	}
	if command.Kind != commandVerifyURLs {
		return urlVerificationSummary{}, fmt.Errorf(
			"official URL verification received command kind %q",
			command.Kind,
		)
	}

	checkedAt := now().UTC()
	if checkedAt.IsZero() {
		return urlVerificationSummary{}, errors.New(
			"official URL verification clock returned zero time",
		)
	}
	selection := urlverify.CandidateSelection{
		PolicyVersion: command.PolicyVersion,
		At:            checkedAt,
		Limit:         command.Limit,
	}
	if err := selection.Validate(); err != nil {
		return urlVerificationSummary{}, err
	}

	references, err := store.ListCurrentProjectionRefs(
		ctx,
		command.Limit,
	)
	if err != nil {
		return urlVerificationSummary{}, fmt.Errorf(
			"list current official URL projection references: %w",
			err,
		)
	}
	batchErrors := make([]error, 0)
	for _, reference := range references {
		if _, err := store.ImportCurrentProjectionCandidates(
			ctx,
			reference,
		); err != nil {
			batchErrors = append(batchErrors, fmt.Errorf(
				"import official URL candidates for Work %s: %w",
				reference.WorkID,
				err,
			))
		}
	}

	candidates, err := store.ListVerificationCandidates(ctx, selection)
	if err != nil {
		return urlVerificationSummary{}, fmt.Errorf(
			"list official URL verification candidates: %w",
			err,
		)
	}
	summary := urlVerificationSummary{Selected: len(candidates)}
	for _, candidate := range candidates {
		hostPolicy, err := store.LoadHostPolicy(
			ctx,
			candidate,
			command.PolicyVersion,
		)
		if err != nil {
			batchErrors = append(batchErrors, fmt.Errorf(
				"load official URL host policy for candidate %s: %w",
				candidate.ID,
				err,
			))
			continue
		}
		observation, err := observer.Observe(
			ctx,
			candidate,
			hostPolicy,
		)
		if err != nil {
			batchErrors = append(batchErrors, fmt.Errorf(
				"observe official URL candidate %s: %w",
				candidate.ID,
				err,
			))
			continue
		}
		verification, err := urlverify.Verify(
			urlverify.VerificationInput{
				Candidate:          candidate,
				ExpectedIdentifier: candidate.Identifier,
				Observation:        observation,
				CheckedAt:          checkedAt,
				ExpiresAt: checkedAt.Add(
					officialURLVerificationTTL,
				),
				EvaluatedAt:     checkedAt,
				VerifierVersion: officialURLVerifierVersion,
				Policy: urlverify.Policy{
					Version:      command.PolicyVersion,
					MaxRedirects: urlverify.MaximumRedirects,
				},
			},
		)
		if err != nil {
			batchErrors = append(batchErrors, fmt.Errorf(
				"verify official URL candidate %s: %w",
				candidate.ID,
				err,
			))
			continue
		}
		verification, err = store.PersistVerification(ctx, verification)
		if err != nil {
			batchErrors = append(batchErrors, fmt.Errorf(
				"persist official URL verification for candidate %s: %w",
				candidate.ID,
				err,
			))
			continue
		}
		switch verification.State {
		case urlverify.VerificationStateVerified:
			summary.Verified++
			if _, err := store.ProjectCurrent(
				ctx,
				verification.ID,
				checkedAt,
			); err != nil {
				batchErrors = append(batchErrors, fmt.Errorf(
					"project current official URL for verification %s: %w",
					verification.ID,
					err,
				))
				continue
			}
			summary.Projected++
		case urlverify.VerificationStateFailed:
			summary.Failed++
		default:
			batchErrors = append(batchErrors, fmt.Errorf(
				"persisted official URL verification %s has invalid state %q",
				verification.ID,
				verification.State,
			))
		}
	}
	return summary, errors.Join(batchErrors...)
}
