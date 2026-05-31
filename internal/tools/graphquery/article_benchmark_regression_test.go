package graphquery

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArticleBenchmarkGraphQueryRegression(t *testing.T) {
	validator := NewCypherValidator()

	t.Run("readonly mutation denial rejects product decision write", func(t *testing.T) {
		err := validator.Validate(`
			MATCH (decision:Fact {team_id: $profileId})
			WHERE decision.subject = 'project:enterprise-export'
			SET decision.object = 'sales says export API ships in Q3'
			RETURN decision
		`)

		require.Error(t, err)
		require.Contains(t, err.Error(), "forbidden clause: SET")
	})

	t.Run("multi-hop condition check accepts fully scoped article query", func(t *testing.T) {
		err := validator.Validate(`
			MATCH (product:SourceFragment {team_id: $profileId})-[:SUPPORTED_BY]->(decision:Fact {team_id: $profileId})<-[:CONTRADICTS]-(claim:Claim {team_id: $profileId})
			WHERE product.source = 'product-roadmap'
			  AND decision.status = 'active'
			  AND claim.status = 'disputed'
			RETURN product.fragment_id, decision.fact_id, claim.claim_id
			ORDER BY decision.recorded_at DESC
			LIMIT 5
		`)

		require.NoError(t, err)
	})

	t.Run("multi-hop condition check rejects unscoped middle node", func(t *testing.T) {
		err := validator.Validate(`
			MATCH (product:SourceFragment {team_id: $profileId})-[:SUPPORTED_BY]->(decision:Fact)<-[:CONTRADICTS]-(claim:Claim {team_id: $profileId})
			WHERE product.source = 'product-roadmap'
			  AND decision.status = 'active'
			  AND claim.status = 'disputed'
			RETURN product.fragment_id, decision.fact_id, claim.claim_id
		`)

		require.Error(t, err)
		require.Contains(t, err.Error(), "team_id")
	})

	t.Run("team isolation rejects boolean escape in multi-hop condition", func(t *testing.T) {
		err := validator.Validate(`
			MATCH (product:SourceFragment {team_id: $profileId})-[:SUPPORTED_BY]->(decision:Fact {team_id: $profileId})<-[:CONTRADICTS]-(claim:Claim {team_id: $profileId})
			WHERE decision.status = 'active'
			  AND (claim.team_id = $profileId OR claim.source = 'sales')
			RETURN product, decision, claim
		`)

		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported boolean operator: OR")
	})
}
