package repository

const recallRelationshipGenerationScopeSQL = `
		recall_relationship_generation AS (
		    SELECT choice.projection_generation_id, choice.created_at, choice.activated_at
		    FROM (
		        SELECT projection_generation_id, created_at, activated_at, 0 AS priority, generation
		        FROM search_projection_generations
		        WHERE team_id = ?::uuid
		          AND source_kind = 'relationship'
		          AND projection_format_version = 2
		        UNION ALL
		        SELECT NULL::uuid, NULL::timestamptz, NULL::timestamptz, 1 AS priority, 0 AS generation
		        WHERE NOT EXISTS (
		            SELECT 1
		            FROM search_projection_generations
		            WHERE team_id = ?::uuid
		              AND source_kind = 'relationship'
		              AND projection_format_version = 2
		        )
		    ) AS choice
		    ORDER BY choice.priority ASC, choice.generation DESC
		    LIMIT 1
		)`

const recallRelationshipGenerationDocumentSQL = `(
		    document.projection_generation_id = generation.projection_generation_id
		    OR (
		        document.projection_generation_id IS NULL
		        AND (
		            generation.projection_generation_id IS NULL
		            OR document.updated_at >= COALESCE(generation.activated_at, generation.created_at)
		        )
		    )
		)`
