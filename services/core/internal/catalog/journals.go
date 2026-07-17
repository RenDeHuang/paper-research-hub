package catalog

import "context"

var journalResource = biomedicalResource{
	listName:       "journals",
	detailName:     "journal_recent_papers",
	table:          "public_catalog_journals",
	manifestColumn: "journal_list_payload",
	countColumn:    "journal_count",
}

func (repository *Repository) Journals(
	ctx context.Context,
	query PageQuery,
) (BiomedicalPage, error) {
	return repository.biomedicalList(ctx, journalResource, query)
}

func (repository *Repository) Journal(
	ctx context.Context,
	slug string,
	query PageQuery,
) (Document, error) {
	return repository.biomedicalDetail(ctx, journalResource, slug, query)
}
