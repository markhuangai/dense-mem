package postgres

import (
	"context"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func ResolveMemoryHandleTx(ctx context.Context, tx *gorm.DB, teamID, table, handle, spaceID string) (string, error) {
	if handle == "" {
		return handle, nil
	}
	if _, err := uuid.Parse(handle); err != nil {
		return "", err
	}
	var space any
	if spaceID != "" {
		space = spaceID
	}
	var resolved string
	err := tx.WithContext(ctx).Raw(`SELECT dense_mem_resolve_memory_handle(?::uuid,?,?::uuid,?::uuid)::text`, teamID, table, handle, space).Row().Scan(&resolved)
	return resolved, err
}

func ResolveMemoryHandlesTx(ctx context.Context, tx *gorm.DB, teamID, table string, handles []string, spaceID string) ([]string, error) {
	resolved := make([]string, 0, len(handles))
	if len(handles) == 0 {
		return resolved, nil
	}
	for _, handle := range handles {
		if handle != "" {
			if _, err := uuid.Parse(handle); err != nil {
				return nil, err
			}
		}
	}
	rows, err := tx.WithContext(ctx).Raw(`SELECT CASE WHEN input.handle='' THEN '' ELSE
        dense_mem_resolve_memory_handle(?::uuid,?,input.handle::uuid,NULLIF(?,'')::uuid)::text END
        FROM unnest(?::text[]) WITH ORDINALITY AS input(handle,ordinal) ORDER BY ordinal`,
		teamID, table, spaceID, pq.Array(handles)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var handle string
		if err := rows.Scan(&handle); err != nil {
			return nil, err
		}
		resolved = append(resolved, handle)
	}
	return resolved, rows.Err()
}
