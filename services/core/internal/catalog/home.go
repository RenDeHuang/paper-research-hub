package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (repository *Repository) Home(ctx context.Context) (Document, error) {
	return readTransaction(ctx, repository.database, func(tx pgx.Tx) (Document, error) {
		generation, err := currentGeneration(ctx, tx)
		if err != nil {
			return Document{}, err
		}

		var payload []byte
		if err := tx.QueryRow(ctx, `
			SELECT payload
			FROM public_catalog_home
			WHERE generation_id = $1
		`, generation.ID).Scan(&payload); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Document{}, ErrCatalogNotPublished
			}
			return Document{}, fmt.Errorf("query public Catalog Home snapshot: %w", err)
		}
		return Document{
			Generation: generation,
			Payload:    json.RawMessage(payload),
		}, nil
	})
}
