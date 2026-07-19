package neo4j

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLegacyCorpusMigrationAdapterRequiresExplicitEnable(t *testing.T) {
	_, err := NewLegacyCorpusMigrationAdapter(&legacyMigrationReadClient{}, LegacyCorpusAdapterConfig{})
	require.ErrorIs(t, err, ErrLegacyMigrationDisabled)

	_, err = NewLegacyCorpusMigrationAdapter(nil, LegacyCorpusAdapterConfig{Enabled: true})
	require.EqualError(t, err, "neo4j legacy migration adapter: client is required")

	adapter, err := NewLegacyCorpusMigrationAdapter(&legacyMigrationReadClient{}, LegacyCorpusAdapterConfig{
		Enabled:     true,
		MaxPageSize: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, adapter)
}

func TestLegacyCorpusPageCypherIsReadOnlyAndKeepsOptionalRelationshipsOptional(t *testing.T) {
	query := strings.ToLower(legacyCorpusPageCypher())

	assert.Contains(t, query, "match (sf:sourcefragment)")
	assert.Contains(t, query, "$after_source_id")
	assert.Contains(t, query, "order by sf.fragment_id asc")
	assert.Contains(t, query, "limit $limit")
	assert.Contains(t, query, "coalesce(sf.status, 'active') <> 'superseded'")
	assert.Contains(t, query, "coalesce(claim.status, '') in ['rejected', 'superseded']")
	assert.Contains(t, query, "coalesce(sourceclaim.status, '') in ['rejected', 'superseded']")
	assert.Contains(t, query, "fact:fact {team_id: sf.team_id, status: 'active'}")
	assert.NotContains(t, query, "where claim is null")

	for _, forbidden := range []string{
		" create ",
		" merge ",
		" delete ",
		" detach ",
		" set ",
		" remove ",
		" drop ",
		" call db.",
		" call gds",
		" apoc.",
	} {
		assert.NotContains(t, query, forbidden)
	}
}

func TestLegacyCorpusMigrationAdapterReadsPageAndPreservesExactContent(t *testing.T) {
	createdAt := time.Date(2026, 7, 17, 9, 30, 0, 0, time.FixedZone("source", -4*60*60))
	content := "  exact legacy evidence\n"
	client := &legacyMigrationReadClient{
		records: []*driver.Record{{
			Keys: []string{
				"source_id",
				"source_hash",
				"team_id",
				"owner_profile_id",
				"owner_profile_name",
				"content",
				"source",
				"source_type",
				"authority",
				"status",
				"labels",
				"metadata_json",
				"classification_json",
				"created_at",
				"updated_at",
				"claim_hints",
				"fact_hints",
			},
			Values: []any{
				" sf-123 ",
				"",
				" team-a ",
				" profile-a ",
				" Ada ",
				content,
				" legacy://note/1 ",
				" document ",
				" user ",
				" active ",
				[]any{"legacy", " ", "migration"},
				`{"legacy_rank":2}`,
				map[string]any{"sensitivity": "normal"},
				createdAt,
				"2026-07-17T14:30:00Z",
				[]map[string]any{{
					"claim_id":  " claim-1 ",
					"subject":   "Ada",
					"predicate": "likes",
					"object":    "Postgres",
					"status":    "accepted",
					"support":   map[string]any{"weight": float64(0.9)},
				}},
				[]any{map[string]any{
					"fact_id":                " fact-1 ",
					"subject":                "Ada",
					"predicate":              "uses",
					"object":                 "Postgres",
					"status":                 "active",
					"promoted_from_claim_id": "claim-1",
				}},
			},
		}},
	}
	adapter, err := NewLegacyCorpusMigrationAdapter(client, LegacyCorpusAdapterConfig{
		Enabled:     true,
		MaxPageSize: 2,
	})
	require.NoError(t, err)

	page, err := adapter.ReadCorpusPage(context.Background(), LegacyCorpusPageRequest{
		AfterSourceID: " sf-000 ",
		Limit:         99,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "sf-123", page.NextCursor)
	require.Len(t, client.queries, 1)
	require.Zero(t, client.writeCalls)
	require.Equal(t, "sf-000", client.params[0]["after_source_id"])
	require.Equal(t, int64(2), client.params[0]["limit"])

	item := page.Items[0]
	require.Equal(t, LegacyCorpusSourceKind, item.SourceKind)
	require.Equal(t, "sf-123", item.SourceID)
	require.Equal(t, legacyContentHash(content), item.SourceHash)
	require.Equal(t, "team-a", item.TeamID)
	require.Equal(t, "profile-a", item.OwnerProfileID)
	require.Equal(t, "Ada", item.OwnerProfileName)
	require.Equal(t, content, item.Content)
	require.Equal(t, "legacy://note/1", item.Source)
	require.Equal(t, "document", item.SourceType)
	require.Equal(t, "user", item.Authority)
	require.Equal(t, []string{"legacy", "migration"}, item.Labels)
	require.Equal(t, float64(2), item.Metadata["legacy_rank"])
	require.Equal(t, "normal", item.Classification["sensitivity"])
	require.NotNil(t, item.CreatedAt)
	require.Equal(t, time.Date(2026, 7, 17, 13, 30, 0, 0, time.UTC), *item.CreatedAt)
	require.NotNil(t, item.UpdatedAt)
	require.Equal(t, "claim-1", item.Claims[0].ClaimID)
	require.Equal(t, map[string]any{"support": map[string]any{"weight": float64(0.9)}}, item.Claims[0].Metadata)
	require.Equal(t, "fact-1", item.Facts[0].FactID)
	require.Equal(t, map[string]any{"promoted_from_claim_id": "claim-1"}, item.Facts[0].Metadata)

	require.NoError(t, adapter.Close(context.Background()))
	require.Equal(t, 1, client.closeCalls)
}

func TestLegacyCorpusMigrationAdapterRejectsInvalidMetadataJSON(t *testing.T) {
	client := &legacyMigrationReadClient{
		records: []*driver.Record{{
			Keys: []string{
				"source_id",
				"content",
				"metadata_json",
				"classification_json",
			},
			Values: []any{
				"sf-123",
				"evidence",
				"{",
				"{}",
			},
		}},
	}
	adapter, err := NewLegacyCorpusMigrationAdapter(client, LegacyCorpusAdapterConfig{Enabled: true})
	require.NoError(t, err)

	_, err = adapter.ReadCorpusPage(context.Background(), LegacyCorpusPageRequest{Limit: 1})
	require.ErrorContains(t, err, "decode metadata_json")
}

type legacyMigrationReadClient struct {
	records    []*driver.Record
	queries    []string
	params     []map[string]any
	writeCalls int
	closeCalls int
}

func (c *legacyMigrationReadClient) Verify(context.Context) error { return nil }

func (c *legacyMigrationReadClient) ExecuteRead(ctx context.Context, fn driver.ManagedTransactionWork) (any, error) {
	tx := &legacyMigrationReadTx{client: c}
	return fn(tx)
}

func (c *legacyMigrationReadClient) ExecuteWrite(context.Context, driver.ManagedTransactionWork) (any, error) {
	c.writeCalls++
	return nil, errors.New("unexpected neo4j write during v2 legacy corpus migration")
}

func (c *legacyMigrationReadClient) Close(context.Context) error {
	c.closeCalls++
	return nil
}

type legacyMigrationReadTx struct {
	driver.ManagedTransaction
	client *legacyMigrationReadClient
}

func (tx *legacyMigrationReadTx) Run(_ context.Context, cypher string, params map[string]any) (driver.ResultWithContext, error) {
	tx.client.queries = append(tx.client.queries, cypher)
	tx.client.params = append(tx.client.params, params)
	return &stubResultWithContext{records: tx.client.records, open: true}, nil
}
