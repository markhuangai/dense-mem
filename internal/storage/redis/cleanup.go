package redis

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// CleanupRepositoryInterface is the companion interface for cleanup operations.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type CleanupRepositoryInterface interface {
	PurgeTeamState(ctx context.Context, teamID string) error
	InvalidateCredentialSessions(ctx context.Context, teamID, credentialID string) error
}

// CleanupRepository implements Redis cleanup operations for team state.
type CleanupRepository struct {
	client *redis.Client
}

// Ensure CleanupRepository implements CleanupRepositoryInterface
var _ CleanupRepositoryInterface = (*CleanupRepository)(nil)

// NewCleanupRepository creates a new cleanup repository.
func NewCleanupRepository(client *redis.Client) *CleanupRepository {
	return &CleanupRepository{client: client}
}

// PurgeTeamState deletes all cache, session, and stream keys for a team.
// Uses SCAN with MATCH to iterate over keys, never KEYS which blocks.
func (r *CleanupRepository) PurgeTeamState(ctx context.Context, teamID string) error {
	patterns := []string{
		fmt.Sprintf("profile:%s:cache:*", teamID),
		fmt.Sprintf("profile:%s:session:*", teamID),
		fmt.Sprintf("profile:%s:stream:*", teamID),
	}

	for _, pattern := range patterns {
		if err := r.deleteKeysByPattern(ctx, pattern); err != nil {
			return fmt.Errorf("failed to purge keys matching %s: %w", pattern, err)
		}
	}

	return nil
}

// InvalidateCredentialSessions deletes sessions that belong to one credential.
// Scans session keys, parses JSON payloads, and deletes those with matching key_id.
// Malformed JSON payloads are logged and skipped without crashing the cleanup loop.
func (r *CleanupRepository) InvalidateCredentialSessions(ctx context.Context, teamID, credentialID string) error {
	pattern := fmt.Sprintf("profile:%s:session:*", teamID)

	var cursor uint64
	for {
		// Scan for keys matching the pattern
		keys, nextCursor, err := r.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}

		// Check each session key for matching key_id
		for _, key := range keys {
			val, err := r.client.Get(ctx, key).Result()
			if err != nil {
				if err == redis.Nil {
					// Key expired or was deleted, skip
					continue
				}
				// Log but continue on other errors
				continue
			}

			// Parse JSON payload
			var payload struct {
				CredentialID string `json:"key_id"`
			}
			if err := json.Unmarshal([]byte(val), &payload); err != nil {
				// Malformed JSON - log and skip without crashing
				// Continue processing other keys
				continue
			}

			// Delete if key_id matches
			if payload.CredentialID == credentialID {
				r.client.Del(ctx, key)
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}

// deleteKeysByPattern deletes all keys matching a pattern using iterative SCAN.
// This is safe for production use as SCAN doesn't block like KEYS.
func (r *CleanupRepository) deleteKeysByPattern(ctx context.Context, pattern string) error {
	var cursor uint64
	for {
		// Scan for keys matching the pattern
		keys, nextCursor, err := r.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}

		// Delete found keys in batch
		if len(keys) > 0 {
			if err := r.client.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("delete failed: %w", err)
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}
