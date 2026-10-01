//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/embedding"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchReconciliationConfiguredBatchUsesRealAdapterAndDurableCursor(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	contractID := insertSearchTestContract(t, adminDB, rls, "reconciliation-batch", 2, "exact", "")
	ledger := NewStore(appDB, rls, ConflictRuntimeConfig{})
	repo := newSearchFixtureStore(appDB, rls)
	for actor := range 2 {
		teamID := createLedgerTeam(t, adminDB, rls, fmt.Sprintf("reconciliation-batch-%d", actor))
		ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
		for index := actor; index < 101; index += 2 {
			text := fmt.Sprintf("Reconciliation document %d.", index)
			ingest, err := createTestIngest(ctx, ledger, CreateIngestInput{
				TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: fmt.Sprintf("batch-%d", index),
				RequestHash: sha256Hex(text), Evidence: []EvidenceInput{{Content: text}},
			})
			require.NoError(t, err)
			_, err = repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
				TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: ingest.Evidence[0].FragmentID,
				SourceVersion: 1, DocumentText: "stale " + text, EmbeddingContractID: contractID,
			})
			require.NoError(t, err)
		}
	}
	var mu sync.Mutex
	var sizes []int
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input []string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		mu.Lock()
		sizes = append(sizes, len(input.Input))
		mu.Unlock()
		if fail.Load() {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
		data := make([]map[string]any, len(input.Input))
		for index := range data {
			data[index] = map[string]any{"index": index, "embedding": []float32{0.6, 0.8}}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	t.Cleanup(server.Close)
	provider := embedding.NewOpenAIEmbeddingProvider(&config.Config{
		AIAPIURL: server.URL, AIAPIKey: "fixture", AIEmbeddingModel: "test-model",
		AIEmbeddingDimensions: 2, AIEmbeddingMaxBatchItems: 100,
	}, server.Client())
	service := searchapp.NewSearchReconciliationService(searchapp.SearchReconciliationDependencies{
		Repository: repo, Projection: repo, Provider: provider, BatchLimit: 100, ProviderTimeout: 200 * time.Millisecond,
	})
	first, err := service.Run(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 100, first.SelectedCount)
	require.EqualValues(t, 100, first.UpdatedCount)
	second, err := service.Run(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, second.SelectedCount)
	require.EqualValues(t, 1, second.UpdatedCount)
	mu.Lock()
	require.Equal(t, []int{100, 1}, sizes)
	mu.Unlock()
	var current int
	require.NoError(t, adminDB.Raw("SELECT count(*) FROM search_documents WHERE embedding_contract_id = ?::uuid AND search_state = 'current' AND embedding IS NOT NULL", contractID).Row().Scan(&current))
	require.Equal(t, 101, current)
	drained, err := service.Run(ctx)
	require.NoError(t, err)
	require.Zero(t, drained.SelectedCount)
	fail.Store(true)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec("UPDATE search_documents SET embedding = NULL, search_state = 'pending' WHERE embedding_contract_id = ?::uuid", contractID).Error
	}))
	failed, err := service.Run(ctx)
	require.ErrorIs(t, err, searchapp.ErrSearchReconciliationFailed)
	require.Equal(t, "embedding_timeout", failed.ErrorCode)
	require.Zero(t, failed.EmbeddedCount)
	require.Zero(t, failed.UpdatedCount)
	require.NoError(t, adminDB.Raw("SELECT count(*) FROM search_documents WHERE embedding_contract_id = ?::uuid AND embedding IS NOT NULL", contractID).Row().Scan(&current))
	require.Zero(t, current)
}
