package storage

import (
	"database/sql"
)

var ErrNoRows = sql.ErrNoRows

type APIKeyRecord struct {
	Key      string
	IsActive bool
}

func GetAPIKeyByHash(hashedKey string) (*APIKeyRecord, error) {
	var apiKey APIKeyRecord
	err := DB.QueryRow(
		"SELECT key, is_active FROM api_keys WHERE key = $1",
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
