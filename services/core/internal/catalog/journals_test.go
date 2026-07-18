package catalog

import (
	"context"
	"errors"
	"testing"
)

func TestJournalsUseStablePaperCountThenSlugCursor(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertBiomedicalCatalogFixture(
		t,
		pool,
		biomedicalFixtureOptions{
			sourceRevision: "biomedical-journal-list",
		},
	)
	repository := mustRepository(t, pool)
	ctx := context.Background()

	first, err := repository.Journals(ctx, PageQuery{Limit: 1})
	if err != nil {
		t.Fatalf("Journals(first page) error = %v", err)
	}
	if first.Generation.ID != fixture.generationID {
		t.Fatalf(
			"Journals() generation = %s, want %s",
			first.Generation.ID,
			fixture.generationID,
		)
	}
	assertPayloadSlugs(t, first.Items, "annals-of-biomedicine")
	if !first.Pagination.HasMore || first.Pagination.NextCursor == "" ||
		first.Pagination.Total != 3 {
		t.Fatalf("Journals(first page) pagination = %#v", first.Pagination)
	}

	second, err := repository.Journals(ctx, PageQuery{
		Limit:  1,
		Cursor: first.Pagination.NextCursor,
	})
	if err != nil {
		t.Fatalf("Journals(second page) error = %v", err)
	}
	assertPayloadSlugs(t, second.Items, "journal-of-clinical-oncology")
}

func TestJournalDetailUsesStoredPayloadAndGenerationBoundRecentPaperCursor(t *testing.T) {
	pool := openCatalogTestPool(t)
	insertBiomedicalCatalogFixture(
		t,
		pool,
		biomedicalFixtureOptions{
			sourceRevision: "biomedical-journal-detail",
		},
	)
	repository := mustRepository(t, pool)
	ctx := context.Background()

	first, err := repository.Journal(
		ctx,
		"journal-of-clinical-oncology",
		PageQuery{Limit: 1},
	)
	if err != nil {
		t.Fatalf("Journal(first page) error = %v", err)
	}
	firstItems, firstPagination := recentPaperPage(t, first.Payload)
	assertPayloadTitles(t, firstItems, "Journal Paper A")
	if firstPagination.NextCursor == nil || *firstPagination.NextCursor == "" ||
		!firstPagination.HasMore ||
		firstPagination.Total != 2 {
		t.Fatalf("Journal(first page) pagination = %#v", firstPagination)
	}

	second, err := repository.Journal(
		ctx,
		"journal-of-clinical-oncology",
		PageQuery{Limit: 1, Cursor: *firstPagination.NextCursor},
	)
	if err != nil {
		t.Fatalf("Journal(second page) error = %v", err)
	}
	secondItems, secondPagination := recentPaperPage(t, second.Payload)
	assertPayloadTitles(t, secondItems, "Journal Paper B")
	if secondPagination.NextCursor != nil || secondPagination.HasMore {
		t.Fatalf("Journal(second page) pagination = %#v, want terminal page", secondPagination)
	}
}

func TestJournalSnapshotConstraintsAndAcceptedOnlyVisibility(t *testing.T) {
	pool := openCatalogTestPool(t)
	generationID := insertBiomedicalGenerationBase(
		t,
		pool,
		"biomedical-journal-constraints",
	)
	ctx := context.Background()

	for _, test := range []struct {
		name   string
		slug   string
		count  int
		detail string
	}{
		{name: "invalid slug", slug: "Invalid Journal", count: 0, detail: "{}"},
		{name: "negative count", slug: "negative-journal", count: -1, detail: "{}"},
		{name: "scalar detail", slug: "scalar-detail", count: 0, detail: "[]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO public_catalog_journals (
					generation_id,
					journal_id,
					slug,
					paper_count,
					summary_payload,
					detail_payload
				) VALUES ($1, gen_random_uuid(), $2, $3, '{}', $4::jsonb)
			`, generationID, test.slug, test.count, test.detail)
			if err == nil {
				t.Fatalf("invalid Journal snapshot row %+v was accepted", test)
			}
		})
	}

	insertBiomedicalCatalogFixture(
		t,
		pool,
		biomedicalFixtureOptions{
			sourceRevision: "biomedical-journal-visibility",
		},
	)
	repository := mustRepository(t, pool)
	for _, slug := range []string{
		"rejected-journal",
		"missing-journal",
		"Invalid Journal",
	} {
		if _, err := repository.Journal(ctx, slug, PageQuery{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Journal(%q) error = %v, want ErrNotFound", slug, err)
		}
	}
}
