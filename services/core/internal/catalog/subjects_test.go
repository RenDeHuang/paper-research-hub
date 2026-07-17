package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestSubjectsUseStableGenerationBoundPaperCountCursor(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertBiomedicalCatalogFixture(
		t,
		pool,
		biomedicalFixtureOptions{
			sourceRevision: "biomedical-subject-list",
			homeMarker:     "subject-list",
		},
	)
	repository := mustRepository(t, pool)
	ctx := context.Background()

	first, err := repository.Subjects(ctx, PageQuery{Limit: 1})
	if err != nil {
		t.Fatalf("Subjects(first page) error = %v", err)
	}
	if first.Generation.ID != fixture.generationID {
		t.Fatalf(
			"Subjects() generation = %s, want %s",
			first.Generation.ID,
			fixture.generationID,
		)
	}
	assertPayloadSlugs(t, first.Items, "cardiology")
	if !first.Pagination.HasMore || first.Pagination.NextCursor == "" ||
		first.Pagination.Total != 3 {
		t.Fatalf("Subjects(first page) pagination = %#v", first.Pagination)
	}
	assertJSONField(t, first.Metadata, "taxonomy_version", "biomedical-jcr-subjects/v1")

	second, err := repository.Subjects(ctx, PageQuery{
		Limit:  1,
		Cursor: first.Pagination.NextCursor,
	})
	if err != nil {
		t.Fatalf("Subjects(second page) error = %v", err)
	}
	assertPayloadSlugs(t, second.Items, "oncology")

	if _, err := repository.Journals(ctx, PageQuery{
		Limit:  1,
		Cursor: first.Pagination.NextCursor,
	}); !errors.Is(err, ErrCursorConflict) {
		t.Fatalf("Journals(subject cursor) error = %v, want ErrCursorConflict", err)
	}

	insertBiomedicalCatalogFixture(
		t,
		pool,
		biomedicalFixtureOptions{
			sourceRevision: "biomedical-subject-list-next-generation",
			homeMarker:     "subject-list-next-generation",
		},
	)
	if _, err := repository.Subjects(ctx, PageQuery{
		Limit:  1,
		Cursor: first.Pagination.NextCursor,
	}); !errors.Is(err, ErrCursorConflict) {
		t.Fatalf("Subjects(stale generation cursor) error = %v, want ErrCursorConflict", err)
	}
}

func TestSubjectDetailPaginatesOnlyStoredRecentPapers(t *testing.T) {
	pool := openCatalogTestPool(t)
	insertBiomedicalCatalogFixture(
		t,
		pool,
		biomedicalFixtureOptions{
			sourceRevision: "biomedical-subject-detail",
			homeMarker:     "subject-detail",
		},
	)
	repository := mustRepository(t, pool)
	ctx := context.Background()

	first, err := repository.Subject(ctx, "oncology", PageQuery{Limit: 1})
	if err != nil {
		t.Fatalf("Subject(first page) error = %v", err)
	}
	firstItems, firstPagination := recentPaperPage(t, first.Payload)
	assertPayloadTitles(t, firstItems, "Oncology Paper A")
	if firstPagination.NextCursor == nil || *firstPagination.NextCursor == "" ||
		!firstPagination.HasMore ||
		firstPagination.Total != 3 {
		t.Fatalf("Subject(first page) pagination = %#v", firstPagination)
	}

	second, err := repository.Subject(ctx, "oncology", PageQuery{
		Limit:  1,
		Cursor: *firstPagination.NextCursor,
	})
	if err != nil {
		t.Fatalf("Subject(second page) error = %v", err)
	}
	secondItems, secondPagination := recentPaperPage(t, second.Payload)
	assertPayloadTitles(t, secondItems, "Oncology Paper B")
	if secondPagination.NextCursor == nil || !secondPagination.HasMore {
		t.Fatalf("Subject(second page) pagination = %#v", secondPagination)
	}

	if _, err := repository.Journal(
		ctx,
		"journal-of-clinical-oncology",
		PageQuery{Limit: 1, Cursor: *firstPagination.NextCursor},
	); !errors.Is(err, ErrCursorConflict) {
		t.Fatalf("Journal(subject detail cursor) error = %v, want ErrCursorConflict", err)
	}
}

func TestSubjectRejectsInvalidSnapshotRowsAndHidesRejectedOrUnknownSlugs(t *testing.T) {
	pool := openCatalogTestPool(t)
	generationID := insertBiomedicalGenerationBase(
		t,
		pool,
		"biomedical-subject-constraints",
	)
	ctx := context.Background()

	for _, test := range []struct {
		name  string
		slug  string
		count int
		body  string
	}{
		{name: "invalid slug", slug: "Invalid Slug", count: 0, body: "{}"},
		{name: "negative count", slug: "negative-count", count: -1, body: "{}"},
		{name: "scalar summary", slug: "scalar-summary", count: 0, body: "[]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO public_catalog_subjects (
					generation_id,
					subject_id,
					slug,
					paper_count,
					summary_payload,
					detail_payload
				) VALUES ($1, gen_random_uuid(), $2, $3, $4::jsonb, '{}')
			`, generationID, test.slug, test.count, test.body)
			if err == nil {
				t.Fatalf("invalid Subject snapshot row %+v was accepted", test)
			}
		})
	}

	insertBiomedicalCatalogFixture(
		t,
		pool,
		biomedicalFixtureOptions{
			sourceRevision: "biomedical-subject-visibility",
			homeMarker:     "subject-visibility",
		},
	)
	repository := mustRepository(t, pool)
	for _, slug := range []string{"rejected-oncology", "does-not-exist", "Invalid Slug"} {
		if _, err := repository.Subject(ctx, slug, PageQuery{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Subject(%q) error = %v, want ErrNotFound", slug, err)
		}
	}
}

type recentPagination struct {
	Limit      int     `json:"limit"`
	Total      int     `json:"total"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

func recentPaperPage(
	t *testing.T,
	payload json.RawMessage,
) ([]json.RawMessage, recentPagination) {
	t.Helper()
	root := decodePayloadObject(t, payload)
	var recent struct {
		Items      []json.RawMessage `json:"items"`
		Pagination recentPagination  `json:"pagination"`
	}
	if err := json.Unmarshal(root["recent_papers"], &recent); err != nil {
		t.Fatalf("decode recent_papers: %v; payload=%s", err, payload)
	}
	return recent.Items, recent.Pagination
}

func assertPayloadSlugs(t *testing.T, items []json.RawMessage, want ...string) {
	t.Helper()
	got := make([]string, len(items))
	for index, item := range items {
		var payload struct {
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(item, &payload); err != nil {
			t.Fatalf("decode item slug: %v", err)
		}
		got[index] = payload.Slug
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("item slugs = %v, want %v", got, want)
	}
}

func assertPayloadTitles(t *testing.T, items []json.RawMessage, want ...string) {
	t.Helper()
	got := make([]string, len(items))
	for index, item := range items {
		var payload struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(item, &payload); err != nil {
			t.Fatalf("decode item title: %v", err)
		}
		got[index] = payload.Title
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("item titles = %v, want %v", got, want)
	}
}
