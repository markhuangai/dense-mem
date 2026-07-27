package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecallEvidenceHistoricalIncludesNonCurrentSearchDocuments(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		token string
	}{
		{name: "not required", state: "not_required", token: "historicalnotrequired"},
		{name: "failed", state: "failed", token: "historicalfailed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
			defer cleanup()
			ctx := context.Background()
			teamID := createLedgerTeam(t, adminDB, rls, "recall-history-"+tc.state)
			ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-history-owner")
			insertSearchTestContract(t, adminDB, rls, "recall-history-"+tc.state, 3, "exact", "")
			ledgerRepo := NewLedgerRepository(appDB, rls)
			searchRepo := NewSearchRepository(appDB, rls)
			content := tc.token + " PostgreSQL evidence was current before projection state changed."
			ingest, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
				TeamID:         teamID,
				OwnerProfileID: ownerID,
				Evidence: []EvidenceInput{{
					Content: content,
				}},
			})
			require.NoError(t, err)
			require.Len(t, ingest.Evidence, 1)
			_, err = searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
				TeamID:         teamID,
				OwnerProfileID: ownerID,
				SourceKind:     "evidence",
				SourceID:       ingest.Evidence[0].FragmentID,
				SourceVersion:  1,
				DocumentText:   content,
			})
			require.NoError(t, err)
			knownAt := time.Now().UTC()
			err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
				return tx.Exec(`
					UPDATE search_documents
					SET search_state = ?,
					    updated_at = now()
					WHERE team_id = ?::uuid
					  AND source_kind = 'evidence'
					  AND source_id = ?::uuid
				`, tc.state, teamID, ingest.Evidence[0].FragmentID).Error
			})
			require.NoError(t, err)

			current, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
				TeamID: teamID,
				Query:  tc.token,
				Limit:  10,
			})
			require.NoError(t, err)
			require.Empty(t, current.Results)

			historical, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
				TeamID:  teamID,
				Query:   tc.token,
				KnownAt: &knownAt,
				Limit:   10,
			})
			require.NoError(t, err)
			require.Len(t, historical.Results, 1)
			require.Equal(t, ingest.Evidence[0].FragmentID, historical.Results[0].EvidenceID)
		})
	}
}
