//go:build integration

package postgres

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/markhuangai/dense-mem/internal/domain"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	semantic "github.com/markhuangai/dense-mem/internal/semanticwrite/contract"
	"github.com/markhuangai/dense-mem/internal/topics/assignment"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTopicEmbeddingBackfillRevisionAndScopeFences(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	_, err := searchpostgres.NewStore(f.admin, f.rls).EnsureActiveSearchContract(ctx, searchmaintenance.EnsureActiveSearchContractInput{Model: "fixture-embedding", Dimensions: 3})
	require.NoError(t, err)
	first, second := testTopic("first-vector-topic"), testTopic("second-vector-topic")
	revalidationPublish(t, f, topic.Change{Record: first}, topic.Change{Record: second})
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	turn, err := f.store.ClaimMaintenanceTurn(topic.WithTopicEmbeddingStage(ctx), window.ID, time.Now().UTC(), 3*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	defer func() { require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn)) }()
	batch, err := f.store.ReadTopicEmbeddingBatch(ctx, *turn)
	require.NoError(t, err)
	require.Len(t, batch.Topics, 2)
	result := semantic.Result{Fence: batch.Fence, Model: batch.Fence.Model}
	for _, item := range batch.Topics {
		result.Embeddings = append(result.Embeddings, semantic.Embedding{DocumentHash: fmt.Sprintf("%s@%d", item.ID, item.Version), Vector: []float32{1, 0, 0}})
	}
	invalid := result
	invalid.Embeddings = append([]semantic.Embedding(nil), result.Embeddings...)
	invalid.Embeddings[0].Vector = []float32{1}
	_, err = f.store.CompleteTopicEmbeddingBatch(ctx, *turn, *batch, invalid)
	require.ErrorIs(t, err, topic.ErrInvalid)
	first.Definition.Description = "Changed while embedding was in flight."
	revalidationPublish(t, f, topic.Change{Record: first, ExpectedVersion: 1})
	count, err := f.store.CompleteTopicEmbeddingBatch(ctx, *turn, *batch, result)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	retry, err := f.store.ReadTopicEmbeddingBatch(ctx, *turn)
	require.NoError(t, err)
	require.Len(t, retry.Topics, 1)
	require.Equal(t, first.ID, retry.Topics[0].ID)
	require.EqualValues(t, 2, retry.Topics[0].Version)
	result = semantic.Result{Fence: retry.Fence, Model: retry.Fence.Model,
		Embeddings: []semantic.Embedding{{DocumentHash: fmt.Sprintf("%s@%d", first.ID, retry.Topics[0].Version), Vector: []float32{0, 1, 0}}}}
	count, err = f.store.CompleteTopicEmbeddingBatch(ctx, *turn, *retry, result)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	remaining, err := f.store.ReadTopicEmbeddingBatch(ctx, *turn)
	require.NoError(t, err)
	require.Nil(t, remaining)

	other := maintenanceOtherTeam(t, f)
	require.NoError(t, f.rls.WithTeamTx(ctx, f.app, other.team, func(tx *gorm.DB) error {
		var visible int
		if err := tx.Raw(`SELECT count(*) FROM topic_embeddings WHERE team_id=?::uuid`, f.team).Row().Scan(&visible); err != nil {
			return err
		}
		require.Zero(t, visible)
		return nil
	}))
}

func TestTopicHybridSearchSkipsStaleCandidates(t *testing.T) {
	f := newOrganizationFixture(t)
	current := seedStaleVocabulary(t, f, 20, 1)[0]
	scope := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString()}
	result, err := f.store.ReadAssignmentCandidates(context.Background(), scope, assignment.Slice{Text: "PostgreSQL relational storage"})
	require.NoError(t, err)
	require.Len(t, result.Topics, 1)
	require.Equal(t, current.ID, result.Topics[0].ID)
}

func TestTopicHybridSearchIndexTiersAndIsolation(t *testing.T) {
	for _, tier := range []struct {
		name, strategy, operator string
		dimensions               int
	}{
		{"vector", string(domain.VectorIndexVectorHNSW), "vector_cosine_ops", 3},
		{"halfvec", string(domain.VectorIndexHalfvecHNSW), "halfvec_cosine_ops", 2001},
		{"binary", string(domain.VectorIndexBinaryHNSW), "bit_hamming_ops", 4001},
	} {
		t.Run(tier.name, func(t *testing.T) {
			f := newOrganizationFixture(t)
			ctx := context.Background()
			search := searchpostgres.NewStore(f.admin, f.rls)
			config := searchmaintenance.EnsureActiveSearchContractInput{Model: "fixture-embedding", Dimensions: tier.dimensions}
			active, err := search.EnsureActiveSearchContract(ctx, config)
			require.NoError(t, err)
			require.Equal(t, tier.strategy, active.Contract.IndexStrategy)
			semanticTopic := testTopic("aqueduct")
			revalidationPublish(t, f, topic.Change{Record: semanticTopic})
			window := maintenanceWindow(t, f, maintenanceSettings(t, f))
			turn, err := f.store.ClaimMaintenanceTurn(topic.WithTopicEmbeddingStage(ctx), window.ID, time.Now(), 3*time.Minute)
			require.NoError(t, err)
			require.NotNil(t, turn)
			batch, err := f.store.ReadTopicEmbeddingBatch(ctx, *turn)
			require.NoError(t, err)
			require.Len(t, batch.Topics, 1)
			vector := make([]float32, tier.dimensions)
			vector[0] = 1
			result := semantic.Result{Fence: batch.Fence, Model: batch.Fence.Model,
				Embeddings: []semantic.Embedding{{DocumentHash: fmt.Sprintf("%s@%d", semanticTopic.ID, 1), Vector: vector}}}
			written, err := f.store.CompleteTopicEmbeddingBatch(ctx, *turn, *batch, result)
			require.NoError(t, err)
			require.Equal(t, 1, written)
			require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
			lexicalTopic := testTopic("sapphire")
			revalidationPublish(t, f, topic.Change{Record: lexicalTopic})
			other := maintenanceOtherTeam(t, f)
			foreignTopic := testTopic("sapphire-foreign")
			revalidationPublish(t, other, topic.Change{Record: foreignTopic})
			require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
				return tx.Exec(`INSERT INTO topic_embeddings(team_id,shared_space_id,space_generation,topic_id,topic_version,embedding_contract_id,embedding_dimensions,embedding)
					VALUES(?::uuid,?::uuid,?,?::uuid,1,?::uuid,?,(?::real[])::vector)`, other.team, other.space, other.generation,
					foreignTopic.ID, active.Contract.EmbeddingContractID, tier.dimensions, pq.Array(vector)).Error
			}))
			scope := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString()}
			input := assignment.Slice{Text: "A sapphire observation.", Embedding: vector,
				EmbeddingContractID: active.Contract.EmbeddingContractID, SearchIndexGenerationID: active.Contract.SearchIndexGenerationID}
			candidates, err := f.store.ReadAssignmentCandidates(ctx, scope, input)
			require.NoError(t, err)
			require.Positive(t, candidates.VectorCount)
			require.Positive(t, candidates.FullTextCount)
			require.Positive(t, candidates.UnembeddedTopicCount)
			require.True(t, slices.ContainsFunc(candidates.Topics, func(item assignment.Topic) bool { return item.ID == semanticTopic.ID }))
			require.True(t, slices.ContainsFunc(candidates.Topics, func(item assignment.Topic) bool { return item.ID == lexicalTopic.ID }))
			require.False(t, slices.ContainsFunc(candidates.Topics, func(item assignment.Topic) bool { return item.ID == foreignTopic.ID }))
			input.Embedding = nil
			lexical, err := f.store.ReadAssignmentCandidates(ctx, scope, input)
			require.NoError(t, err)
			require.True(t, lexical.VectorUnavailable)
			require.True(t, slices.ContainsFunc(lexical.Topics, func(item assignment.Topic) bool { return item.ID == lexicalTopic.ID }))
			foreignScope := assignment.Scope{TeamID: other.team, SpaceID: other.space, Generation: other.generation, OperationID: uuid.NewString()}
			input.Embedding = vector
			foreign, err := other.store.ReadAssignmentCandidates(ctx, foreignScope, input)
			require.NoError(t, err)
			require.True(t, slices.ContainsFunc(foreign.Topics, func(item assignment.Topic) bool { return item.ID == foreignTopic.ID }))
			require.False(t, slices.ContainsFunc(foreign.Topics, func(item assignment.Topic) bool { return item.ID == semanticTopic.ID || item.ID == lexicalTopic.ID }))
			semanticTopic.Definition.Description = "A materially revised aqueduct topic."
			revalidationPublish(t, f, topic.Change{Record: semanticTopic, ExpectedVersion: 1})
			current, err := f.store.ReadAssignmentCandidates(ctx, scope, input)
			require.NoError(t, err)
			require.False(t, slices.ContainsFunc(current.Topics, func(item assignment.Topic) bool { return item.ID == semanticTopic.ID }), "an old vector must not retrieve a newer Topic revision")
			var definition string
			require.NoError(t, f.admin.Raw(`SELECT indexdef FROM pg_indexes WHERE tablename='topic_embeddings' AND indexname=?`, strings.Replace(active.Contract.PhysicalIndexName, "search_", "topic_", 1)).Row().Scan(&definition))
			require.Contains(t, definition, tier.operator)
			if tier.name == "vector" {
				index := strings.Replace(active.Contract.PhysicalIndexName, "search_", "topic_", 1)
				require.NoError(t, f.admin.Exec("DROP INDEX CONCURRENTLY "+pq.QuoteIdentifier(index)).Error)
				err := f.admin.Exec("CREATE INDEX CONCURRENTLY " + pq.QuoteIdentifier(index) +
					" ON topic_embeddings USING hnsw ((embedding::vector(2)) vector_cosine_ops)").Error
				require.Error(t, err, "the fixture must leave an invalid HNSW build")
				var valid bool
				require.NoError(t, f.admin.Raw(`SELECT indisvalid FROM pg_index WHERE indexrelid=?::regclass`, index).Row().Scan(&valid))
				require.False(t, valid)
				_, err = search.EnsureActiveSearchContract(ctx, config)
				require.NoError(t, err)
				require.NoError(t, f.admin.Raw(`SELECT indisvalid FROM pg_index WHERE indexrelid=?::regclass`, index).Row().Scan(&valid))
				require.True(t, valid)
				require.NoError(t, f.admin.Raw(`SELECT pg_get_indexdef(?::regclass)`, index).Row().Scan(&definition))
				require.Contains(t, definition, "vector_cosine_ops")
				require.Contains(t, definition, "vector(3)")
			}
		})
	}
}
