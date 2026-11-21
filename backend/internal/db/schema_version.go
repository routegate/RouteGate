package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SchemaVersionRepository struct {
	pool *pgxpool.Pool
}

func NewSchemaVersionRepository(pool *pgxpool.Pool) *SchemaVersionRepository {
	return &SchemaVersionRepository{pool: pool}
}

func (r *SchemaVersionRepository) AppliedSchemaVersion(ctx context.Context) (string, error) {
	var version string
	err := r.pool.QueryRow(ctx, `
		SELECT version
		FROM public.schema_migrations
		ORDER BY version DESC
		LIMIT 1
	`).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read applied schema version: %w", err)
	}
	return version, nil
}
