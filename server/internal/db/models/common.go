// Package models provides database model definitions for SolidPing.
package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONMap represents flexible key-value data stored as JSON.
type JSONMap map[string]any

// Value implements the driver.Valuer interface for database storage.
func (m JSONMap) Value() (driver.Value, error) {
	if len(m) == 0 {
		return "{}", nil
	}

	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSONMap: %w", err)
	}

	return string(data), nil
}

// Scan implements the sql.Scanner interface for database retrieval.
func (m *JSONMap) Scan(value any) error {
	if value == nil {
		*m = make(JSONMap)
		return nil
	}

	var data []byte

	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		*m = make(JSONMap)
		return nil
	}

	if len(data) == 0 {
		*m = make(JSONMap)
		return nil
	}

	return json.Unmarshal(data, m)
}
