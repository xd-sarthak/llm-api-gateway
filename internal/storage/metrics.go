package storage

import (
	"database/sql"
	"encoding/json"

	"github.com/xd-sarthak/llm-api-gateway/internal/metrics"
)

func InsertMetricEvent(event metrics.Event) error {
	if DB == nil {
		return ErrNilDB
	}

	labels, err := json.Marshal(event.Labels)
	if err != nil {
		return err
	}

	_, err = DB.Exec(`
	INSERT INTO metric_events (category, name, value, unit, labels)
	VALUES ($1, $2, $3, $4, $5::jsonb)`,
		event.Category,
		event.Name,
		event.Value,
		event.Unit,
		string(labels),
	)
	if err == sql.ErrNoRows {
		return nil
	}
	return err
}
