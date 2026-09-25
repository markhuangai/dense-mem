package postgres

import (
	"fmt"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

// RecallRelationshipGenerationScopeSQL selects an activated generation, the
// latest generation if none is activated, or a no-generation row.
const RecallRelationshipGenerationScopeSQL = `
		recall_relationship_generation_team AS (
		    SELECT ?::uuid AS team_id
		),
		activated_relationship_generation AS (
		    SELECT generation.projection_generation_id, generation.created_at, generation.activated_at, generation.generation
		    FROM recall_relationship_generation_team AS scope
		    JOIN search_projection_generations AS generation
		      ON generation.team_id = scope.team_id
		     AND generation.source_kind = 'relationship'
		     AND generation.projection_format_version = 2
		     AND generation.state = 'current'
		     AND generation.activated_at IS NOT NULL
		    ORDER BY generation.generation DESC, generation.created_at DESC
		    LIMIT 1
		),
		recall_relationship_generation AS (
		    SELECT choice.projection_generation_id, choice.created_at, choice.activated_at
		    FROM (
		        SELECT projection_generation_id, created_at, activated_at, 0 AS priority, generation
		        FROM activated_relationship_generation
		        UNION ALL
		        SELECT generation.projection_generation_id, generation.created_at, generation.activated_at, 1 AS priority, generation.generation
		        FROM recall_relationship_generation_team AS scope
		        JOIN search_projection_generations AS generation
		          ON generation.team_id = scope.team_id
		         AND generation.source_kind = 'relationship'
		         AND generation.projection_format_version = 2
		        WHERE NOT EXISTS (SELECT 1 FROM activated_relationship_generation)
		        UNION ALL
		        SELECT NULL::uuid, NULL::timestamptz, NULL::timestamptz, 2 AS priority, 0 AS generation
		        WHERE NOT EXISTS (
		            SELECT 1
		            FROM recall_relationship_generation_team AS scope
		            JOIN search_projection_generations AS generation
		              ON generation.team_id = scope.team_id
		             AND generation.source_kind = 'relationship'
		             AND generation.projection_format_version = 2
		        )
		    ) AS choice
		    ORDER BY choice.priority ASC, choice.generation DESC
		    LIMIT 1
		)`

// RecallRelationshipVectorGenerationScopeSQL selects only a current
// generation, or a no-generation row when none has ever existed.
const RecallRelationshipVectorGenerationScopeSQL = `generation_count AS (
			    SELECT count(*) AS value
			    FROM search_projection_generations
			    WHERE team_id = ?::uuid
			      AND source_kind = 'relationship'
			      AND projection_format_version = 2
			),
			current_generation AS (
			    SELECT choice.projection_generation_id
			    FROM (
			        SELECT projection_generation_id, 0 AS priority, generation
			        FROM search_projection_generations
			        WHERE team_id = ?::uuid
			          AND source_kind = 'relationship'
			          AND projection_format_version = 2
			          AND state = 'current'
			        UNION ALL
			        SELECT NULL::uuid, 1 AS priority, 0 AS generation
			        FROM generation_count
			        WHERE value = 0
			    ) AS choice
			    ORDER BY choice.priority ASC, choice.generation DESC
			    LIMIT 1
			)`

// RelationshipGenerationDocumentSQL tests membership in a selected generation.
// Callers supply fixed SQL references; request values remain bound parameters.
func RelationshipGenerationDocumentSQL(documentAlias, generationIDSQL, generationTextSQL string) string {
	return fmt.Sprintf(`(
        %s.projection_generation_id = %s
        OR (
            %s.projection_generation_id IS NULL
            AND (
                %s IS NULL
                OR COALESCE(%s.metadata->>'%s', '') = %s
            )
        )
    )`,
		documentAlias, generationIDSQL, documentAlias,
		generationIDSQL, documentAlias,
		knowledgecontract.RelationshipForegroundRecallGenerationMetadataKey,
		generationTextSQL,
	)
}
