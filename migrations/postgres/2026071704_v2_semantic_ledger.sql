-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE OR REPLACE FUNCTION prevent_v2_reference_definition_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF current_setting('app.tx_mode', true) NOT IN ('system', 'migration') THEN
        RAISE EXCEPTION '% is reference data: % requires system or migration mode', TG_TABLE_NAME, TG_OP;
    END IF;
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        RAISE EXCEPTION '% is versioned reference data: % operations are not allowed', TG_TABLE_NAME, TG_OP;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS predicate_definitions (
    predicate_key TEXT NOT NULL,
    version INTEGER NOT NULL,
    aliases TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    allowed_subject_kinds TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    allowed_object_kinds TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    relationship_kind TEXT NOT NULL,
    current_cardinality TEXT NOT NULL,
    lifecycle_state TEXT NOT NULL DEFAULT 'active',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (predicate_key, version),
    CONSTRAINT predicate_definitions_key_nonempty CHECK (btrim(predicate_key) <> ''),
    CONSTRAINT predicate_definitions_version_check CHECK (version >= 1),
    CONSTRAINT predicate_definitions_relationship_kind_check CHECK (relationship_kind IN ('state', 'event')),
    CONSTRAINT predicate_definitions_current_cardinality_check CHECK (current_cardinality IN ('one', 'many')),
    CONSTRAINT predicate_definitions_lifecycle_state_check CHECK (lifecycle_state IN ('active', 'deprecated', 'retired')),
    CONSTRAINT predicate_definitions_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS predicate_definitions_aliases_idx
    ON predicate_definitions USING GIN (aliases);

DROP TRIGGER IF EXISTS predicate_definitions_reference_guard ON predicate_definitions;
CREATE TRIGGER predicate_definitions_reference_guard
    BEFORE INSERT OR UPDATE OR DELETE ON predicate_definitions
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_reference_definition_mutation();

INSERT INTO predicate_definitions (
    predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
    relationship_kind, current_cardinality
) VALUES
    ('works_on', 1, ARRAY['is_working_on']::text[], ARRAY['person','organization','project','product','other']::text[], ARRAY['project','product','organization','concept','other']::text[], 'state', 'many'),
    ('uses', 1, ARRAY['depends_on','uses_runtime']::text[], ARRAY['person','organization','project','product','concept','other']::text[], ARRAY['project','product','organization','concept','other']::text[], 'state', 'many'),
    ('primary_database', 1, ARRAY['main_database']::text[], ARRAY['project','product','organization','other']::text[], ARRAY['project','product','concept','other']::text[], 'state', 'one'),
	    ('released', 1, ARRAY['shipped']::text[], ARRAY['project','product','other']::text[], ARRAY['date','date_time','string']::text[], 'event', 'many')
	ON CONFLICT (predicate_key, version) DO NOTHING;

ALTER TABLE evidence_fragments
    DROP CONSTRAINT IF EXISTS evidence_fragments_fragment_owner_ref_unique;
ALTER TABLE evidence_fragments
    ADD CONSTRAINT evidence_fragments_fragment_owner_ref_unique
    UNIQUE (team_id, fragment_id, owner_profile_id);

ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_owner_ref_unique;
ALTER TABLE placement_items
    ADD CONSTRAINT placement_items_owner_ref_unique
    UNIQUE (team_id, placement_item_id, owner_profile_id);

CREATE TABLE IF NOT EXISTS entity_records (
    team_id UUID NOT NULL,
    entity_id UUID NOT NULL DEFAULT gen_random_uuid(),
    entity_kind TEXT NOT NULL,
    identity_context JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'active',
    version INTEGER NOT NULL DEFAULT 1,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, entity_id),
    FOREIGN KEY (team_id) REFERENCES semantic_team_refs(team_id) ON DELETE RESTRICT,
    CONSTRAINT entity_records_kind_check CHECK (entity_kind IN (
        'person', 'organization', 'project', 'product', 'place', 'document', 'concept', 'other'
    )),
    CONSTRAINT entity_records_status_check CHECK (status IN ('active', 'retired', 'needs_review')),
    CONSTRAINT entity_records_version_check CHECK (version >= 1),
    CONSTRAINT entity_records_context_object_check CHECK (jsonb_typeof(identity_context) = 'object'),
    CONSTRAINT entity_records_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS entity_records_team_kind_idx
    ON entity_records(team_id, entity_kind, created_at DESC);

CREATE TABLE IF NOT EXISTS entity_names (
    team_id UUID NOT NULL,
    entity_name_id UUID NOT NULL DEFAULT gen_random_uuid(),
    entity_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    display_name TEXT NOT NULL,
    normalized_name TEXT NOT NULL,
    name_kind TEXT NOT NULL,
    locale TEXT NOT NULL DEFAULT '',
    valid_from TIMESTAMPTZ NULL,
    valid_to TIMESTAMPTZ NULL,
    fragment_id UUID NULL,
    span_start INTEGER NULL,
    span_end INTEGER NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	    PRIMARY KEY (team_id, entity_name_id),
	    FOREIGN KEY (team_id, entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, fragment_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, fragment_id, owner_profile_id)
	        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
	    CONSTRAINT entity_names_display_nonempty CHECK (btrim(display_name) <> ''),
    CONSTRAINT entity_names_normalized_nonempty CHECK (btrim(normalized_name) <> ''),
    CONSTRAINT entity_names_kind_check CHECK (name_kind IN ('canonical', 'alias', 'former')),
    CONSTRAINT entity_names_span_check CHECK (
        (span_start IS NULL AND span_end IS NULL)
        OR (span_start IS NOT NULL AND span_end IS NOT NULL AND span_start >= 0 AND span_end > span_start)
    ),
    CONSTRAINT entity_names_valid_window_check CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from),
    CONSTRAINT entity_names_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS entity_names_current_canonical_unique
    ON entity_names(team_id, entity_id)
    WHERE name_kind = 'canonical' AND valid_to IS NULL;

CREATE INDEX IF NOT EXISTS entity_names_lookup_idx
    ON entity_names(team_id, normalized_name, name_kind, entity_id);

CREATE TABLE IF NOT EXISTS value_records (
    team_id UUID NOT NULL,
    value_id UUID NOT NULL DEFAULT gen_random_uuid(),
    value_type TEXT NOT NULL,
    canonical_value TEXT NOT NULL,
    unit TEXT NULL,
    display TEXT NOT NULL DEFAULT '',
    normalization_version INTEGER NOT NULL DEFAULT 1,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, value_id),
    FOREIGN KEY (team_id) REFERENCES semantic_team_refs(team_id) ON DELETE RESTRICT,
    CONSTRAINT value_records_type_check CHECK (value_type IN ('string', 'number', 'boolean', 'date', 'date_time')),
    CONSTRAINT value_records_canonical_nonempty CHECK (btrim(canonical_value) <> ''),
    CONSTRAINT value_records_normalization_version_check CHECK (normalization_version >= 1),
    CONSTRAINT value_records_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT value_records_canonical_unique UNIQUE NULLS NOT DISTINCT (
        team_id, value_type, canonical_value, unit, normalization_version
    )
);

CREATE TABLE IF NOT EXISTS relationship_records (
    team_id UUID NOT NULL,
    relationship_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    semantic_group_key TEXT NOT NULL,
    subject_entity_id UUID NOT NULL,
    predicate_key TEXT NOT NULL,
    predicate_version INTEGER NOT NULL,
    object_entity_id UUID NULL,
    object_value_id UUID NULL,
    relationship_kind TEXT NOT NULL,
    current_cardinality TEXT NOT NULL,
    tier TEXT NOT NULL,
    status TEXT NOT NULL,
    polarity TEXT NOT NULL DEFAULT '+',
    scope_key TEXT NULL,
    valid_from TIMESTAMPTZ NULL,
    valid_to TIMESTAMPTZ NULL,
    support_count INTEGER NOT NULL DEFAULT 0,
    source_group_count INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    recorded_to TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, relationship_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, subject_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, object_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, object_value_id) REFERENCES value_records(team_id, value_id) ON DELETE RESTRICT,
    FOREIGN KEY (predicate_key, predicate_version) REFERENCES predicate_definitions(predicate_key, version) ON DELETE RESTRICT,
    CONSTRAINT relationship_records_object_check CHECK ((object_entity_id IS NULL) <> (object_value_id IS NULL)),
    CONSTRAINT relationship_records_semantic_group_nonempty CHECK (btrim(semantic_group_key) <> ''),
    CONSTRAINT relationship_records_kind_check CHECK (relationship_kind IN ('state', 'event')),
    CONSTRAINT relationship_records_cardinality_check CHECK (current_cardinality IN ('one', 'many')),
    CONSTRAINT relationship_records_tier_check CHECK (tier IN ('candidate', 'validated_claim', 'fact')),
    CONSTRAINT relationship_records_status_check CHECK (status IN (
        'pending_evidence', 'active', 'needs_review', 'quarantined',
        'superseded', 'disputed', 'retracted', 'rejected'
    )),
    CONSTRAINT relationship_records_active_tier_check CHECK (
        (tier = 'candidate' AND status <> 'active')
        OR (status = 'active' AND tier IN ('validated_claim', 'fact'))
        OR (tier <> 'candidate' AND status <> 'active')
    ),
    CONSTRAINT relationship_records_polarity_check CHECK (polarity IN ('+', '-')),
    CONSTRAINT relationship_records_counts_check CHECK (support_count >= 0 AND source_group_count >= 0),
    CONSTRAINT relationship_records_version_check CHECK (version >= 1),
    CONSTRAINT relationship_records_valid_window_check CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from),
    CONSTRAINT relationship_records_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object'),
	    CONSTRAINT relationship_records_identity_unique UNIQUE NULLS NOT DISTINCT (
	        team_id, owner_profile_id, subject_entity_id, predicate_key,
	        object_entity_id, object_value_id, polarity, valid_from, valid_to, scope_key
	    ),
	    CONSTRAINT relationship_records_owner_fk_unique UNIQUE (team_id, relationship_id, owner_profile_id)
	);

CREATE UNIQUE INDEX IF NOT EXISTS relationship_records_active_one_current_unique
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        polarity, valid_from, valid_to, scope_key
    )
    NULLS NOT DISTINCT
    WHERE current_cardinality = 'one'
      AND status = 'active'
      AND tier IN ('validated_claim', 'fact');

CREATE INDEX IF NOT EXISTS relationship_records_active_subject_idx
    ON relationship_records(team_id, subject_entity_id, predicate_key)
    WHERE status = 'active' AND tier IN ('validated_claim', 'fact');

CREATE INDEX IF NOT EXISTS relationship_records_active_object_entity_idx
    ON relationship_records(team_id, object_entity_id, predicate_key)
    WHERE status = 'active' AND tier IN ('validated_claim', 'fact') AND object_entity_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS relationship_records_active_object_value_idx
    ON relationship_records(team_id, object_value_id, predicate_key)
    WHERE status = 'active' AND tier IN ('validated_claim', 'fact') AND object_value_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS relationship_observations (
    team_id UUID NOT NULL,
    observation_id UUID NOT NULL DEFAULT gen_random_uuid(),
    relationship_id UUID NULL,
    ingest_id UUID NOT NULL,
    placement_item_id UUID NULL,
    owner_profile_id UUID NOT NULL,
    subject_ref TEXT NOT NULL,
    original_predicate TEXT NOT NULL,
    object_ref TEXT NOT NULL,
    subject_entity_id UUID NULL,
    predicate_key TEXT NULL,
    predicate_version INTEGER NULL,
    object_entity_id UUID NULL,
    object_value_id UUID NULL,
    polarity TEXT NOT NULL DEFAULT '+',
    scope_key TEXT NULL,
    valid_from TIMESTAMPTZ NULL,
    valid_to TIMESTAMPTZ NULL,
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	    PRIMARY KEY (team_id, observation_id),
	    CONSTRAINT relationship_observations_owner_ref_unique UNIQUE (team_id, observation_id, owner_profile_id),
	    FOREIGN KEY (team_id, relationship_id, owner_profile_id)
	        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, ingest_id, owner_profile_id)
	        REFERENCES knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, placement_item_id) REFERENCES placement_items(team_id, placement_item_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, placement_item_id, owner_profile_id)
	        REFERENCES placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, subject_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, object_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, object_value_id) REFERENCES value_records(team_id, value_id) ON DELETE RESTRICT,
    FOREIGN KEY (predicate_key, predicate_version) REFERENCES predicate_definitions(predicate_key, version) ON DELETE RESTRICT,
    CONSTRAINT relationship_observations_subject_ref_nonempty CHECK (btrim(subject_ref) <> ''),
    CONSTRAINT relationship_observations_predicate_nonempty CHECK (btrim(original_predicate) <> ''),
    CONSTRAINT relationship_observations_object_ref_nonempty CHECK (btrim(object_ref) <> ''),
    CONSTRAINT relationship_observations_object_check CHECK (NOT (object_entity_id IS NOT NULL AND object_value_id IS NOT NULL)),
    CONSTRAINT relationship_observations_polarity_check CHECK (polarity IN ('+', '-')),
    CONSTRAINT relationship_observations_valid_window_check CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from),
    CONSTRAINT relationship_observations_evidence_array_check CHECK (jsonb_typeof(evidence) = 'array'),
    CONSTRAINT relationship_observations_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS relationship_observations_relationship_idx
    ON relationship_observations(team_id, relationship_id, created_at ASC)
    WHERE relationship_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS verification_events (
    team_id UUID NOT NULL,
    verification_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    observation_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    evidence_verdict TEXT NOT NULL,
    confidence NUMERIC(5,4) NULL,
    rationale TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    response_hash TEXT NOT NULL DEFAULT '',
	    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	    PRIMARY KEY (team_id, verification_event_id),
	    CONSTRAINT verification_events_owner_ref_unique UNIQUE (team_id, verification_event_id, owner_profile_id),
	    FOREIGN KEY (team_id, observation_id, owner_profile_id)
	        REFERENCES relationship_observations(team_id, observation_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT verification_events_verdict_check CHECK (evidence_verdict IN ('entailed', 'contradicted', 'insufficient')),
    CONSTRAINT verification_events_confidence_check CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    CONSTRAINT verification_events_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS relationship_evidence_supports (
    team_id UUID NOT NULL,
    support_id UUID NOT NULL DEFAULT gen_random_uuid(),
    relationship_id UUID NOT NULL,
    observation_id UUID NOT NULL,
    verification_event_id UUID NOT NULL,
    fragment_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    source_group_key TEXT NOT NULL,
    source_id UUID NULL,
    source_revision_id UUID NULL,
    span_start INTEGER NOT NULL,
    span_end INTEGER NOT NULL,
    quote TEXT NOT NULL DEFAULT '',
    authority TEXT NOT NULL DEFAULT 'primary',
	    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	    PRIMARY KEY (team_id, support_id),
	    CONSTRAINT relationship_supports_owner_ref_unique UNIQUE (team_id, support_id, owner_profile_id),
	    FOREIGN KEY (team_id, relationship_id, owner_profile_id)
	        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, observation_id, owner_profile_id)
	        REFERENCES relationship_observations(team_id, observation_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, verification_event_id, owner_profile_id)
	        REFERENCES verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, fragment_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, fragment_id, owner_profile_id)
	        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, source_id) REFERENCES evidence_sources(team_id, source_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, source_id, owner_profile_id)
	        REFERENCES evidence_sources(team_id, source_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, source_revision_id) REFERENCES evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, source_revision_id, owner_profile_id)
	        REFERENCES evidence_source_revisions(team_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT,
	    CONSTRAINT relationship_supports_source_group_nonempty CHECK (btrim(source_group_key) <> ''),
    CONSTRAINT relationship_supports_span_check CHECK (span_start >= 0 AND span_end > span_start),
    CONSTRAINT relationship_supports_authority_check CHECK (authority IN ('primary', 'secondary', 'derived', 'authoritative')),
    CONSTRAINT relationship_supports_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object'),
	    CONSTRAINT relationship_supports_identity_unique UNIQUE (
	        team_id, relationship_id, owner_profile_id, fragment_id, span_start, span_end
	    )
	);

CREATE TABLE IF NOT EXISTS relationship_support_decision_events (
    team_id UUID NOT NULL,
    support_decision_id UUID NOT NULL DEFAULT gen_random_uuid(),
    support_id UUID NOT NULL,
    relationship_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    actor_profile_id UUID NULL,
    decision TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
	    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	    PRIMARY KEY (team_id, support_decision_id),
	    CONSTRAINT relationship_support_decisions_owner_ref_unique UNIQUE (team_id, support_decision_id, owner_profile_id),
	    FOREIGN KEY (team_id, support_id, owner_profile_id)
	        REFERENCES relationship_evidence_supports(team_id, support_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, relationship_id, owner_profile_id)
	        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, actor_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_support_decisions_decision_check CHECK (decision IN ('grant', 'revoke', 'reinstate')),
    CONSTRAINT relationship_support_decisions_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS relationship_support_decisions_support_idx
    ON relationship_support_decision_events(team_id, support_id, created_at DESC, support_decision_id DESC);

CREATE TABLE IF NOT EXISTS relationship_transition_events (
    team_id UUID NOT NULL,
    transition_id UUID NOT NULL DEFAULT gen_random_uuid(),
    relationship_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    from_tier TEXT NULL,
    from_status TEXT NULL,
    to_tier TEXT NOT NULL,
    to_status TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    verification_event_id UUID NULL,
    support_decision_id UUID NULL,
	    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	    PRIMARY KEY (team_id, transition_id),
	    FOREIGN KEY (team_id, relationship_id, owner_profile_id)
	        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, verification_event_id, owner_profile_id)
	        REFERENCES verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, support_decision_id, owner_profile_id)
	        REFERENCES relationship_support_decision_events(team_id, support_decision_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_transitions_tier_check CHECK (
        (from_tier IS NULL OR from_tier IN ('candidate', 'validated_claim', 'fact'))
        AND to_tier IN ('candidate', 'validated_claim', 'fact')
    ),
    CONSTRAINT relationship_transitions_status_check CHECK (
        (from_status IS NULL OR from_status IN (
            'pending_evidence', 'active', 'needs_review', 'quarantined',
            'superseded', 'disputed', 'retracted', 'rejected'
        ))
        AND to_status IN (
            'pending_evidence', 'active', 'needs_review', 'quarantined',
            'superseded', 'disputed', 'retracted', 'rejected'
        )
    ),
    CONSTRAINT relationship_transitions_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS entity_resolution_events (
    team_id UUID NOT NULL,
    resolution_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ingest_id UUID NOT NULL,
    placement_item_id UUID NULL,
    owner_profile_id UUID NOT NULL,
    mention_ref TEXT NOT NULL,
    action TEXT NOT NULL,
    entity_id UUID NULL,
    fragment_id UUID NULL,
    span_start INTEGER NULL,
    span_end INTEGER NULL,
    verifier_result JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	    PRIMARY KEY (team_id, resolution_event_id),
	    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, ingest_id, owner_profile_id)
	        REFERENCES knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, placement_item_id) REFERENCES placement_items(team_id, placement_item_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, placement_item_id, owner_profile_id)
	        REFERENCES placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, fragment_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, fragment_id, owner_profile_id)
	        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT entity_resolution_events_ref_nonempty CHECK (btrim(mention_ref) <> ''),
    CONSTRAINT entity_resolution_events_action_check CHECK (action IN ('reuse', 'create', 'ambiguous')),
    CONSTRAINT entity_resolution_events_action_entity_check CHECK (
        (action IN ('reuse', 'create') AND entity_id IS NOT NULL)
        OR (action = 'ambiguous' AND entity_id IS NULL)
    ),
    CONSTRAINT entity_resolution_events_span_check CHECK (
        (span_start IS NULL AND span_end IS NULL)
        OR (span_start IS NOT NULL AND span_end IS NOT NULL AND span_start >= 0 AND span_end > span_start)
    ),
    CONSTRAINT entity_resolution_events_verifier_object_check CHECK (jsonb_typeof(verifier_result) = 'object'),
    CONSTRAINT entity_resolution_events_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS entity_correction_events (
    team_id UUID NOT NULL,
    correction_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    action TEXT NOT NULL,
    survivor_entity_id UUID NULL,
    new_entity_id UUID NULL,
    selected_observation_ids UUID[] NOT NULL DEFAULT ARRAY[]::uuid[],
    reason TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, correction_event_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, survivor_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, new_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    CONSTRAINT entity_correction_events_action_check CHECK (action IN ('merge', 'split')),
    CONSTRAINT entity_correction_events_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS relationship_cross_references (
    team_id UUID NOT NULL,
    cross_reference_id UUID NOT NULL DEFAULT gen_random_uuid(),
    author_profile_id UUID NOT NULL,
    source_relationship_id UUID NOT NULL,
    source_relationship_version INTEGER NOT NULL,
    target_relationship_id UUID NOT NULL,
    target_relationship_version INTEGER NOT NULL,
    kind TEXT NOT NULL,
    verification_event_id UUID NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	    PRIMARY KEY (team_id, cross_reference_id),
	    FOREIGN KEY (team_id, author_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, source_relationship_id, author_profile_id)
	        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, target_relationship_id) REFERENCES relationship_records(team_id, relationship_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, verification_event_id, author_profile_id)
	        REFERENCES verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_cross_references_version_check CHECK (
        source_relationship_version >= 1 AND target_relationship_version >= 1
    ),
    CONSTRAINT relationship_cross_references_kind_check CHECK (
        kind IN ('confirms', 'challenges', 'corrects', 'adopts_evidence_from')
    ),
    CONSTRAINT relationship_cross_references_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT relationship_cross_references_identity_unique UNIQUE (
        team_id, author_profile_id, source_relationship_id, target_relationship_id, kind, verification_event_id
    )
);

CREATE TABLE IF NOT EXISTS review_tasks (
    team_id UUID NOT NULL,
    review_task_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    ingest_id UUID NULL,
    placement_item_id UUID NULL,
    relationship_id UUID NULL,
    observation_id UUID NULL,
    task_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    reason TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    resolution JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ NULL,
	    PRIMARY KEY (team_id, review_task_id),
	    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, ingest_id, owner_profile_id)
	        REFERENCES knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, placement_item_id) REFERENCES placement_items(team_id, placement_item_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, placement_item_id, owner_profile_id)
	        REFERENCES placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, relationship_id, owner_profile_id)
	        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
	    FOREIGN KEY (team_id, observation_id, owner_profile_id)
	        REFERENCES relationship_observations(team_id, observation_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT review_tasks_type_check CHECK (task_type IN (
        'identity_needs_review', 'predicate_needs_review', 'relationship_needs_review',
        'correction_needs_review', 'policy_needs_review'
    )),
    CONSTRAINT review_tasks_status_check CHECK (status IN ('open', 'acknowledged', 'resolved', 'canceled')),
    CONSTRAINT review_tasks_payload_object_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT review_tasks_resolution_object_check CHECK (jsonb_typeof(resolution) = 'object')
);

CREATE INDEX IF NOT EXISTS review_tasks_open_owner_idx
    ON review_tasks(team_id, owner_profile_id, created_at ASC)
    WHERE status IN ('open', 'acknowledged');

CREATE TABLE IF NOT EXISTS hypotheses (
    team_id UUID NOT NULL,
    hypothesis_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'candidate',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, hypothesis_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT hypotheses_status_check CHECK (status IN ('candidate', 'reinforced', 'rejected', 'promoted_candidate', 'stale')),
    CONSTRAINT hypotheses_payload_object_check CHECK (jsonb_typeof(payload) = 'object')
);

DROP TRIGGER IF EXISTS entity_resolution_events_append_only ON entity_resolution_events;
CREATE TRIGGER entity_resolution_events_append_only
    BEFORE UPDATE OR DELETE ON entity_resolution_events
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS entity_correction_events_append_only ON entity_correction_events;
CREATE TRIGGER entity_correction_events_append_only
    BEFORE UPDATE OR DELETE ON entity_correction_events
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS relationship_observations_append_only ON relationship_observations;
CREATE TRIGGER relationship_observations_append_only
    BEFORE UPDATE OR DELETE ON relationship_observations
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS verification_events_append_only ON verification_events;
CREATE TRIGGER verification_events_append_only
    BEFORE UPDATE OR DELETE ON verification_events
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS relationship_supports_append_only ON relationship_evidence_supports;
CREATE TRIGGER relationship_supports_append_only
    BEFORE UPDATE OR DELETE ON relationship_evidence_supports
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS relationship_support_decisions_append_only ON relationship_support_decision_events;
CREATE TRIGGER relationship_support_decisions_append_only
    BEFORE UPDATE OR DELETE ON relationship_support_decision_events
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS relationship_transitions_append_only ON relationship_transition_events;
CREATE TRIGGER relationship_transitions_append_only
    BEFORE UPDATE OR DELETE ON relationship_transition_events
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS relationship_cross_references_append_only ON relationship_cross_references;
CREATE TRIGGER relationship_cross_references_append_only
    BEFORE UPDATE OR DELETE ON relationship_cross_references
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

ALTER TABLE entity_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE entity_records FORCE ROW LEVEL SECURITY;
ALTER TABLE entity_names ENABLE ROW LEVEL SECURITY;
ALTER TABLE entity_names FORCE ROW LEVEL SECURITY;
ALTER TABLE value_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE value_records FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_records FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_observations FORCE ROW LEVEL SECURITY;
ALTER TABLE verification_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE verification_events FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_evidence_supports ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_evidence_supports FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_support_decision_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_support_decision_events FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_transition_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_transition_events FORCE ROW LEVEL SECURITY;
ALTER TABLE entity_resolution_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE entity_resolution_events FORCE ROW LEVEL SECURITY;
ALTER TABLE entity_correction_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE entity_correction_events FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_cross_references ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_cross_references FORCE ROW LEVEL SECURITY;
ALTER TABLE review_tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE review_tasks FORCE ROW LEVEL SECURITY;
ALTER TABLE hypotheses ENABLE ROW LEVEL SECURITY;
ALTER TABLE hypotheses FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY['entity_records', 'value_records']
    LOOP
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR SELECT USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_select',
            table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_insert',
            table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR UPDATE USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            ) WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_update',
            table_name
        );
    END LOOP;

    FOREACH table_name IN ARRAY ARRAY['entity_names', 'relationship_records', 'review_tasks', 'hypotheses']
    LOOP
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR SELECT USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_select',
            table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            )',
            table_name || '_insert',
            table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR UPDATE USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            ) WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            )',
            table_name || '_update',
            table_name
        );
    END LOOP;

    FOREACH table_name IN ARRAY ARRAY[
        'relationship_observations',
        'verification_events',
        'relationship_evidence_supports',
        'relationship_support_decision_events',
        'relationship_transition_events',
        'entity_resolution_events',
        'entity_correction_events'
    ]
    LOOP
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR SELECT USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_select',
            table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            )',
            table_name || '_insert',
            table_name
        );
    END LOOP;
END $$;

CREATE POLICY relationship_cross_references_select ON relationship_cross_references
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

CREATE POLICY relationship_cross_references_insert ON relationship_cross_references
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'team'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
            AND author_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
        )
    );

DROP VIEW IF EXISTS semantic_edges;
CREATE VIEW semantic_edges
WITH (security_invoker = true) AS
SELECT relationship_id,
       team_id,
       owner_profile_id,
       semantic_group_key,
       subject_entity_id,
       predicate_key,
       predicate_version,
       object_entity_id,
       object_value_id,
       relationship_kind,
       current_cardinality,
       polarity,
       scope_key,
       valid_from,
       valid_to,
       tier,
       support_count,
       source_group_count,
       version
FROM relationship_records
WHERE status = 'active'
  AND tier IN ('validated_claim', 'fact');

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

DROP VIEW IF EXISTS semantic_edges;
DROP TABLE IF EXISTS hypotheses;
DROP TABLE IF EXISTS review_tasks;
DROP TABLE IF EXISTS relationship_cross_references;
DROP TABLE IF EXISTS entity_correction_events;
DROP TABLE IF EXISTS entity_resolution_events;
DROP TABLE IF EXISTS relationship_transition_events;
DROP TABLE IF EXISTS relationship_support_decision_events;
DROP TABLE IF EXISTS relationship_evidence_supports;
DROP TABLE IF EXISTS verification_events;
DROP TABLE IF EXISTS relationship_observations;
DROP TABLE IF EXISTS relationship_records;
DROP TABLE IF EXISTS value_records;
DROP TABLE IF EXISTS entity_names;
DROP TABLE IF EXISTS entity_records;
DROP TABLE IF EXISTS predicate_definitions;
ALTER TABLE placement_items DROP CONSTRAINT IF EXISTS placement_items_owner_ref_unique;
ALTER TABLE evidence_fragments DROP CONSTRAINT IF EXISTS evidence_fragments_fragment_owner_ref_unique;
DROP FUNCTION IF EXISTS prevent_v2_reference_definition_mutation();

-- +goose StatementEnd
