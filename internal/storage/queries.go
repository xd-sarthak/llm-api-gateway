package storage

import (
	"context"
	"database/sql"
)

var ErrNoRows = sql.ErrNoRows

type APIKeyRecord struct {
	Key      string
	IsActive bool
}

func GetAPIKeyByHash(ctx context.Context, hashedKey string) (*APIKeyRecord, error) {
	var apiKey APIKeyRecord
	err := DB.QueryRowContext(
		ctx,
		"SELECT key_hash, is_active FROM api_keys WHERE key_hash = $1",
		hashedKey,
	).Scan(&apiKey.Key, &apiKey.IsActive)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &apiKey, nil
}
