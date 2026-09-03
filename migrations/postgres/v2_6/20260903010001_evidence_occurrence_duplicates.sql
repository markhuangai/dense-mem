-- Lock/rewrite impact: additive occurrence/alias tables are copied in resumable
-- 500-row batches. RLS impact: reads stay team-scoped and migration writes use
-- system mode. Backfill selects exact aliases by owner, space, source lineage,
-- content, hash, and creation order; current source revisions win. Backward compatibility:
-- historical aliases remain for known_at recall. Rollback refuses
-- once occurrence or alias history exists.

-- +goose NO TRANSACTION

-- +goose Up

SET lock_timeout = '30s';

ALTER TABLE evidence_fragments
    ADD COLUMN IF NOT EXISTS force_insert BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS evidence_occurrences (
    team_id UUID NOT NULL,
    occurrence_id UUID NOT NULL DEFAULT gen_random_uuid(),
    canonical_fragment_id UUID NOT NULL,
    canonical_owner_profile_id UUID NOT NULL,
    ingest_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    space_id UUID NULL,
    space_generation BIGINT NULL,
    evidence_index INTEGER NOT NULL,
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    source_type TEXT NOT NULL DEFAULT 'conversation',
    authority TEXT NOT NULL DEFAULT 'primary',
    source_ref TEXT NOT NULL DEFAULT '',
    source_id UUID NULL,
    source_revision_id UUID NULL,
    labels TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    force_insert BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, occurrence_id),
    UNIQUE (team_id, occurrence_id, owner_profile_id),
    UNIQUE (team_id, ingest_id, evidence_index),
    FOREIGN KEY (team_id, canonical_fragment_id, canonical_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, ingest_id, owner_profile_id)
        REFERENCES knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES ownership_aliases(team_id, legacy_owner_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id)
        REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_id, owner_profile_id)
        REFERENCES evidence_sources(team_id, source_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_revision_id, owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_id, source_revision_id, owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT evidence_occurrences_index_check CHECK (evidence_index >= 0),
    CONSTRAINT evidence_occurrences_content_nonempty CHECK (btrim(content) <> ''),
    CONSTRAINT evidence_occurrences_hash_nonempty CHECK (btrim(content_hash) <> ''),
    CONSTRAINT evidence_occurrences_space_pair_check CHECK (
        (space_id IS NULL AND space_generation IS NULL)
        OR (space_id IS NOT NULL AND space_generation > 0)
    ),
    CONSTRAINT evidence_occurrences_source_revision_pair_check CHECK (
        (source_id IS NULL AND source_revision_id IS NULL)
        OR (source_id IS NOT NULL AND source_revision_id IS NOT NULL)
    ),
    CONSTRAINT evidence_occurrences_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS evidence_exact_aliases (
    team_id UUID NOT NULL,
    alias_fragment_id UUID NOT NULL,
    alias_owner_profile_id UUID NOT NULL,
    canonical_fragment_id UUID NOT NULL,
    canonical_owner_profile_id UUID NOT NULL,
    space_id UUID NULL,
    space_generation BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, alias_fragment_id),
    UNIQUE (team_id, alias_fragment_id, alias_owner_profile_id),
    FOREIGN KEY (team_id, alias_fragment_id, alias_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, canonical_fragment_id, canonical_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id)
        REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    CONSTRAINT evidence_exact_aliases_distinct_check CHECK (alias_fragment_id <> canonical_fragment_id),
    CONSTRAINT evidence_exact_aliases_space_pair_check CHECK (
        (space_id IS NULL AND space_generation IS NULL)
        OR (space_id IS NOT NULL AND space_generation > 0)
    )
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION prevent_evidence_occurrence_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = OLD.space_id
    THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS evidence_occurrences_append_only ON evidence_occurrences;
CREATE TRIGGER evidence_occurrences_append_only
    BEFORE UPDATE OR DELETE ON evidence_occurrences
    FOR EACH ROW EXECUTE FUNCTION prevent_evidence_occurrence_mutation();
DROP TRIGGER IF EXISTS evidence_exact_aliases_append_only ON evidence_exact_aliases;
CREATE TRIGGER evidence_exact_aliases_append_only
    BEFORE UPDATE OR DELETE ON evidence_exact_aliases
    FOR EACH ROW EXECUTE FUNCTION prevent_evidence_occurrence_mutation();

ALTER TABLE evidence_occurrences ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_occurrences FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_exact_aliases ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_exact_aliases FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS evidence_occurrences_select ON evidence_occurrences;
CREATE POLICY evidence_occurrences_select ON evidence_occurrences
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );
DROP POLICY IF EXISTS evidence_occurrences_insert ON evidence_occurrences;
CREATE POLICY evidence_occurrences_insert ON evidence_occurrences
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
        )
    );
DROP POLICY IF EXISTS evidence_occurrences_private_erasure_delete ON evidence_occurrences;
CREATE POLICY evidence_occurrences_private_erasure_delete ON evidence_occurrences
    FOR DELETE USING (
        current_setting('app.tx_mode', true) = 'system'
        AND space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
    );
DROP POLICY IF EXISTS evidence_exact_aliases_select ON evidence_exact_aliases;
CREATE POLICY evidence_exact_aliases_select ON evidence_exact_aliases
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );
DROP POLICY IF EXISTS evidence_exact_aliases_insert ON evidence_exact_aliases;
CREATE POLICY evidence_exact_aliases_insert ON evidence_exact_aliases
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
            AND alias_owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
        )
    );
DROP POLICY IF EXISTS evidence_exact_aliases_private_erasure_delete ON evidence_exact_aliases;
CREATE POLICY evidence_exact_aliases_private_erasure_delete ON evidence_exact_aliases
    FOR DELETE USING (
        current_setting('app.tx_mode', true) = 'system'
        AND space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
    );

ALTER TABLE evidence_security_events
    ADD COLUMN IF NOT EXISTS occurrence_id UUID NULL,
    ADD COLUMN IF NOT EXISTS evidence_owner_profile_id UUID NULL;
ALTER TABLE entity_resolution_events
    ADD COLUMN IF NOT EXISTS occurrence_id UUID NULL,
    ADD COLUMN IF NOT EXISTS evidence_owner_profile_id UUID NULL;
ALTER TABLE relationship_evidence_supports
    ADD COLUMN IF NOT EXISTS occurrence_id UUID NULL,
    ADD COLUMN IF NOT EXISTS occurrence_owner_profile_id UUID NULL;
ALTER TABLE evidence_lifecycle_events
    ADD COLUMN IF NOT EXISTS target_occurrence_id UUID NULL,
    ADD COLUMN IF NOT EXISTS replacement_occurrence_id UUID NULL;

-- Keep direct legacy writers valid while occurrence fields roll out; defaults
-- bind only the historical self-occurrence (Remember supplies explicit values).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION dense_mem_evidence_fragment_occurrence_defaults()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO evidence_occurrences (
        team_id, occurrence_id, canonical_fragment_id, canonical_owner_profile_id,
        ingest_id, owner_profile_id, space_id, space_generation, evidence_index,
        content, content_hash, source_type, authority, source_ref, source_id,
        source_revision_id, labels, metadata, force_insert, created_at
    ) VALUES (
        NEW.team_id, NEW.fragment_id, NEW.fragment_id, NEW.owner_profile_id,
        NEW.ingest_id, NEW.owner_profile_id, NEW.space_id, NEW.space_generation, NEW.evidence_index,
        NEW.content, NEW.content_hash, NEW.source_type, NEW.authority, NEW.source_ref, NEW.source_id,
        NEW.source_revision_id, NEW.labels, NEW.metadata, NEW.force_insert, NEW.created_at
    ) ON CONFLICT (team_id, occurrence_id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS evidence_fragments_occurrence_defaults ON evidence_fragments;
CREATE TRIGGER evidence_fragments_occurrence_defaults
    AFTER INSERT ON evidence_fragments
    FOR EACH ROW EXECUTE FUNCTION dense_mem_evidence_fragment_occurrence_defaults();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION dense_mem_evidence_security_lineage_defaults()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.occurrence_id IS NULL THEN
        NEW.occurrence_id := NEW.fragment_id;
    END IF;
    IF NEW.evidence_owner_profile_id IS NULL THEN
        NEW.evidence_owner_profile_id := NEW.owner_profile_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS evidence_security_events_lineage_defaults ON evidence_security_events;
CREATE TRIGGER evidence_security_events_lineage_defaults
    BEFORE INSERT ON evidence_security_events
    FOR EACH ROW EXECUTE FUNCTION dense_mem_evidence_security_lineage_defaults();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION dense_mem_relationship_support_occurrence_defaults()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.occurrence_id IS NULL THEN
        NEW.occurrence_id := NEW.fragment_id;
    END IF;
    IF NEW.occurrence_owner_profile_id IS NULL THEN
        NEW.occurrence_owner_profile_id := NEW.owner_profile_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS relationship_supports_occurrence_defaults ON relationship_evidence_supports;
CREATE TRIGGER relationship_supports_occurrence_defaults
    BEFORE INSERT ON relationship_evidence_supports
    FOR EACH ROW EXECUTE FUNCTION dense_mem_relationship_support_occurrence_defaults();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION dense_mem_evidence_lifecycle_occurrence_defaults()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.target_occurrence_id IS NULL THEN
        NEW.target_occurrence_id := NEW.target_fragment_id;
    END IF;
    IF NEW.replacement_occurrence_id IS NULL AND NEW.replacement_fragment_id IS NOT NULL THEN
        NEW.replacement_occurrence_id := NEW.replacement_fragment_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS evidence_lifecycle_events_occurrence_defaults ON evidence_lifecycle_events;
CREATE TRIGGER evidence_lifecycle_events_occurrence_defaults
    BEFORE INSERT ON evidence_lifecycle_events
    FOR EACH ROW EXECUTE FUNCTION dense_mem_evidence_lifecycle_occurrence_defaults();

-- Retain legal-hold handling and add only the transaction-local lineage
-- backfill gate, which is cleared before application writes resume.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION prevent_append_only_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND TG_TABLE_NAME = 'remember_failure_artifacts' THEN
        IF current_setting('app.tx_mode', true) = 'system'
           AND NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid IS NOT NULL
           AND COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) =
               (current_setting('app.remember_failure_artifact_retention_value', true) = 'true')
           AND (to_jsonb(NEW) - ARRAY['retained_by_legal_hold']) =
               (to_jsonb(OLD) - ARRAY['retained_by_legal_hold'])
           AND EXISTS (
               SELECT 1
               FROM remember_attempts AS attempt
               WHERE attempt.team_id = NEW.team_id
                 AND attempt.attempt_id = NEW.attempt_id
                 AND attempt.owner_profile_id = NEW.owner_profile_id
                 AND attempt.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
           )
           AND (
               (COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) AND EXISTS (
                   SELECT 1 FROM private_memory_legal_holds AS hold
                   WHERE hold.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
                     AND hold.released_at IS NULL
               ))
               OR
               (NOT COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) AND NOT EXISTS (
                   SELECT 1 FROM private_memory_legal_holds AS hold
                   WHERE hold.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
                     AND hold.released_at IS NULL
               ))
           ) THEN
            RETURN NEW;
        END IF;
    END IF;
    IF TG_OP = 'UPDATE'
       AND current_setting('app.tx_mode', true) = 'migration'
       AND current_setting('app.evidence_occurrence_backfill', true) = 'true'
       AND TG_TABLE_NAME IN (
           'evidence_security_events', 'entity_resolution_events',
           'relationship_evidence_supports', 'evidence_lifecycle_events'
       ) THEN
        IF TG_TABLE_NAME = 'evidence_security_events'
           AND (to_jsonb(NEW) - ARRAY['occurrence_id', 'evidence_owner_profile_id']) =
               (to_jsonb(OLD) - ARRAY['occurrence_id', 'evidence_owner_profile_id'])
        THEN
            RETURN NEW;
        ELSIF TG_TABLE_NAME = 'entity_resolution_events'
           AND (to_jsonb(NEW) - ARRAY['occurrence_id', 'evidence_owner_profile_id']) =
               (to_jsonb(OLD) - ARRAY['occurrence_id', 'evidence_owner_profile_id'])
        THEN
            RETURN NEW;
        ELSIF TG_TABLE_NAME = 'relationship_evidence_supports'
           AND (to_jsonb(NEW) - ARRAY['occurrence_id', 'occurrence_owner_profile_id']) =
               (to_jsonb(OLD) - ARRAY['occurrence_id', 'occurrence_owner_profile_id'])
        THEN
            RETURN NEW;
        ELSIF TG_TABLE_NAME = 'evidence_lifecycle_events'
           AND (to_jsonb(NEW) - ARRAY['target_occurrence_id', 'replacement_occurrence_id']) =
               (to_jsonb(OLD) - ARRAY['target_occurrence_id', 'replacement_occurrence_id'])
        THEN
            RETURN NEW;
        END IF;
    END IF;
    IF TG_TABLE_NAME = 'relationship_evidence_supports'
       AND TG_OP = 'UPDATE'
       AND current_setting('app.tx_mode', true) = 'migration'
       AND current_setting('app.known_evidence_support_ownership_backfill', true) = 'true'
       AND (to_jsonb(OLD)->>'evidence_owner_profile_id') IS NULL
       AND (to_jsonb(NEW)->>'evidence_owner_profile_id') IS NOT NULL
       AND (to_jsonb(NEW)->>'evidence_owner_profile_id') IS NOT DISTINCT FROM (to_jsonb(OLD)->>'owner_profile_id')
       AND (to_jsonb(NEW) - 'evidence_owner_profile_id') = (to_jsonb(OLD) - 'evidence_owner_profile_id')
    THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND (
           NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
               = NULLIF(to_jsonb(OLD)->>'space_id', '')::uuid
           OR (
               TG_TABLE_NAME IN ('remember_attempt_events', 'remember_failure_artifacts', 'semantic_assessments')
               AND EXISTS (
                   SELECT 1
                   FROM remember_attempts AS attempt
                   WHERE attempt.team_id = NULLIF(to_jsonb(OLD)->>'team_id', '')::uuid
                     AND attempt.attempt_id = NULLIF(to_jsonb(OLD)->>'attempt_id', '')::uuid
                     AND attempt.owner_profile_id = NULLIF(to_jsonb(OLD)->>'owner_profile_id', '')::uuid
                     AND attempt.space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
               )
           )
           OR (
               TG_TABLE_NAME = 'remember_failure_artifacts'
               AND current_setting('app.remember_failure_artifact_purge', true) = 'true'
           )
           OR (
               TG_TABLE_NAME = 'relationship_cross_references'
               AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = (
                   SELECT target.space_id
                   FROM relationship_records AS target
                   WHERE target.team_id = NULLIF(to_jsonb(OLD)->>'team_id', '')::uuid
                     AND target.relationship_id = NULLIF(to_jsonb(OLD)->>'target_relationship_id', '')::uuid
               )
           )
       ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
DROP POLICY IF EXISTS evidence_security_events_occurrence_backfill_update ON evidence_security_events;
CREATE POLICY evidence_security_events_occurrence_backfill_update ON evidence_security_events
    FOR UPDATE
    USING (current_setting('app.tx_mode', true) = 'migration' AND current_setting('app.evidence_occurrence_backfill', true) = 'true'
           AND (occurrence_id IS NULL OR evidence_owner_profile_id IS NULL))
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration' AND current_setting('app.evidence_occurrence_backfill', true) = 'true'
                AND occurrence_id IS NOT NULL AND evidence_owner_profile_id IS NOT NULL);
DROP POLICY IF EXISTS entity_resolution_events_occurrence_backfill_update ON entity_resolution_events;
CREATE POLICY entity_resolution_events_occurrence_backfill_update ON entity_resolution_events
    FOR UPDATE
    USING (current_setting('app.tx_mode', true) = 'migration' AND current_setting('app.evidence_occurrence_backfill', true) = 'true'
           AND (occurrence_id IS NULL OR evidence_owner_profile_id IS NULL))
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration' AND current_setting('app.evidence_occurrence_backfill', true) = 'true');
DROP POLICY IF EXISTS relationship_supports_occurrence_backfill_update ON relationship_evidence_supports;
CREATE POLICY relationship_supports_occurrence_backfill_update ON relationship_evidence_supports
    FOR UPDATE
    USING (current_setting('app.tx_mode', true) = 'migration' AND current_setting('app.evidence_occurrence_backfill', true) = 'true'
           AND (occurrence_id IS NULL OR occurrence_owner_profile_id IS NULL))
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration' AND current_setting('app.evidence_occurrence_backfill', true) = 'true'
                AND occurrence_id IS NOT NULL AND occurrence_owner_profile_id IS NOT NULL);
DROP POLICY IF EXISTS evidence_lifecycle_events_occurrence_backfill_update ON evidence_lifecycle_events;
CREATE POLICY evidence_lifecycle_events_occurrence_backfill_update ON evidence_lifecycle_events
    FOR UPDATE
    USING (current_setting('app.tx_mode', true) = 'migration' AND current_setting('app.evidence_occurrence_backfill', true) = 'true'
           AND target_occurrence_id IS NULL)
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration' AND current_setting('app.evidence_occurrence_backfill', true) = 'true'
                AND target_occurrence_id IS NOT NULL);
-- Link existing append-only rows to historical self-occurrences through the
-- shared trigger while the migration-local flag is set.
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('app.evidence_occurrence_backfill', 'true', true);
-- +goose StatementEnd
-- Select aliases and self-occurrences in deterministic, resumable batches;
-- each commit re-establishes migration RLS and append-only gates.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE dense_mem_backfill_evidence_occurrences_20260903010001()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    affected_rows INTEGER;
BEGIN
    LOOP
        PERFORM set_config('app.tx_mode', 'migration', true); PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true); PERFORM set_config('app.allowed_space_ids', '', true); PERFORM set_config('app.evidence_occurrence_backfill', 'true', true);
        WITH batch AS MATERIALIZED (SELECT ctid FROM evidence_security_events WHERE occurrence_id IS NULL OR evidence_owner_profile_id IS NULL ORDER BY team_id, security_event_id LIMIT 500)
        UPDATE evidence_security_events AS event SET occurrence_id = COALESCE(event.occurrence_id, event.fragment_id), evidence_owner_profile_id = COALESCE(event.evidence_owner_profile_id, event.owner_profile_id) FROM batch WHERE event.ctid = batch.ctid;
        GET DIAGNOSTICS affected_rows = ROW_COUNT; COMMIT; EXIT WHEN affected_rows = 0;
    END LOOP;
    LOOP
        PERFORM set_config('app.tx_mode', 'migration', true); PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true); PERFORM set_config('app.allowed_space_ids', '', true); PERFORM set_config('app.evidence_occurrence_backfill', 'true', true);
        WITH batch AS MATERIALIZED (SELECT ctid FROM entity_resolution_events WHERE evidence_owner_profile_id IS NULL OR (occurrence_id IS NULL AND fragment_id IS NOT NULL) ORDER BY team_id, resolution_event_id LIMIT 500)
        UPDATE entity_resolution_events AS event SET occurrence_id = COALESCE(event.occurrence_id, event.fragment_id), evidence_owner_profile_id = COALESCE(event.evidence_owner_profile_id, event.owner_profile_id) FROM batch WHERE event.ctid = batch.ctid;
        GET DIAGNOSTICS affected_rows = ROW_COUNT; COMMIT; EXIT WHEN affected_rows = 0;
    END LOOP;
    LOOP
        PERFORM set_config('app.tx_mode', 'migration', true); PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true); PERFORM set_config('app.allowed_space_ids', '', true); PERFORM set_config('app.evidence_occurrence_backfill', 'true', true);
        WITH batch AS MATERIALIZED (SELECT ctid FROM relationship_evidence_supports WHERE occurrence_id IS NULL OR occurrence_owner_profile_id IS NULL ORDER BY team_id, support_id LIMIT 500)
        UPDATE relationship_evidence_supports AS support SET occurrence_id = COALESCE(support.occurrence_id, support.fragment_id), occurrence_owner_profile_id = COALESCE(support.occurrence_owner_profile_id, support.evidence_owner_profile_id, support.owner_profile_id) FROM batch WHERE support.ctid = batch.ctid;
        GET DIAGNOSTICS affected_rows = ROW_COUNT; COMMIT; EXIT WHEN affected_rows = 0;
    END LOOP;
    LOOP
        PERFORM set_config('app.tx_mode', 'migration', true); PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true); PERFORM set_config('app.allowed_space_ids', '', true); PERFORM set_config('app.evidence_occurrence_backfill', 'true', true);
        WITH batch AS MATERIALIZED (SELECT ctid FROM evidence_lifecycle_events WHERE target_occurrence_id IS NULL OR (replacement_occurrence_id IS NULL AND replacement_fragment_id IS NOT NULL) ORDER BY team_id, lifecycle_event_id LIMIT 500)
        UPDATE evidence_lifecycle_events AS event SET target_occurrence_id = COALESCE(event.target_occurrence_id, event.target_fragment_id), replacement_occurrence_id = COALESCE(event.replacement_occurrence_id, event.replacement_fragment_id) FROM batch WHERE event.ctid = batch.ctid;
        GET DIAGNOSTICS affected_rows = ROW_COUNT; COMMIT; EXIT WHEN affected_rows = 0;
    END LOOP;
    LOOP
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
        PERFORM set_config('app.allowed_space_ids', '', true);
        PERFORM set_config('app.evidence_occurrence_backfill', 'true', true);
        WITH ranked AS MATERIALIZED (
            SELECT fragment.team_id,
                   fragment.fragment_id,
                   fragment.owner_profile_id,
                   fragment.space_id,
                   fragment.space_generation,
                   row_number() OVER (
                       PARTITION BY fragment.team_id, fragment.owner_profile_id,
                                    fragment.space_id, fragment.space_generation,
                                    fragment.content_hash, fragment.content,
                                    fragment.source_id
                       ORDER BY
                           CASE
                               WHEN fragment.source_id IS NULL
                                    OR source.current_revision_id = fragment.source_revision_id
                               THEN 0
                               ELSE 1
                           END ASC,
                           fragment.created_at ASC, fragment.fragment_id ASC
                   ) AS ordinal,
                   first_value(fragment.fragment_id) OVER (
                       PARTITION BY fragment.team_id, fragment.owner_profile_id,
                                    fragment.space_id, fragment.space_generation,
                                    fragment.content_hash, fragment.content,
                                    fragment.source_id
                       ORDER BY
                           CASE
                               WHEN fragment.source_id IS NULL
                                    OR source.current_revision_id = fragment.source_revision_id
                               THEN 0
                               ELSE 1
                           END ASC,
                           fragment.created_at ASC, fragment.fragment_id ASC
                   ) AS canonical_fragment_id,
                   first_value(
                       CASE
                           WHEN fragment.source_id IS NULL
                                OR source.current_revision_id = fragment.source_revision_id
                           THEN 0
                           ELSE 1
                       END
                   ) OVER (
                       PARTITION BY fragment.team_id, fragment.owner_profile_id,
                                    fragment.space_id, fragment.space_generation,
                                    fragment.content_hash, fragment.content,
                                    fragment.source_id
                       ORDER BY
                           CASE
                               WHEN fragment.source_id IS NULL
                                    OR source.current_revision_id = fragment.source_revision_id
                               THEN 0
                               ELSE 1
                           END ASC,
                           fragment.created_at ASC, fragment.fragment_id ASC
                   ) AS canonical_source_staleness
            FROM evidence_fragments AS fragment
            LEFT JOIN evidence_sources AS source
              ON source.team_id = fragment.team_id
             AND source.source_id = fragment.source_id
             AND source.owner_profile_id = fragment.owner_profile_id
            WHERE NOT EXISTS (
                      SELECT 1 FROM evidence_lifecycle_events AS lifecycle
                      WHERE lifecycle.team_id = fragment.team_id
                        AND lifecycle.target_fragment_id = fragment.fragment_id
                  )
              AND NOT EXISTS (
                      SELECT 1 FROM evidence_quarantines AS quarantine
                      WHERE quarantine.team_id = fragment.team_id
                        AND quarantine.fragment_id = fragment.fragment_id
                        AND quarantine.status = 'active'
                  )
        ), batch AS MATERIALIZED (
            SELECT ranked.*
            FROM ranked
            WHERE ranked.ordinal > 1
              AND ranked.fragment_id <> ranked.canonical_fragment_id
              AND ranked.canonical_source_staleness = 0
              AND NOT EXISTS (
                  SELECT 1 FROM evidence_exact_aliases AS alias
                  WHERE alias.team_id = ranked.team_id
                    AND alias.alias_fragment_id = ranked.fragment_id
              )
            ORDER BY ranked.team_id, ranked.fragment_id
            LIMIT 500
        )
        INSERT INTO evidence_exact_aliases (
            team_id, alias_fragment_id, alias_owner_profile_id,
            canonical_fragment_id, canonical_owner_profile_id, space_id, space_generation
        )
        SELECT batch.team_id, batch.fragment_id, batch.owner_profile_id,
               batch.canonical_fragment_id, batch.owner_profile_id,
               batch.space_id, batch.space_generation
        FROM batch
        ON CONFLICT (team_id, alias_fragment_id) DO NOTHING;
        GET DIAGNOSTICS affected_rows = ROW_COUNT;
        COMMIT;
        EXIT WHEN affected_rows = 0;
    END LOOP;
    LOOP
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
        PERFORM set_config('app.allowed_space_ids', '', true);
        PERFORM set_config('app.evidence_occurrence_backfill', 'true', true);
        INSERT INTO evidence_occurrences (
            team_id, occurrence_id, canonical_fragment_id, canonical_owner_profile_id,
            ingest_id, owner_profile_id, space_id, space_generation, evidence_index,
            content, content_hash, source_type, authority, source_ref, source_id,
            source_revision_id, labels, metadata, force_insert, created_at
        )
        SELECT fragment.team_id,
               fragment.fragment_id,
               COALESCE(alias.canonical_fragment_id, fragment.fragment_id),
               COALESCE(alias.canonical_owner_profile_id, fragment.owner_profile_id),
               fragment.ingest_id, fragment.owner_profile_id, fragment.space_id,
               fragment.space_generation, fragment.evidence_index, fragment.content,
               fragment.content_hash, fragment.source_type, fragment.authority,
               fragment.source_ref, fragment.source_id, fragment.source_revision_id,
               fragment.labels, fragment.metadata, fragment.force_insert, fragment.created_at
        FROM (
            SELECT fragment.*
            FROM evidence_fragments AS fragment
            WHERE NOT EXISTS (
                SELECT 1 FROM evidence_occurrences AS occurrence
                WHERE occurrence.team_id = fragment.team_id
                  AND occurrence.occurrence_id = fragment.fragment_id
            )
            ORDER BY fragment.team_id, fragment.fragment_id
            LIMIT 500
        ) AS fragment
        LEFT JOIN evidence_exact_aliases AS alias
          ON alias.team_id = fragment.team_id
         AND alias.alias_fragment_id = fragment.fragment_id
        ON CONFLICT (team_id, occurrence_id) DO NOTHING;
        GET DIAGNOSTICS affected_rows = ROW_COUNT;
        COMMIT;
        EXIT WHEN affected_rows = 0;
    END LOOP;
END
$procedure$;
-- +goose StatementEnd
CALL dense_mem_backfill_evidence_occurrences_20260903010001();
DROP PROCEDURE dense_mem_backfill_evidence_occurrences_20260903010001();
-- Retain alias search projections for historical known_at recall while runtime
-- search excludes them; remove their embedding jobs in bounded commits.
-- +goose StatementBegin
DO $embedding_jobs_alias_cleanup_policy$
BEGIN
    IF to_regclass('public.embedding_jobs') IS NOT NULL THEN
        EXECUTE 'DROP POLICY IF EXISTS embedding_jobs_alias_cleanup_delete ON embedding_jobs';
        EXECUTE $policy$
            CREATE POLICY embedding_jobs_alias_cleanup_delete ON embedding_jobs
                FOR DELETE USING (
                    current_setting('app.tx_mode', true) = 'migration'
                    AND EXISTS (
                        SELECT 1
                        FROM search_documents AS document
                        JOIN evidence_exact_aliases AS alias
                          ON alias.team_id = document.team_id
                         AND alias.alias_fragment_id = document.source_id
                        WHERE document.team_id = embedding_jobs.team_id
                          AND document.search_document_id = embedding_jobs.search_document_id
                          AND document.source_kind = 'evidence'
                    )
                )
        $policy$;
    END IF;
END
$embedding_jobs_alias_cleanup_policy$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE dense_mem_cleanup_evidence_alias_embedding_jobs_20260903010001()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    affected_rows INTEGER;
BEGIN
    IF to_regclass('public.embedding_jobs') IS NOT NULL THEN
        LOOP
            PERFORM set_config('app.tx_mode', 'migration', true);
            PERFORM set_config('app.current_team_id', '', true);
            PERFORM set_config('app.current_profile_id', '', true);
            WITH batch AS MATERIALIZED (
                SELECT job.ctid
                FROM embedding_jobs AS job
                JOIN search_documents AS document
                  ON document.team_id = job.team_id
                 AND document.search_document_id = job.search_document_id
                 AND document.source_kind = 'evidence'
                JOIN evidence_exact_aliases AS alias
                  ON alias.team_id = document.team_id
                 AND alias.alias_fragment_id = document.source_id
                ORDER BY job.team_id, job.embedding_job_id
                LIMIT 500
            )
            DELETE FROM embedding_jobs AS job
            USING batch
            WHERE job.ctid = batch.ctid;
            GET DIAGNOSTICS affected_rows = ROW_COUNT;
            COMMIT;
            EXIT WHEN affected_rows = 0;
        END LOOP;
    END IF;
END
$procedure$;
-- +goose StatementEnd
CALL dense_mem_cleanup_evidence_alias_embedding_jobs_20260903010001();
DROP PROCEDURE dense_mem_cleanup_evidence_alias_embedding_jobs_20260903010001();
-- +goose StatementBegin
DO $embedding_jobs_alias_cleanup_drop$
BEGIN
    IF to_regclass('public.embedding_jobs') IS NOT NULL THEN
        EXECUTE 'DROP POLICY IF EXISTS embedding_jobs_alias_cleanup_delete ON embedding_jobs';
    END IF;
END
$embedding_jobs_alias_cleanup_drop$;
-- +goose StatementEnd
-- Occurrence identity is part of a support span; preserve the constraint name
-- for runtime ON CONFLICT. Match legacy foreign keys by definition because
-- PostgreSQL truncates generated names before installing occurrence-aware ones.
-- +goose StatementBegin
DO $dense_mem_occurrence_legacy_fk_cleanup$
DECLARE
    constraint_row RECORD;
BEGIN
    FOR constraint_row IN
        SELECT constraint_item.conrelid::regclass AS table_name, constraint_item.conname
        FROM pg_constraint AS constraint_item
        WHERE constraint_item.contype = 'f'
          AND constraint_item.confrelid = 'evidence_fragments'::regclass
          AND (
              (constraint_item.conrelid = 'evidence_security_events'::regclass
               AND pg_get_constraintdef(constraint_item.oid) LIKE
                   'FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id) REFERENCES evidence_fragments%')
              OR
              (constraint_item.conrelid = 'entity_resolution_events'::regclass
               AND pg_get_constraintdef(constraint_item.oid) LIKE
                   'FOREIGN KEY (team_id, fragment_id, owner_profile_id) REFERENCES evidence_fragments%')
          )
    LOOP
        EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', constraint_row.table_name, constraint_row.conname);
    END LOOP;
END
$dense_mem_occurrence_legacy_fk_cleanup$;
-- +goose StatementEnd

ALTER TABLE relationship_evidence_supports
    DROP CONSTRAINT IF EXISTS relationship_supports_identity_unique,
    ADD CONSTRAINT relationship_supports_identity_unique UNIQUE (
        team_id, relationship_id, owner_profile_id, fragment_id, occurrence_id,
        span_start, span_end
    );
ALTER TABLE evidence_occurrences
    ALTER COLUMN occurrence_id SET NOT NULL,
    ALTER COLUMN canonical_fragment_id SET NOT NULL,
    ALTER COLUMN canonical_owner_profile_id SET NOT NULL,
    ALTER COLUMN owner_profile_id SET NOT NULL;
ALTER TABLE evidence_security_events
    ALTER COLUMN occurrence_id SET NOT NULL,
    ALTER COLUMN evidence_owner_profile_id SET NOT NULL;
ALTER TABLE entity_resolution_events
    ALTER COLUMN occurrence_id DROP NOT NULL,
    ALTER COLUMN evidence_owner_profile_id DROP NOT NULL;
ALTER TABLE relationship_evidence_supports
    ALTER COLUMN occurrence_id SET NOT NULL,
    ALTER COLUMN occurrence_owner_profile_id SET NOT NULL;
ALTER TABLE evidence_lifecycle_events
    ALTER COLUMN target_occurrence_id SET NOT NULL;
DROP INDEX CONCURRENTLY IF EXISTS evidence_occurrences_canonical_idx;
CREATE INDEX CONCURRENTLY evidence_occurrences_canonical_idx
    ON evidence_occurrences(team_id, canonical_fragment_id, created_at ASC, occurrence_id ASC);
DROP INDEX CONCURRENTLY IF EXISTS evidence_occurrences_owner_idx;
CREATE INDEX CONCURRENTLY evidence_occurrences_owner_idx
    ON evidence_occurrences(team_id, owner_profile_id, space_id, space_generation, created_at ASC);
DROP INDEX CONCURRENTLY IF EXISTS evidence_exact_aliases_canonical_idx;
CREATE INDEX CONCURRENTLY evidence_exact_aliases_canonical_idx
    ON evidence_exact_aliases(team_id, canonical_fragment_id, alias_fragment_id);

ALTER TABLE evidence_security_events
    DROP CONSTRAINT IF EXISTS evidence_security_events_team_id_fragment_id_ingest_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS evidence_security_events_occurrence_fkey,
    DROP CONSTRAINT IF EXISTS evidence_security_events_evidence_owner_fkey,
    ADD CONSTRAINT evidence_security_events_occurrence_fkey
        FOREIGN KEY (team_id, occurrence_id, owner_profile_id)
        REFERENCES evidence_occurrences(team_id, occurrence_id, owner_profile_id) ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT evidence_security_events_evidence_owner_fkey
        FOREIGN KEY (team_id, fragment_id, evidence_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE entity_resolution_events
    DROP CONSTRAINT IF EXISTS entity_resolution_events_team_id_fragment_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS entity_resolution_events_occurrence_fkey,
    DROP CONSTRAINT IF EXISTS entity_resolution_events_evidence_owner_fkey,
    ADD CONSTRAINT entity_resolution_events_occurrence_fkey
        FOREIGN KEY (team_id, occurrence_id, owner_profile_id)
        REFERENCES evidence_occurrences(team_id, occurrence_id, owner_profile_id) ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT entity_resolution_events_evidence_owner_fkey
        FOREIGN KEY (team_id, fragment_id, evidence_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE relationship_evidence_supports
    DROP CONSTRAINT IF EXISTS relationship_supports_fragment_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_occurrence_fkey,
    ADD CONSTRAINT relationship_supports_occurrence_fkey
        FOREIGN KEY (team_id, occurrence_id, occurrence_owner_profile_id)
        REFERENCES evidence_occurrences(team_id, occurrence_id, owner_profile_id) ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT relationship_supports_fragment_evidence_owner_fkey
        FOREIGN KEY (team_id, fragment_id, evidence_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE evidence_lifecycle_events
    DROP CONSTRAINT IF EXISTS evidence_lifecycle_events_target_occurrence_fkey,
    DROP CONSTRAINT IF EXISTS evidence_lifecycle_events_replacement_occurrence_fkey,
    ADD CONSTRAINT evidence_lifecycle_events_target_occurrence_fkey
        FOREIGN KEY (team_id, target_occurrence_id, owner_profile_id)
        REFERENCES evidence_occurrences(team_id, occurrence_id, owner_profile_id) ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT evidence_lifecycle_events_replacement_occurrence_fkey
        FOREIGN KEY (team_id, replacement_occurrence_id, owner_profile_id)
        REFERENCES evidence_occurrences(team_id, occurrence_id, owner_profile_id) ON DELETE RESTRICT NOT VALID;

ALTER TABLE evidence_security_events
    VALIDATE CONSTRAINT evidence_security_events_occurrence_fkey;
ALTER TABLE evidence_security_events
    VALIDATE CONSTRAINT evidence_security_events_evidence_owner_fkey;
ALTER TABLE entity_resolution_events
    VALIDATE CONSTRAINT entity_resolution_events_occurrence_fkey;
ALTER TABLE entity_resolution_events
    VALIDATE CONSTRAINT entity_resolution_events_evidence_owner_fkey;
ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_supports_occurrence_fkey;
ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_supports_fragment_evidence_owner_fkey;
ALTER TABLE evidence_lifecycle_events
    VALIDATE CONSTRAINT evidence_lifecycle_events_target_occurrence_fkey;
ALTER TABLE evidence_lifecycle_events
    VALIDATE CONSTRAINT evidence_lifecycle_events_replacement_occurrence_fkey;

DROP POLICY evidence_security_events_occurrence_backfill_update ON evidence_security_events;
DROP POLICY entity_resolution_events_occurrence_backfill_update ON entity_resolution_events;
DROP POLICY relationship_supports_occurrence_backfill_update ON relationship_evidence_supports;
DROP POLICY evidence_lifecycle_events_occurrence_backfill_update ON evidence_lifecycle_events;
SELECT set_config('app.evidence_occurrence_backfill', 'false', true);
RESET lock_timeout;

-- +goose Down

-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM evidence_occurrences)
       OR EXISTS (SELECT 1 FROM evidence_exact_aliases) THEN
        RAISE EXCEPTION 'cannot roll back 20260903010001: occurrence or alias history exists';
    END IF;
END $$;
-- +goose StatementEnd

DROP POLICY IF EXISTS evidence_occurrences_private_erasure_delete ON evidence_occurrences;
DROP POLICY IF EXISTS evidence_exact_aliases_private_erasure_delete ON evidence_exact_aliases;
DROP POLICY IF EXISTS evidence_occurrences_insert ON evidence_occurrences;
DROP POLICY IF EXISTS evidence_occurrences_select ON evidence_occurrences;
DROP POLICY IF EXISTS evidence_exact_aliases_insert ON evidence_exact_aliases;
DROP POLICY IF EXISTS evidence_exact_aliases_select ON evidence_exact_aliases;

DROP TRIGGER IF EXISTS evidence_fragments_occurrence_defaults ON evidence_fragments;
DROP FUNCTION IF EXISTS dense_mem_evidence_fragment_occurrence_defaults();
DROP TRIGGER IF EXISTS evidence_security_events_lineage_defaults ON evidence_security_events;
DROP FUNCTION IF EXISTS dense_mem_evidence_security_lineage_defaults();
DROP TRIGGER IF EXISTS relationship_supports_occurrence_defaults ON relationship_evidence_supports;
DROP FUNCTION IF EXISTS dense_mem_relationship_support_occurrence_defaults();
DROP TRIGGER IF EXISTS evidence_lifecycle_events_occurrence_defaults ON evidence_lifecycle_events;
DROP FUNCTION IF EXISTS dense_mem_evidence_lifecycle_occurrence_defaults();

DROP TRIGGER IF EXISTS evidence_occurrences_append_only ON evidence_occurrences;
DROP TRIGGER IF EXISTS evidence_exact_aliases_append_only ON evidence_exact_aliases;
DROP FUNCTION IF EXISTS prevent_evidence_occurrence_mutation();

DROP INDEX CONCURRENTLY IF EXISTS evidence_occurrences_canonical_idx;
DROP INDEX CONCURRENTLY IF EXISTS evidence_occurrences_owner_idx;
DROP INDEX CONCURRENTLY IF EXISTS evidence_exact_aliases_canonical_idx;

ALTER TABLE evidence_security_events
    DROP CONSTRAINT IF EXISTS evidence_security_events_occurrence_fkey,
    DROP CONSTRAINT IF EXISTS evidence_security_events_evidence_owner_fkey;
ALTER TABLE entity_resolution_events
    DROP CONSTRAINT IF EXISTS entity_resolution_events_occurrence_fkey,
    DROP CONSTRAINT IF EXISTS entity_resolution_events_evidence_owner_fkey;
ALTER TABLE relationship_evidence_supports
    DROP CONSTRAINT IF EXISTS relationship_supports_occurrence_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_fragment_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_identity_unique;
ALTER TABLE evidence_lifecycle_events
    DROP CONSTRAINT IF EXISTS evidence_lifecycle_events_target_occurrence_fkey,
    DROP CONSTRAINT IF EXISTS evidence_lifecycle_events_replacement_occurrence_fkey;

ALTER TABLE relationship_evidence_supports
    ADD CONSTRAINT relationship_supports_identity_unique UNIQUE (
        team_id, relationship_id, owner_profile_id, fragment_id, span_start, span_end
    ),
    ADD CONSTRAINT relationship_supports_fragment_evidence_owner_fkey
        FOREIGN KEY (team_id, fragment_id, evidence_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;

DO $dense_mem_occurrence_legacy_fk_restore$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'evidence_security_events'::regclass
          AND confrelid = 'evidence_fragments'::regclass
          AND pg_get_constraintdef(oid) LIKE
              'FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id) REFERENCES evidence_fragments%'
    ) THEN
        ALTER TABLE evidence_security_events
            ADD CONSTRAINT evidence_security_events_fragment_ingest_owner_fkey
            FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id)
            REFERENCES evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'entity_resolution_events'::regclass
          AND confrelid = 'evidence_fragments'::regclass
          AND pg_get_constraintdef(oid) LIKE
              'FOREIGN KEY (team_id, fragment_id, owner_profile_id) REFERENCES evidence_fragments%'
    ) THEN
        ALTER TABLE entity_resolution_events
            ADD CONSTRAINT entity_resolution_events_fragment_owner_fkey
            FOREIGN KEY (team_id, fragment_id, owner_profile_id)
            REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;
    END IF;
END
$dense_mem_occurrence_legacy_fk_restore$;

ALTER TABLE evidence_security_events
    DROP COLUMN IF EXISTS occurrence_id,
    DROP COLUMN IF EXISTS evidence_owner_profile_id;
ALTER TABLE entity_resolution_events
    DROP COLUMN IF EXISTS occurrence_id,
    DROP COLUMN IF EXISTS evidence_owner_profile_id;
ALTER TABLE relationship_evidence_supports
    DROP COLUMN IF EXISTS occurrence_id,
    DROP COLUMN IF EXISTS occurrence_owner_profile_id;
ALTER TABLE evidence_lifecycle_events
    DROP COLUMN IF EXISTS target_occurrence_id,
    DROP COLUMN IF EXISTS replacement_occurrence_id;

DROP TABLE IF EXISTS evidence_exact_aliases;
DROP TABLE IF EXISTS evidence_occurrences;

ALTER TABLE evidence_fragments DROP COLUMN IF EXISTS force_insert;

-- Restore the pre-occurrence append-only owner while retaining exceptions used
-- by migrations that remain installed below this one.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION prevent_append_only_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND TG_TABLE_NAME = 'remember_failure_artifacts' THEN
        IF current_setting('app.tx_mode', true) = 'system'
           AND NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid IS NOT NULL
           AND COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) =
               (current_setting('app.remember_failure_artifact_retention_value', true) = 'true')
           AND (to_jsonb(NEW) - ARRAY['retained_by_legal_hold']) =
               (to_jsonb(OLD) - ARRAY['retained_by_legal_hold'])
           AND EXISTS (
               SELECT 1 FROM remember_attempts AS attempt
               WHERE attempt.team_id = NEW.team_id
                 AND attempt.attempt_id = NEW.attempt_id
                 AND attempt.owner_profile_id = NEW.owner_profile_id
                 AND attempt.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
           )
           AND (
               (COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) AND EXISTS (
                   SELECT 1 FROM private_memory_legal_holds AS hold
                   WHERE hold.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
                     AND hold.released_at IS NULL
               ))
               OR
               (NOT COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) AND NOT EXISTS (
                   SELECT 1 FROM private_memory_legal_holds AS hold
                   WHERE hold.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
                     AND hold.released_at IS NULL
               ))
           ) THEN
            RETURN NEW;
        END IF;
    END IF;
    IF TG_TABLE_NAME = 'relationship_evidence_supports'
       AND TG_OP = 'UPDATE'
       AND current_setting('app.tx_mode', true) = 'migration'
       AND current_setting('app.known_evidence_support_ownership_backfill', true) = 'true'
       AND (to_jsonb(OLD)->>'evidence_owner_profile_id') IS NULL
       AND (to_jsonb(NEW)->>'evidence_owner_profile_id') IS NOT NULL
       AND (to_jsonb(NEW)->>'evidence_owner_profile_id') IS NOT DISTINCT FROM (to_jsonb(OLD)->>'owner_profile_id')
       AND (to_jsonb(NEW) - 'evidence_owner_profile_id') = (to_jsonb(OLD) - 'evidence_owner_profile_id')
    THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND (
           NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid =
               NULLIF(to_jsonb(OLD)->>'space_id', '')::uuid
           OR (
               TG_TABLE_NAME IN ('remember_attempt_events', 'remember_failure_artifacts', 'semantic_assessments')
               AND EXISTS (
                   SELECT 1 FROM remember_attempts AS attempt
                   WHERE attempt.team_id = NULLIF(to_jsonb(OLD)->>'team_id', '')::uuid
                     AND attempt.attempt_id = NULLIF(to_jsonb(OLD)->>'attempt_id', '')::uuid
                     AND attempt.owner_profile_id = NULLIF(to_jsonb(OLD)->>'owner_profile_id', '')::uuid
                     AND attempt.space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
               )
           )
           OR (
               TG_TABLE_NAME = 'remember_failure_artifacts'
               AND current_setting('app.remember_failure_artifact_purge', true) = 'true'
           )
           OR (
               TG_TABLE_NAME = 'relationship_cross_references'
               AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = (
                   SELECT target.space_id
                   FROM relationship_records AS target
                   WHERE target.team_id = NULLIF(to_jsonb(OLD)->>'team_id', '')::uuid
                     AND target.relationship_id = NULLIF(to_jsonb(OLD)->>'target_relationship_id', '')::uuid
               )
           )
       ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
