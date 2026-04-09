package storage

import (
	"database/sql"
)

var ErrNoRows = sql.ErrNoRows

func IsValidAPIKey(hashedKey string) (bool, error) {
	var isActive bool
	err := DB.QueryRow(
		"SELECT is_active FROM api_keys WHERE key = $1",
		hashedKey,
	).Scan(&isActive)

	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return isActive, nil
}
