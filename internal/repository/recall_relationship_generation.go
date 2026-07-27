package repository

const relationshipForegroundRecallGenerationMetadataKey = "relationship_foreground_recall_generation_id"

const recallRelationshipGenerationScopeSQL = `
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

const recallRelationshipGenerationDocumentSQL = `(
		    document.projection_generation_id = generation.projection_generation_id
		    OR (
		        document.projection_generation_id IS NULL
		        AND (
		            generation.projection_generation_id IS NULL
		            OR COALESCE(document.metadata->>'` + relationshipForegroundRecallGenerationMetadataKey + `', '') = generation.projection_generation_id::text
		        )
		    )
		)`
