package postgres

import (
	"context"
	"github.com/google/uuid"
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
	result := make([]string, len(handles))
	for i, handle := range handles {
		value, err := ResolveMemoryHandleTx(ctx, tx, teamID, table, handle, spaceID)
		if err != nil {
			return nil, err
		}
		result[i] = value
	}
	return result, nil
}
