//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedSessionErasureFixture(t *testing.T, ctx context.Context, adminDB *gorm.DB, rls *storage.RLS, teamID, ownerID, spaceID uuid.UUID) {
	t.Helper()
	sessionID := uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		generation := int64(0)
		if err := tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?`, spaceID).Row().Scan(&generation); err != nil {
			return err
		}
		for _, statement := range []string{
			`INSERT INTO session_submissions (team_id,owner_profile_id,space_id,space_generation,submission_id,idempotency_key,request_hash,intake,prior_context,new_event_indices,accepted_event_count,duplicate_event_count) VALUES (?,?,?,?,?,'session-erase','sha256:' || repeat('a',64),'{}','[]','[0]',1,0)`,
			`INSERT INTO session_events (team_id,owner_profile_id,space_id,space_generation,submission_id,identity_hash,event_index,framework,app_name,user_id,session_id,event_id,body) VALUES (?,?,?,?,?,'sha256:' || repeat('b',64),0,'generic','notes','external-user','session','event','{"text":"private session raw text"}')`,
			`INSERT INTO session_extraction_checkpoints (team_id,owner_profile_id,space_id,space_generation,submission_id,window_index,body) VALUES (?,?,?,?,?,0,'{}')`,
			`INSERT INTO session_submission_receipts (team_id,owner_profile_id,space_id,space_generation,submission_id,body) VALUES (?,?,?,?,?,'{}')`,
			`INSERT INTO session_submission_diagnostics (team_id,owner_profile_id,space_id,space_generation,submission_id,body) VALUES (?,?,?,?,?,'{}')`,
		} {
			if err := tx.Exec(statement, teamID, ownerID, spaceID, generation, sessionID).Error; err != nil {
				return err
			}
		}
		return nil
	}))
}
