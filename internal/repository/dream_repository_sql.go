package repository

const hypothesisExactDerivationIneligiblePredicateSQL = `(
	(
		hypotheses.lane = 'evidence_discovery'
		AND (
			NOT EXISTS (
				SELECT 1
				FROM hypothesis_evidence_derivation_sources derivation
				WHERE derivation.team_id = hypotheses.team_id
				  AND derivation.space_id = hypotheses.space_id
				  AND derivation.space_generation = hypotheses.space_generation
				  AND derivation.hypothesis_id = hypotheses.hypothesis_id
			)
			OR EXISTS (
				SELECT 1
				FROM hypothesis_evidence_derivation_sources derivation
				LEFT JOIN evidence_fragments fragment
				  ON fragment.team_id = derivation.team_id
				 AND fragment.fragment_id = derivation.fragment_id
				 AND fragment.space_id = derivation.space_id
				 AND fragment.space_generation = derivation.space_generation
				LEFT JOIN evidence_sources source
				  ON source.team_id = fragment.team_id
				 AND source.source_id = fragment.source_id
				 AND source.space_id = fragment.space_id
				 AND source.space_generation = fragment.space_generation
				WHERE derivation.team_id = hypotheses.team_id
				  AND derivation.space_id = hypotheses.space_id
				  AND derivation.space_generation = hypotheses.space_generation
				  AND derivation.hypothesis_id = hypotheses.hypothesis_id
					AND (
						fragment.fragment_id IS NULL
						OR COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') = 'true'
						OR EXISTS (
							SELECT 1
							FROM knowledge_ingests ingest
							WHERE ingest.team_id = fragment.team_id
							  AND ingest.ingest_id = fragment.ingest_id
							  AND COALESCE(ingest.metadata->>'conflict_resolution_deletion_only', '') = 'true'
						)
						OR fragment.source_id IS DISTINCT FROM NULLIF(derivation.source_id::text, '')::uuid
					OR fragment.source_revision_id IS DISTINCT FROM NULLIF(derivation.source_revision_id::text, '')::uuid
					OR fragment.authority <> derivation.authority
					OR substring(fragment.content FROM derivation.span_start + 1 FOR derivation.span_end - derivation.span_start) <> derivation.quote
					OR (fragment.source_id IS NOT NULL AND source.current_revision_id IS DISTINCT FROM fragment.source_revision_id)
					OR EXISTS (
						SELECT 1 FROM evidence_exact_aliases alias
						WHERE alias.team_id = fragment.team_id AND alias.alias_fragment_id = fragment.fragment_id
					)
					OR EXISTS (
						SELECT 1 FROM evidence_quarantines quarantine
						WHERE quarantine.team_id = fragment.team_id AND quarantine.fragment_id = fragment.fragment_id
						  AND quarantine.space_id = fragment.space_id AND quarantine.space_generation = fragment.space_generation
						  AND quarantine.status = 'active'
					)
					OR EXISTS (
						SELECT 1 FROM evidence_lifecycle_events lifecycle
						WHERE lifecycle.team_id = fragment.team_id AND lifecycle.target_fragment_id = fragment.fragment_id
						  AND lifecycle.space_id = fragment.space_id AND lifecycle.space_generation = fragment.space_generation
					)
					OR COALESCE((
						SELECT security.decision
						FROM evidence_security_events security
						WHERE security.team_id = fragment.team_id AND security.fragment_id = fragment.fragment_id
						ORDER BY security.created_at DESC, security.security_event_id DESC
						LIMIT 1
					), '') NOT IN ('pass', 'released')
				  )
			)
		)
	)
	OR (
		hypotheses.lane <> 'evidence_discovery'
		AND (
			hypotheses.generator_kind <> 'provider'
			OR NOT EXISTS (
		SELECT 1
		FROM hypothesis_derivation_sources derivation
		WHERE derivation.team_id = hypotheses.team_id
		  AND derivation.space_id = hypotheses.space_id
		  AND derivation.space_generation = hypotheses.space_generation
		  AND derivation.hypothesis_id = hypotheses.hypothesis_id
		  AND derivation.premise_position = 1
			)
			OR NOT EXISTS (
		SELECT 1
		FROM hypothesis_derivation_sources derivation
		WHERE derivation.team_id = hypotheses.team_id
		  AND derivation.space_id = hypotheses.space_id
		  AND derivation.space_generation = hypotheses.space_generation
		  AND derivation.hypothesis_id = hypotheses.hypothesis_id
		  AND derivation.premise_position = 2
			)
			OR EXISTS (
		SELECT 1
		FROM hypothesis_derivation_sources derivation
		LEFT JOIN relationship_records relationship
		  ON relationship.team_id = derivation.team_id
		 AND relationship.relationship_id = derivation.relationship_id
		 AND relationship.space_id = derivation.space_id
		 AND relationship.space_generation = derivation.space_generation
		WHERE derivation.team_id = hypotheses.team_id
		  AND derivation.space_id = hypotheses.space_id
		  AND derivation.space_generation = hypotheses.space_generation
		  AND derivation.hypothesis_id = hypotheses.hypothesis_id
		  AND NOT COALESCE(
			relationship.relationship_id IS NOT NULL
			AND relationship.identity_alias_of_relationship_id IS NULL
			AND relationship.version = derivation.relationship_version
			AND EXISTS (
				SELECT 1
					FROM entity_records subject_entity
					WHERE subject_entity.team_id = relationship.team_id
					  AND subject_entity.entity_id = relationship.subject_entity_id
					  AND subject_entity.space_id = relationship.space_id
					  AND subject_entity.space_generation = relationship.space_generation
				  AND subject_entity.status = 'active'
			)
			AND (
				relationship.object_entity_id IS NULL
				OR EXISTS (
					SELECT 1
					FROM entity_records object_entity
					WHERE object_entity.team_id = relationship.team_id
					  AND object_entity.entity_id = relationship.object_entity_id
					  AND object_entity.space_id = relationship.space_id
					  AND object_entity.space_generation = relationship.space_generation
					  AND object_entity.status = 'active'
				)
			)
			AND NOT EXISTS (
				SELECT 1
				FROM relationship_cross_references cross_reference
					WHERE cross_reference.team_id = relationship.team_id
					  AND cross_reference.space_id = relationship.space_id
					  AND cross_reference.space_generation = relationship.space_generation
				  AND cross_reference.target_relationship_id = relationship.relationship_id
				  AND cross_reference.kind = 'challenges'
			)
			AND (
				(
					relationship.status = 'active'
					AND derivation.support_id IS NOT NULL
					AND EXISTS (
						SELECT 1
						FROM relationship_evidence_supports support
						JOIN evidence_fragments fragment
						  ON fragment.team_id = support.team_id
						 AND fragment.fragment_id = support.fragment_id
						 AND fragment.space_id = support.space_id
						 AND fragment.space_generation = support.space_generation
			LEFT JOIN evidence_sources source
			  ON source.team_id = support.team_id
			 AND source.source_id = support.source_id
			 AND source.space_id = support.space_id
			 AND source.space_generation = support.space_generation
						WHERE support.team_id = derivation.team_id
						  AND support.space_id = derivation.space_id
						  AND support.space_generation = derivation.space_generation
						  AND support.support_id = derivation.support_id
						  AND support.relationship_id = derivation.relationship_id
						  AND support.fragment_id = derivation.fragment_id
						  AND support.source_id IS NOT DISTINCT FROM derivation.source_id
						  AND support.source_revision_id IS NOT DISTINCT FROM derivation.source_revision_id
						  AND support.source_group_key = derivation.source_group_key
						  AND support.span_start = derivation.span_start
						  AND support.span_end = derivation.span_end
						  AND support.authority = derivation.authority
						  AND substring(fragment.content FROM support.span_start + 1 FOR support.span_end - support.span_start) = derivation.quote
						  AND COALESCE((
							SELECT decision.decision
								FROM relationship_support_decision_events decision
								WHERE decision.team_id = support.team_id
								  AND decision.space_id = support.space_id
								  AND decision.space_generation = support.space_generation
							  AND decision.support_id = support.support_id
							ORDER BY decision.created_at DESC, decision.support_decision_id DESC
							LIMIT 1
						), '') IN ('grant', 'reinstate')
						  AND NOT EXISTS (
							SELECT 1
								FROM evidence_quarantines quarantine
								WHERE quarantine.team_id = support.team_id
								  AND quarantine.space_id = support.space_id
								  AND quarantine.space_generation = support.space_generation
							  AND quarantine.fragment_id = support.fragment_id
							  AND quarantine.status = 'active'
						)
						  AND NOT EXISTS (
							SELECT 1
								FROM evidence_lifecycle_events lifecycle
								WHERE lifecycle.team_id = support.team_id
								  AND lifecycle.space_id = support.space_id
								  AND lifecycle.space_generation = support.space_generation
							  AND lifecycle.target_fragment_id = support.fragment_id
						)
						  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
					)
				)
				OR (
					relationship.status = 'pending_evidence'
					AND derivation.observation_id IS NOT NULL
					AND EXISTS (
						SELECT 1
						FROM relationship_observations observation
						JOIN verification_events verification
						  ON verification.team_id = observation.team_id
						 AND verification.observation_id = observation.observation_id
						 AND verification.space_id = observation.space_id
						 AND verification.space_generation = observation.space_generation
						JOIN LATERAL jsonb_array_elements(observation.evidence) evidence(value) ON true
						JOIN evidence_fragments fragment
						  ON fragment.team_id = observation.team_id
						 AND evidence.value->>'fragment_id' = fragment.fragment_id::text
						 AND fragment.space_id = observation.space_id
						 AND fragment.space_generation = observation.space_generation
			LEFT JOIN evidence_sources source
			  ON source.team_id = fragment.team_id
			 AND source.source_id = fragment.source_id
			 AND source.space_id = fragment.space_id
			 AND source.space_generation = fragment.space_generation
						WHERE observation.team_id = derivation.team_id
						  AND observation.space_id = derivation.space_id
						  AND observation.space_generation = derivation.space_generation
						  AND observation.observation_id = derivation.observation_id
						  AND observation.relationship_id = derivation.relationship_id
						  AND verification.evidence_verdict IN ('insufficient', 'entailed')
						  AND verification.gate_result = 'below_write_threshold'
						  AND fragment.fragment_id = derivation.fragment_id
						  AND fragment.source_id IS NOT DISTINCT FROM derivation.source_id
						  AND fragment.source_revision_id IS NOT DISTINCT FROM derivation.source_revision_id
						  AND COALESCE(NULLIF(fragment.source_ref, ''), 'pending_observation') = derivation.source_group_key
						  AND fragment.authority = derivation.authority
						  AND evidence.value->>'start' = derivation.span_start::text
						  AND evidence.value->>'end' = derivation.span_end::text
						  AND substring(fragment.content FROM derivation.span_start + 1 FOR derivation.span_end - derivation.span_start) = derivation.quote
						  AND NOT EXISTS (
							SELECT 1
								FROM evidence_quarantines quarantine
								WHERE quarantine.team_id = fragment.team_id
								  AND quarantine.space_id = fragment.space_id
								  AND quarantine.space_generation = fragment.space_generation
							  AND quarantine.fragment_id = fragment.fragment_id
							  AND quarantine.status = 'active'
						)
						  AND NOT EXISTS (
							SELECT 1
								FROM evidence_lifecycle_events lifecycle
								WHERE lifecycle.team_id = fragment.team_id
								  AND lifecycle.space_id = fragment.space_id
								  AND lifecycle.space_generation = fragment.space_generation
							  AND lifecycle.target_fragment_id = fragment.fragment_id
						)
						  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
					)
				)
			), false)
			)
		)
	))`
