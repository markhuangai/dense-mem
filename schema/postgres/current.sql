--
-- PostgreSQL database dump
--

\restrict DenseMemSchemaCatalogV1

-- Dumped from database version 18.4 (Debian 18.4-1.pgdg13+1)
-- Dumped by pg_dump version 18.4 (Debian 18.4-1.pgdg13+1)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: pgcrypto; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;


--
-- Name: EXTENSION pgcrypto; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pgcrypto IS 'cryptographic functions';


--
-- Name: vector; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;


--
-- Name: EXTENSION vector; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION vector IS 'vector data type and ivfflat and hnsw access methods';


--
-- Name: community_record_search_vector(text, text[], text[]); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.community_record_search_vector(record_summary text, record_top_entities text[], record_top_predicates text[]) RETURNS tsvector
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    AS $$
    SELECT to_tsvector(
        'simple'::regconfig,
        COALESCE(record_summary, '') || ' ' ||
        COALESCE(array_to_string(record_top_entities, ' '), '') || ' ' ||
        COALESCE(array_to_string(record_top_predicates, ' '), '')
    )
$$;


--
-- Name: ensure_submission_hold_for_awaiting_review(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.ensure_submission_hold_for_awaiting_review() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.status = 'awaiting_review'
       AND EXISTS (
           SELECT 1
           FROM placement_assessments AS assessment
           WHERE assessment.team_id = NEW.team_id
             AND assessment.placement_run_id = NEW.placement_run_id
             AND assessment.ingest_id = NEW.ingest_id
             AND assessment.owner_profile_id = NEW.owner_profile_id
             AND assessment.assessment_scope = 'submission'
       )
       AND NOT EXISTS (
           SELECT 1
           FROM submission_holds AS hold
           WHERE hold.team_id = NEW.team_id
             AND hold.placement_run_id = NEW.placement_run_id
       )
    THEN
        RAISE EXCEPTION 'submission placement run % requires a durable hold before awaiting_review', NEW.placement_run_id;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: guard_search_index_generation_lifecycle(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.guard_search_index_generation_lifecycle() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF COALESCE(current_setting('app.tx_mode', true), '') NOT IN ('system', 'migration') THEN
        RAISE EXCEPTION '% is reference data: % requires system or migration mode', TG_TABLE_NAME, TG_OP;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION '% is versioned reference data: % operations are not allowed', TG_TABLE_NAME, TG_OP;
    END IF;
    IF TG_OP = 'UPDATE' AND (
        OLD.search_index_generation_id IS DISTINCT FROM NEW.search_index_generation_id
        OR OLD.generation IS DISTINCT FROM NEW.generation
        OR OLD.embedding_contract_id IS DISTINCT FROM NEW.embedding_contract_id
        OR OLD.embedding_dimensions IS DISTINCT FROM NEW.embedding_dimensions
        OR OLD.ann_strategy IS DISTINCT FROM NEW.ann_strategy
        OR OLD.operator_class IS DISTINCT FROM NEW.operator_class
        OR OLD.indexed_expression IS DISTINCT FROM NEW.indexed_expression
        OR OLD.physical_index_name IS DISTINCT FROM NEW.physical_index_name
        OR OLD.hnsw_m IS DISTINCT FROM NEW.hnsw_m
        OR OLD.hnsw_ef_construction IS DISTINCT FROM NEW.hnsw_ef_construction
        OR OLD.query_ef_search IS DISTINCT FROM NEW.query_ef_search
        OR OLD.exact_max_rows IS DISTINCT FROM NEW.exact_max_rows
        OR OLD.candidate_limit IS DISTINCT FROM NEW.candidate_limit
        OR OLD.allow_exact_fallback IS DISTINCT FROM NEW.allow_exact_fallback
        OR OLD.metadata IS DISTINCT FROM NEW.metadata
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
    ) THEN
        RAISE EXCEPTION '% immutable fields cannot be changed after insertion', TG_TABLE_NAME;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: hypotheses_guard_provenance(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.hypotheses_guard_provenance() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF current_setting('app.tx_mode', true) IN ('system', 'migration') THEN
        RETURN NEW;
    END IF;
    IF NEW.team_id IS DISTINCT FROM OLD.team_id
       OR NEW.created_by_profile_id IS DISTINCT FROM OLD.created_by_profile_id
       OR NEW.canonical_hypothesis_id IS DISTINCT FROM OLD.canonical_hypothesis_id
       OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
       OR NEW.target_identity IS DISTINCT FROM OLD.target_identity THEN
        RAISE EXCEPTION 'hypothesis provenance columns are immutable';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: prevent_append_only_mutation(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.prevent_append_only_mutation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$;


--
-- Name: prevent_audit_log_mutation(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.prevent_audit_log_mutation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'audit_log is append-only: UPDATE operations are not allowed';
    ELSIF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'audit_log is append-only: DELETE operations are not allowed';
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: prevent_reference_definition_mutation(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.prevent_reference_definition_mutation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF COALESCE(current_setting('app.tx_mode', true), '') NOT IN ('system', 'migration') THEN
        RAISE EXCEPTION '% is reference data: % requires system or migration mode', TG_TABLE_NAME, TG_OP;
    END IF;
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        RAISE EXCEPTION '% is versioned reference data: % operations are not allowed', TG_TABLE_NAME, TG_OP;
    END IF;
    RETURN NEW;
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: app_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.app_config (
    key text NOT NULL,
    value text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.app_config FORCE ROW LEVEL SECURITY;


--
-- Name: audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_log (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    team_id uuid,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    operation character varying(64) NOT NULL,
    entity_type character varying(64) NOT NULL,
    entity_id text NOT NULL,
    before_payload jsonb,
    after_payload jsonb,
    actor_profile_id uuid,
    actor_role character varying(20),
    client_ip inet,
    correlation_id text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL
);

ALTER TABLE ONLY public.audit_log FORCE ROW LEVEL SECURITY;


--
-- Name: community_memberships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.community_memberships (
    team_id uuid NOT NULL,
    community_id uuid NOT NULL,
    entity_id uuid NOT NULL,
    rank integer NOT NULL,
    membership_score numeric(5,4) DEFAULT 1 NOT NULL,
    source_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT community_memberships_rank_check CHECK ((rank >= 0)),
    CONSTRAINT community_memberships_score_check CHECK (((membership_score >= (0)::numeric) AND (membership_score <= (1)::numeric))),
    CONSTRAINT community_memberships_source_count_check CHECK ((source_count >= 0))
);

ALTER TABLE ONLY public.community_memberships FORCE ROW LEVEL SECURITY;


--
-- Name: community_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.community_records (
    team_id uuid NOT NULL,
    community_id uuid NOT NULL,
    run_id uuid NOT NULL,
    ordinal integer NOT NULL,
    status text DEFAULT 'current'::text NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    summary_version text DEFAULT ''::text NOT NULL,
    member_count integer DEFAULT 0 NOT NULL,
    source_count integer DEFAULT 0 NOT NULL,
    top_entities text[] DEFAULT ARRAY[]::text[] NOT NULL,
    top_predicates text[] DEFAULT ARRAY[]::text[] NOT NULL,
    source_fingerprint text DEFAULT ''::text NOT NULL,
    stale_reason text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    superseded_at timestamp with time zone,
    logical_community_id uuid CONSTRAINT community_records_logical_community_id_not_null1 NOT NULL,
    summary_input_hash text DEFAULT ''::text NOT NULL,
    summary_provider_model text DEFAULT ''::text NOT NULL,
    summary_prompt_hash text DEFAULT ''::text NOT NULL,
    summary_response_hash text DEFAULT ''::text NOT NULL,
    summary_generated_at timestamp with time zone,
    CONSTRAINT community_records_counts_check CHECK (((member_count >= 0) AND (source_count >= 0))),
    CONSTRAINT community_records_ordinal_check CHECK ((ordinal >= 0)),
    CONSTRAINT community_records_status_check CHECK ((status = ANY (ARRAY['current'::text, 'stale'::text, 'superseded'::text])))
);

ALTER TABLE ONLY public.community_records FORCE ROW LEVEL SECURITY;


--
-- Name: community_snapshot_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.community_snapshot_runs (
    team_id uuid NOT NULL,
    run_id uuid DEFAULT gen_random_uuid() NOT NULL,
    window_key text NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    algorithm_kind text DEFAULT 'louvain'::text NOT NULL,
    algorithm_version text DEFAULT 'v2'::text NOT NULL,
    profile_version text DEFAULT 'postgres'::text NOT NULL,
    configuration_hash text DEFAULT ''::text NOT NULL,
    source_fingerprint text DEFAULT ''::text NOT NULL,
    source_snapshot jsonb DEFAULT '[]'::jsonb NOT NULL,
    node_count integer DEFAULT 0 NOT NULL,
    edge_count integer DEFAULT 0 NOT NULL,
    community_count integer DEFAULT 0 NOT NULL,
    max_nodes integer DEFAULT 0 NOT NULL,
    max_edges integer DEFAULT 0 NOT NULL,
    lease_until timestamp with time zone,
    error text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT community_snapshot_runs_algorithm_nonempty CHECK (((btrim(algorithm_kind) <> ''::text) AND (btrim(algorithm_version) <> ''::text))),
    CONSTRAINT community_snapshot_runs_counts_check CHECK (((node_count >= 0) AND (edge_count >= 0) AND (community_count >= 0) AND (max_nodes >= 0) AND (max_edges >= 0))),
    CONSTRAINT community_snapshot_runs_snapshot_array_check CHECK ((jsonb_typeof(source_snapshot) = 'array'::text)),
    CONSTRAINT community_snapshot_runs_status_check CHECK ((status = ANY (ARRAY['running'::text, 'completed'::text, 'failed'::text, 'skipped'::text, 'too_large'::text, 'cancelled'::text]))),
    CONSTRAINT community_snapshot_runs_window_nonempty CHECK ((btrim(window_key) <> ''::text))
);

ALTER TABLE ONLY public.community_snapshot_runs FORCE ROW LEVEL SECURITY;


--
-- Name: community_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.community_sources (
    team_id uuid NOT NULL,
    community_id uuid NOT NULL,
    relationship_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    relationship_version integer NOT NULL,
    source_rank integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    semantic_group_key text DEFAULT ''::text NOT NULL,
    source_state_hash text DEFAULT ''::text NOT NULL,
    CONSTRAINT community_sources_rank_check CHECK ((source_rank >= 0)),
    CONSTRAINT community_sources_version_check CHECK ((relationship_version >= 1))
);

ALTER TABLE ONLY public.community_sources FORCE ROW LEVEL SECURITY;


--
-- Name: community_summary_attempts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.community_summary_attempts (
    team_id uuid NOT NULL,
    attempt_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    community_id uuid,
    attempt integer NOT NULL,
    provider_model text DEFAULT ''::text NOT NULL,
    prompt_hash text DEFAULT ''::text NOT NULL,
    response_hash text DEFAULT ''::text NOT NULL,
    input_hash text DEFAULT ''::text NOT NULL,
    admitted_relationship_ids uuid[] DEFAULT ARRAY[]::uuid[] NOT NULL,
    admitted_evidence_ids uuid[] DEFAULT ARRAY[]::uuid[] NOT NULL,
    admitted_support_quotes jsonb DEFAULT '[]'::jsonb NOT NULL,
    response_summary text DEFAULT ''::text NOT NULL,
    valid boolean DEFAULT false NOT NULL,
    error_code text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT community_summary_attempts_attempt_check CHECK (((attempt >= 1) AND (attempt <= 3))),
    CONSTRAINT community_summary_attempts_quotes_array_check CHECK ((jsonb_typeof(admitted_support_quotes) = 'array'::text))
);

ALTER TABLE ONLY public.community_summary_attempts FORCE ROW LEVEL SECURITY;


--
-- Name: dream_cycle_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dream_cycle_runs (
    team_id uuid NOT NULL,
    run_id uuid DEFAULT gen_random_uuid() NOT NULL,
    initiated_by_profile_id uuid,
    run_date text NOT NULL,
    window_key text NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    lease_until timestamp with time zone,
    input_count integer DEFAULT 0 NOT NULL,
    created_hypotheses integer DEFAULT 0 NOT NULL,
    rejected_hypotheses integer DEFAULT 0 NOT NULL,
    source_snapshot jsonb DEFAULT '[]'::jsonb NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    canonical_run_id uuid,
    scheduled_for timestamp with time zone,
    lease_token uuid,
    attempt_count integer DEFAULT 0 NOT NULL,
    provider_model text DEFAULT ''::text NOT NULL,
    provider_turns integer DEFAULT 0 NOT NULL,
    provider_input_tokens integer DEFAULT 0 NOT NULL,
    provider_output_tokens integer DEFAULT 0 NOT NULL,
    attempted_paths integer DEFAULT 0 NOT NULL,
    provider_proposals integer DEFAULT 0 NOT NULL,
    outcome_summary jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT dream_cycle_runs_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT dream_cycle_runs_counts_check CHECK (((input_count >= 0) AND (created_hypotheses >= 0) AND (rejected_hypotheses >= 0))),
    CONSTRAINT dream_cycle_runs_outcome_summary_object_check CHECK ((jsonb_typeof(outcome_summary) = 'object'::text)),
    CONSTRAINT dream_cycle_runs_provider_counts_check CHECK (((provider_turns >= 0) AND (provider_input_tokens >= 0) AND (provider_output_tokens >= 0) AND (attempted_paths >= 0) AND (provider_proposals >= 0))),
    CONSTRAINT dream_cycle_runs_snapshot_array_check CHECK ((jsonb_typeof(source_snapshot) = 'array'::text)),
    CONSTRAINT dream_cycle_runs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'running'::text, 'completed'::text, 'failed'::text, 'skipped'::text, 'cancelled'::text, 'missed'::text]))),
    CONSTRAINT dream_cycle_runs_window_nonempty CHECK ((btrim(window_key) <> ''::text))
);

ALTER TABLE ONLY public.dream_cycle_runs FORCE ROW LEVEL SECURITY;


--
-- Name: dream_path_evaluations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dream_path_evaluations (
    team_id uuid NOT NULL,
    path_evaluation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    first_relationship_id uuid NOT NULL,
    first_relationship_version integer NOT NULL,
    second_relationship_id uuid NOT NULL,
    second_relationship_version integer NOT NULL,
    provider_model text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    allowed_predicate_fingerprint text NOT NULL,
    CONSTRAINT dream_path_evaluations_distinct_relationships_check CHECK ((first_relationship_id <> second_relationship_id)),
    CONSTRAINT dream_path_evaluations_model_nonempty CHECK ((btrim(provider_model) <> ''::text)),
    CONSTRAINT dream_path_evaluations_predicate_fingerprint_nonempty CHECK ((btrim(allowed_predicate_fingerprint) <> ''::text)),
    CONSTRAINT dream_path_evaluations_versions_check CHECK (((first_relationship_version >= 1) AND (second_relationship_version >= 1)))
);

ALTER TABLE ONLY public.dream_path_evaluations FORCE ROW LEVEL SECURITY;


--
-- Name: embedding_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.embedding_config (
    id smallint DEFAULT 1 NOT NULL,
    model character varying(255) NOT NULL,
    dimensions integer NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT embedding_config_singleton CHECK ((id = 1))
);


--
-- Name: embedding_contracts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.embedding_contracts (
    embedding_contract_id uuid DEFAULT gen_random_uuid() NOT NULL,
    contract_key text NOT NULL,
    version integer NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    dimensions integer NOT NULL,
    distance_metric text DEFAULT 'cosine'::text NOT NULL,
    vector_normalization text DEFAULT 'provider'::text NOT NULL,
    document_format_version integer DEFAULT 1 NOT NULL,
    query_format_version integer DEFAULT 1 NOT NULL,
    lifecycle_state text DEFAULT 'active'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT embedding_contracts_dimensions_check CHECK (((dimensions >= 1) AND (dimensions <= 16000))),
    CONSTRAINT embedding_contracts_distance_check CHECK ((distance_metric = 'cosine'::text)),
    CONSTRAINT embedding_contracts_format_check CHECK (((document_format_version >= 1) AND (query_format_version >= 1))),
    CONSTRAINT embedding_contracts_key_nonempty CHECK ((btrim(contract_key) <> ''::text)),
    CONSTRAINT embedding_contracts_lifecycle_check CHECK ((lifecycle_state = ANY (ARRAY['active'::text, 'deprecated'::text, 'retired'::text]))),
    CONSTRAINT embedding_contracts_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT embedding_contracts_model_nonempty CHECK ((btrim(model) <> ''::text)),
    CONSTRAINT embedding_contracts_normalization_check CHECK ((vector_normalization = ANY (ARRAY['provider'::text, 'unit'::text, 'none'::text]))),
    CONSTRAINT embedding_contracts_provider_nonempty CHECK ((btrim(provider) <> ''::text)),
    CONSTRAINT embedding_contracts_version_check CHECK ((version >= 1))
);


--
-- Name: embedding_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.embedding_jobs (
    team_id uuid NOT NULL,
    embedding_job_id uuid DEFAULT gen_random_uuid() NOT NULL,
    search_document_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    source_kind text NOT NULL,
    source_id uuid NOT NULL,
    source_version bigint NOT NULL,
    document_version bigint NOT NULL,
    embedding_contract_id uuid NOT NULL,
    embedding_dimensions integer NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 20 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_until timestamp with time zone,
    worker_id text DEFAULT ''::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    projection_format_version integer DEFAULT 1 NOT NULL,
    projection_generation_id uuid,
    CONSTRAINT embedding_jobs_attempts_check CHECK (((attempts >= 0) AND (max_attempts > 0) AND (attempts <= max_attempts))),
    CONSTRAINT embedding_jobs_document_version_check CHECK ((document_version >= 1)),
    CONSTRAINT embedding_jobs_projection_format_check CHECK ((projection_format_version >= 1)),
    CONSTRAINT embedding_jobs_source_kind_check CHECK ((source_kind = ANY (ARRAY['evidence'::text, 'relationship'::text, 'entity'::text]))),
    CONSTRAINT embedding_jobs_source_version_check CHECK ((source_version >= 1)),
    CONSTRAINT embedding_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'completed'::text, 'failed'::text, 'stale'::text, 'cancelled'::text]))),
    CONSTRAINT embedding_jobs_terminal_time_check CHECK ((((status = ANY (ARRAY['completed'::text, 'failed'::text, 'stale'::text, 'cancelled'::text])) AND (completed_at IS NOT NULL)) OR (status <> ALL (ARRAY['completed'::text, 'failed'::text, 'stale'::text, 'cancelled'::text]))))
);

ALTER TABLE ONLY public.embedding_jobs FORCE ROW LEVEL SECURITY;


--
-- Name: entity_correction_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_correction_events (
    team_id uuid NOT NULL,
    correction_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    action text NOT NULL,
    survivor_entity_id uuid,
    new_entity_id uuid,
    selected_observation_ids uuid[] DEFAULT ARRAY[]::uuid[] NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entity_correction_events_action_check CHECK ((action = ANY (ARRAY['merge'::text, 'split'::text]))),
    CONSTRAINT entity_correction_events_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.entity_correction_events FORCE ROW LEVEL SECURITY;


--
-- Name: entity_correction_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_correction_plans (
    team_id uuid NOT NULL,
    plan_token uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    action text NOT NULL,
    source_entity_id uuid NOT NULL,
    target_entity_id uuid,
    new_entity_id uuid,
    selected_observation_ids uuid[] DEFAULT ARRAY[]::uuid[] NOT NULL,
    blocked_observation_ids uuid[] DEFAULT ARRAY[]::uuid[] NOT NULL,
    affected_relationships jsonb DEFAULT '[]'::jsonb NOT NULL,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    impact_summary text DEFAULT ''::text NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'planned'::text NOT NULL,
    correction_event_id uuid,
    expires_at timestamp with time zone NOT NULL,
    applied_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entity_correction_plans_action_check CHECK ((action = ANY (ARRAY['merge'::text, 'split'::text]))),
    CONSTRAINT entity_correction_plans_affected_array_check CHECK ((jsonb_typeof(affected_relationships) = 'array'::text)),
    CONSTRAINT entity_correction_plans_evidence_array_check CHECK ((jsonb_typeof(evidence) = 'array'::text)),
    CONSTRAINT entity_correction_plans_merge_target_check CHECK ((((action = 'merge'::text) AND (target_entity_id IS NOT NULL)) OR ((action = 'split'::text) AND (target_entity_id IS NULL)))),
    CONSTRAINT entity_correction_plans_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT entity_correction_plans_status_check CHECK ((status = ANY (ARRAY['planned'::text, 'applied'::text])))
);

ALTER TABLE ONLY public.entity_correction_plans FORCE ROW LEVEL SECURITY;


--
-- Name: entity_names; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_names (
    team_id uuid NOT NULL,
    entity_name_id uuid DEFAULT gen_random_uuid() NOT NULL,
    entity_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    display_name text NOT NULL,
    normalized_name text NOT NULL,
    name_kind text NOT NULL,
    locale text DEFAULT ''::text NOT NULL,
    valid_from timestamp with time zone,
    valid_to timestamp with time zone,
    fragment_id uuid,
    span_start integer,
    span_end integer,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entity_names_display_nonempty CHECK ((btrim(display_name) <> ''::text)),
    CONSTRAINT entity_names_kind_check CHECK ((name_kind = ANY (ARRAY['canonical'::text, 'alias'::text, 'former'::text]))),
    CONSTRAINT entity_names_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT entity_names_normalized_nonempty CHECK ((btrim(normalized_name) <> ''::text)),
    CONSTRAINT entity_names_span_check CHECK ((((span_start IS NULL) AND (span_end IS NULL)) OR ((span_start IS NOT NULL) AND (span_end IS NOT NULL) AND (span_start >= 0) AND (span_end > span_start)))),
    CONSTRAINT entity_names_valid_window_check CHECK (((valid_to IS NULL) OR (valid_from IS NULL) OR (valid_to >= valid_from)))
);

ALTER TABLE ONLY public.entity_names FORCE ROW LEVEL SECURITY;


--
-- Name: entity_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_records (
    team_id uuid NOT NULL,
    entity_id uuid DEFAULT gen_random_uuid() NOT NULL,
    entity_kind text NOT NULL,
    identity_context jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entity_records_context_object_check CHECK ((jsonb_typeof(identity_context) = 'object'::text)),
    CONSTRAINT entity_records_kind_check CHECK ((entity_kind = ANY (ARRAY['person'::text, 'organization'::text, 'project'::text, 'product'::text, 'place'::text, 'document'::text, 'concept'::text, 'other'::text]))),
    CONSTRAINT entity_records_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT entity_records_status_check CHECK ((status = ANY (ARRAY['active'::text, 'retired'::text, 'needs_review'::text]))),
    CONSTRAINT entity_records_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.entity_records FORCE ROW LEVEL SECURITY;


--
-- Name: entity_resolution_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_resolution_events (
    team_id uuid NOT NULL,
    resolution_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    ingest_id uuid NOT NULL,
    placement_item_id uuid,
    owner_profile_id uuid NOT NULL,
    mention_ref text NOT NULL,
    action text NOT NULL,
    entity_id uuid,
    fragment_id uuid,
    span_start integer,
    span_end integer,
    verifier_result jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    assessment_id uuid,
    CONSTRAINT entity_resolution_events_action_check CHECK ((action = ANY (ARRAY['reuse'::text, 'create'::text, 'ambiguous'::text]))),
    CONSTRAINT entity_resolution_events_action_entity_check CHECK ((((action = ANY (ARRAY['reuse'::text, 'create'::text])) AND (entity_id IS NOT NULL)) OR ((action = 'ambiguous'::text) AND (entity_id IS NULL)))),
    CONSTRAINT entity_resolution_events_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT entity_resolution_events_ref_nonempty CHECK ((btrim(mention_ref) <> ''::text)),
    CONSTRAINT entity_resolution_events_span_check CHECK ((((span_start IS NULL) AND (span_end IS NULL)) OR ((span_start IS NOT NULL) AND (span_end IS NOT NULL) AND (span_start >= 0) AND (span_end > span_start)))),
    CONSTRAINT entity_resolution_events_verifier_object_check CHECK ((jsonb_typeof(verifier_result) = 'object'::text))
);

ALTER TABLE ONLY public.entity_resolution_events FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_fragments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_fragments (
    team_id uuid NOT NULL,
    fragment_id uuid DEFAULT gen_random_uuid() NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    source_id uuid,
    source_revision_id uuid,
    evidence_index integer NOT NULL,
    content text NOT NULL,
    content_hash text NOT NULL,
    source_type text DEFAULT 'conversation'::text NOT NULL,
    authority text DEFAULT 'primary'::text NOT NULL,
    source_ref text DEFAULT ''::text NOT NULL,
    labels text[] DEFAULT ARRAY[]::text[] NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_fragments_authority_check CHECK ((authority = ANY (ARRAY['authoritative'::text, 'primary'::text, 'secondary'::text, 'inferred'::text, 'unknown'::text]))),
    CONSTRAINT evidence_fragments_content_nonempty CHECK ((btrim(content) <> ''::text)),
    CONSTRAINT evidence_fragments_hash_nonempty CHECK ((btrim(content_hash) <> ''::text)),
    CONSTRAINT evidence_fragments_index_check CHECK ((evidence_index >= 0)),
    CONSTRAINT evidence_fragments_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT evidence_fragments_source_revision_pair_check CHECK ((((source_id IS NULL) AND (source_revision_id IS NULL)) OR ((source_id IS NOT NULL) AND (source_revision_id IS NOT NULL)))),
    CONSTRAINT evidence_fragments_source_type_check CHECK ((source_type = ANY (ARRAY['conversation'::text, 'document'::text, 'observation'::text, 'manual'::text])))
);

ALTER TABLE ONLY public.evidence_fragments FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_lifecycle_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_lifecycle_events (
    team_id uuid NOT NULL,
    lifecycle_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    lifecycle_operation_id uuid NOT NULL,
    target_fragment_id uuid NOT NULL,
    replacement_fragment_id uuid,
    owner_profile_id uuid NOT NULL,
    action text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_lifecycle_events_action_check CHECK ((action = ANY (ARRAY['supersede'::text, 'retract'::text]))),
    CONSTRAINT evidence_lifecycle_events_distinct_replacement_check CHECK (((replacement_fragment_id IS NULL) OR (replacement_fragment_id <> target_fragment_id))),
    CONSTRAINT evidence_lifecycle_events_replacement_check CHECK ((((action = 'supersede'::text) AND (replacement_fragment_id IS NOT NULL)) OR ((action = 'retract'::text) AND (replacement_fragment_id IS NULL))))
);

ALTER TABLE ONLY public.evidence_lifecycle_events FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_lifecycle_operations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_lifecycle_operations (
    team_id uuid NOT NULL,
    lifecycle_operation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    action text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    replacement_ingest_id uuid,
    result jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    actor_profile_id uuid,
    CONSTRAINT evidence_lifecycle_operations_action_check CHECK ((action = ANY (ARRAY['supersede'::text, 'retract'::text]))),
    CONSTRAINT evidence_lifecycle_operations_idempotency_nonempty CHECK ((btrim(idempotency_key) <> ''::text)),
    CONSTRAINT evidence_lifecycle_operations_reason_length_check CHECK ((char_length(reason) <= 1000)),
    CONSTRAINT evidence_lifecycle_operations_request_hash_nonempty CHECK ((btrim(request_hash) <> ''::text)),
    CONSTRAINT evidence_lifecycle_operations_result_object_check CHECK ((jsonb_typeof(result) = 'object'::text))
);

ALTER TABLE ONLY public.evidence_lifecycle_operations FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_quarantines; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_quarantines (
    team_id uuid NOT NULL,
    quarantine_id uuid DEFAULT gen_random_uuid() NOT NULL,
    fragment_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    released_by_profile_id uuid,
    release_reason text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    released_at timestamp with time zone,
    CONSTRAINT evidence_quarantines_release_check CHECK ((((status = 'active'::text) AND (released_at IS NULL)) OR ((status = 'released'::text) AND (released_at IS NOT NULL) AND (released_by_profile_id IS NOT NULL)))),
    CONSTRAINT evidence_quarantines_status_check CHECK ((status = ANY (ARRAY['active'::text, 'released'::text])))
);

ALTER TABLE ONLY public.evidence_quarantines FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_security_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_security_events (
    team_id uuid NOT NULL,
    security_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    fragment_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    event_kind text NOT NULL,
    decision text NOT NULL,
    actor_profile_id uuid,
    reason text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_security_decision_check CHECK ((decision = ANY (ARRAY['pass'::text, 'guarded'::text, 'quarantine'::text, 'released'::text]))),
    CONSTRAINT evidence_security_event_kind_check CHECK ((event_kind = ANY (ARRAY['deterministic_scan'::text, 'reviewer_signal'::text, 'verifier_signal'::text, 'quarantine_release'::text]))),
    CONSTRAINT evidence_security_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.evidence_security_events FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_security_signals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_security_signals (
    team_id uuid NOT NULL,
    security_event_id uuid NOT NULL,
    signal_index integer NOT NULL,
    owner_profile_id uuid NOT NULL,
    kind text NOT NULL,
    severity text NOT NULL,
    span_start integer NOT NULL,
    span_end integer NOT NULL,
    quote text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_security_signal_index_check CHECK ((signal_index >= 0)),
    CONSTRAINT evidence_security_signal_kind_check CHECK ((kind = ANY (ARRAY['role_control_spoofing'::text, 'instruction_override'::text, 'prompt_secret_extraction'::text, 'tool_exfiltration'::text, 'obfuscated_instruction'::text, 'hidden_control_markup'::text]))),
    CONSTRAINT evidence_security_signal_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT evidence_security_signal_severity_check CHECK ((severity = ANY (ARRAY['low'::text, 'medium'::text, 'high'::text, 'critical'::text]))),
    CONSTRAINT evidence_security_signal_span_check CHECK (((span_start >= 0) AND (span_end > span_start)))
);

ALTER TABLE ONLY public.evidence_security_signals FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_source_revisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_source_revisions (
    team_id uuid NOT NULL,
    source_revision_id uuid DEFAULT gen_random_uuid() NOT NULL,
    source_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    revision_token text NOT NULL,
    expected_previous_revision_token text DEFAULT ''::text CONSTRAINT evidence_source_revisions_expected_previous_revision_t_not_null NOT NULL,
    supersedes_revision_id uuid,
    content_hash text NOT NULL,
    envelope jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_source_revisions_envelope_object_check CHECK ((jsonb_typeof(envelope) = 'object'::text)),
    CONSTRAINT evidence_source_revisions_hash_nonempty CHECK ((btrim(content_hash) <> ''::text)),
    CONSTRAINT evidence_source_revisions_token_nonempty CHECK ((btrim(revision_token) <> ''::text))
);

ALTER TABLE ONLY public.evidence_source_revisions FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_sources (
    team_id uuid NOT NULL,
    source_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    source_key text NOT NULL,
    source_kind text DEFAULT 'conversation'::text NOT NULL,
    authority text DEFAULT 'primary'::text NOT NULL,
    current_revision_id uuid,
    current_revision_token text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_sources_authority_check CHECK ((authority = ANY (ARRAY['authoritative'::text, 'primary'::text, 'secondary'::text, 'inferred'::text, 'unknown'::text]))),
    CONSTRAINT evidence_sources_key_nonempty CHECK ((btrim(source_key) <> ''::text)),
    CONSTRAINT evidence_sources_kind_check CHECK ((source_kind = ANY (ARRAY['conversation'::text, 'document'::text, 'integration'::text, 'manual'::text]))),
    CONSTRAINT evidence_sources_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.evidence_sources FORCE ROW LEVEL SECURITY;


--
-- Name: hypotheses; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hypotheses (
    team_id uuid NOT NULL,
    hypothesis_id uuid DEFAULT gen_random_uuid() NOT NULL,
    created_by_profile_id uuid,
    status text DEFAULT 'proposed'::text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    statement text DEFAULT ''::text NOT NULL,
    rationale text DEFAULT ''::text NOT NULL,
    likelihood numeric(5,4),
    confidence numeric(5,4),
    subject_entity_id uuid,
    predicate_key text,
    predicate_version integer,
    object_entity_id uuid,
    object_value_id uuid,
    source_refs jsonb DEFAULT '[]'::jsonb NOT NULL,
    source_versions jsonb DEFAULT '{}'::jsonb NOT NULL,
    source_owner_profile_ids uuid[] DEFAULT ARRAY[]::uuid[] NOT NULL,
    content_hash text,
    cycle_run_id uuid,
    generator_kind text DEFAULT ''::text NOT NULL,
    generator_version text DEFAULT ''::text NOT NULL,
    invalidated_reason text DEFAULT ''::text NOT NULL,
    submitted_ingest_id uuid,
    submitted_at timestamp with time zone,
    canonical_hypothesis_id uuid,
    target_identity text,
    CONSTRAINT hypotheses_endpoint_choice_check CHECK ((((object_entity_id IS NULL) AND (object_value_id IS NULL)) OR ((object_entity_id IS NULL) <> (object_value_id IS NULL)))),
    CONSTRAINT hypotheses_payload_object_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT hypotheses_probability_check CHECK ((((likelihood IS NULL) OR ((likelihood >= (0)::numeric) AND (likelihood <= (1)::numeric))) AND ((confidence IS NULL) OR ((confidence >= (0)::numeric) AND (confidence <= (1)::numeric))))),
    CONSTRAINT hypotheses_source_refs_array_check CHECK ((jsonb_typeof(source_refs) = 'array'::text)),
    CONSTRAINT hypotheses_source_versions_object_check CHECK ((jsonb_typeof(source_versions) = 'object'::text)),
    CONSTRAINT hypotheses_statement_nonempty_when_sourced CHECK (((statement <> ''::text) OR (content_hash IS NULL))),
    CONSTRAINT hypotheses_status_check CHECK ((status = ANY (ARRAY['proposed'::text, 'reinforced'::text, 'stale'::text, 'rejected'::text, 'submitted'::text])))
);

ALTER TABLE ONLY public.hypotheses FORCE ROW LEVEL SECURITY;


--
-- Name: hypothesis_derivation_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hypothesis_derivation_sources (
    team_id uuid NOT NULL,
    derivation_source_id uuid DEFAULT gen_random_uuid() NOT NULL,
    hypothesis_id uuid NOT NULL,
    premise_position smallint NOT NULL,
    relationship_id uuid NOT NULL,
    relationship_version integer NOT NULL,
    support_id uuid,
    observation_id uuid,
    fragment_id uuid NOT NULL,
    source_id uuid,
    source_revision_id uuid,
    source_group_key text DEFAULT ''::text NOT NULL,
    span_start integer NOT NULL,
    span_end integer NOT NULL,
    quote text NOT NULL,
    authority text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT hypothesis_derivation_sources_authority_check CHECK ((authority = ANY (ARRAY['authoritative'::text, 'primary'::text, 'secondary'::text, 'inferred'::text, 'unknown'::text]))),
    CONSTRAINT hypothesis_derivation_sources_group_nonempty CHECK ((btrim(source_group_key) <> ''::text)),
    CONSTRAINT hypothesis_derivation_sources_origin_check CHECK (((support_id IS NULL) <> (observation_id IS NULL))),
    CONSTRAINT hypothesis_derivation_sources_premise_check CHECK ((premise_position = ANY (ARRAY[1, 2]))),
    CONSTRAINT hypothesis_derivation_sources_quote_nonempty CHECK ((btrim(quote) <> ''::text)),
    CONSTRAINT hypothesis_derivation_sources_source_revision_pair_check CHECK (((source_id IS NULL) = (source_revision_id IS NULL))),
    CONSTRAINT hypothesis_derivation_sources_span_check CHECK (((span_start >= 0) AND (span_end > span_start))),
    CONSTRAINT hypothesis_derivation_sources_version_check CHECK ((relationship_version >= 1))
);

ALTER TABLE ONLY public.hypothesis_derivation_sources FORCE ROW LEVEL SECURITY;


--
-- Name: hypothesis_feedback_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hypothesis_feedback_events (
    team_id uuid NOT NULL,
    feedback_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    hypothesis_id uuid NOT NULL,
    actor_profile_id uuid NOT NULL,
    decision text NOT NULL,
    feedback text DEFAULT ''::text NOT NULL,
    submitted_ingest_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT hypothesis_feedback_events_decision_check CHECK ((decision = ANY (ARRAY['reject'::text, 'stale'::text, 'reinforce'::text, 'confirm_true'::text, 'confirm_false'::text, 'promote_candidate'::text])))
);

ALTER TABLE ONLY public.hypothesis_feedback_events FORCE ROW LEVEL SECURITY;


--
-- Name: knowledge_ingests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.knowledge_ingests (
    team_id uuid NOT NULL,
    ingest_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    request_hash text DEFAULT ''::text NOT NULL,
    source_summary text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    proposal jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    migration_run_id uuid,
    CONSTRAINT knowledge_ingests_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT knowledge_ingests_proposal_object_check CHECK ((jsonb_typeof(proposal) = 'object'::text)),
    CONSTRAINT knowledge_ingests_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'guarded'::text, 'quarantined'::text, 'processing'::text, 'completed'::text, 'failed'::text])))
);

ALTER TABLE ONLY public.knowledge_ingests FORCE ROW LEVEL SECURITY;


--
-- Name: operation_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.operation_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    severity character varying(16) NOT NULL,
    severity_rank smallint NOT NULL,
    message text NOT NULL,
    source text DEFAULT ''::text NOT NULL,
    team_id uuid,
    profile_id uuid,
    correlation_id text DEFAULT ''::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    attrs jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.operation_logs FORCE ROW LEVEL SECURITY;


--
-- Name: placement_assessments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.placement_assessments (
    team_id uuid NOT NULL,
    assessment_id uuid DEFAULT gen_random_uuid() NOT NULL,
    placement_item_id uuid,
    claim_key uuid,
    owner_profile_id uuid NOT NULL,
    request_id text NOT NULL,
    assessor_contract_version text NOT NULL,
    model text NOT NULL,
    tokenizer text NOT NULL,
    input_tokens integer NOT NULL,
    output_tokens integer NOT NULL,
    candidate_context_tokens integer NOT NULL,
    candidate_context_truncated boolean DEFAULT false NOT NULL,
    normalized_response jsonb NOT NULL,
    response_hash text NOT NULL,
    validated_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    assessment_scope text DEFAULT 'item'::text NOT NULL,
    placement_run_id uuid,
    ingest_id uuid,
    CONSTRAINT placement_assessments_contract_nonempty CHECK ((btrim(assessor_contract_version) <> ''::text)),
    CONSTRAINT placement_assessments_model_nonempty CHECK ((btrim(model) <> ''::text)),
    CONSTRAINT placement_assessments_request_nonempty CHECK ((btrim(request_id) <> ''::text)),
    CONSTRAINT placement_assessments_response_hash_nonempty CHECK ((btrim(response_hash) <> ''::text)),
    CONSTRAINT placement_assessments_response_object_check CHECK ((jsonb_typeof(normalized_response) = 'object'::text)),
    CONSTRAINT placement_assessments_scope_shape_check CHECK ((((assessment_scope = 'item'::text) AND (placement_item_id IS NOT NULL) AND (claim_key IS NOT NULL) AND (placement_run_id IS NULL) AND (ingest_id IS NULL)) OR ((assessment_scope = 'submission'::text) AND (placement_item_id IS NULL) AND (claim_key IS NULL) AND (placement_run_id IS NOT NULL) AND (ingest_id IS NOT NULL)))),
    CONSTRAINT placement_assessments_token_counts_check CHECK (((input_tokens >= 0) AND (output_tokens >= 0) AND (candidate_context_tokens >= 0))),
    CONSTRAINT placement_assessments_tokenizer_nonempty CHECK ((btrim(tokenizer) <> ''::text))
);

ALTER TABLE ONLY public.placement_assessments FORCE ROW LEVEL SECURITY;


--
-- Name: placement_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.placement_items (
    team_id uuid NOT NULL,
    placement_item_id uuid DEFAULT gen_random_uuid() NOT NULL,
    placement_run_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    fragment_id uuid NOT NULL,
    evidence_index integer NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    category text DEFAULT 'pending'::text NOT NULL,
    result jsonb DEFAULT '{}'::jsonb NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    claim_key uuid DEFAULT gen_random_uuid() NOT NULL,
    assessor_attempt_id uuid,
    assessor_attempted_at timestamp with time zone,
    CONSTRAINT placement_items_assessor_attempt_pair_check CHECK (((assessor_attempt_id IS NULL) = (assessor_attempted_at IS NULL))),
    CONSTRAINT placement_items_category_check CHECK ((category = ANY (ARRAY['pending'::text, 'fragment_only'::text, 'candidate'::text, 'validated_claim'::text, 'fact'::text, 'quarantined'::text, 'failed'::text]))),
    CONSTRAINT placement_items_index_check CHECK ((evidence_index >= 0)),
    CONSTRAINT placement_items_result_object_check CHECK ((jsonb_typeof(result) = 'object'::text)),
    CONSTRAINT placement_items_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'awaiting_review'::text, 'completed'::text, 'failed'::text, 'quarantined'::text]))),
    CONSTRAINT placement_items_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.placement_items FORCE ROW LEVEL SECURITY;


--
-- Name: placement_outcomes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.placement_outcomes (
    team_id uuid NOT NULL,
    outcome_id uuid DEFAULT gen_random_uuid() NOT NULL,
    placement_run_id uuid NOT NULL,
    placement_item_id uuid,
    owner_profile_id uuid NOT NULL,
    outcome_kind text NOT NULL,
    status text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    CONSTRAINT placement_outcomes_kind_nonempty CHECK ((btrim(outcome_kind) <> ''::text)),
    CONSTRAINT placement_outcomes_payload_object_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT placement_outcomes_status_nonempty CHECK ((btrim(status) <> ''::text))
);

ALTER TABLE ONLY public.placement_outcomes FORCE ROW LEVEL SECURITY;


--
-- Name: placement_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.placement_runs (
    team_id uuid NOT NULL,
    placement_run_id uuid DEFAULT gen_random_uuid() NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 5 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_until timestamp with time zone,
    worker_id text DEFAULT ''::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    assessor_attempt_id uuid,
    assessor_attempted_at timestamp with time zone,
    semantic_hold_state text,
    semantic_hold_version integer DEFAULT 0 NOT NULL,
    semantic_hold_updated_at timestamp with time zone,
    replaces_placement_run_id uuid,
    superseded_by_placement_run_id uuid,
    quarantine_expires_at timestamp with time zone,
    CONSTRAINT placement_runs_assessor_attempt_pair_check CHECK (((assessor_attempt_id IS NULL) = (assessor_attempted_at IS NULL))),
    CONSTRAINT placement_runs_attempts_check CHECK (((attempts >= 0) AND (max_attempts >= 1) AND (attempts <= max_attempts))),
    CONSTRAINT placement_runs_completion_check CHECK ((((status = ANY (ARRAY['awaiting_review'::text, 'completed'::text, 'failed'::text, 'quarantined'::text])) AND (completed_at IS NOT NULL)) OR (status <> ALL (ARRAY['awaiting_review'::text, 'completed'::text, 'failed'::text, 'quarantined'::text])))),
    CONSTRAINT placement_runs_quarantine_expiry_check CHECK (((status <> 'quarantined'::text) OR ((completed_at IS NOT NULL) AND (quarantine_expires_at IS NOT NULL) AND (quarantine_expires_at = (completed_at + '24:00:00'::interval))))),
    CONSTRAINT placement_runs_semantic_hold_state_check CHECK (((semantic_hold_state IS NULL) OR (semantic_hold_state = ANY (ARRAY['active'::text, 'expired'::text, 'superseded'::text])))),
    CONSTRAINT placement_runs_semantic_hold_version_check CHECK ((semantic_hold_version >= 0)),
    CONSTRAINT placement_runs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'guarded'::text, 'quarantined'::text, 'processing'::text, 'awaiting_review'::text, 'completed'::text, 'failed'::text])))
);

ALTER TABLE ONLY public.placement_runs FORCE ROW LEVEL SECURITY;


--
-- Name: predicate_definitions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.predicate_definitions (
    predicate_key text NOT NULL,
    version integer NOT NULL,
    aliases text[] DEFAULT ARRAY[]::text[] NOT NULL,
    allowed_subject_kinds text[] DEFAULT ARRAY[]::text[] NOT NULL,
    allowed_object_kinds text[] DEFAULT ARRAY[]::text[] NOT NULL,
    relationship_kind text NOT NULL,
    current_cardinality text NOT NULL,
    lifecycle_state text DEFAULT 'active'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT predicate_definitions_current_cardinality_check CHECK ((current_cardinality = ANY (ARRAY['one'::text, 'many'::text]))),
    CONSTRAINT predicate_definitions_key_nonempty CHECK ((btrim(predicate_key) <> ''::text)),
    CONSTRAINT predicate_definitions_lifecycle_state_check CHECK ((lifecycle_state = ANY (ARRAY['active'::text, 'deprecated'::text, 'retired'::text]))),
    CONSTRAINT predicate_definitions_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT predicate_definitions_relationship_kind_check CHECK ((relationship_kind = ANY (ARRAY['state'::text, 'event'::text]))),
    CONSTRAINT predicate_definitions_version_check CHECK ((version >= 1))
);


--
-- Name: predicate_registration_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.predicate_registration_events (
    team_id uuid NOT NULL,
    predicate_registration_event_id uuid DEFAULT gen_random_uuid() CONSTRAINT predicate_registration_even_predicate_registration_eve_not_null NOT NULL,
    placement_run_id uuid NOT NULL,
    assessment_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    relationship_ref text NOT NULL,
    registration_action text NOT NULL,
    predicate_key text NOT NULL,
    predicate_version integer NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT predicate_registration_events_action_check CHECK ((registration_action = ANY (ARRAY['created'::text, 'reused'::text]))),
    CONSTRAINT predicate_registration_events_key_nonempty CHECK ((btrim(predicate_key) <> ''::text)),
    CONSTRAINT predicate_registration_events_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT predicate_registration_events_ref_nonempty CHECK ((btrim(relationship_ref) <> ''::text)),
    CONSTRAINT predicate_registration_events_version_check CHECK ((predicate_version >= 1))
);

ALTER TABLE ONLY public.predicate_registration_events FORCE ROW LEVEL SECURITY;


--
-- Name: recall_feedback_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.recall_feedback_events (
    recall_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    feedback_at timestamp with time zone,
    team_id uuid,
    profile_id uuid,
    key_id uuid,
    auth_method text DEFAULT ''::text NOT NULL,
    tool_name text DEFAULT 'recall_memory'::text NOT NULL,
    query text DEFAULT ''::text NOT NULL,
    tool_args jsonb DEFAULT '{}'::jsonb NOT NULL,
    result_refs jsonb DEFAULT '[]'::jsonb NOT NULL,
    result_count integer DEFAULT 0 NOT NULL,
    snapshot_state text DEFAULT 'captured'::text NOT NULL,
    used boolean,
    answer_supported boolean,
    quality text DEFAULT ''::text NOT NULL,
    missing_context boolean,
    irrelevant boolean,
    failure_reason text DEFAULT ''::text NOT NULL,
    expected_context text DEFAULT ''::text NOT NULL,
    irrelevant_result_refs jsonb DEFAULT '[]'::jsonb NOT NULL,
    feedback_comment text DEFAULT ''::text NOT NULL,
    dream_feedback jsonb DEFAULT '[]'::jsonb NOT NULL,
    contract_version text DEFAULT ''::text NOT NULL,
    ranking_profile_version text DEFAULT ''::text NOT NULL,
    embedding_contract_version text DEFAULT ''::text NOT NULL,
    search_index_profile_version text DEFAULT ''::text NOT NULL,
    search_state text DEFAULT ''::text NOT NULL,
    degradation jsonb DEFAULT '{}'::jsonb NOT NULL,
    snapshot_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT recall_feedback_events_degradation_object_check CHECK ((jsonb_typeof(degradation) = 'object'::text)),
    CONSTRAINT recall_feedback_events_expected_context_length_check CHECK ((char_length(expected_context) <= 1000)),
    CONSTRAINT recall_feedback_events_failure_reason_length_check CHECK ((char_length(failure_reason) <= 1000)),
    CONSTRAINT recall_feedback_events_feedback_comment_length_check CHECK ((char_length(feedback_comment) <= 1000)),
    CONSTRAINT recall_feedback_events_irrelevant_result_refs_array_check CHECK (
CASE
    WHEN (jsonb_typeof(irrelevant_result_refs) = 'array'::text) THEN (jsonb_array_length(irrelevant_result_refs) <= 20)
    ELSE false
END),
    CONSTRAINT recall_feedback_events_quality_check CHECK ((quality = ANY (ARRAY[''::text, 'high'::text, 'medium'::text, 'low'::text]))),
    CONSTRAINT recall_feedback_events_result_count_check CHECK ((result_count >= 0)),
    CONSTRAINT recall_feedback_events_snapshot_metadata_object_check CHECK ((jsonb_typeof(snapshot_metadata) = 'object'::text)),
    CONSTRAINT recall_feedback_events_snapshot_state_check CHECK ((snapshot_state = ANY (ARRAY['captured'::text, 'feedback_only'::text])))
);

ALTER TABLE ONLY public.recall_feedback_events FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_ai_assessment_attempts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_ai_assessment_attempts (
    team_id uuid NOT NULL,
    assessment_attempt_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_conflict_ai_assessm_assessment_attempt_id_not_null NOT NULL,
    conflict_id uuid CONSTRAINT relationship_conflict_ai_assessment_attemp_conflict_id_not_null NOT NULL,
    case_version integer CONSTRAINT relationship_conflict_ai_assessment_attem_case_version_not_null NOT NULL,
    local_assessment_date date CONSTRAINT relationship_conflict_ai_assessm_local_assessment_date_not_null NOT NULL,
    model text NOT NULL,
    policy_version text CONSTRAINT relationship_conflict_ai_assessment_att_policy_version_not_null NOT NULL,
    status text DEFAULT 'reserved'::text NOT NULL,
    selected_position_id uuid,
    confidence double precision,
    provider_turns integer DEFAULT 0 CONSTRAINT relationship_conflict_ai_assessment_att_provider_turns_not_null NOT NULL,
    response_hash text DEFAULT ''::text CONSTRAINT relationship_conflict_ai_assessment_atte_response_hash_not_null NOT NULL,
    failure_class text DEFAULT ''::text CONSTRAINT relationship_conflict_ai_assessment_atte_failure_class_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT now() CONSTRAINT relationship_conflict_ai_assessment_attempt_created_at_not_null NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT relationship_conflict_ai_assessment_case_version_check CHECK ((case_version >= 1)),
    CONSTRAINT relationship_conflict_ai_assessment_confidence_check CHECK (((confidence IS NULL) OR ((confidence >= (0)::double precision) AND (confidence <= (1)::double precision)))),
    CONSTRAINT relationship_conflict_ai_assessment_failure_class_length_check CHECK ((char_length(failure_class) <= 128)),
    CONSTRAINT relationship_conflict_ai_assessment_model_nonempty CHECK ((btrim(model) <> ''::text)),
    CONSTRAINT relationship_conflict_ai_assessment_policy_nonempty CHECK ((btrim(policy_version) <> ''::text)),
    CONSTRAINT relationship_conflict_ai_assessment_provider_turns_check CHECK ((provider_turns >= 0)),
    CONSTRAINT relationship_conflict_ai_assessment_response_hash_length_check CHECK ((char_length(response_hash) <= 128)),
    CONSTRAINT relationship_conflict_ai_assessment_selected_shape_check CHECK ((((status = 'selected'::text) AND (selected_position_id IS NOT NULL) AND (confidence IS NOT NULL)) OR ((status = 'abstained'::text) AND (selected_position_id IS NULL) AND (confidence = (0)::double precision)) OR ((status = ANY (ARRAY['reserved'::text, 'failed'::text, 'superseded'::text])) AND (selected_position_id IS NULL)))),
    CONSTRAINT relationship_conflict_ai_assessment_status_check CHECK ((status = ANY (ARRAY['reserved'::text, 'selected'::text, 'abstained'::text, 'failed'::text, 'superseded'::text])))
);

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_attempts FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_ai_assessment_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_ai_assessment_events (
    team_id uuid NOT NULL,
    assessment_event_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_conflict_ai_assessmen_assessment_event_id_not_null NOT NULL,
    assessment_attempt_id uuid CONSTRAINT relationship_conflict_ai_assess_assessment_attempt_id_not_null1 NOT NULL,
    action text NOT NULL,
    outcome text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_conflict_ai_assessment_event_action_check CHECK ((action = ANY (ARRAY['reserved'::text, 'selected'::text, 'abstained'::text, 'failed'::text, 'superseded'::text]))),
    CONSTRAINT relationship_conflict_ai_assessment_event_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_events FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_cases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_cases (
    team_id uuid NOT NULL,
    conflict_id uuid DEFAULT gen_random_uuid() NOT NULL,
    semantic_scope_key text NOT NULL,
    kind text DEFAULT 'cross_profile_current_state'::text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    subject_entity_id uuid NOT NULL,
    predicate_key text NOT NULL,
    predicate_version integer DEFAULT 1 NOT NULL,
    relationship_kind text NOT NULL,
    current_cardinality text NOT NULL,
    polarity text DEFAULT '+'::text NOT NULL,
    scope_key text,
    question text DEFAULT ''::text NOT NULL,
    policy_version text DEFAULT 'cross_profile_conflict_v1'::text NOT NULL,
    review_due_at timestamp with time zone NOT NULL,
    next_review_at timestamp with time zone NOT NULL,
    review_ttl_days integer NOT NULL,
    timezone text DEFAULT 'Local'::text NOT NULL,
    preferred_position_id uuid,
    resolved_at timestamp with time zone,
    effective_at timestamp with time zone,
    effective_time_basis text DEFAULT ''::text NOT NULL,
    resolution_reason text DEFAULT ''::text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    lease_worker_id text DEFAULT ''::text NOT NULL,
    lease_until timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    last_review_run_id uuid,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_conflict_cases_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT relationship_conflict_cases_cardinality_check CHECK ((current_cardinality = ANY (ARRAY['one'::text, 'many'::text]))),
    CONSTRAINT relationship_conflict_cases_kind_check CHECK ((kind = 'cross_profile_current_state'::text)),
    CONSTRAINT relationship_conflict_cases_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_conflict_cases_polarity_check CHECK ((polarity = ANY (ARRAY['+'::text, '-'::text]))),
    CONSTRAINT relationship_conflict_cases_relationship_kind_check CHECK ((relationship_kind = ANY (ARRAY['state'::text, 'event'::text]))),
    CONSTRAINT relationship_conflict_cases_review_ttl_check CHECK (((review_ttl_days >= 1) AND (review_ttl_days <= 30))),
    CONSTRAINT relationship_conflict_cases_scope_nonempty CHECK ((btrim(semantic_scope_key) <> ''::text)),
    CONSTRAINT relationship_conflict_cases_status_check CHECK ((status = ANY (ARRAY['open'::text, 'overdue'::text, 'resolved'::text, 'dismissed'::text]))),
    CONSTRAINT relationship_conflict_cases_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.relationship_conflict_cases FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_derived_evidence_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_derived_evidence_tasks (
    team_id uuid NOT NULL,
    derived_evidence_task_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_conflict_derived_derived_evidence_task_id_not_null NOT NULL,
    resolution_plan_id uuid CONSTRAINT relationship_conflict_derived_evide_resolution_plan_id_not_null NOT NULL,
    conflict_id uuid CONSTRAINT relationship_conflict_derived_evidence_tas_conflict_id_not_null NOT NULL,
    target_fragment_id uuid CONSTRAINT relationship_conflict_derived_evide_target_fragment_id_not_null NOT NULL,
    target_owner_profile_id uuid CONSTRAINT relationship_conflict_derived__target_owner_profile_id_not_null NOT NULL,
    selected_position_id uuid CONSTRAINT relationship_conflict_derived_evi_selected_position_id_not_null NOT NULL,
    system_profile_id uuid CONSTRAINT relationship_conflict_derived_eviden_system_profile_id_not_null NOT NULL,
    source_group_key text CONSTRAINT relationship_conflict_derived_evidenc_source_group_key_not_null NOT NULL,
    origin_evidence_index integer CONSTRAINT relationship_conflict_derived_ev_origin_evidence_index_not_null NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    lease_worker_id text,
    lease_until timestamp with time zone,
    last_review_run_id uuid,
    last_failure_class text DEFAULT ''::text CONSTRAINT relationship_conflict_derived_evide_last_failure_class_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT now() CONSTRAINT relationship_conflict_derived_evidence_task_created_at_not_null NOT NULL,
    updated_at timestamp with time zone DEFAULT now() CONSTRAINT relationship_conflict_derived_evidence_task_updated_at_not_null NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT relationship_conflict_derived_evidence_tasks_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_failure_length_che CHECK ((char_length(last_failure_class) <= 128)),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_origin_index_check CHECK ((origin_evidence_index >= 0)),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_source_group_check CHECK ((btrim(source_group_key) <> ''::text)),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'completed'::text])))
);

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_events (
    team_id uuid NOT NULL,
    conflict_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    conflict_id uuid NOT NULL,
    position_id uuid,
    relationship_id uuid,
    owner_profile_id uuid,
    action text NOT NULL,
    outcome text DEFAULT ''::text NOT NULL,
    actor_kind text DEFAULT 'system'::text NOT NULL,
    actor_profile_id uuid,
    policy_version text DEFAULT 'cross_profile_conflict_v1'::text NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_conflict_events_action_check CHECK ((action = ANY (ARRAY['opened'::text, 'position_added'::text, 'member_added'::text, 'evaluated'::text, 'marked_overdue'::text, 'resolved'::text, 'relationship_updated'::text, 'dismissed'::text, 'ai_assessment_reserved'::text, 'ai_assessed'::text, 'resolution_pending'::text, 'evidence_retracted'::text, 'derived_replacement_staged'::text, 'derived_replacement_failed'::text]))),
    CONSTRAINT relationship_conflict_events_actor_check CHECK ((actor_kind = ANY (ARRAY['system'::text, 'profile'::text]))),
    CONSTRAINT relationship_conflict_events_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.relationship_conflict_events FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_evidence_derivations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_evidence_derivations (
    team_id uuid NOT NULL,
    derivation_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_conflict_evidence_derivatio_derivation_id_not_null NOT NULL,
    conflict_id uuid NOT NULL,
    target_fragment_id uuid CONSTRAINT relationship_conflict_evidence_deri_target_fragment_id_not_null NOT NULL,
    target_owner_profile_id uuid CONSTRAINT relationship_conflict_evidence_target_owner_profile_id_not_null NOT NULL,
    selected_position_id uuid CONSTRAINT relationship_conflict_evidence_de_selected_position_id_not_null NOT NULL,
    replacement_fragment_id uuid,
    system_profile_id uuid CONSTRAINT relationship_conflict_evidence_deriv_system_profile_id_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_position_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_position_members (
    team_id uuid NOT NULL,
    conflict_id uuid NOT NULL,
    position_id uuid NOT NULL,
    relationship_id uuid NOT NULL,
    owner_profile_id uuid CONSTRAINT relationship_conflict_position_member_owner_profile_id_not_null NOT NULL,
    support_id uuid,
    verification_event_id uuid,
    fragment_id uuid,
    source_group_key text CONSTRAINT relationship_conflict_position_member_source_group_key_not_null NOT NULL,
    authority text DEFAULT 'primary'::text NOT NULL,
    effective_at timestamp with time zone,
    effective_time_basis text DEFAULT ''::text CONSTRAINT relationship_conflict_position_me_effective_time_basis_not_null NOT NULL,
    recorded_fallback boolean DEFAULT false CONSTRAINT relationship_conflict_position_membe_recorded_fallback_not_null NOT NULL,
    active boolean DEFAULT true NOT NULL,
    retired_at timestamp with time zone,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    accepted_at timestamp with time zone NOT NULL,
    CONSTRAINT relationship_conflict_members_authority_check CHECK ((authority = ANY (ARRAY['authoritative'::text, 'primary'::text, 'secondary'::text, 'inferred'::text, 'unknown'::text]))),
    CONSTRAINT relationship_conflict_members_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_conflict_members_source_group_nonempty CHECK ((btrim(source_group_key) <> ''::text))
);

ALTER TABLE ONLY public.relationship_conflict_position_members FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_positions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_positions (
    team_id uuid NOT NULL,
    conflict_id uuid NOT NULL,
    position_id uuid DEFAULT gen_random_uuid() NOT NULL,
    position_key text NOT NULL,
    object_entity_id uuid,
    object_value_id uuid,
    disposition text DEFAULT 'candidate'::text NOT NULL,
    support_group_count integer DEFAULT 0 NOT NULL,
    authoritative_group_count integer DEFAULT 0 CONSTRAINT relationship_conflict_positi_authoritative_group_count_not_null NOT NULL,
    active boolean DEFAULT true NOT NULL,
    retired_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_conflict_positions_counts_check CHECK (((support_group_count >= 0) AND (authoritative_group_count >= 0))),
    CONSTRAINT relationship_conflict_positions_disposition_check CHECK ((disposition = ANY (ARRAY['candidate'::text, 'preferred'::text, 'suppressed_current'::text]))),
    CONSTRAINT relationship_conflict_positions_key_nonempty CHECK ((btrim(position_key) <> ''::text)),
    CONSTRAINT relationship_conflict_positions_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_conflict_positions_object_check CHECK (((object_entity_id IS NULL) <> (object_value_id IS NULL)))
);

ALTER TABLE ONLY public.relationship_conflict_positions FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_resolution_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_resolution_plans (
    team_id uuid NOT NULL,
    resolution_plan_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_conflict_resolution_pl_resolution_plan_id_not_null NOT NULL,
    conflict_id uuid NOT NULL,
    expected_case_version integer CONSTRAINT relationship_conflict_resolution_expected_case_version_not_null NOT NULL,
    preferred_position_id uuid CONSTRAINT relationship_conflict_resolution_preferred_position_id_not_null NOT NULL,
    assessment_attempt_id uuid,
    method text NOT NULL,
    effective_at timestamp with time zone NOT NULL,
    effective_time_basis text DEFAULT 'recorded_at'::text CONSTRAINT relationship_conflict_resolution__effective_time_basis_not_null NOT NULL,
    status text DEFAULT 'resolution_pending'::text NOT NULL,
    failure_reason text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    applied_at timestamp with time zone,
    CONSTRAINT relationship_conflict_resolution_plans_effective_basis_check CHECK ((effective_time_basis = ANY (ARRAY['valid_from'::text, 'recorded_at'::text]))),
    CONSTRAINT relationship_conflict_resolution_plans_method_check CHECK ((method = ANY (ARRAY['ai'::text, 'last_write_wins'::text]))),
    CONSTRAINT relationship_conflict_resolution_plans_status_check CHECK ((status = ANY (ARRAY['resolution_pending'::text, 'applied'::text, 'superseded'::text, 'failed'::text]))),
    CONSTRAINT relationship_conflict_resolution_plans_version_check CHECK ((expected_case_version >= 1))
);

ALTER TABLE ONLY public.relationship_conflict_resolution_plans FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_review_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_review_runs (
    team_id uuid NOT NULL,
    review_run_id uuid DEFAULT gen_random_uuid() NOT NULL,
    local_run_date date NOT NULL,
    policy_version text DEFAULT 'cross_profile_conflict_v1'::text NOT NULL,
    status text DEFAULT 'reserved'::text NOT NULL,
    worker_id text DEFAULT ''::text NOT NULL,
    timezone text DEFAULT 'Local'::text NOT NULL,
    lease_until timestamp with time zone,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    claimed_cases integer DEFAULT 0 NOT NULL,
    resolved_cases integer DEFAULT 0 NOT NULL,
    overdue_cases integer DEFAULT 0 NOT NULL,
    no_op_cases integer DEFAULT 0 NOT NULL,
    failed_cases integer DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_conflict_review_runs_counts_check CHECK (((claimed_cases >= 0) AND (resolved_cases >= 0) AND (overdue_cases >= 0) AND (no_op_cases >= 0) AND (failed_cases >= 0))),
    CONSTRAINT relationship_conflict_review_runs_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_conflict_review_runs_status_check CHECK ((status = ANY (ARRAY['reserved'::text, 'running'::text, 'completed'::text, 'failed'::text])))
);

ALTER TABLE ONLY public.relationship_conflict_review_runs FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_correction_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_correction_events (
    team_id uuid NOT NULL,
    correction_id uuid DEFAULT gen_random_uuid() NOT NULL,
    submission_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    original_relationship_id uuid CONSTRAINT relationship_correction_event_original_relationship_id_not_null NOT NULL,
    original_relationship_version integer CONSTRAINT relationship_correction_eve_original_relationship_vers_not_null NOT NULL,
    successor_relationship_id uuid CONSTRAINT relationship_correction_even_successor_relationship_id_not_null NOT NULL,
    successor_relationship_version integer CONSTRAINT relationship_correction_eve_successor_relationship_ver_not_null NOT NULL,
    reused_successor boolean DEFAULT false NOT NULL,
    patch jsonb DEFAULT '{}'::jsonb NOT NULL,
    supports jsonb DEFAULT '[]'::jsonb NOT NULL,
    reason text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_correction_events_patch_check CHECK ((jsonb_typeof(patch) = 'object'::text)),
    CONSTRAINT relationship_correction_events_reason_check CHECK (((btrim(reason) <> ''::text) AND (char_length(reason) <= 1000))),
    CONSTRAINT relationship_correction_events_supports_check CHECK ((jsonb_typeof(supports) = 'array'::text)),
    CONSTRAINT relationship_correction_events_version_check CHECK (((original_relationship_version >= 1) AND (successor_relationship_version >= 1)))
);

ALTER TABLE ONLY public.relationship_correction_events FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_correction_submissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_correction_submissions (
    team_id uuid NOT NULL,
    submission_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    relationship_id uuid NOT NULL,
    expected_version integer NOT NULL,
    request_hash text NOT NULL,
    patch jsonb DEFAULT '{}'::jsonb NOT NULL,
    supports jsonb DEFAULT '[]'::jsonb NOT NULL,
    reason text NOT NULL,
    idempotency_key text NOT NULL,
    confirmation_idempotency_key text DEFAULT ''::text CONSTRAINT relationship_correction_sub_confirmation_idempotency_k_not_null NOT NULL,
    confirmation_request_hash text DEFAULT ''::text CONSTRAINT relationship_correction_subm_confirmation_request_hash_not_null NOT NULL,
    processing_state text NOT NULL,
    confirmation_round integer DEFAULT 0 NOT NULL,
    confirmation_token text DEFAULT ''::text NOT NULL,
    confirmation_expires_at timestamp with time zone,
    candidates jsonb DEFAULT '[]'::jsonb NOT NULL,
    selection jsonb DEFAULT '{}'::jsonb NOT NULL,
    successor_relationship_id uuid,
    reused_successor boolean DEFAULT false NOT NULL,
    error_code text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT relationship_correction_submissions_candidates_check CHECK ((jsonb_typeof(candidates) = 'array'::text)),
    CONSTRAINT relationship_correction_submissions_confirmation_idempotency_ch CHECK ((((confirmation_idempotency_key = ''::text) AND (confirmation_request_hash = ''::text)) OR ((btrim(confirmation_idempotency_key) <> ''::text) AND (btrim(confirmation_request_hash) <> ''::text)))),
    CONSTRAINT relationship_correction_submissions_confirmation_round_check CHECK (((confirmation_round >= 0) AND (confirmation_round <= 1))),
    CONSTRAINT relationship_correction_submissions_confirmation_state_check CHECK ((((processing_state = 'awaiting_confirmation'::text) AND (confirmation_round = 0) AND (btrim(confirmation_token) <> ''::text) AND (confirmation_expires_at IS NOT NULL) AND (jsonb_array_length(candidates) > 1)) OR (processing_state <> 'awaiting_confirmation'::text))),
    CONSTRAINT relationship_correction_submissions_expected_version_check CHECK ((expected_version >= 1)),
    CONSTRAINT relationship_correction_submissions_idempotency_check CHECK ((btrim(idempotency_key) <> ''::text)),
    CONSTRAINT relationship_correction_submissions_patch_check CHECK ((jsonb_typeof(patch) = 'object'::text)),
    CONSTRAINT relationship_correction_submissions_reason_check CHECK (((btrim(reason) <> ''::text) AND (char_length(reason) <= 1000))),
    CONSTRAINT relationship_correction_submissions_request_hash_check CHECK ((btrim(request_hash) <> ''::text)),
    CONSTRAINT relationship_correction_submissions_result_check CHECK ((((processing_state = 'completed'::text) AND (successor_relationship_id IS NOT NULL)) OR (processing_state <> 'completed'::text))),
    CONSTRAINT relationship_correction_submissions_selection_check CHECK ((jsonb_typeof(selection) = 'object'::text)),
    CONSTRAINT relationship_correction_submissions_state_check CHECK ((processing_state = ANY (ARRAY['processing'::text, 'awaiting_confirmation'::text, 'completed'::text, 'rejected'::text, 'failed'::text]))),
    CONSTRAINT relationship_correction_submissions_supports_check CHECK ((jsonb_typeof(supports) = 'array'::text)),
    CONSTRAINT relationship_correction_submissions_terminal_state_check CHECK ((((processing_state = ANY (ARRAY['completed'::text, 'rejected'::text, 'failed'::text])) AND (completed_at IS NOT NULL)) OR (processing_state = ANY (ARRAY['processing'::text, 'awaiting_confirmation'::text]))))
);

ALTER TABLE ONLY public.relationship_correction_submissions FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_cross_references; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_cross_references (
    team_id uuid NOT NULL,
    cross_reference_id uuid DEFAULT gen_random_uuid() NOT NULL,
    author_profile_id uuid NOT NULL,
    source_relationship_id uuid NOT NULL,
    source_relationship_version integer CONSTRAINT relationship_cross_referenc_source_relationship_versio_not_null NOT NULL,
    target_relationship_id uuid NOT NULL,
    target_relationship_version integer CONSTRAINT relationship_cross_referenc_target_relationship_versio_not_null NOT NULL,
    kind text NOT NULL,
    verification_event_id uuid NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_cross_references_kind_check CHECK ((kind = ANY (ARRAY['confirms'::text, 'challenges'::text, 'corrects'::text, 'adopts_evidence_from'::text]))),
    CONSTRAINT relationship_cross_references_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_cross_references_version_check CHECK (((source_relationship_version >= 1) AND (target_relationship_version >= 1)))
);

ALTER TABLE ONLY public.relationship_cross_references FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_evidence_supports; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_evidence_supports (
    team_id uuid NOT NULL,
    support_id uuid DEFAULT gen_random_uuid() NOT NULL,
    relationship_id uuid NOT NULL,
    observation_id uuid NOT NULL,
    verification_event_id uuid NOT NULL,
    fragment_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    source_group_key text NOT NULL,
    source_id uuid,
    source_revision_id uuid,
    span_start integer NOT NULL,
    span_end integer NOT NULL,
    quote text DEFAULT ''::text NOT NULL,
    authority text DEFAULT 'primary'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_supports_authority_check CHECK ((authority = ANY (ARRAY['authoritative'::text, 'primary'::text, 'secondary'::text, 'inferred'::text, 'unknown'::text]))),
    CONSTRAINT relationship_supports_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_supports_source_group_nonempty CHECK ((btrim(source_group_key) <> ''::text)),
    CONSTRAINT relationship_supports_source_revision_pair_check CHECK ((((source_id IS NULL) AND (source_revision_id IS NULL)) OR ((source_id IS NOT NULL) AND (source_revision_id IS NOT NULL)))),
    CONSTRAINT relationship_supports_span_check CHECK (((span_start >= 0) AND (span_end > span_start)))
);

ALTER TABLE ONLY public.relationship_evidence_supports FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_observations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_observations (
    team_id uuid NOT NULL,
    observation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    relationship_id uuid,
    ingest_id uuid NOT NULL,
    placement_item_id uuid,
    owner_profile_id uuid NOT NULL,
    subject_ref text NOT NULL,
    original_predicate text NOT NULL,
    object_ref text NOT NULL,
    subject_entity_id uuid,
    predicate_key text,
    predicate_version integer,
    object_entity_id uuid,
    object_value_id uuid,
    polarity text DEFAULT '+'::text NOT NULL,
    scope_key text,
    valid_from timestamp with time zone,
    valid_to timestamp with time zone,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_observations_evidence_array_check CHECK ((jsonb_typeof(evidence) = 'array'::text)),
    CONSTRAINT relationship_observations_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_observations_object_check CHECK ((NOT ((object_entity_id IS NOT NULL) AND (object_value_id IS NOT NULL)))),
    CONSTRAINT relationship_observations_object_ref_nonempty CHECK ((btrim(object_ref) <> ''::text)),
    CONSTRAINT relationship_observations_polarity_check CHECK ((polarity = ANY (ARRAY['+'::text, '-'::text]))),
    CONSTRAINT relationship_observations_predicate_nonempty CHECK ((btrim(original_predicate) <> ''::text)),
    CONSTRAINT relationship_observations_subject_ref_nonempty CHECK ((btrim(subject_ref) <> ''::text)),
    CONSTRAINT relationship_observations_valid_window_check CHECK (((valid_to IS NULL) OR (valid_from IS NULL) OR (valid_to >= valid_from)))
);

ALTER TABLE ONLY public.relationship_observations FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_records (
    team_id uuid NOT NULL,
    relationship_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    semantic_group_key text NOT NULL,
    subject_entity_id uuid NOT NULL,
    predicate_key text NOT NULL,
    predicate_version integer NOT NULL,
    object_entity_id uuid,
    object_value_id uuid,
    relationship_kind text NOT NULL,
    current_cardinality text NOT NULL,
    status text NOT NULL,
    polarity text DEFAULT '+'::text NOT NULL,
    scope_key text,
    valid_from timestamp with time zone,
    valid_to timestamp with time zone,
    support_count integer DEFAULT 0 NOT NULL,
    source_group_count integer DEFAULT 0 NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    recorded_to timestamp with time zone,
    identity_alias_of_relationship_id uuid,
    CONSTRAINT relationship_records_cardinality_check CHECK ((current_cardinality = ANY (ARRAY['one'::text, 'many'::text]))),
    CONSTRAINT relationship_records_counts_check CHECK (((support_count >= 0) AND (source_group_count >= 0))),
    CONSTRAINT relationship_records_identity_alias_not_self_check CHECK (((identity_alias_of_relationship_id IS NULL) OR (identity_alias_of_relationship_id <> relationship_id))),
    CONSTRAINT relationship_records_kind_check CHECK ((relationship_kind = ANY (ARRAY['state'::text, 'event'::text]))),
    CONSTRAINT relationship_records_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_records_object_check CHECK (((object_entity_id IS NULL) <> (object_value_id IS NULL))),
    CONSTRAINT relationship_records_polarity_check CHECK ((polarity = ANY (ARRAY['+'::text, '-'::text]))),
    CONSTRAINT relationship_records_semantic_group_nonempty CHECK ((btrim(semantic_group_key) <> ''::text)),
    CONSTRAINT relationship_records_status_check CHECK ((status = ANY (ARRAY['pending_evidence'::text, 'active'::text, 'needs_review'::text, 'quarantined'::text, 'superseded'::text, 'disputed'::text, 'retracted'::text, 'rejected'::text]))),
    CONSTRAINT relationship_records_valid_window_check CHECK (((valid_to IS NULL) OR (valid_from IS NULL) OR (valid_to >= valid_from))),
    CONSTRAINT relationship_records_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.relationship_records FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_support_decision_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_support_decision_events (
    team_id uuid NOT NULL,
    support_decision_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_support_decision_even_support_decision_id_not_null NOT NULL,
    support_id uuid NOT NULL,
    relationship_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    actor_profile_id uuid,
    decision text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    CONSTRAINT relationship_support_decisions_decision_check CHECK ((decision = ANY (ARRAY['grant'::text, 'revoke'::text, 'reinstate'::text]))),
    CONSTRAINT relationship_support_decisions_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.relationship_support_decision_events FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_transition_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_transition_events (
    team_id uuid NOT NULL,
    transition_id uuid DEFAULT gen_random_uuid() NOT NULL,
    relationship_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    from_status text,
    to_status text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    verification_event_id uuid,
    support_decision_id uuid,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    CONSTRAINT relationship_transitions_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_transitions_status_check CHECK ((((from_status IS NULL) OR (from_status = ANY (ARRAY['pending_evidence'::text, 'active'::text, 'needs_review'::text, 'quarantined'::text, 'superseded'::text, 'disputed'::text, 'retracted'::text, 'rejected'::text]))) AND (to_status = ANY (ARRAY['pending_evidence'::text, 'active'::text, 'needs_review'::text, 'quarantined'::text, 'superseded'::text, 'disputed'::text, 'retracted'::text, 'rejected'::text]))))
);

ALTER TABLE ONLY public.relationship_transition_events FORCE ROW LEVEL SECURITY;


--
-- Name: review_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.review_tasks (
    team_id uuid NOT NULL,
    review_task_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    ingest_id uuid,
    placement_item_id uuid,
    relationship_id uuid,
    observation_id uuid,
    task_type text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    resolution jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    dedupe_key text DEFAULT ''::text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    expires_at timestamp with time zone,
    assessment_id uuid,
    CONSTRAINT review_tasks_payload_object_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT review_tasks_resolution_object_check CHECK ((jsonb_typeof(resolution) = 'object'::text)),
    CONSTRAINT review_tasks_status_check CHECK ((status = ANY (ARRAY['open'::text, 'acknowledged'::text, 'resolved'::text, 'canceled'::text, 'expired'::text]))),
    CONSTRAINT review_tasks_type_check CHECK ((task_type = ANY (ARRAY['identity_needs_review'::text, 'predicate_needs_review'::text, 'relationship_needs_review'::text, 'correction_needs_review'::text, 'policy_needs_review'::text]))),
    CONSTRAINT review_tasks_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.review_tasks FORCE ROW LEVEL SECURITY;


--
-- Name: search_documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.search_documents (
    team_id uuid NOT NULL,
    search_document_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    source_kind text NOT NULL,
    source_id uuid NOT NULL,
    source_version bigint NOT NULL,
    document_version bigint DEFAULT 1 NOT NULL,
    embedding_contract_id uuid NOT NULL,
    embedding_dimensions integer NOT NULL,
    search_state text DEFAULT 'pending'::text NOT NULL,
    document_text text NOT NULL,
    document_hash text NOT NULL,
    search_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, document_text)) STORED,
    embedding public.vector,
    embedding_updated_at timestamp with time zone,
    embedding_error text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    projection_format_version integer DEFAULT 1 NOT NULL,
    projection_generation_id uuid,
    CONSTRAINT search_documents_document_version_check CHECK ((document_version >= 1)),
    CONSTRAINT search_documents_embedding_dims_check CHECK (((embedding IS NULL) OR (public.vector_dims(embedding) = embedding_dimensions))),
    CONSTRAINT search_documents_hash_nonempty CHECK ((btrim(document_hash) <> ''::text)),
    CONSTRAINT search_documents_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT search_documents_projection_format_check CHECK ((projection_format_version >= 1)),
    CONSTRAINT search_documents_source_kind_check CHECK ((source_kind = ANY (ARRAY['evidence'::text, 'relationship'::text, 'entity'::text]))),
    CONSTRAINT search_documents_source_version_check CHECK ((source_version >= 1)),
    CONSTRAINT search_documents_state_check CHECK ((search_state = ANY (ARRAY['not_required'::text, 'pending'::text, 'current'::text, 'failed'::text]))),
    CONSTRAINT search_documents_text_nonempty CHECK ((btrim(document_text) <> ''::text))
);

ALTER TABLE ONLY public.search_documents FORCE ROW LEVEL SECURITY;


--
-- Name: search_index_generations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.search_index_generations (
    search_index_generation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    generation integer NOT NULL,
    embedding_contract_id uuid NOT NULL,
    embedding_dimensions integer NOT NULL,
    ann_strategy text DEFAULT 'exact'::text NOT NULL,
    operator_class text DEFAULT ''::text NOT NULL,
    indexed_expression text DEFAULT ''::text NOT NULL,
    physical_index_name text DEFAULT ''::text NOT NULL,
    hnsw_m integer DEFAULT 16 NOT NULL,
    hnsw_ef_construction integer DEFAULT 64 NOT NULL,
    query_ef_search integer DEFAULT 40 NOT NULL,
    exact_max_rows integer DEFAULT 10000 NOT NULL,
    candidate_limit integer DEFAULT 200 NOT NULL,
    allow_exact_fallback boolean DEFAULT false NOT NULL,
    activation_state text DEFAULT 'building'::text NOT NULL,
    failure_reason text DEFAULT ''::text NOT NULL,
    activated_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT search_index_generations_active_time_check CHECK ((((activation_state = 'active'::text) AND (activated_at IS NOT NULL)) OR (activation_state <> 'active'::text))),
    CONSTRAINT search_index_generations_dimension_strategy_check CHECK (((ann_strategy = 'exact'::text) OR ((ann_strategy = 'vector_hnsw'::text) AND ((embedding_dimensions >= 1) AND (embedding_dimensions <= 2000))) OR ((ann_strategy = 'halfvec_hnsw'::text) AND ((embedding_dimensions >= 1) AND (embedding_dimensions <= 4000))) OR ((ann_strategy = 'binary_hnsw'::text) AND ((embedding_dimensions >= 1) AND (embedding_dimensions <= 16000))))),
    CONSTRAINT search_index_generations_generation_check CHECK ((generation >= 1)),
    CONSTRAINT search_index_generations_hnsw_index_check CHECK (((ann_strategy = 'exact'::text) OR ((btrim(physical_index_name) <> ''::text) AND (btrim(operator_class) <> ''::text) AND (btrim(indexed_expression) <> ''::text)))),
    CONSTRAINT search_index_generations_hnsw_positive CHECK (((hnsw_m > 0) AND (hnsw_ef_construction > 0) AND (query_ef_search > 0) AND (exact_max_rows > 0) AND (candidate_limit > 0))),
    CONSTRAINT search_index_generations_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT search_index_generations_operator_class_check CHECK (((ann_strategy = 'exact'::text) OR ((ann_strategy = 'vector_hnsw'::text) AND (operator_class = 'vector_cosine_ops'::text)) OR ((ann_strategy = 'halfvec_hnsw'::text) AND (operator_class = 'halfvec_cosine_ops'::text)) OR ((ann_strategy = 'binary_hnsw'::text) AND (operator_class = 'bit_hamming_ops'::text)))),
    CONSTRAINT search_index_generations_state_check CHECK ((activation_state = ANY (ARRAY['building'::text, 'active'::text, 'failed'::text, 'deprecated'::text, 'retired'::text]))),
    CONSTRAINT search_index_generations_strategy_check CHECK ((ann_strategy = ANY (ARRAY['exact'::text, 'vector_hnsw'::text, 'halfvec_hnsw'::text, 'binary_hnsw'::text])))
);


--
-- Name: search_projection_generations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.search_projection_generations (
    team_id uuid NOT NULL,
    projection_generation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    source_kind text NOT NULL,
    generation integer NOT NULL,
    projection_format_version integer CONSTRAINT search_projection_generation_projection_format_version_not_null NOT NULL,
    state text DEFAULT 'projecting_text'::text NOT NULL,
    eligible_count bigint DEFAULT 0 NOT NULL,
    projected_count bigint DEFAULT 0 NOT NULL,
    current_vector_count bigint DEFAULT 0 NOT NULL,
    failed_job_count bigint DEFAULT 0 NOT NULL,
    last_projected_source_id uuid,
    last_error text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    activated_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT search_projection_generations_counts_check CHECK (((eligible_count >= 0) AND (projected_count >= 0) AND (current_vector_count >= 0) AND (failed_job_count >= 0))),
    CONSTRAINT search_projection_generations_current_time_check CHECK ((((state = 'current'::text) AND (activated_at IS NOT NULL)) OR (state <> 'current'::text))),
    CONSTRAINT search_projection_generations_format_check CHECK ((projection_format_version >= 1)),
    CONSTRAINT search_projection_generations_generation_check CHECK ((generation >= 1)),
    CONSTRAINT search_projection_generations_source_kind_check CHECK ((source_kind = ANY (ARRAY['relationship'::text, 'evidence'::text, 'entity'::text]))),
    CONSTRAINT search_projection_generations_state_check CHECK ((state = ANY (ARRAY['projecting_text'::text, 'embedding'::text, 'current'::text, 'failed'::text])))
);

ALTER TABLE ONLY public.search_projection_generations FORCE ROW LEVEL SECURITY;


--
-- Name: security_ip_bans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.security_ip_bans (
    ip text NOT NULL,
    reason text NOT NULL,
    source character varying(16) NOT NULL,
    failure_count integer DEFAULT 0 NOT NULL,
    banned_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone,
    last_failed_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT security_ip_bans_failure_count_check CHECK ((failure_count >= 0)),
    CONSTRAINT security_ip_bans_source_check CHECK (((source)::text = ANY (ARRAY[('auto'::character varying)::text, ('manual'::character varying)::text])))
);


--
-- Name: security_ip_failures; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.security_ip_failures (
    ip text NOT NULL,
    failure_count integer DEFAULT 0 NOT NULL,
    first_failed_at timestamp with time zone NOT NULL,
    last_failed_at timestamp with time zone NOT NULL,
    last_reason text DEFAULT ''::text NOT NULL,
    last_surface text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT security_ip_failures_failure_count_check CHECK ((failure_count >= 0))
);


--
-- Name: security_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.security_settings (
    id boolean DEFAULT true NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    failure_threshold integer DEFAULT 10 NOT NULL,
    failure_window_seconds integer DEFAULT 600 NOT NULL,
    ban_duration_seconds integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT security_settings_ban_duration_seconds_check CHECK ((ban_duration_seconds >= 0)),
    CONSTRAINT security_settings_failure_threshold_check CHECK ((failure_threshold > 0)),
    CONSTRAINT security_settings_failure_window_seconds_check CHECK ((failure_window_seconds > 0)),
    CONSTRAINT security_settings_id_check CHECK (id)
);


--
-- Name: semantic_edges; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.semantic_edges WITH (security_invoker='true') AS
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
    support_count,
    source_group_count,
    version
   FROM public.relationship_records
  WHERE ((identity_alias_of_relationship_id IS NULL) AND (status = 'active'::text) AND (support_count > 0));


--
-- Name: semantic_profile_refs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.semantic_profile_refs (
    team_id uuid NOT NULL,
    profile_id uuid NOT NULL
);

ALTER TABLE ONLY public.semantic_profile_refs FORCE ROW LEVEL SECURITY;


--
-- Name: semantic_team_refs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.semantic_team_refs (
    team_id uuid NOT NULL
);

ALTER TABLE ONLY public.semantic_team_refs FORCE ROW LEVEL SECURITY;


--
-- Name: sso_control_admin_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_control_admin_groups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    group_id text NOT NULL,
    group_name text DEFAULT ''::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    retired_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_control_admin_groups_group_id_check CHECK ((char_length(group_id) <= 512)),
    CONSTRAINT sso_control_admin_groups_group_name_check CHECK ((char_length(group_name) <= 512))
);

ALTER TABLE ONLY public.sso_control_admin_groups FORCE ROW LEVEL SECURITY;


--
-- Name: sso_control_oauth_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_control_oauth_states (
    state_hash text NOT NULL,
    provider_id uuid NOT NULL,
    pkce_verifier text NOT NULL,
    nonce text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_control_oauth_states_nonce_check CHECK ((char_length(nonce) <= 256)),
    CONSTRAINT sso_control_oauth_states_pkce_verifier_check CHECK ((char_length(pkce_verifier) <= 256)),
    CONSTRAINT sso_control_oauth_states_state_hash_check CHECK ((char_length(state_hash) = 64))
);

ALTER TABLE ONLY public.sso_control_oauth_states FORCE ROW LEVEL SECURITY;


--
-- Name: sso_control_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_control_sessions (
    session_hash text NOT NULL,
    identity_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    group_ids text[] DEFAULT ARRAY[]::text[] NOT NULL,
    csrf_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_control_sessions_csrf_hash_check CHECK ((char_length(csrf_hash) = 64)),
    CONSTRAINT sso_control_sessions_session_hash_check CHECK ((char_length(session_hash) = 64))
);

ALTER TABLE ONLY public.sso_control_sessions FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_connectors; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_connectors (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    status text DEFAULT 'disabled'::text NOT NULL,
    group_pattern text NOT NULL,
    role_entitlements jsonb DEFAULT '{}'::jsonb NOT NULL,
    max_auto_teams integer DEFAULT 100 NOT NULL,
    credential_version integer DEFAULT 1 NOT NULL,
    bearer_token_hash text DEFAULT ''::text NOT NULL,
    oauth_client_id text DEFAULT ''::text NOT NULL,
    oauth_client_secret_hash text DEFAULT ''::text NOT NULL,
    last_activation_at timestamp with time zone,
    reconcile_version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_connectors_bearer_token_hash_check CHECK ((char_length(bearer_token_hash) <= 512)),
    CONSTRAINT sso_directory_connectors_credential_version_check CHECK ((credential_version >= 1)),
    CONSTRAINT sso_directory_connectors_group_pattern_check CHECK ((char_length(group_pattern) <= 1024)),
    CONSTRAINT sso_directory_connectors_max_auto_teams_check CHECK (((max_auto_teams >= 1) AND (max_auto_teams <= 1000))),
    CONSTRAINT sso_directory_connectors_oauth_client_id_check CHECK ((char_length(oauth_client_id) <= 128)),
    CONSTRAINT sso_directory_connectors_oauth_client_secret_hash_check CHECK ((char_length(oauth_client_secret_hash) <= 512)),
    CONSTRAINT sso_directory_connectors_reconcile_version_check CHECK ((reconcile_version >= 1)),
    CONSTRAINT sso_directory_connectors_role_entitlements_check CHECK ((jsonb_typeof(role_entitlements) = 'object'::text)),
    CONSTRAINT sso_directory_connectors_status_check CHECK ((status = ANY (ARRAY['disabled'::text, 'observe'::text, 'active'::text])))
);

ALTER TABLE ONLY public.sso_directory_connectors FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_group_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_group_bindings (
    connector_id uuid NOT NULL,
    group_id uuid NOT NULL,
    team_id uuid NOT NULL,
    origin text NOT NULL,
    scopes text[] DEFAULT ARRAY['read'::text] NOT NULL,
    role character varying(20) DEFAULT 'member'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_group_bindings_origin_check CHECK ((origin = ANY (ARRAY['directory_created'::text, 'exact_name'::text, 'adopted'::text]))),
    CONSTRAINT sso_directory_group_bindings_role_check CHECK (((role)::text = ANY (ARRAY[('manager'::character varying)::text, ('member'::character varying)::text])))
);

ALTER TABLE ONLY public.sso_directory_group_bindings FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_group_memberships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_group_memberships (
    connector_id uuid NOT NULL,
    group_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.sso_directory_group_memberships FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_groups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    connector_id uuid NOT NULL,
    external_id text DEFAULT ''::text NOT NULL,
    display_name text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_groups_display_name_check CHECK ((btrim(display_name) <> ''::text)),
    CONSTRAINT sso_directory_groups_display_name_check1 CHECK ((char_length(display_name) <= 512)),
    CONSTRAINT sso_directory_groups_external_id_check CHECK ((char_length(external_id) <= 512))
);

ALTER TABLE ONLY public.sso_directory_groups FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_issues; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_issues (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    connector_id uuid NOT NULL,
    group_id uuid,
    issue_key text NOT NULL,
    kind text NOT NULL,
    detail text DEFAULT ''::text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_issues_detail_check CHECK ((char_length(detail) <= 1024)),
    CONSTRAINT sso_directory_issues_issue_key_check CHECK ((char_length(issue_key) <= 256)),
    CONSTRAINT sso_directory_issues_kind_check CHECK ((kind = ANY (ARRAY['invalid_group'::text, 'ambiguous_group'::text, 'team_collision'::text, 'auto_team_capacity'::text])))
);

ALTER TABLE ONLY public.sso_directory_issues FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_oauth_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_oauth_tokens (
    token_hash text NOT NULL,
    connector_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_oauth_tokens_token_hash_check CHECK ((char_length(token_hash) = 64))
);

ALTER TABLE ONLY public.sso_directory_oauth_tokens FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    connector_id uuid NOT NULL,
    external_id text DEFAULT ''::text NOT NULL,
    user_name text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    identity_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_users_display_name_check CHECK ((char_length(display_name) <= 512)),
    CONSTRAINT sso_directory_users_email_check CHECK ((char_length(email) <= 512)),
    CONSTRAINT sso_directory_users_external_id_check CHECK ((char_length(external_id) <= 512)),
    CONSTRAINT sso_directory_users_user_name_check CHECK ((btrim(user_name) <> ''::text)),
    CONSTRAINT sso_directory_users_user_name_check1 CHECK ((char_length(user_name) <= 512))
);

ALTER TABLE ONLY public.sso_directory_users FORCE ROW LEVEL SECURITY;


--
-- Name: sso_entitlement_cache; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_entitlement_cache (
    provider_id uuid NOT NULL,
    subject text NOT NULL,
    groups text[] DEFAULT ARRAY[]::text[] NOT NULL,
    status character varying(20) NOT NULL,
    checked_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    CONSTRAINT sso_entitlement_cache_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('denied'::character varying)::text, ('error'::character varying)::text])))
);

ALTER TABLE ONLY public.sso_entitlement_cache FORCE ROW LEVEL SECURITY;


--
-- Name: sso_group_mappings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_group_mappings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    team_id uuid NOT NULL,
    group_id text NOT NULL,
    group_name text DEFAULT ''::text NOT NULL,
    scopes text[] DEFAULT ARRAY['read'::text] NOT NULL,
    role character varying(20) DEFAULT 'member'::character varying NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    origin text DEFAULT 'manual'::text NOT NULL,
    retired_at timestamp with time zone,
    CONSTRAINT sso_group_mappings_origin_check CHECK ((origin = ANY (ARRAY['manual'::text, 'directory'::text]))),
    CONSTRAINT sso_group_mappings_role_check CHECK (((role)::text = ANY (ARRAY[('manager'::character varying)::text, ('member'::character varying)::text])))
);

ALTER TABLE ONLY public.sso_group_mappings FORCE ROW LEVEL SECURITY;


--
-- Name: sso_identities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_identities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    subject text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    last_login_at timestamp with time zone,
    last_entitlement_check_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    external_id text DEFAULT ''::text NOT NULL,
    active boolean DEFAULT true NOT NULL
);

ALTER TABLE ONLY public.sso_identities FORCE ROW LEVEL SECURITY;


--
-- Name: sso_oauth_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_oauth_states (
    state_hash text NOT NULL,
    provider_id uuid NOT NULL,
    pkce_verifier text NOT NULL,
    nonce text NOT NULL,
    redirect_path text DEFAULT '/ui'::text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.sso_oauth_states FORCE ROW LEVEL SECURITY;


--
-- Name: sso_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(100) NOT NULL,
    kind character varying(32) NOT NULL,
    issuer_url text NOT NULL,
    client_id text NOT NULL,
    client_secret_env text DEFAULT ''::text NOT NULL,
    scopes text[] DEFAULT ARRAY['openid'::text, 'profile'::text, 'email'::text] NOT NULL,
    group_claims text[] DEFAULT ARRAY['groups'::text] NOT NULL,
    groups_endpoint text DEFAULT ''::text NOT NULL,
    groups_scopes text[] DEFAULT ARRAY[]::text[] NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text DEFAULT ''::text NOT NULL,
    identity_claim text DEFAULT ''::text NOT NULL,
    retired_at timestamp with time zone,
    CONSTRAINT sso_providers_kind_check CHECK (((kind)::text = ANY (ARRAY[('azure_ad'::character varying)::text, ('pingone'::character varying)::text, ('generic_oidc'::character varying)::text])))
);

ALTER TABLE ONLY public.sso_providers FORCE ROW LEVEL SECURITY;


--
-- Name: sso_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_sessions (
    session_hash text NOT NULL,
    identity_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    team_profile_id uuid NOT NULL,
    team_id uuid NOT NULL,
    csrf_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.sso_sessions FORCE ROW LEVEL SECURITY;


--
-- Name: submission_holds; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.submission_holds (
    team_id uuid NOT NULL,
    submission_hold_id uuid DEFAULT gen_random_uuid() NOT NULL,
    placement_run_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    assessment_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    reason_code text NOT NULL,
    held_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT submission_holds_expiry_check CHECK ((expires_at = (held_at + '24:00:00'::interval))),
    CONSTRAINT submission_holds_reason_nonempty CHECK ((btrim(reason_code) <> ''::text)),
    CONSTRAINT submission_holds_time_order_check CHECK ((expires_at > held_at))
);

ALTER TABLE ONLY public.submission_holds FORCE ROW LEVEL SECURITY;


--
-- Name: submission_quarantine_payloads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.submission_quarantine_payloads (
    team_id uuid NOT NULL,
    quarantine_payload_id uuid DEFAULT gen_random_uuid() NOT NULL,
    placement_run_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    proposal jsonb DEFAULT '{}'::jsonb NOT NULL,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    assessor_response jsonb DEFAULT '{}'::jsonb NOT NULL,
    payload_sha256 text DEFAULT ''::text NOT NULL,
    quarantined_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT submission_quarantine_payloads_expiry_check CHECK ((expires_at = (quarantined_at + '24:00:00'::interval))),
    CONSTRAINT submission_quarantine_payloads_json_check CHECK (((jsonb_typeof(proposal) = 'object'::text) AND (jsonb_typeof(evidence) = 'array'::text) AND (jsonb_typeof(assessor_response) = 'object'::text)))
);

ALTER TABLE ONLY public.submission_quarantine_payloads FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE submission_quarantine_payloads; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.submission_quarantine_payloads IS 'System/migration-only raw quarantined submission payload copy. Purge after exactly 24 hours; immutable source ledger rows remain for audit and lineage; public reads are forbidden.';


--
-- Name: submission_quarantine_tombstones; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.submission_quarantine_tombstones (
    team_id uuid NOT NULL,
    fragment_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    content_hash text NOT NULL,
    tombstoned_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.submission_quarantine_tombstones FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE submission_quarantine_tombstones; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.submission_quarantine_tombstones IS 'System/migration-only hashes identifying raw quarantine fragments after payload purge; source evidence remains immutable audit history.';


--
-- Name: team_predicate_definitions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_predicate_definitions (
    team_id uuid NOT NULL,
    predicate_key text NOT NULL,
    version integer NOT NULL,
    aliases text[] DEFAULT ARRAY[]::text[] NOT NULL,
    allowed_subject_kinds text[] DEFAULT ARRAY[]::text[] NOT NULL,
    allowed_object_kinds text[] DEFAULT ARRAY[]::text[] NOT NULL,
    relationship_kind text NOT NULL,
    current_cardinality text NOT NULL,
    lifecycle_state text DEFAULT 'active'::text NOT NULL,
    origin text DEFAULT 'provider_generated'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT team_predicate_definitions_cardinality_check CHECK ((current_cardinality = ANY (ARRAY['one'::text, 'many'::text]))),
    CONSTRAINT team_predicate_definitions_key_nonempty CHECK ((btrim(predicate_key) <> ''::text)),
    CONSTRAINT team_predicate_definitions_lifecycle_check CHECK ((lifecycle_state = ANY (ARRAY['active'::text, 'deprecated'::text, 'retired'::text]))),
    CONSTRAINT team_predicate_definitions_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT team_predicate_definitions_origin_nonempty CHECK ((btrim(origin) <> ''::text)),
    CONSTRAINT team_predicate_definitions_relationship_kind_check CHECK ((relationship_kind = ANY (ARRAY['state'::text, 'event'::text]))),
    CONSTRAINT team_predicate_definitions_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.team_predicate_definitions FORCE ROW LEVEL SECURITY;


--
-- Name: team_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_profiles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    team_id uuid NOT NULL,
    key_hash text,
    key_prefix character varying(24),
    key_suffix character varying(6),
    name character varying(100) DEFAULT ''::character varying NOT NULL,
    scopes text[] DEFAULT ARRAY['read'::text, 'write'::text] NOT NULL,
    role character varying(20) DEFAULT 'member'::character varying NOT NULL,
    rate_limit integer DEFAULT 0 NOT NULL,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    auth_source character varying(20) DEFAULT 'api_key'::character varying NOT NULL,
    sso_identity_id uuid,
    sso_provider_id uuid,
    sso_subject text,
    sso_email text DEFAULT ''::text NOT NULL,
    sso_group_id text DEFAULT ''::text NOT NULL,
    sso_entitlement_status character varying(20) DEFAULT 'unlinked'::character varying NOT NULL,
    sso_last_entitlement_checked_at timestamp with time zone,
    sso_last_login_at timestamp with time zone,
    sso_owner_identity_id uuid,
    is_system boolean DEFAULT false NOT NULL,
    CONSTRAINT team_profiles_auth_source_check CHECK (((auth_source)::text = ANY (ARRAY[('api_key'::character varying)::text, ('sso'::character varying)::text, ('system'::character varying)::text]))),
    CONSTRAINT team_profiles_auth_source_shape_check CHECK (((((auth_source)::text = 'api_key'::text) AND (key_hash IS NOT NULL) AND (key_prefix IS NOT NULL) AND (sso_identity_id IS NULL) AND (sso_provider_id IS NULL) AND (NULLIF(sso_subject, ''::text) IS NULL) AND ((sso_entitlement_status)::text = 'unlinked'::text)) OR (((auth_source)::text = 'sso'::text) AND (key_hash IS NULL) AND (key_prefix IS NULL) AND (NULLIF(sso_subject, ''::text) IS NOT NULL) AND ((sso_entitlement_status)::text = ANY (ARRAY[('active'::character varying)::text, ('denied'::character varying)::text, ('error'::character varying)::text])) AND (((sso_identity_id IS NOT NULL) AND (sso_provider_id IS NOT NULL)) OR ((sso_identity_id IS NULL) AND (sso_provider_id IS NULL)))) OR (((auth_source)::text = 'system'::text) AND (key_hash IS NULL) AND (key_prefix IS NULL) AND (sso_identity_id IS NULL) AND (sso_provider_id IS NULL) AND (NULLIF(sso_subject, ''::text) IS NULL) AND ((sso_entitlement_status)::text = 'unlinked'::text) AND (revoked_at IS NOT NULL) AND is_system))),
    CONSTRAINT team_profiles_rate_limit_check CHECK ((rate_limit >= 0)),
    CONSTRAINT team_profiles_role_check CHECK (((role)::text = ANY (ARRAY[('manager'::character varying)::text, ('member'::character varying)::text]))),
    CONSTRAINT team_profiles_sso_entitlement_status_check CHECK (((sso_entitlement_status)::text = ANY (ARRAY[('unlinked'::character varying)::text, ('active'::character varying)::text, ('denied'::character varying)::text, ('error'::character varying)::text]))),
    CONSTRAINT team_profiles_system_marker_check CHECK ((((auth_source)::text = 'system'::text) = is_system))
);

ALTER TABLE ONLY public.team_profiles FORCE ROW LEVEL SECURITY;


--
-- Name: teams; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.teams (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(100) NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    config_version bigint DEFAULT 1 NOT NULL,
    directory_connector_id uuid,
    directory_group_id uuid,
    directory_managed boolean DEFAULT false NOT NULL,
    CONSTRAINT teams_config_version_check CHECK ((config_version >= 1)),
    CONSTRAINT teams_directory_managed_shape_check CHECK ((((NOT directory_managed) AND (directory_connector_id IS NULL) AND (directory_group_id IS NULL)) OR (directory_managed AND (directory_connector_id IS NOT NULL) AND (directory_group_id IS NOT NULL)))),
    CONSTRAINT teams_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('archived'::character varying)::text, ('deleted'::character varying)::text])))
);

ALTER TABLE ONLY public.teams FORCE ROW LEVEL SECURITY;


--
-- Name: telemetry_first_disposition_backfill_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.telemetry_first_disposition_backfill_state (
    state_key text NOT NULL,
    cursor_team_id uuid,
    cursor_ingest_id uuid,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT telemetry_first_disposition_backfill_state_cursor_check CHECK ((((cursor_team_id IS NULL) AND (cursor_ingest_id IS NULL)) OR ((cursor_team_id IS NOT NULL) AND (cursor_ingest_id IS NOT NULL))))
);

ALTER TABLE ONLY public.telemetry_first_disposition_backfill_state FORCE ROW LEVEL SECURITY;


--
-- Name: usage_metric_buckets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_metric_buckets (
    bucket_start timestamp with time zone NOT NULL,
    team_id uuid NOT NULL,
    key_id uuid NOT NULL,
    route text NOT NULL,
    method character varying(16) NOT NULL,
    status_class smallint NOT NULL,
    request_count bigint DEFAULT 0 NOT NULL,
    error_count bigint DEFAULT 0 NOT NULL,
    total_latency_ms bigint DEFAULT 0 NOT NULL,
    max_latency_ms bigint DEFAULT 0 NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT usage_metric_buckets_error_count_check CHECK ((error_count >= 0)),
    CONSTRAINT usage_metric_buckets_max_latency_ms_check CHECK ((max_latency_ms >= 0)),
    CONSTRAINT usage_metric_buckets_request_count_check CHECK ((request_count >= 0)),
    CONSTRAINT usage_metric_buckets_status_class_check CHECK (((status_class >= 1) AND (status_class <= 5))),
    CONSTRAINT usage_metric_buckets_total_latency_ms_check CHECK ((total_latency_ms >= 0))
);

ALTER TABLE ONLY public.usage_metric_buckets FORCE ROW LEVEL SECURITY;


--
-- Name: user_portal_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_portal_sessions (
    session_hash text NOT NULL,
    key_id uuid NOT NULL,
    csrf_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.user_portal_sessions FORCE ROW LEVEL SECURITY;


--
-- Name: v2_compatibility_markers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_compatibility_markers (
    marker_id uuid DEFAULT gen_random_uuid() NOT NULL,
    marker_kind text NOT NULL,
    version text NOT NULL,
    status text NOT NULL,
    run_id uuid,
    corpus_hash text DEFAULT ''::text NOT NULL,
    gate_report_hash text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_compatibility_markers_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT v2_compatibility_markers_status_check CHECK ((status = ANY (ARRAY['compatible'::text, 'incompatible'::text, 'corrupt'::text])))
);

ALTER TABLE ONLY public.v2_compatibility_markers FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_checkpoints; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_checkpoints (
    run_id uuid NOT NULL,
    checkpoint_key text NOT NULL,
    checkpoint_value jsonb DEFAULT '{}'::jsonb NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_checkpoints_json_check CHECK ((jsonb_typeof(checkpoint_value) = 'object'::text))
);

ALTER TABLE ONLY public.v2_migration_checkpoints FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_corpus_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_corpus_items (
    item_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    team_id uuid NOT NULL,
    owner_profile_id uuid,
    source_kind text DEFAULT 'neo4j'::text NOT NULL,
    source_id text NOT NULL,
    source_hash text DEFAULT ''::text NOT NULL,
    item_kind text NOT NULL,
    outcome text DEFAULT 'pending'::text NOT NULL,
    ingest_id uuid,
    placement_item_id uuid,
    exclusion_reason text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_corpus_items_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT v2_migration_corpus_items_outcome_check CHECK ((outcome = ANY (ARRAY['pending'::text, 'accepted'::text, 'needs_review'::text, 'rejected'::text, 'quarantined'::text, 'failed'::text, 'excluded'::text])))
);

ALTER TABLE ONLY public.v2_migration_corpus_items FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_errors; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_errors (
    error_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    source_kind text DEFAULT ''::text NOT NULL,
    source_id text DEFAULT ''::text NOT NULL,
    phase text NOT NULL,
    error_code text NOT NULL,
    message text NOT NULL,
    retryable boolean DEFAULT true NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_errors_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.v2_migration_errors FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_exclusions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_exclusions (
    exclusion_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    source_kind text DEFAULT 'neo4j'::text NOT NULL,
    source_id text NOT NULL,
    reason text NOT NULL,
    blocks_cutover boolean DEFAULT true NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_exclusions_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.v2_migration_exclusions FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_gate_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_gate_results (
    gate_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    gate_name text NOT NULL,
    outcome text NOT NULL,
    evidence_ref text DEFAULT ''::text NOT NULL,
    evidence_hash text DEFAULT ''::text NOT NULL,
    message text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_gate_results_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT v2_migration_gate_results_outcome_check CHECK ((outcome = ANY (ARRAY['pass'::text, 'fail'::text, 'warning'::text])))
);

ALTER TABLE ONLY public.v2_migration_gate_results FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_operator_actions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_operator_actions (
    action_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid,
    action text NOT NULL,
    actor text NOT NULL,
    remote_ip text DEFAULT ''::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_operator_actions_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.v2_migration_operator_actions FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_runs (
    run_id uuid DEFAULT gen_random_uuid() NOT NULL,
    migration_contract_version text NOT NULL,
    corpus_version text DEFAULT ''::text NOT NULL,
    source_kind text DEFAULT 'neo4j'::text NOT NULL,
    state text NOT NULL,
    phase text DEFAULT ''::text NOT NULL,
    required boolean DEFAULT true NOT NULL,
    preflight_approved boolean DEFAULT false NOT NULL,
    backup_reference text DEFAULT ''::text NOT NULL,
    preflight_checks jsonb DEFAULT '{}'::jsonb NOT NULL,
    corpus_watermark text DEFAULT ''::text NOT NULL,
    corpus_hash text DEFAULT ''::text NOT NULL,
    total_items integer DEFAULT 0 NOT NULL,
    completed_items integer DEFAULT 0 NOT NULL,
    failed_items integer DEFAULT 0 NOT NULL,
    excluded_items integer DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    retryable boolean DEFAULT true NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    checkpoint_key text DEFAULT ''::text NOT NULL,
    checkpoint_value jsonb DEFAULT '{}'::jsonb NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    cutover_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    claim_epoch integer DEFAULT 1 NOT NULL,
    CONSTRAINT v2_migration_runs_completed_items_check CHECK ((completed_items >= 0)),
    CONSTRAINT v2_migration_runs_excluded_items_check CHECK ((excluded_items >= 0)),
    CONSTRAINT v2_migration_runs_failed_items_check CHECK ((failed_items >= 0)),
    CONSTRAINT v2_migration_runs_json_check CHECK (((jsonb_typeof(preflight_checks) = 'object'::text) AND (jsonb_typeof(checkpoint_value) = 'object'::text))),
    CONSTRAINT v2_migration_runs_state_check CHECK ((state = ANY (ARRAY['required'::text, 'preflight'::text, 'ready'::text, 'running'::text, 'paused_retryable'::text, 'failed'::text, 'verifying'::text, 'ready_to_cutover'::text, 'cut_over'::text, 'incompatible'::text]))),
    CONSTRAINT v2_migration_runs_total_items_check CHECK ((total_items >= 0))
);

ALTER TABLE ONLY public.v2_migration_runs FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_source_maps; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_source_maps (
    map_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    source_kind text DEFAULT 'neo4j'::text NOT NULL,
    source_id text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_source_maps_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.v2_migration_source_maps FORCE ROW LEVEL SECURITY;


--
-- Name: value_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.value_records (
    team_id uuid NOT NULL,
    value_id uuid DEFAULT gen_random_uuid() NOT NULL,
    value_type text NOT NULL,
    canonical_value text NOT NULL,
    unit text,
    display text DEFAULT ''::text NOT NULL,
    normalization_version integer DEFAULT 1 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT value_records_canonical_nonempty CHECK ((btrim(canonical_value) <> ''::text)),
    CONSTRAINT value_records_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT value_records_normalization_version_check CHECK ((normalization_version >= 1)),
    CONSTRAINT value_records_type_check CHECK ((value_type = ANY (ARRAY['string'::text, 'number'::text, 'boolean'::text, 'date'::text, 'date_time'::text])))
);

ALTER TABLE ONLY public.value_records FORCE ROW LEVEL SECURITY;


--
-- Name: verification_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.verification_events (
    team_id uuid NOT NULL,
    verification_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    observation_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    evidence_verdict text NOT NULL,
    confidence numeric(5,4),
    rationale text DEFAULT ''::text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    response_hash text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    assessment_id uuid,
    assessment_policy_version text,
    threshold_used numeric(12,10),
    gate_result text,
    CONSTRAINT verification_events_confidence_check CHECK (((confidence IS NULL) OR ((confidence >= (0)::numeric) AND (confidence <= (1)::numeric)))),
    CONSTRAINT verification_events_gate_result_check CHECK (((gate_result IS NULL) OR (gate_result = ANY (ARRAY['meets_write_threshold'::text, 'below_write_threshold'::text, 'not_applicable'::text])))),
    CONSTRAINT verification_events_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT verification_events_threshold_used_check CHECK (((threshold_used IS NULL) OR ((threshold_used >= (0)::numeric) AND (threshold_used <= (1)::numeric)))),
    CONSTRAINT verification_events_verdict_check CHECK ((evidence_verdict = ANY (ARRAY['entailed'::text, 'contradicted'::text, 'insufficient'::text])))
);

ALTER TABLE ONLY public.verification_events FORCE ROW LEVEL SECURITY;


--
-- Name: app_config app_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_config
    ADD CONSTRAINT app_config_pkey PRIMARY KEY (key);


--
-- Name: audit_log audit_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_log
    ADD CONSTRAINT audit_log_pkey PRIMARY KEY (id);


--
-- Name: community_memberships community_memberships_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_memberships
    ADD CONSTRAINT community_memberships_pkey PRIMARY KEY (team_id, community_id, entity_id);


--
-- Name: community_records community_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_records
    ADD CONSTRAINT community_records_pkey PRIMARY KEY (team_id, community_id);


--
-- Name: community_snapshot_runs community_snapshot_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_snapshot_runs
    ADD CONSTRAINT community_snapshot_runs_pkey PRIMARY KEY (team_id, run_id);


--
-- Name: community_snapshot_runs community_snapshot_runs_window_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_snapshot_runs
    ADD CONSTRAINT community_snapshot_runs_window_unique UNIQUE (team_id, window_key);


--
-- Name: community_sources community_sources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_sources
    ADD CONSTRAINT community_sources_pkey PRIMARY KEY (team_id, community_id, relationship_id);


--
-- Name: community_summary_attempts community_summary_attempts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_summary_attempts
    ADD CONSTRAINT community_summary_attempts_pkey PRIMARY KEY (team_id, attempt_id);


--
-- Name: dream_cycle_runs dream_cycle_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_pkey PRIMARY KEY (team_id, run_id);


--
-- Name: dream_path_evaluations dream_path_evaluations_exact_path_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_exact_path_unique UNIQUE (team_id, first_relationship_id, first_relationship_version, second_relationship_id, second_relationship_version, allowed_predicate_fingerprint);


--
-- Name: dream_path_evaluations dream_path_evaluations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_pkey PRIMARY KEY (team_id, path_evaluation_id);


--
-- Name: embedding_config embedding_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_config
    ADD CONSTRAINT embedding_config_pkey PRIMARY KEY (id);


--
-- Name: embedding_contracts embedding_contracts_contract_key_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_contracts
    ADD CONSTRAINT embedding_contracts_contract_key_version_key UNIQUE (contract_key, version);


--
-- Name: embedding_contracts embedding_contracts_embedding_contract_id_dimensions_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_contracts
    ADD CONSTRAINT embedding_contracts_embedding_contract_id_dimensions_key UNIQUE (embedding_contract_id, dimensions);


--
-- Name: embedding_contracts embedding_contracts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_contracts
    ADD CONSTRAINT embedding_contracts_pkey PRIMARY KEY (embedding_contract_id);


--
-- Name: embedding_jobs embedding_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_jobs
    ADD CONSTRAINT embedding_jobs_pkey PRIMARY KEY (team_id, embedding_job_id);


--
-- Name: embedding_jobs embedding_jobs_team_id_source_kind_source_id_source_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_jobs
    ADD CONSTRAINT embedding_jobs_team_id_source_kind_source_id_source_version_key UNIQUE (team_id, source_kind, source_id, source_version, document_version, embedding_contract_id);


--
-- Name: entity_correction_events entity_correction_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_events
    ADD CONSTRAINT entity_correction_events_pkey PRIMARY KEY (team_id, correction_event_id);


--
-- Name: entity_correction_plans entity_correction_plans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_pkey PRIMARY KEY (team_id, plan_token);


--
-- Name: entity_names entity_names_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_names
    ADD CONSTRAINT entity_names_pkey PRIMARY KEY (team_id, entity_name_id);


--
-- Name: entity_records entity_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_records
    ADD CONSTRAINT entity_records_pkey PRIMARY KEY (team_id, entity_id);


--
-- Name: entity_resolution_events entity_resolution_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_pkey PRIMARY KEY (team_id, resolution_event_id);


--
-- Name: evidence_fragments evidence_fragments_fragment_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_fragment_owner_ref_unique UNIQUE (team_id, fragment_id, owner_profile_id);


--
-- Name: evidence_fragments evidence_fragments_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_owner_ref_unique UNIQUE (team_id, fragment_id, ingest_id, owner_profile_id);


--
-- Name: evidence_fragments evidence_fragments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_pkey PRIMARY KEY (team_id, fragment_id);


--
-- Name: evidence_fragments evidence_fragments_team_id_ingest_id_evidence_index_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_ingest_id_evidence_index_key UNIQUE (team_id, ingest_id, evidence_index);


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_events
    ADD CONSTRAINT evidence_lifecycle_events_pkey PRIMARY KEY (team_id, lifecycle_event_id);


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_owner_ref_unique UNIQUE (team_id, lifecycle_operation_id, owner_profile_id);


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_pkey PRIMARY KEY (team_id, lifecycle_operation_id);


--
-- Name: evidence_quarantines evidence_quarantines_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_pkey PRIMARY KEY (team_id, quarantine_id);


--
-- Name: evidence_quarantines evidence_quarantines_team_id_fragment_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_fragment_id_key UNIQUE (team_id, fragment_id);


--
-- Name: evidence_security_events evidence_security_events_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_owner_ref_unique UNIQUE (team_id, security_event_id, owner_profile_id);


--
-- Name: evidence_security_events evidence_security_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_pkey PRIMARY KEY (team_id, security_event_id);


--
-- Name: evidence_security_signals evidence_security_signals_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_signals
    ADD CONSTRAINT evidence_security_signals_pkey PRIMARY KEY (team_id, security_event_id, signal_index);


--
-- Name: evidence_source_revisions evidence_source_revisions_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_owner_ref_unique UNIQUE (team_id, source_revision_id, owner_profile_id);


--
-- Name: evidence_source_revisions evidence_source_revisions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_pkey PRIMARY KEY (team_id, source_revision_id);


--
-- Name: evidence_source_revisions evidence_source_revisions_source_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_source_owner_ref_unique UNIQUE (team_id, source_id, source_revision_id, owner_profile_id);


--
-- Name: evidence_sources evidence_sources_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_sources
    ADD CONSTRAINT evidence_sources_owner_ref_unique UNIQUE (team_id, source_id, owner_profile_id);


--
-- Name: evidence_sources evidence_sources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_sources
    ADD CONSTRAINT evidence_sources_pkey PRIMARY KEY (team_id, source_id);


--
-- Name: hypotheses hypotheses_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_pkey PRIMARY KEY (team_id, hypothesis_id);


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_pkey PRIMARY KEY (team_id, derivation_source_id);


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_unique UNIQUE (team_id, hypothesis_id, relationship_id, relationship_version, fragment_id, span_start, span_end);


--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_feedback_events
    ADD CONSTRAINT hypothesis_feedback_events_pkey PRIMARY KEY (team_id, feedback_event_id);


--
-- Name: knowledge_ingests knowledge_ingests_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.knowledge_ingests
    ADD CONSTRAINT knowledge_ingests_owner_ref_unique UNIQUE (team_id, ingest_id, owner_profile_id);


--
-- Name: knowledge_ingests knowledge_ingests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.knowledge_ingests
    ADD CONSTRAINT knowledge_ingests_pkey PRIMARY KEY (team_id, ingest_id);


--
-- Name: operation_logs operation_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operation_logs
    ADD CONSTRAINT operation_logs_pkey PRIMARY KEY (id);


--
-- Name: placement_assessments placement_assessments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_pkey PRIMARY KEY (team_id, assessment_id);


--
-- Name: placement_assessments placement_assessments_team_id_claim_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_team_id_claim_key_key UNIQUE (team_id, claim_key);


--
-- Name: placement_assessments placement_assessments_team_id_placement_item_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_team_id_placement_item_id_key UNIQUE (team_id, placement_item_id);


--
-- Name: placement_items placement_items_claim_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_claim_ref_unique UNIQUE (team_id, placement_item_id, claim_key);


--
-- Name: placement_items placement_items_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_owner_ref_unique UNIQUE (team_id, placement_item_id, owner_profile_id);


--
-- Name: placement_items placement_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_pkey PRIMARY KEY (team_id, placement_item_id);


--
-- Name: placement_items placement_items_run_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_run_owner_ref_unique UNIQUE (team_id, placement_item_id, placement_run_id, owner_profile_id);


--
-- Name: placement_items placement_items_team_id_placement_run_id_evidence_index_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_placement_run_id_evidence_index_key UNIQUE (team_id, placement_run_id, evidence_index);


--
-- Name: placement_outcomes placement_outcomes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_pkey PRIMARY KEY (team_id, outcome_id);


--
-- Name: placement_runs placement_runs_ingest_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_ingest_owner_ref_unique UNIQUE (team_id, placement_run_id, ingest_id, owner_profile_id);


--
-- Name: placement_runs placement_runs_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_owner_ref_unique UNIQUE (team_id, placement_run_id, owner_profile_id);


--
-- Name: placement_runs placement_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_pkey PRIMARY KEY (team_id, placement_run_id);


--
-- Name: placement_runs placement_runs_team_id_ingest_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_team_id_ingest_id_key UNIQUE (team_id, ingest_id);


--
-- Name: predicate_definitions predicate_definitions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_definitions
    ADD CONSTRAINT predicate_definitions_pkey PRIMARY KEY (predicate_key, version);


--
-- Name: predicate_registration_events predicate_registration_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_pkey PRIMARY KEY (team_id, predicate_registration_event_id);


--
-- Name: predicate_registration_events predicate_registration_events_team_id_placement_run_id_rela_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_team_id_placement_run_id_rela_key UNIQUE (team_id, placement_run_id, relationship_ref);


--
-- Name: recall_feedback_events recall_feedback_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recall_feedback_events
    ADD CONSTRAINT recall_feedback_events_pkey PRIMARY KEY (recall_id);


--
-- Name: relationship_conflict_ai_assessment_attempts relationship_conflict_ai_asse_team_id_conflict_id_case_vers_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_attempts
    ADD CONSTRAINT relationship_conflict_ai_asse_team_id_conflict_id_case_vers_key UNIQUE (team_id, conflict_id, case_version, local_assessment_date, model, policy_version);


--
-- Name: relationship_conflict_ai_assessment_attempts relationship_conflict_ai_assessment_attempts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_attempts
    ADD CONSTRAINT relationship_conflict_ai_assessment_attempts_pkey PRIMARY KEY (team_id, assessment_attempt_id);


--
-- Name: relationship_conflict_ai_assessment_events relationship_conflict_ai_assessment_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_events
    ADD CONSTRAINT relationship_conflict_ai_assessment_events_pkey PRIMARY KEY (team_id, assessment_event_id);


--
-- Name: relationship_conflict_cases relationship_conflict_cases_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_cases
    ADD CONSTRAINT relationship_conflict_cases_pkey PRIMARY KEY (team_id, conflict_id);


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_evidence_tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_evidence_tasks_pkey PRIMARY KEY (team_id, derived_evidence_task_id);


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_team_id_conflict_id_target_fr_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_team_id_conflict_id_target_fr_key UNIQUE (team_id, conflict_id, target_fragment_id);


--
-- Name: relationship_conflict_events relationship_conflict_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_pkey PRIMARY KEY (team_id, conflict_event_id);


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidenc_team_id_conflict_id_target_fr_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidenc_team_id_conflict_id_target_fr_key UNIQUE (team_id, conflict_id, target_fragment_id);


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_derivations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidence_derivations_pkey PRIMARY KEY (team_id, derivation_id);


--
-- Name: relationship_conflict_positions relationship_conflict_positio_team_id_conflict_id_position__key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_positions
    ADD CONSTRAINT relationship_conflict_positio_team_id_conflict_id_position__key UNIQUE (team_id, conflict_id, position_key);


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_position_members_pkey PRIMARY KEY (team_id, position_id, relationship_id, source_group_key);


--
-- Name: relationship_conflict_positions relationship_conflict_positions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_positions
    ADD CONSTRAINT relationship_conflict_positions_pkey PRIMARY KEY (team_id, position_id);


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolut_team_id_conflict_id_expected__key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_resolution_plans
    ADD CONSTRAINT relationship_conflict_resolut_team_id_conflict_id_expected__key UNIQUE (team_id, conflict_id, expected_case_version);


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolution_plans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_resolution_plans
    ADD CONSTRAINT relationship_conflict_resolution_plans_pkey PRIMARY KEY (team_id, resolution_plan_id);


--
-- Name: relationship_conflict_review_runs relationship_conflict_review__team_id_local_run_date_policy_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_review_runs
    ADD CONSTRAINT relationship_conflict_review__team_id_local_run_date_policy_key UNIQUE (team_id, local_run_date, policy_version);


--
-- Name: relationship_conflict_review_runs relationship_conflict_review_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_review_runs
    ADD CONSTRAINT relationship_conflict_review_runs_pkey PRIMARY KEY (team_id, review_run_id);


--
-- Name: relationship_correction_events relationship_correction_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_events
    ADD CONSTRAINT relationship_correction_events_pkey PRIMARY KEY (team_id, correction_id);


--
-- Name: relationship_correction_events relationship_correction_events_submission_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_events
    ADD CONSTRAINT relationship_correction_events_submission_unique UNIQUE (team_id, submission_id);


--
-- Name: relationship_correction_submissions relationship_correction_submissions_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_submissions
    ADD CONSTRAINT relationship_correction_submissions_owner_ref_unique UNIQUE (team_id, submission_id, owner_profile_id);


--
-- Name: relationship_correction_submissions relationship_correction_submissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_submissions
    ADD CONSTRAINT relationship_correction_submissions_pkey PRIMARY KEY (team_id, submission_id);


--
-- Name: relationship_cross_references relationship_cross_references_identity_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_identity_unique UNIQUE (team_id, author_profile_id, source_relationship_id, target_relationship_id, kind, verification_event_id);


--
-- Name: relationship_cross_references relationship_cross_references_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_pkey PRIMARY KEY (team_id, cross_reference_id);


--
-- Name: relationship_evidence_supports relationship_evidence_supports_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_pkey PRIMARY KEY (team_id, support_id);


--
-- Name: relationship_observations relationship_observations_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_owner_ref_unique UNIQUE (team_id, observation_id, owner_profile_id);


--
-- Name: relationship_observations relationship_observations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_pkey PRIMARY KEY (team_id, observation_id);


--
-- Name: relationship_records relationship_records_owner_fk_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_owner_fk_unique UNIQUE (team_id, relationship_id, owner_profile_id);


--
-- Name: relationship_records relationship_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_pkey PRIMARY KEY (team_id, relationship_id);


--
-- Name: relationship_support_decision_events relationship_support_decision_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decision_events_pkey PRIMARY KEY (team_id, support_decision_id);


--
-- Name: relationship_support_decision_events relationship_support_decisions_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decisions_owner_ref_unique UNIQUE (team_id, support_decision_id, owner_profile_id);


--
-- Name: relationship_evidence_supports relationship_supports_identity_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_supports_identity_unique UNIQUE (team_id, relationship_id, owner_profile_id, fragment_id, span_start, span_end);


--
-- Name: relationship_evidence_supports relationship_supports_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_supports_owner_ref_unique UNIQUE (team_id, support_id, owner_profile_id);


--
-- Name: relationship_transition_events relationship_transition_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_transition_events
    ADD CONSTRAINT relationship_transition_events_pkey PRIMARY KEY (team_id, transition_id);


--
-- Name: review_tasks review_tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_pkey PRIMARY KEY (team_id, review_task_id);


--
-- Name: search_documents search_documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_documents
    ADD CONSTRAINT search_documents_pkey PRIMARY KEY (team_id, search_document_id);


--
-- Name: search_documents search_documents_team_id_source_kind_source_id_embedding_co_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_documents
    ADD CONSTRAINT search_documents_team_id_source_kind_source_id_embedding_co_key UNIQUE (team_id, source_kind, source_id, embedding_contract_id);


--
-- Name: search_index_generations search_index_generations_embedding_contract_id_generation_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_index_generations
    ADD CONSTRAINT search_index_generations_embedding_contract_id_generation_key UNIQUE (embedding_contract_id, generation);


--
-- Name: search_index_generations search_index_generations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_index_generations
    ADD CONSTRAINT search_index_generations_pkey PRIMARY KEY (search_index_generation_id);


--
-- Name: search_index_generations search_index_generations_search_index_generation_id_embeddi_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_index_generations
    ADD CONSTRAINT search_index_generations_search_index_generation_id_embeddi_key UNIQUE (search_index_generation_id, embedding_contract_id);


--
-- Name: search_projection_generations search_projection_generations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_projection_generations
    ADD CONSTRAINT search_projection_generations_pkey PRIMARY KEY (team_id, projection_generation_id);


--
-- Name: search_projection_generations search_projection_generations_team_id_source_kind_projectio_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_projection_generations
    ADD CONSTRAINT search_projection_generations_team_id_source_kind_projectio_key UNIQUE (team_id, source_kind, projection_format_version, generation);


--
-- Name: security_ip_bans security_ip_bans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.security_ip_bans
    ADD CONSTRAINT security_ip_bans_pkey PRIMARY KEY (ip);


--
-- Name: security_ip_failures security_ip_failures_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.security_ip_failures
    ADD CONSTRAINT security_ip_failures_pkey PRIMARY KEY (ip);


--
-- Name: security_settings security_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.security_settings
    ADD CONSTRAINT security_settings_pkey PRIMARY KEY (id);


--
-- Name: semantic_profile_refs semantic_profile_refs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.semantic_profile_refs
    ADD CONSTRAINT semantic_profile_refs_pkey PRIMARY KEY (team_id, profile_id);


--
-- Name: semantic_team_refs semantic_team_refs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.semantic_team_refs
    ADD CONSTRAINT semantic_team_refs_pkey PRIMARY KEY (team_id);


--
-- Name: sso_control_admin_groups sso_control_admin_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_admin_groups
    ADD CONSTRAINT sso_control_admin_groups_pkey PRIMARY KEY (id);


--
-- Name: sso_control_admin_groups sso_control_admin_groups_provider_id_group_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_admin_groups
    ADD CONSTRAINT sso_control_admin_groups_provider_id_group_id_key UNIQUE (provider_id, group_id);


--
-- Name: sso_control_oauth_states sso_control_oauth_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_oauth_states
    ADD CONSTRAINT sso_control_oauth_states_pkey PRIMARY KEY (state_hash);


--
-- Name: sso_control_sessions sso_control_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_sessions
    ADD CONSTRAINT sso_control_sessions_pkey PRIMARY KEY (session_hash);


--
-- Name: sso_directory_connectors sso_directory_connectors_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_connectors
    ADD CONSTRAINT sso_directory_connectors_pkey PRIMARY KEY (id);


--
-- Name: sso_directory_connectors sso_directory_connectors_provider_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_connectors
    ADD CONSTRAINT sso_directory_connectors_provider_id_key UNIQUE (provider_id);


--
-- Name: sso_directory_group_bindings sso_directory_group_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_bindings
    ADD CONSTRAINT sso_directory_group_bindings_pkey PRIMARY KEY (connector_id, group_id);


--
-- Name: sso_directory_group_memberships sso_directory_group_memberships_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_memberships
    ADD CONSTRAINT sso_directory_group_memberships_pkey PRIMARY KEY (connector_id, group_id, user_id);


--
-- Name: sso_directory_groups sso_directory_groups_connector_id_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_groups
    ADD CONSTRAINT sso_directory_groups_connector_id_id_key UNIQUE (connector_id, id);


--
-- Name: sso_directory_groups sso_directory_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_groups
    ADD CONSTRAINT sso_directory_groups_pkey PRIMARY KEY (id);


--
-- Name: sso_directory_issues sso_directory_issues_connector_id_issue_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_issues
    ADD CONSTRAINT sso_directory_issues_connector_id_issue_key_key UNIQUE (connector_id, issue_key);


--
-- Name: sso_directory_issues sso_directory_issues_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_issues
    ADD CONSTRAINT sso_directory_issues_pkey PRIMARY KEY (id);


--
-- Name: sso_directory_oauth_tokens sso_directory_oauth_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_oauth_tokens
    ADD CONSTRAINT sso_directory_oauth_tokens_pkey PRIMARY KEY (token_hash);


--
-- Name: sso_directory_users sso_directory_users_connector_id_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_users
    ADD CONSTRAINT sso_directory_users_connector_id_id_key UNIQUE (connector_id, id);


--
-- Name: sso_directory_users sso_directory_users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_users
    ADD CONSTRAINT sso_directory_users_pkey PRIMARY KEY (id);


--
-- Name: sso_entitlement_cache sso_entitlement_cache_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_entitlement_cache
    ADD CONSTRAINT sso_entitlement_cache_pkey PRIMARY KEY (provider_id, subject);


--
-- Name: sso_group_mappings sso_group_mappings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_group_mappings
    ADD CONSTRAINT sso_group_mappings_pkey PRIMARY KEY (id);


--
-- Name: sso_group_mappings sso_group_mappings_provider_id_team_id_group_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_group_mappings
    ADD CONSTRAINT sso_group_mappings_provider_id_team_id_group_id_key UNIQUE (provider_id, team_id, group_id);


--
-- Name: sso_identities sso_identities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_identities
    ADD CONSTRAINT sso_identities_pkey PRIMARY KEY (id);


--
-- Name: sso_identities sso_identities_provider_id_subject_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_identities
    ADD CONSTRAINT sso_identities_provider_id_subject_key UNIQUE (provider_id, subject);


--
-- Name: sso_oauth_states sso_oauth_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_oauth_states
    ADD CONSTRAINT sso_oauth_states_pkey PRIMARY KEY (state_hash);


--
-- Name: sso_providers sso_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_providers
    ADD CONSTRAINT sso_providers_pkey PRIMARY KEY (id);


--
-- Name: sso_sessions sso_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_sessions
    ADD CONSTRAINT sso_sessions_pkey PRIMARY KEY (session_hash);


--
-- Name: submission_holds submission_holds_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_pkey PRIMARY KEY (team_id, submission_hold_id);


--
-- Name: submission_holds submission_holds_team_id_assessment_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_team_id_assessment_id_key UNIQUE (team_id, assessment_id);


--
-- Name: submission_holds submission_holds_team_id_placement_run_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_team_id_placement_run_id_key UNIQUE (team_id, placement_run_id);


--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_quarantine_payloads
    ADD CONSTRAINT submission_quarantine_payloads_pkey PRIMARY KEY (team_id, quarantine_payload_id);


--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_team_id_placement_run_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_quarantine_payloads
    ADD CONSTRAINT submission_quarantine_payloads_team_id_placement_run_id_key UNIQUE (team_id, placement_run_id);


--
-- Name: submission_quarantine_tombstones submission_quarantine_tombstones_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_quarantine_tombstones
    ADD CONSTRAINT submission_quarantine_tombstones_pkey PRIMARY KEY (team_id, fragment_id);


--
-- Name: team_predicate_definitions team_predicate_definitions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_predicate_definitions
    ADD CONSTRAINT team_predicate_definitions_pkey PRIMARY KEY (team_id, predicate_key, version);


--
-- Name: team_profiles team_profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_profiles
    ADD CONSTRAINT team_profiles_pkey PRIMARY KEY (id);


--
-- Name: teams teams_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_pkey PRIMARY KEY (id);


--
-- Name: telemetry_first_disposition_backfill_state telemetry_first_disposition_backfill_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.telemetry_first_disposition_backfill_state
    ADD CONSTRAINT telemetry_first_disposition_backfill_state_pkey PRIMARY KEY (state_key);


--
-- Name: usage_metric_buckets usage_metric_buckets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_metric_buckets
    ADD CONSTRAINT usage_metric_buckets_pkey PRIMARY KEY (bucket_start, team_id, key_id, route, method, status_class);


--
-- Name: user_portal_sessions user_portal_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_portal_sessions
    ADD CONSTRAINT user_portal_sessions_pkey PRIMARY KEY (session_hash);


--
-- Name: v2_compatibility_markers v2_compatibility_markers_marker_kind_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_compatibility_markers
    ADD CONSTRAINT v2_compatibility_markers_marker_kind_version_key UNIQUE (marker_kind, version);


--
-- Name: v2_compatibility_markers v2_compatibility_markers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_compatibility_markers
    ADD CONSTRAINT v2_compatibility_markers_pkey PRIMARY KEY (marker_id);


--
-- Name: v2_migration_checkpoints v2_migration_checkpoints_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_checkpoints
    ADD CONSTRAINT v2_migration_checkpoints_pkey PRIMARY KEY (run_id, checkpoint_key);


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_pkey PRIMARY KEY (item_id);


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_run_id_source_kind_source_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_run_id_source_kind_source_id_key UNIQUE (run_id, source_kind, source_id);


--
-- Name: v2_migration_errors v2_migration_errors_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_errors
    ADD CONSTRAINT v2_migration_errors_pkey PRIMARY KEY (error_id);


--
-- Name: v2_migration_exclusions v2_migration_exclusions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_exclusions
    ADD CONSTRAINT v2_migration_exclusions_pkey PRIMARY KEY (exclusion_id);


--
-- Name: v2_migration_exclusions v2_migration_exclusions_run_id_source_kind_source_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_exclusions
    ADD CONSTRAINT v2_migration_exclusions_run_id_source_kind_source_id_key UNIQUE (run_id, source_kind, source_id);


--
-- Name: v2_migration_gate_results v2_migration_gate_results_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_gate_results
    ADD CONSTRAINT v2_migration_gate_results_pkey PRIMARY KEY (gate_id);


--
-- Name: v2_migration_gate_results v2_migration_gate_results_run_id_gate_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_gate_results
    ADD CONSTRAINT v2_migration_gate_results_run_id_gate_name_key UNIQUE (run_id, gate_name);


--
-- Name: v2_migration_operator_actions v2_migration_operator_actions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_operator_actions
    ADD CONSTRAINT v2_migration_operator_actions_pkey PRIMARY KEY (action_id);


--
-- Name: v2_migration_runs v2_migration_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_runs
    ADD CONSTRAINT v2_migration_runs_pkey PRIMARY KEY (run_id);


--
-- Name: v2_migration_source_maps v2_migration_source_maps_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_source_maps
    ADD CONSTRAINT v2_migration_source_maps_pkey PRIMARY KEY (map_id);


--
-- Name: v2_migration_source_maps v2_migration_source_maps_run_id_source_kind_source_id_targe_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_source_maps
    ADD CONSTRAINT v2_migration_source_maps_run_id_source_kind_source_id_targe_key UNIQUE (run_id, source_kind, source_id, target_type, target_id);


--
-- Name: value_records value_records_canonical_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.value_records
    ADD CONSTRAINT value_records_canonical_unique UNIQUE NULLS NOT DISTINCT (team_id, value_type, canonical_value, unit, normalization_version);


--
-- Name: value_records value_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.value_records
    ADD CONSTRAINT value_records_pkey PRIMARY KEY (team_id, value_id);


--
-- Name: verification_events verification_events_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_events
    ADD CONSTRAINT verification_events_owner_ref_unique UNIQUE (team_id, verification_event_id, owner_profile_id);


--
-- Name: verification_events verification_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_events
    ADD CONSTRAINT verification_events_pkey PRIMARY KEY (team_id, verification_event_id);


--
-- Name: community_memberships_entity_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_memberships_entity_idx ON public.community_memberships USING btree (team_id, entity_id, community_id);


--
-- Name: community_records_current_fts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_records_current_fts_idx ON public.community_records USING gin (public.community_record_search_vector(summary, top_entities, top_predicates)) WHERE (status = 'current'::text);


--
-- Name: community_records_current_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_records_current_idx ON public.community_records USING btree (team_id, member_count DESC, community_id) WHERE (status = 'current'::text);


--
-- Name: community_records_current_logical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX community_records_current_logical_unique ON public.community_records USING btree (team_id, logical_community_id) WHERE (status = 'current'::text);


--
-- Name: community_records_run_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_records_run_idx ON public.community_records USING btree (team_id, run_id, ordinal);


--
-- Name: community_snapshot_runs_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_snapshot_runs_status_idx ON public.community_snapshot_runs USING btree (team_id, status, updated_at DESC);


--
-- Name: community_sources_community_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_sources_community_idx ON public.community_sources USING btree (team_id, community_id);


--
-- Name: community_sources_group_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_sources_group_idx ON public.community_sources USING btree (team_id, semantic_group_key, community_id);


--
-- Name: community_sources_relationship_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_sources_relationship_idx ON public.community_sources USING btree (team_id, relationship_id, relationship_version);


--
-- Name: community_summary_attempts_lookup_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_summary_attempts_lookup_idx ON public.community_summary_attempts USING btree (team_id, community_id, created_at DESC);


--
-- Name: dream_cycle_runs_due_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dream_cycle_runs_due_idx ON public.dream_cycle_runs USING btree (team_id, started_at DESC) WHERE (canonical_run_id IS NULL);


--
-- Name: dream_cycle_runs_recovery_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dream_cycle_runs_recovery_idx ON public.dream_cycle_runs USING btree (team_id, lease_until, started_at) WHERE ((canonical_run_id IS NULL) AND (status = 'running'::text));


--
-- Name: dream_cycle_runs_team_window_canonical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX dream_cycle_runs_team_window_canonical_unique ON public.dream_cycle_runs USING btree (team_id, window_key) WHERE (canonical_run_id IS NULL);


--
-- Name: dream_path_evaluations_first_relationship_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dream_path_evaluations_first_relationship_idx ON public.dream_path_evaluations USING btree (team_id, first_relationship_id, first_relationship_version);


--
-- Name: embedding_jobs_contract_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX embedding_jobs_contract_status_idx ON public.embedding_jobs USING btree (embedding_contract_id, embedding_dimensions, status, updated_at DESC);


--
-- Name: embedding_jobs_lease_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX embedding_jobs_lease_idx ON public.embedding_jobs USING btree (team_id, lease_until) WHERE (status = 'processing'::text);


--
-- Name: embedding_jobs_projection_generation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX embedding_jobs_projection_generation_idx ON public.embedding_jobs USING btree (team_id, projection_generation_id, status) WHERE (projection_generation_id IS NOT NULL);


--
-- Name: embedding_jobs_ready_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX embedding_jobs_ready_idx ON public.embedding_jobs USING btree (team_id, available_at, created_at, embedding_job_id) WHERE (status = 'queued'::text);


--
-- Name: entity_correction_plans_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX entity_correction_plans_idempotency_unique ON public.entity_correction_plans USING btree (team_id, owner_profile_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: entity_names_current_canonical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX entity_names_current_canonical_unique ON public.entity_names USING btree (team_id, entity_id) WHERE ((name_kind = 'canonical'::text) AND (valid_to IS NULL));


--
-- Name: entity_names_lookup_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX entity_names_lookup_idx ON public.entity_names USING btree (team_id, normalized_name, name_kind, entity_id);


--
-- Name: entity_records_team_kind_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX entity_records_team_kind_idx ON public.entity_records USING btree (team_id, entity_kind, created_at DESC);


--
-- Name: evidence_fragments_ingest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_fragments_ingest_idx ON public.evidence_fragments USING btree (team_id, ingest_id, evidence_index);


--
-- Name: evidence_fragments_source_revision_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_fragments_source_revision_idx ON public.evidence_fragments USING btree (team_id, source_revision_id, evidence_index) WHERE (source_revision_id IS NOT NULL);


--
-- Name: evidence_lifecycle_events_operation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_lifecycle_events_operation_idx ON public.evidence_lifecycle_events USING btree (team_id, lifecycle_operation_id, created_at, lifecycle_event_id);


--
-- Name: evidence_lifecycle_events_replacement_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_lifecycle_events_replacement_idx ON public.evidence_lifecycle_events USING btree (team_id, replacement_fragment_id) WHERE (replacement_fragment_id IS NOT NULL);


--
-- Name: evidence_lifecycle_events_terminal_target_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX evidence_lifecycle_events_terminal_target_unique ON public.evidence_lifecycle_events USING btree (team_id, target_fragment_id);


--
-- Name: evidence_lifecycle_operations_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX evidence_lifecycle_operations_idempotency_unique ON public.evidence_lifecycle_operations USING btree (team_id, owner_profile_id, idempotency_key);


--
-- Name: evidence_quarantines_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_quarantines_status_idx ON public.evidence_quarantines USING btree (team_id, status, created_at);


--
-- Name: evidence_security_events_decision_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_security_events_decision_idx ON public.evidence_security_events USING btree (team_id, decision, created_at DESC);


--
-- Name: evidence_security_events_fragment_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_security_events_fragment_idx ON public.evidence_security_events USING btree (team_id, fragment_id, created_at, security_event_id);


--
-- Name: evidence_source_revisions_token_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX evidence_source_revisions_token_unique ON public.evidence_source_revisions USING btree (team_id, source_id, revision_token);


--
-- Name: evidence_sources_owner_key_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX evidence_sources_owner_key_unique ON public.evidence_sources USING btree (team_id, owner_profile_id, source_key);


--
-- Name: hypotheses_related_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hypotheses_related_active_idx ON public.hypotheses USING btree (team_id, status, updated_at DESC) WHERE ((canonical_hypothesis_id IS NULL) AND (status = ANY (ARRAY['proposed'::text, 'reinforced'::text])));


--
-- Name: hypotheses_team_content_hash_canonical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX hypotheses_team_content_hash_canonical_unique ON public.hypotheses USING btree (team_id, content_hash) WHERE ((content_hash IS NOT NULL) AND (canonical_hypothesis_id IS NULL));


--
-- Name: hypotheses_team_target_identity_canonical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX hypotheses_team_target_identity_canonical_unique ON public.hypotheses USING btree (team_id, target_identity) WHERE ((target_identity IS NOT NULL) AND (canonical_hypothesis_id IS NULL));


--
-- Name: hypothesis_derivation_sources_hypothesis_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hypothesis_derivation_sources_hypothesis_idx ON public.hypothesis_derivation_sources USING btree (team_id, hypothesis_id, premise_position, created_at);


--
-- Name: hypothesis_feedback_events_hypothesis_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hypothesis_feedback_events_hypothesis_created_idx ON public.hypothesis_feedback_events USING btree (team_id, hypothesis_id, created_at DESC);


--
-- Name: idx_audit_log_team_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_log_team_timestamp ON public.audit_log USING btree (team_id, "timestamp" DESC);


--
-- Name: idx_audit_log_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_log_timestamp ON public.audit_log USING btree ("timestamp" DESC);


--
-- Name: idx_operation_logs_severity_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_operation_logs_severity_timestamp ON public.operation_logs USING btree (severity_rank DESC, "timestamp" DESC, id DESC);


--
-- Name: idx_operation_logs_severity_value_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_operation_logs_severity_value_timestamp ON public.operation_logs USING btree (severity, "timestamp" DESC, id DESC);


--
-- Name: idx_operation_logs_team_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_operation_logs_team_timestamp ON public.operation_logs USING btree (team_id, "timestamp" DESC) WHERE (team_id IS NOT NULL);


--
-- Name: idx_operation_logs_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_operation_logs_timestamp ON public.operation_logs USING btree ("timestamp" DESC, id DESC);


--
-- Name: idx_recall_feedback_events_contract_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recall_feedback_events_contract_created_at ON public.recall_feedback_events USING btree (contract_version, created_at DESC) WHERE (contract_version <> ''::text);


--
-- Name: idx_recall_feedback_events_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recall_feedback_events_created_at ON public.recall_feedback_events USING btree (created_at DESC, recall_id DESC);


--
-- Name: idx_recall_feedback_events_feedback_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recall_feedback_events_feedback_at ON public.recall_feedback_events USING btree (feedback_at DESC) WHERE (feedback_at IS NOT NULL);


--
-- Name: idx_recall_feedback_events_negative_flags; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recall_feedback_events_negative_flags ON public.recall_feedback_events USING btree (missing_context, irrelevant, created_at DESC) WHERE ((missing_context IS TRUE) OR (irrelevant IS TRUE));


--
-- Name: idx_recall_feedback_events_profile_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recall_feedback_events_profile_created_at ON public.recall_feedback_events USING btree (profile_id, created_at DESC) WHERE (profile_id IS NOT NULL);


--
-- Name: idx_recall_feedback_events_quality_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recall_feedback_events_quality_created_at ON public.recall_feedback_events USING btree (quality, created_at DESC) WHERE (quality <> ''::text);


--
-- Name: idx_recall_feedback_events_search_state_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recall_feedback_events_search_state_created_at ON public.recall_feedback_events USING btree (search_state, created_at DESC) WHERE (search_state <> ''::text);


--
-- Name: idx_recall_feedback_events_team_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recall_feedback_events_team_created_at ON public.recall_feedback_events USING btree (team_id, created_at DESC) WHERE (team_id IS NOT NULL);


--
-- Name: idx_security_ip_bans_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_ip_bans_active ON public.security_ip_bans USING btree (ip) WHERE (revoked_at IS NULL);


--
-- Name: idx_security_ip_bans_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_ip_bans_expires_at ON public.security_ip_bans USING btree (expires_at) WHERE (expires_at IS NOT NULL);


--
-- Name: idx_sso_control_admin_groups_provider_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_control_admin_groups_provider_active ON public.sso_control_admin_groups USING btree (provider_id, group_id) WHERE (enabled AND (retired_at IS NULL));


--
-- Name: idx_sso_control_oauth_states_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_control_oauth_states_expiry ON public.sso_control_oauth_states USING btree (expires_at);


--
-- Name: idx_sso_control_sessions_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_control_sessions_expiry ON public.sso_control_sessions USING btree (expires_at);


--
-- Name: idx_sso_directory_connectors_oauth_client_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_directory_connectors_oauth_client_id_unique ON public.sso_directory_connectors USING btree (oauth_client_id) WHERE (oauth_client_id <> ''::text);


--
-- Name: idx_sso_directory_group_bindings_team; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_directory_group_bindings_team ON public.sso_directory_group_bindings USING btree (team_id);


--
-- Name: idx_sso_directory_group_memberships_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_directory_group_memberships_user ON public.sso_directory_group_memberships USING btree (connector_id, user_id);


--
-- Name: idx_sso_directory_groups_connector_external_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_directory_groups_connector_external_id_unique ON public.sso_directory_groups USING btree (connector_id, external_id) WHERE (external_id <> ''::text);


--
-- Name: idx_sso_directory_oauth_tokens_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_directory_oauth_tokens_expires_at ON public.sso_directory_oauth_tokens USING btree (expires_at);


--
-- Name: idx_sso_directory_users_connector_external_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_directory_users_connector_external_id_unique ON public.sso_directory_users USING btree (connector_id, external_id) WHERE (external_id <> ''::text);


--
-- Name: idx_sso_directory_users_connector_username_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_directory_users_connector_username_unique ON public.sso_directory_users USING btree (connector_id, lower(user_name));


--
-- Name: idx_sso_group_mappings_provider_group; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_group_mappings_provider_group ON public.sso_group_mappings USING btree (provider_id, group_id) WHERE (enabled = true);


--
-- Name: idx_sso_identities_provider_external_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_identities_provider_external_id_unique ON public.sso_identities USING btree (provider_id, external_id) WHERE (external_id <> ''::text);


--
-- Name: idx_sso_identities_provider_subject; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_identities_provider_subject ON public.sso_identities USING btree (provider_id, subject);


--
-- Name: idx_sso_oauth_states_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_oauth_states_expires_at ON public.sso_oauth_states USING btree (expires_at);


--
-- Name: idx_sso_providers_name_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_providers_name_unique ON public.sso_providers USING btree (lower((name)::text));


--
-- Name: idx_sso_sessions_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_sessions_expires_at ON public.sso_sessions USING btree (expires_at);


--
-- Name: idx_sso_sessions_identity_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_sessions_identity_id ON public.sso_sessions USING btree (identity_id);


--
-- Name: idx_team_profiles_key_prefix_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_profiles_key_prefix_unique ON public.team_profiles USING btree (key_prefix) WHERE (key_prefix IS NOT NULL);


--
-- Name: idx_team_profiles_sso_identity_team_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_profiles_sso_identity_team_unique ON public.team_profiles USING btree (sso_identity_id, team_id) WHERE (sso_identity_id IS NOT NULL);


--
-- Name: idx_team_profiles_sso_owner_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_team_profiles_sso_owner_identity ON public.team_profiles USING btree (sso_owner_identity_id) WHERE (sso_owner_identity_id IS NOT NULL);


--
-- Name: idx_team_profiles_sso_owner_team_active_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_profiles_sso_owner_team_active_unique ON public.team_profiles USING btree (sso_owner_identity_id, team_id) WHERE ((sso_owner_identity_id IS NOT NULL) AND ((auth_source)::text = 'api_key'::text) AND (revoked_at IS NULL));


--
-- Name: idx_team_profiles_sso_provider_subject; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_team_profiles_sso_provider_subject ON public.team_profiles USING btree (sso_provider_id, sso_subject) WHERE ((sso_provider_id IS NOT NULL) AND (sso_subject IS NOT NULL));


--
-- Name: idx_team_profiles_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_team_profiles_team_id ON public.team_profiles USING btree (team_id);


--
-- Name: idx_team_profiles_team_id_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_profiles_team_id_id_unique ON public.team_profiles USING btree (team_id, id);


--
-- Name: idx_team_profiles_team_name_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_profiles_team_name_unique ON public.team_profiles USING btree (team_id, lower((name)::text));


--
-- Name: idx_teams_directory_managed_group_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_teams_directory_managed_group_unique ON public.teams USING btree (directory_connector_id, directory_group_id) WHERE directory_managed;


--
-- Name: idx_teams_name_unique_active; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_teams_name_unique_active ON public.teams USING btree (lower((name)::text)) WHERE (deleted_at IS NULL);


--
-- Name: idx_usage_metric_buckets_bucket_start; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_metric_buckets_bucket_start ON public.usage_metric_buckets USING btree (bucket_start DESC);


--
-- Name: idx_usage_metric_buckets_key_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_metric_buckets_key_bucket ON public.usage_metric_buckets USING btree (key_id, bucket_start DESC);


--
-- Name: idx_usage_metric_buckets_team_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_metric_buckets_team_bucket ON public.usage_metric_buckets USING btree (team_id, bucket_start DESC);


--
-- Name: idx_user_portal_sessions_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_portal_sessions_expires_at ON public.user_portal_sessions USING btree (expires_at);


--
-- Name: idx_user_portal_sessions_key_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_portal_sessions_key_id ON public.user_portal_sessions USING btree (key_id);


--
-- Name: idx_v2_compatibility_markers_kind_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_compatibility_markers_kind_created ON public.v2_compatibility_markers USING btree (marker_kind, created_at DESC);


--
-- Name: idx_v2_migration_corpus_run_outcome; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_migration_corpus_run_outcome ON public.v2_migration_corpus_items USING btree (run_id, outcome, updated_at DESC);


--
-- Name: idx_v2_migration_corpus_team_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_migration_corpus_team_owner ON public.v2_migration_corpus_items USING btree (run_id, team_id, owner_profile_id, outcome);


--
-- Name: idx_v2_migration_errors_run_phase; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_migration_errors_run_phase ON public.v2_migration_errors USING btree (run_id, phase, created_at DESC);


--
-- Name: idx_v2_migration_operator_actions_run_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_migration_operator_actions_run_created ON public.v2_migration_operator_actions USING btree (run_id, created_at DESC);


--
-- Name: idx_v2_migration_runs_single_active; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_v2_migration_runs_single_active ON public.v2_migration_runs USING btree ((true)) WHERE (state = ANY (ARRAY['required'::text, 'preflight'::text, 'ready'::text, 'running'::text, 'paused_retryable'::text, 'verifying'::text, 'ready_to_cutover'::text]));


--
-- Name: idx_v2_migration_runs_state_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_migration_runs_state_updated ON public.v2_migration_runs USING btree (state, updated_at DESC);


--
-- Name: knowledge_ingests_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX knowledge_ingests_idempotency_unique ON public.knowledge_ingests USING btree (team_id, owner_profile_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: knowledge_ingests_migration_run_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX knowledge_ingests_migration_run_idx ON public.knowledge_ingests USING btree (team_id, migration_run_id) WHERE (migration_run_id IS NOT NULL);


--
-- Name: knowledge_ingests_team_status_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX knowledge_ingests_team_status_created_idx ON public.knowledge_ingests USING btree (team_id, status, created_at, ingest_id);


--
-- Name: knowledge_ingests_telemetry_remember_backfill_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX knowledge_ingests_telemetry_remember_backfill_idx ON public.knowledge_ingests USING btree (team_id, ingest_id) WHERE (((metadata ->> '_dense_mem_telemetry_origin'::text) = 'remember'::text) OR ((NULLIF((metadata ->> 'contract_version'::text), ''::text) IS NOT NULL) AND (jsonb_typeof((metadata -> 'actor'::text)) = 'object'::text) AND ((metadata #>> '{actor,team_id}'::text[]) = (team_id)::text) AND ((metadata #>> '{actor,profile_id}'::text[]) = (owner_profile_id)::text)));


--
-- Name: placement_assessments_owner_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_assessments_owner_created_idx ON public.placement_assessments USING btree (team_id, owner_profile_id, created_at);


--
-- Name: placement_assessments_submission_owner_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_assessments_submission_owner_created_idx ON public.placement_assessments USING btree (team_id, owner_profile_id, created_at, placement_run_id) WHERE (assessment_scope = 'submission'::text);


--
-- Name: placement_assessments_submission_run_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX placement_assessments_submission_run_unique ON public.placement_assessments USING btree (team_id, placement_run_id) WHERE (assessment_scope = 'submission'::text);


--
-- Name: placement_items_migration_ingest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_items_migration_ingest_idx ON public.placement_items USING btree (team_id, ingest_id) WHERE (evidence_index = 0);


--
-- Name: placement_items_run_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_items_run_idx ON public.placement_items USING btree (team_id, placement_run_id, evidence_index);


--
-- Name: placement_items_team_claim_key_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX placement_items_team_claim_key_unique ON public.placement_items USING btree (team_id, claim_key);


--
-- Name: placement_outcomes_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX placement_outcomes_idempotency_unique ON public.placement_outcomes USING btree (team_id, owner_profile_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: placement_outcomes_run_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_outcomes_run_idx ON public.placement_outcomes USING btree (team_id, placement_run_id, created_at, outcome_id);


--
-- Name: placement_outcomes_telemetry_first_disposition_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX placement_outcomes_telemetry_first_disposition_unique ON public.placement_outcomes USING btree (team_id, placement_run_id) WHERE (outcome_kind = 'telemetry_first_disposition'::text);


--
-- Name: placement_runs_active_replacement_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX placement_runs_active_replacement_unique ON public.placement_runs USING btree (team_id, replaces_placement_run_id) WHERE ((replaces_placement_run_id IS NOT NULL) AND (status = ANY (ARRAY['queued'::text, 'guarded'::text, 'processing'::text])));


--
-- Name: placement_runs_owner_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_runs_owner_created_idx ON public.placement_runs USING btree (team_id, owner_profile_id, created_at DESC);


--
-- Name: placement_runs_replacement_target_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_runs_replacement_target_idx ON public.placement_runs USING btree (team_id, replaces_placement_run_id, created_at, placement_run_id) WHERE (replaces_placement_run_id IS NOT NULL);


--
-- Name: placement_runs_team_expired_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_runs_team_expired_claim_idx ON public.placement_runs USING btree (team_id, lease_until, created_at, placement_run_id) WHERE ((status = 'processing'::text) AND (lease_until IS NOT NULL) AND (attempts < max_attempts));


--
-- Name: placement_runs_team_status_available_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_runs_team_status_available_idx ON public.placement_runs USING btree (team_id, status, available_at, created_at, placement_run_id);


--
-- Name: predicate_definitions_aliases_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX predicate_definitions_aliases_idx ON public.predicate_definitions USING gin (aliases);


--
-- Name: predicate_registration_events_run_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX predicate_registration_events_run_created_idx ON public.predicate_registration_events USING btree (team_id, placement_run_id, created_at, predicate_registration_event_id);


--
-- Name: relationship_conflict_ai_assessment_case_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_ai_assessment_case_idx ON public.relationship_conflict_ai_assessment_attempts USING btree (team_id, conflict_id, case_version, created_at);


--
-- Name: relationship_conflict_ai_assessment_events_attempt_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_ai_assessment_events_attempt_idx ON public.relationship_conflict_ai_assessment_events USING btree (team_id, assessment_attempt_id, created_at, assessment_event_id);


--
-- Name: relationship_conflict_ai_assessment_failure_count_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_ai_assessment_failure_count_idx ON public.relationship_conflict_ai_assessment_attempts USING btree (team_id, conflict_id, case_version, model, policy_version) WHERE (status = 'failed'::text);


--
-- Name: relationship_conflict_cases_due_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_cases_due_idx ON public.relationship_conflict_cases USING btree (team_id, next_review_at, conflict_id) WHERE (status = ANY (ARRAY['open'::text, 'overdue'::text]));


--
-- Name: relationship_conflict_cases_lease_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_cases_lease_idx ON public.relationship_conflict_cases USING btree (team_id, lease_until) WHERE ((status = ANY (ARRAY['open'::text, 'overdue'::text])) AND (lease_until IS NOT NULL));


--
-- Name: relationship_conflict_cases_open_scope_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_conflict_cases_open_scope_unique ON public.relationship_conflict_cases USING btree (team_id, semantic_scope_key) WHERE (status = ANY (ARRAY['open'::text, 'overdue'::text]));


--
-- Name: relationship_conflict_cases_subject_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_cases_subject_idx ON public.relationship_conflict_cases USING btree (team_id, subject_entity_id, predicate_key) WHERE (status = ANY (ARRAY['open'::text, 'overdue'::text, 'resolved'::text]));


--
-- Name: relationship_conflict_derived_evidence_tasks_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_derived_evidence_tasks_claim_idx ON public.relationship_conflict_derived_evidence_tasks USING btree (team_id, status, lease_until, created_at, derived_evidence_task_id) WHERE (status = ANY (ARRAY['pending'::text, 'processing'::text]));


--
-- Name: relationship_conflict_events_case_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_events_case_idx ON public.relationship_conflict_events USING btree (team_id, conflict_id, created_at DESC, conflict_event_id DESC);


--
-- Name: relationship_conflict_events_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_conflict_events_idempotency_unique ON public.relationship_conflict_events USING btree (team_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: relationship_conflict_evidence_derivations_conflict_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_evidence_derivations_conflict_idx ON public.relationship_conflict_evidence_derivations USING btree (team_id, conflict_id, created_at);


--
-- Name: relationship_conflict_members_case_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_members_case_idx ON public.relationship_conflict_position_members USING btree (team_id, conflict_id, relationship_id);


--
-- Name: relationship_conflict_members_relationship_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_members_relationship_idx ON public.relationship_conflict_position_members USING btree (team_id, relationship_id);


--
-- Name: relationship_conflict_positions_case_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_positions_case_idx ON public.relationship_conflict_positions USING btree (team_id, conflict_id, disposition, position_id);


--
-- Name: relationship_conflict_resolution_plans_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_resolution_plans_pending_idx ON public.relationship_conflict_resolution_plans USING btree (team_id, status, created_at) WHERE (status = 'resolution_pending'::text);


--
-- Name: relationship_conflict_review_runs_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_review_runs_status_idx ON public.relationship_conflict_review_runs USING btree (team_id, status, lease_until);


--
-- Name: relationship_correction_submissions_confirmation_idempotency_un; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_correction_submissions_confirmation_idempotency_un ON public.relationship_correction_submissions USING btree (team_id, owner_profile_id, confirmation_idempotency_key) WHERE (confirmation_idempotency_key <> ''::text);


--
-- Name: relationship_correction_submissions_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_correction_submissions_idempotency_unique ON public.relationship_correction_submissions USING btree (team_id, owner_profile_id, idempotency_key);


--
-- Name: relationship_correction_submissions_owner_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_correction_submissions_owner_created_idx ON public.relationship_correction_submissions USING btree (team_id, owner_profile_id, created_at DESC, submission_id DESC);


--
-- Name: relationship_observations_relationship_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_observations_relationship_idx ON public.relationship_observations USING btree (team_id, relationship_id, created_at) WHERE (relationship_id IS NOT NULL);


--
-- Name: relationship_records_active_object_entity_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_records_active_object_entity_idx ON public.relationship_records USING btree (team_id, object_entity_id, predicate_key) WHERE ((identity_alias_of_relationship_id IS NULL) AND (status = 'active'::text) AND (support_count > 0) AND (object_entity_id IS NOT NULL));


--
-- Name: relationship_records_active_object_value_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_records_active_object_value_idx ON public.relationship_records USING btree (team_id, object_value_id, predicate_key) WHERE ((identity_alias_of_relationship_id IS NULL) AND (status = 'active'::text) AND (support_count > 0) AND (object_value_id IS NOT NULL));


--
-- Name: relationship_records_active_one_current_canonical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_records_active_one_current_canonical_unique ON public.relationship_records USING btree (team_id, owner_profile_id, subject_entity_id, predicate_key, polarity, valid_from, scope_key) NULLS NOT DISTINCT WHERE ((identity_alias_of_relationship_id IS NULL) AND (current_cardinality = 'one'::text) AND (status = 'active'::text) AND (support_count > 0));


--
-- Name: relationship_records_active_subject_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_records_active_subject_idx ON public.relationship_records USING btree (team_id, subject_entity_id, predicate_key) WHERE ((identity_alias_of_relationship_id IS NULL) AND (status = 'active'::text) AND (support_count > 0));


--
-- Name: relationship_records_active_supported_team_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_records_active_supported_team_idx ON public.relationship_records USING btree (team_id) WHERE ((identity_alias_of_relationship_id IS NULL) AND (status = 'active'::text) AND (support_count > 0));


--
-- Name: relationship_records_canonical_identity_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_records_canonical_identity_unique ON public.relationship_records USING btree (team_id, owner_profile_id, subject_entity_id, predicate_key, object_entity_id, object_value_id, polarity, valid_from, scope_key) NULLS NOT DISTINCT WHERE (identity_alias_of_relationship_id IS NULL);


--
-- Name: relationship_records_identity_alias_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_records_identity_alias_idx ON public.relationship_records USING btree (team_id, identity_alias_of_relationship_id) WHERE (identity_alias_of_relationship_id IS NOT NULL);


--
-- Name: relationship_support_decision_events_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_support_decision_events_idempotency_unique ON public.relationship_support_decision_events USING btree (team_id, owner_profile_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: relationship_support_decisions_support_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_support_decisions_support_idx ON public.relationship_support_decision_events USING btree (team_id, support_id, created_at DESC, support_decision_id DESC);


--
-- Name: relationship_transition_events_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_transition_events_idempotency_unique ON public.relationship_transition_events USING btree (team_id, owner_profile_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: relationship_transition_events_relationship_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_transition_events_relationship_created_idx ON public.relationship_transition_events USING btree (team_id, relationship_id, created_at DESC, transition_id DESC);


--
-- Name: review_tasks_open_dedupe_key_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX review_tasks_open_dedupe_key_unique ON public.review_tasks USING btree (team_id, dedupe_key) WHERE ((dedupe_key <> ''::text) AND (status = ANY (ARRAY['open'::text, 'acknowledged'::text])));


--
-- Name: review_tasks_open_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX review_tasks_open_expiry_idx ON public.review_tasks USING btree (team_id, expires_at, review_task_id) WHERE ((status = ANY (ARRAY['open'::text, 'acknowledged'::text])) AND (expires_at IS NOT NULL));


--
-- Name: review_tasks_open_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX review_tasks_open_owner_idx ON public.review_tasks USING btree (team_id, owner_profile_id, created_at) WHERE (status = ANY (ARRAY['open'::text, 'acknowledged'::text]));


--
-- Name: review_tasks_open_placement_item_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX review_tasks_open_placement_item_idx ON public.review_tasks USING btree (team_id, placement_item_id) WHERE ((placement_item_id IS NOT NULL) AND (status = ANY (ARRAY['open'::text, 'acknowledged'::text])));


--
-- Name: search_documents_contract_state_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_documents_contract_state_idx ON public.search_documents USING btree (team_id, embedding_contract_id, search_state, source_kind);


--
-- Name: search_documents_fts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_documents_fts_idx ON public.search_documents USING gin (search_tsv);


--
-- Name: search_documents_relationship_projection_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_documents_relationship_projection_idx ON public.search_documents USING btree (team_id, source_kind, projection_format_version, projection_generation_id, search_state) WHERE (source_kind = 'relationship'::text);


--
-- Name: search_documents_source_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_documents_source_idx ON public.search_documents USING btree (team_id, source_kind, source_id, source_version DESC);


--
-- Name: search_documents_state_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_documents_state_idx ON public.search_documents USING btree (team_id, search_state, updated_at DESC, search_document_id);


--
-- Name: search_projection_generations_current_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX search_projection_generations_current_unique ON public.search_projection_generations USING btree (team_id, source_kind, projection_format_version) WHERE (state = 'current'::text);


--
-- Name: search_projection_generations_state_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_projection_generations_state_idx ON public.search_projection_generations USING btree (team_id, source_kind, projection_format_version, state);


--
-- Name: submission_holds_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX submission_holds_expiry_idx ON public.submission_holds USING btree (team_id, expires_at, placement_run_id);


--
-- Name: submission_holds_owner_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX submission_holds_owner_expiry_idx ON public.submission_holds USING btree (team_id, owner_profile_id, expires_at, placement_run_id);


--
-- Name: submission_quarantine_payloads_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX submission_quarantine_payloads_expiry_idx ON public.submission_quarantine_payloads USING btree (expires_at, team_id, quarantine_payload_id);


--
-- Name: team_predicate_definitions_aliases_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX team_predicate_definitions_aliases_idx ON public.team_predicate_definitions USING gin (aliases);


--
-- Name: team_profiles_system_team_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX team_profiles_system_team_unique ON public.team_profiles USING btree (team_id) WHERE is_system;


--
-- Name: v2_migration_corpus_run_ingest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX v2_migration_corpus_run_ingest_idx ON public.v2_migration_corpus_items USING btree (run_id, team_id, ingest_id) WHERE (ingest_id IS NOT NULL);


--
-- Name: audit_log audit_log_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER audit_log_append_only BEFORE DELETE OR UPDATE ON public.audit_log FOR EACH ROW EXECUTE FUNCTION public.prevent_audit_log_mutation();


--
-- Name: dream_path_evaluations dream_path_evaluations_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER dream_path_evaluations_append_only BEFORE DELETE OR UPDATE ON public.dream_path_evaluations FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: embedding_contracts embedding_contracts_reference_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER embedding_contracts_reference_guard BEFORE INSERT OR DELETE OR UPDATE ON public.embedding_contracts FOR EACH ROW EXECUTE FUNCTION public.prevent_reference_definition_mutation();


--
-- Name: entity_correction_events entity_correction_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER entity_correction_events_append_only BEFORE DELETE OR UPDATE ON public.entity_correction_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: entity_resolution_events entity_resolution_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER entity_resolution_events_append_only BEFORE DELETE OR UPDATE ON public.entity_resolution_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_fragments evidence_fragments_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_fragments_append_only BEFORE DELETE OR UPDATE ON public.evidence_fragments FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_lifecycle_events_append_only BEFORE DELETE OR UPDATE ON public.evidence_lifecycle_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_lifecycle_operations_append_only BEFORE DELETE OR UPDATE ON public.evidence_lifecycle_operations FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_security_events evidence_security_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_security_events_append_only BEFORE DELETE OR UPDATE ON public.evidence_security_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_security_signals evidence_security_signals_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_security_signals_append_only BEFORE DELETE OR UPDATE ON public.evidence_security_signals FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_source_revisions evidence_source_revisions_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_source_revisions_append_only BEFORE DELETE OR UPDATE ON public.evidence_source_revisions FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: hypotheses hypotheses_guard_provenance_trg; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER hypotheses_guard_provenance_trg BEFORE UPDATE ON public.hypotheses FOR EACH ROW EXECUTE FUNCTION public.hypotheses_guard_provenance();


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER hypothesis_derivation_sources_append_only BEFORE DELETE OR UPDATE ON public.hypothesis_derivation_sources FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: placement_assessments placement_assessments_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER placement_assessments_append_only BEFORE DELETE OR UPDATE ON public.placement_assessments FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: placement_outcomes placement_outcomes_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER placement_outcomes_append_only BEFORE DELETE OR UPDATE ON public.placement_outcomes FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: placement_runs placement_runs_submission_hold_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE CONSTRAINT TRIGGER placement_runs_submission_hold_guard AFTER INSERT OR UPDATE OF status, ingest_id, owner_profile_id ON public.placement_runs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.ensure_submission_hold_for_awaiting_review();


--
-- Name: predicate_definitions predicate_definitions_reference_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER predicate_definitions_reference_guard BEFORE INSERT OR DELETE OR UPDATE ON public.predicate_definitions FOR EACH ROW EXECUTE FUNCTION public.prevent_reference_definition_mutation();


--
-- Name: predicate_registration_events predicate_registration_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER predicate_registration_events_append_only BEFORE DELETE OR UPDATE ON public.predicate_registration_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_conflict_ai_assessment_events relationship_conflict_ai_assessment_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_conflict_ai_assessment_events_append_only BEFORE DELETE OR UPDATE ON public.relationship_conflict_ai_assessment_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_conflict_events relationship_conflict_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_conflict_events_append_only BEFORE DELETE OR UPDATE ON public.relationship_conflict_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_derivations_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_conflict_evidence_derivations_append_only BEFORE DELETE OR UPDATE ON public.relationship_conflict_evidence_derivations FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_correction_events relationship_correction_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_correction_events_append_only BEFORE DELETE OR UPDATE ON public.relationship_correction_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_cross_references relationship_cross_references_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_cross_references_append_only BEFORE DELETE OR UPDATE ON public.relationship_cross_references FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_observations relationship_observations_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_observations_append_only BEFORE DELETE OR UPDATE ON public.relationship_observations FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_support_decision_events relationship_support_decisions_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_support_decisions_append_only BEFORE DELETE OR UPDATE ON public.relationship_support_decision_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_evidence_supports relationship_supports_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_supports_append_only BEFORE DELETE OR UPDATE ON public.relationship_evidence_supports FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_transition_events relationship_transitions_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_transitions_append_only BEFORE DELETE OR UPDATE ON public.relationship_transition_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: search_index_generations search_index_generations_lifecycle_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER search_index_generations_lifecycle_guard BEFORE INSERT OR DELETE OR UPDATE ON public.search_index_generations FOR EACH ROW EXECUTE FUNCTION public.guard_search_index_generation_lifecycle();


--
-- Name: submission_holds submission_holds_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER submission_holds_append_only BEFORE DELETE OR UPDATE ON public.submission_holds FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: team_predicate_definitions team_predicate_definitions_reference_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER team_predicate_definitions_reference_guard BEFORE DELETE OR UPDATE ON public.team_predicate_definitions FOR EACH ROW EXECUTE FUNCTION public.prevent_reference_definition_mutation();


--
-- Name: verification_events verification_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER verification_events_append_only BEFORE DELETE OR UPDATE ON public.verification_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: community_memberships community_memberships_team_id_community_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_memberships
    ADD CONSTRAINT community_memberships_team_id_community_id_fkey FOREIGN KEY (team_id, community_id) REFERENCES public.community_records(team_id, community_id) ON DELETE RESTRICT;


--
-- Name: community_memberships community_memberships_team_id_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_memberships
    ADD CONSTRAINT community_memberships_team_id_entity_id_fkey FOREIGN KEY (team_id, entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: community_records community_records_team_id_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_records
    ADD CONSTRAINT community_records_team_id_run_id_fkey FOREIGN KEY (team_id, run_id) REFERENCES public.community_snapshot_runs(team_id, run_id) ON DELETE RESTRICT;


--
-- Name: community_snapshot_runs community_snapshot_runs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_snapshot_runs
    ADD CONSTRAINT community_snapshot_runs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE RESTRICT;


--
-- Name: community_sources community_sources_team_id_community_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_sources
    ADD CONSTRAINT community_sources_team_id_community_id_fkey FOREIGN KEY (team_id, community_id) REFERENCES public.community_records(team_id, community_id) ON DELETE RESTRICT;


--
-- Name: community_sources community_sources_team_id_relationship_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_sources
    ADD CONSTRAINT community_sources_team_id_relationship_id_owner_profile_id_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: community_summary_attempts community_summary_attempts_team_id_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_summary_attempts
    ADD CONSTRAINT community_summary_attempts_team_id_run_id_fkey FOREIGN KEY (team_id, run_id) REFERENCES public.community_snapshot_runs(team_id, run_id) ON DELETE RESTRICT;


--
-- Name: dream_cycle_runs dream_cycle_runs_canonical_run_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_canonical_run_fk FOREIGN KEY (team_id, canonical_run_id) REFERENCES public.dream_cycle_runs(team_id, run_id) ON DELETE RESTRICT;


--
-- Name: dream_cycle_runs dream_cycle_runs_team_id_initiated_by_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_team_id_initiated_by_profile_id_fkey FOREIGN KEY (team_id, initiated_by_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: dream_path_evaluations dream_path_evaluations_team_id_first_relationship_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_team_id_first_relationship_id_fkey FOREIGN KEY (team_id, first_relationship_id) REFERENCES public.relationship_records(team_id, relationship_id) ON DELETE RESTRICT;


--
-- Name: dream_path_evaluations dream_path_evaluations_team_id_second_relationship_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_team_id_second_relationship_id_fkey FOREIGN KEY (team_id, second_relationship_id) REFERENCES public.relationship_records(team_id, relationship_id) ON DELETE RESTRICT;


--
-- Name: embedding_jobs embedding_jobs_embedding_contract_id_embedding_dimensions_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_jobs
    ADD CONSTRAINT embedding_jobs_embedding_contract_id_embedding_dimensions_fkey FOREIGN KEY (embedding_contract_id, embedding_dimensions) REFERENCES public.embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT;


--
-- Name: embedding_jobs embedding_jobs_projection_generation_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_jobs
    ADD CONSTRAINT embedding_jobs_projection_generation_fk FOREIGN KEY (team_id, projection_generation_id) REFERENCES public.search_projection_generations(team_id, projection_generation_id) ON DELETE RESTRICT;


--
-- Name: embedding_jobs embedding_jobs_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_jobs
    ADD CONSTRAINT embedding_jobs_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: embedding_jobs embedding_jobs_team_id_search_document_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_jobs
    ADD CONSTRAINT embedding_jobs_team_id_search_document_id_fkey FOREIGN KEY (team_id, search_document_id) REFERENCES public.search_documents(team_id, search_document_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_events entity_correction_events_team_id_new_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_events
    ADD CONSTRAINT entity_correction_events_team_id_new_entity_id_fkey FOREIGN KEY (team_id, new_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_events entity_correction_events_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_events
    ADD CONSTRAINT entity_correction_events_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_events entity_correction_events_team_id_survivor_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_events
    ADD CONSTRAINT entity_correction_events_team_id_survivor_entity_id_fkey FOREIGN KEY (team_id, survivor_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_plans entity_correction_plans_team_id_correction_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_team_id_correction_event_id_fkey FOREIGN KEY (team_id, correction_event_id) REFERENCES public.entity_correction_events(team_id, correction_event_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_plans entity_correction_plans_team_id_new_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_team_id_new_entity_id_fkey FOREIGN KEY (team_id, new_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_plans entity_correction_plans_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_plans entity_correction_plans_team_id_source_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_team_id_source_entity_id_fkey FOREIGN KEY (team_id, source_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_plans entity_correction_plans_team_id_target_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_team_id_target_entity_id_fkey FOREIGN KEY (team_id, target_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_names entity_names_team_id_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_names
    ADD CONSTRAINT entity_names_team_id_entity_id_fkey FOREIGN KEY (team_id, entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_names entity_names_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_names
    ADD CONSTRAINT entity_names_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: entity_names entity_names_team_id_fragment_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_names
    ADD CONSTRAINT entity_names_team_id_fragment_id_owner_profile_id_fkey FOREIGN KEY (team_id, fragment_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: entity_names entity_names_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_names
    ADD CONSTRAINT entity_names_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: entity_records entity_records_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_records
    ADD CONSTRAINT entity_records_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_assessment_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_assessment_ref FOREIGN KEY (team_id, assessment_id) REFERENCES public.placement_assessments(team_id, assessment_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_entity_id_fkey FOREIGN KEY (team_id, entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_fragment_id_owner_profile_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_fragment_id_owner_profile_fkey FOREIGN KEY (team_id, fragment_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_ingest_id_owner_profile_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_ingest_id_owner_profile_i_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_placement_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_placement_item_id_fkey FOREIGN KEY (team_id, placement_item_id) REFERENCES public.placement_items(team_id, placement_item_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_placement_item_id_owner_p_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_placement_item_id_owner_p_fkey FOREIGN KEY (team_id, placement_item_id, owner_profile_id) REFERENCES public.placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_ingest_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_ingest_id_owner_profile_id_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_source_id_fkey FOREIGN KEY (team_id, source_id) REFERENCES public.evidence_sources(team_id, source_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_source_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_source_id_owner_profile_id_fkey FOREIGN KEY (team_id, source_id, owner_profile_id) REFERENCES public.evidence_sources(team_id, source_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_source_id_source_revision_id_ow_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_source_id_source_revision_id_ow_fkey FOREIGN KEY (team_id, source_id, source_revision_id, owner_profile_id) REFERENCES public.evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_source_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_source_revision_id_fkey FOREIGN KEY (team_id, source_revision_id) REFERENCES public.evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_team_id_lifecycle_operation_id_o_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_events
    ADD CONSTRAINT evidence_lifecycle_events_team_id_lifecycle_operation_id_o_fkey FOREIGN KEY (team_id, lifecycle_operation_id, owner_profile_id) REFERENCES public.evidence_lifecycle_operations(team_id, lifecycle_operation_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_team_id_replacement_fragment_id__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_events
    ADD CONSTRAINT evidence_lifecycle_events_team_id_replacement_fragment_id__fkey FOREIGN KEY (team_id, replacement_fragment_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_team_id_target_fragment_id_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_events
    ADD CONSTRAINT evidence_lifecycle_events_team_id_target_fragment_id_owner_fkey FOREIGN KEY (team_id, target_fragment_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_actor_profile_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_actor_profile_fk FOREIGN KEY (team_id, actor_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_team_id_replacement_ingest_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_team_id_replacement_ingest_i_fkey FOREIGN KEY (team_id, replacement_ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_fragment_id_ingest_id_owner_p_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_fragment_id_ingest_id_owner_p_fkey FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_ingest_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_ingest_id_owner_profile_id_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_released_by_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_released_by_profile_id_fkey FOREIGN KEY (team_id, released_by_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_actor_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_actor_profile_id_fkey FOREIGN KEY (team_id, actor_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_fragment_id_ingest_id_own_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_fragment_id_ingest_id_own_fkey FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_ingest_id_owner_profile_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_ingest_id_owner_profile_i_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_signals evidence_security_signals_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_signals
    ADD CONSTRAINT evidence_security_signals_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_signals evidence_security_signals_team_id_security_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_signals
    ADD CONSTRAINT evidence_security_signals_team_id_security_event_id_fkey FOREIGN KEY (team_id, security_event_id) REFERENCES public.evidence_security_events(team_id, security_event_id) ON DELETE CASCADE;


--
-- Name: evidence_security_signals evidence_security_signals_team_id_security_event_id_owner__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_signals
    ADD CONSTRAINT evidence_security_signals_team_id_security_event_id_owner__fkey FOREIGN KEY (team_id, security_event_id, owner_profile_id) REFERENCES public.evidence_security_events(team_id, security_event_id, owner_profile_id) ON DELETE CASCADE;


--
-- Name: evidence_source_revisions evidence_source_revisions_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_source_revisions evidence_source_revisions_team_id_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_team_id_source_id_fkey FOREIGN KEY (team_id, source_id) REFERENCES public.evidence_sources(team_id, source_id) ON DELETE RESTRICT;


--
-- Name: evidence_source_revisions evidence_source_revisions_team_id_source_id_owner_profile__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_team_id_source_id_owner_profile__fkey FOREIGN KEY (team_id, source_id, owner_profile_id) REFERENCES public.evidence_sources(team_id, source_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_source_revisions evidence_source_revisions_team_id_source_id_supersedes_rev_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_team_id_source_id_supersedes_rev_fkey FOREIGN KEY (team_id, source_id, supersedes_revision_id, owner_profile_id) REFERENCES public.evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_source_revisions evidence_source_revisions_team_id_supersedes_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_team_id_supersedes_revision_id_fkey FOREIGN KEY (team_id, supersedes_revision_id) REFERENCES public.evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT;


--
-- Name: evidence_sources evidence_sources_current_revision_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_sources
    ADD CONSTRAINT evidence_sources_current_revision_fk FOREIGN KEY (team_id, source_id, current_revision_id, owner_profile_id) REFERENCES public.evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT DEFERRABLE;


--
-- Name: evidence_sources evidence_sources_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_sources
    ADD CONSTRAINT evidence_sources_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_canonical_hypothesis_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_canonical_hypothesis_fk FOREIGN KEY (team_id, canonical_hypothesis_id) REFERENCES public.hypotheses(team_id, hypothesis_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_cycle_run_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_cycle_run_fk FOREIGN KEY (team_id, cycle_run_id) REFERENCES public.dream_cycle_runs(team_id, run_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_object_entity_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_object_entity_fk FOREIGN KEY (team_id, object_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_object_value_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_object_value_fk FOREIGN KEY (team_id, object_value_id) REFERENCES public.value_records(team_id, value_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_subject_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_subject_fk FOREIGN KEY (team_id, subject_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_submitted_ingest_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_submitted_ingest_fk FOREIGN KEY (team_id, submitted_ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_team_id_created_by_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_team_id_created_by_profile_id_fkey FOREIGN KEY (team_id, created_by_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_team_predicate_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_team_predicate_fk FOREIGN KEY (team_id, predicate_key, predicate_version) REFERENCES public.team_predicate_definitions(team_id, predicate_key, version) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_hypothesis_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_hypothesis_id_fkey FOREIGN KEY (team_id, hypothesis_id) REFERENCES public.hypotheses(team_id, hypothesis_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_observation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_observation_id_fkey FOREIGN KEY (team_id, observation_id) REFERENCES public.relationship_observations(team_id, observation_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_relationship_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_relationship_id_fkey FOREIGN KEY (team_id, relationship_id) REFERENCES public.relationship_records(team_id, relationship_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_source_id_fkey FOREIGN KEY (team_id, source_id) REFERENCES public.evidence_sources(team_id, source_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_source_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_source_revision_id_fkey FOREIGN KEY (team_id, source_revision_id) REFERENCES public.evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_support_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_support_id_fkey FOREIGN KEY (team_id, support_id) REFERENCES public.relationship_evidence_supports(team_id, support_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_team_id_actor_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_feedback_events
    ADD CONSTRAINT hypothesis_feedback_events_team_id_actor_profile_id_fkey FOREIGN KEY (team_id, actor_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_team_id_hypothesis_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_feedback_events
    ADD CONSTRAINT hypothesis_feedback_events_team_id_hypothesis_id_fkey FOREIGN KEY (team_id, hypothesis_id) REFERENCES public.hypotheses(team_id, hypothesis_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_team_id_submitted_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_feedback_events
    ADD CONSTRAINT hypothesis_feedback_events_team_id_submitted_ingest_id_fkey FOREIGN KEY (team_id, submitted_ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: knowledge_ingests knowledge_ingests_migration_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.knowledge_ingests
    ADD CONSTRAINT knowledge_ingests_migration_run_id_fkey FOREIGN KEY (migration_run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE RESTRICT;


--
-- Name: knowledge_ingests knowledge_ingests_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.knowledge_ingests
    ADD CONSTRAINT knowledge_ingests_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: placement_assessments placement_assessments_claim_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_claim_ref FOREIGN KEY (team_id, placement_item_id, claim_key) REFERENCES public.placement_items(team_id, placement_item_id, claim_key) ON DELETE RESTRICT;


--
-- Name: placement_assessments placement_assessments_item_owner_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_item_owner_ref FOREIGN KEY (team_id, placement_item_id, owner_profile_id) REFERENCES public.placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_assessments placement_assessments_submission_run_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_submission_run_ref FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_fragment_id_ingest_id_owner_profil_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_fragment_id_ingest_id_owner_profil_fkey FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_ingest_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_ingest_id_owner_profile_id_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_placement_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_placement_run_id_fkey FOREIGN KEY (team_id, placement_run_id) REFERENCES public.placement_runs(team_id, placement_run_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_placement_run_id_ingest_id_owner_p_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_placement_run_id_ingest_id_owner_p_fkey FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_outcomes placement_outcomes_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: placement_outcomes placement_outcomes_team_id_placement_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_team_id_placement_item_id_fkey FOREIGN KEY (team_id, placement_item_id) REFERENCES public.placement_items(team_id, placement_item_id) ON DELETE RESTRICT;


--
-- Name: placement_outcomes placement_outcomes_team_id_placement_item_id_placement_run_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_team_id_placement_item_id_placement_run_fkey FOREIGN KEY (team_id, placement_item_id, placement_run_id, owner_profile_id) REFERENCES public.placement_items(team_id, placement_item_id, placement_run_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_outcomes placement_outcomes_team_id_placement_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_team_id_placement_run_id_fkey FOREIGN KEY (team_id, placement_run_id) REFERENCES public.placement_runs(team_id, placement_run_id) ON DELETE RESTRICT;


--
-- Name: placement_outcomes placement_outcomes_team_id_placement_run_id_owner_profile__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_team_id_placement_run_id_owner_profile__fkey FOREIGN KEY (team_id, placement_run_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_runs placement_runs_replaces_run_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_replaces_run_ref FOREIGN KEY (team_id, replaces_placement_run_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_runs placement_runs_superseded_by_run_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_superseded_by_run_ref FOREIGN KEY (team_id, superseded_by_placement_run_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_runs placement_runs_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: placement_runs placement_runs_team_id_ingest_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_team_id_ingest_id_owner_profile_id_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_runs placement_runs_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: predicate_registration_events predicate_registration_events_team_id_assessment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_team_id_assessment_id_fkey FOREIGN KEY (team_id, assessment_id) REFERENCES public.placement_assessments(team_id, assessment_id) ON DELETE RESTRICT;


--
-- Name: predicate_registration_events predicate_registration_events_team_id_placement_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_team_id_placement_run_id_fkey FOREIGN KEY (team_id, placement_run_id) REFERENCES public.placement_runs(team_id, placement_run_id) ON DELETE RESTRICT;


--
-- Name: predicate_registration_events predicate_registration_events_team_id_placement_run_id_own_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_team_id_placement_run_id_own_fkey FOREIGN KEY (team_id, placement_run_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_ai_assessment_events relationship_conflict_ai_asse_team_id_assessment_attempt_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_events
    ADD CONSTRAINT relationship_conflict_ai_asse_team_id_assessment_attempt_i_fkey FOREIGN KEY (team_id, assessment_attempt_id) REFERENCES public.relationship_conflict_ai_assessment_attempts(team_id, assessment_attempt_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_ai_assessment_attempts relationship_conflict_ai_asse_team_id_selected_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_attempts
    ADD CONSTRAINT relationship_conflict_ai_asse_team_id_selected_position_id_fkey FOREIGN KEY (team_id, selected_position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_ai_assessment_attempts relationship_conflict_ai_assessment_at_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_attempts
    ADD CONSTRAINT relationship_conflict_ai_assessment_at_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_cases relationship_conflict_cases_team_id_predicate_key_predicat_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_cases
    ADD CONSTRAINT relationship_conflict_cases_team_id_predicate_key_predicat_fkey FOREIGN KEY (team_id, predicate_key, predicate_version) REFERENCES public.team_predicate_definitions(team_id, predicate_key, version) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_cases relationship_conflict_cases_team_id_subject_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_cases
    ADD CONSTRAINT relationship_conflict_cases_team_id_subject_entity_id_fkey FOREIGN KEY (team_id, subject_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_e_team_id_resolution_plan_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_e_team_id_resolution_plan_id_fkey FOREIGN KEY (team_id, resolution_plan_id) REFERENCES public.relationship_conflict_resolution_plans(team_id, resolution_plan_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_ev_team_id_system_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_ev_team_id_system_profile_id_fkey FOREIGN KEY (team_id, system_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_evidence_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_evidence_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_team_id_selected_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_team_id_selected_position_id_fkey FOREIGN KEY (team_id, selected_position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_team_id_target_fragment_id_t_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_team_id_target_fragment_id_t_fkey FOREIGN KEY (team_id, target_fragment_id, target_owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_events relationship_conflict_events_team_id_actor_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_team_id_actor_profile_id_fkey FOREIGN KEY (team_id, actor_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_events relationship_conflict_events_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_events relationship_conflict_events_team_id_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_team_id_position_id_fkey FOREIGN KEY (team_id, position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_events relationship_conflict_events_team_id_relationship_id_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_team_id_relationship_id_owner_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidenc_team_id_replacement_fragment_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidenc_team_id_replacement_fragment_fkey FOREIGN KEY (team_id, replacement_fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidenc_team_id_selected_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidenc_team_id_selected_position_id_fkey FOREIGN KEY (team_id, selected_position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidenc_team_id_target_fragment_id_t_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidenc_team_id_target_fragment_id_t_fkey FOREIGN KEY (team_id, target_fragment_id, target_owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_d_team_id_system_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidence_d_team_id_system_profile_id_fkey FOREIGN KEY (team_id, system_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_derivat_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidence_derivat_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_positio_team_id_relationship_id_owne_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_positio_team_id_relationship_id_owne_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_positio_team_id_support_id_owner_pro_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_positio_team_id_support_id_owner_pro_fkey FOREIGN KEY (team_id, support_id, owner_profile_id) REFERENCES public.relationship_evidence_supports(team_id, support_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_positio_team_id_verification_event_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_positio_team_id_verification_event_i_fkey FOREIGN KEY (team_id, verification_event_id, owner_profile_id) REFERENCES public.verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_position_members_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_position_members_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_team_id_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_position_members_team_id_position_id_fkey FOREIGN KEY (team_id, position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_positions relationship_conflict_positions_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_positions
    ADD CONSTRAINT relationship_conflict_positions_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_positions relationship_conflict_positions_team_id_object_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_positions
    ADD CONSTRAINT relationship_conflict_positions_team_id_object_entity_id_fkey FOREIGN KEY (team_id, object_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_positions relationship_conflict_positions_team_id_object_value_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_positions
    ADD CONSTRAINT relationship_conflict_positions_team_id_object_value_id_fkey FOREIGN KEY (team_id, object_value_id) REFERENCES public.value_records(team_id, value_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolut_team_id_assessment_attempt_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_resolution_plans
    ADD CONSTRAINT relationship_conflict_resolut_team_id_assessment_attempt_i_fkey FOREIGN KEY (team_id, assessment_attempt_id) REFERENCES public.relationship_conflict_ai_assessment_attempts(team_id, assessment_attempt_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolut_team_id_preferred_position_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_resolution_plans
    ADD CONSTRAINT relationship_conflict_resolut_team_id_preferred_position_i_fkey FOREIGN KEY (team_id, preferred_position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolution_plans_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_resolution_plans
    ADD CONSTRAINT relationship_conflict_resolution_plans_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_review_runs relationship_conflict_review_runs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_review_runs
    ADD CONSTRAINT relationship_conflict_review_runs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_events relationship_correction_events_original_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_events
    ADD CONSTRAINT relationship_correction_events_original_fk FOREIGN KEY (team_id, original_relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_events relationship_correction_events_submission_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_events
    ADD CONSTRAINT relationship_correction_events_submission_fk FOREIGN KEY (team_id, submission_id, owner_profile_id) REFERENCES public.relationship_correction_submissions(team_id, submission_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_events relationship_correction_events_successor_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_events
    ADD CONSTRAINT relationship_correction_events_successor_fk FOREIGN KEY (team_id, successor_relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_submissions relationship_correction_submissions_owner_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_submissions
    ADD CONSTRAINT relationship_correction_submissions_owner_fk FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_submissions relationship_correction_submissions_successor_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_submissions
    ADD CONSTRAINT relationship_correction_submissions_successor_fk FOREIGN KEY (team_id, successor_relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_submissions relationship_correction_submissions_target_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_submissions
    ADD CONSTRAINT relationship_correction_submissions_target_fk FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_cross_references relationship_cross_references_team_id_author_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_team_id_author_profile_id_fkey FOREIGN KEY (team_id, author_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_cross_references relationship_cross_references_team_id_source_relationship__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_team_id_source_relationship__fkey FOREIGN KEY (team_id, source_relationship_id, author_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_cross_references relationship_cross_references_team_id_target_relationship__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_team_id_target_relationship__fkey FOREIGN KEY (team_id, target_relationship_id) REFERENCES public.relationship_records(team_id, relationship_id) ON DELETE RESTRICT;


--
-- Name: relationship_cross_references relationship_cross_references_team_id_verification_event_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_team_id_verification_event_i_fkey FOREIGN KEY (team_id, verification_event_id, author_profile_id) REFERENCES public.verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_fragment_id_owner_pr_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_fragment_id_owner_pr_fkey FOREIGN KEY (team_id, fragment_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_observation_id_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_observation_id_owner_fkey FOREIGN KEY (team_id, observation_id, owner_profile_id) REFERENCES public.relationship_observations(team_id, observation_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_relationship_id_owne_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_relationship_id_owne_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_source_id_owner_prof_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_source_id_owner_prof_fkey FOREIGN KEY (team_id, source_id, owner_profile_id) REFERENCES public.evidence_sources(team_id, source_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_source_id_source_rev_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_source_id_source_rev_fkey FOREIGN KEY (team_id, source_id, source_revision_id, owner_profile_id) REFERENCES public.evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_source_revision_id_o_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_source_revision_id_o_fkey FOREIGN KEY (team_id, source_revision_id, owner_profile_id) REFERENCES public.evidence_source_revisions(team_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_verification_event_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_verification_event_i_fkey FOREIGN KEY (team_id, verification_event_id, owner_profile_id) REFERENCES public.verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_supports_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_supports_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_supports_team_id_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_team_id_source_id_fkey FOREIGN KEY (team_id, source_id) REFERENCES public.evidence_sources(team_id, source_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_supports_team_id_source_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_team_id_source_revision_id_fkey FOREIGN KEY (team_id, source_revision_id) REFERENCES public.evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_ingest_id_owner_profile__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_ingest_id_owner_profile__fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_object_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_object_entity_id_fkey FOREIGN KEY (team_id, object_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_object_value_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_object_value_id_fkey FOREIGN KEY (team_id, object_value_id) REFERENCES public.value_records(team_id, value_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_placement_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_placement_item_id_fkey FOREIGN KEY (team_id, placement_item_id) REFERENCES public.placement_items(team_id, placement_item_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_placement_item_id_owner__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_placement_item_id_owner__fkey FOREIGN KEY (team_id, placement_item_id, owner_profile_id) REFERENCES public.placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_relationship_id_owner_pr_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_relationship_id_owner_pr_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_subject_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_subject_entity_id_fkey FOREIGN KEY (team_id, subject_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_predicate_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_predicate_fkey FOREIGN KEY (team_id, predicate_key, predicate_version) REFERENCES public.team_predicate_definitions(team_id, predicate_key, version) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_identity_alias_owner_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_identity_alias_owner_fk FOREIGN KEY (team_id, identity_alias_of_relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_team_id_object_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_team_id_object_entity_id_fkey FOREIGN KEY (team_id, object_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_team_id_object_value_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_team_id_object_value_id_fkey FOREIGN KEY (team_id, object_value_id) REFERENCES public.value_records(team_id, value_id) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_team_id_subject_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_team_id_subject_entity_id_fkey FOREIGN KEY (team_id, subject_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_team_predicate_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_team_predicate_fkey FOREIGN KEY (team_id, predicate_key, predicate_version) REFERENCES public.team_predicate_definitions(team_id, predicate_key, version) ON DELETE RESTRICT;


--
-- Name: relationship_support_decision_events relationship_support_decision_eve_team_id_actor_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decision_eve_team_id_actor_profile_id_fkey FOREIGN KEY (team_id, actor_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_support_decision_events relationship_support_decision_eve_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decision_eve_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_support_decision_events relationship_support_decision_team_id_relationship_id_owne_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decision_team_id_relationship_id_owne_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_support_decision_events relationship_support_decision_team_id_support_id_owner_pro_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decision_team_id_support_id_owner_pro_fkey FOREIGN KEY (team_id, support_id, owner_profile_id) REFERENCES public.relationship_evidence_supports(team_id, support_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_transition_events relationship_transition_event_team_id_relationship_id_owne_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_transition_events
    ADD CONSTRAINT relationship_transition_event_team_id_relationship_id_owne_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_transition_events relationship_transition_event_team_id_support_decision_id__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_transition_events
    ADD CONSTRAINT relationship_transition_event_team_id_support_decision_id__fkey FOREIGN KEY (team_id, support_decision_id, owner_profile_id) REFERENCES public.relationship_support_decision_events(team_id, support_decision_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_transition_events relationship_transition_event_team_id_verification_event_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_transition_events
    ADD CONSTRAINT relationship_transition_event_team_id_verification_event_i_fkey FOREIGN KEY (team_id, verification_event_id, owner_profile_id) REFERENCES public.verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_transition_events relationship_transition_events_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_transition_events
    ADD CONSTRAINT relationship_transition_events_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_assessment_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_assessment_ref FOREIGN KEY (team_id, assessment_id) REFERENCES public.placement_assessments(team_id, assessment_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_ingest_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_ingest_id_owner_profile_id_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_observation_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_observation_id_owner_profile_id_fkey FOREIGN KEY (team_id, observation_id, owner_profile_id) REFERENCES public.relationship_observations(team_id, observation_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_placement_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_placement_item_id_fkey FOREIGN KEY (team_id, placement_item_id) REFERENCES public.placement_items(team_id, placement_item_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_placement_item_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_placement_item_id_owner_profile_id_fkey FOREIGN KEY (team_id, placement_item_id, owner_profile_id) REFERENCES public.placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_relationship_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_relationship_id_owner_profile_id_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: search_documents search_documents_embedding_contract_id_embedding_dimension_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_documents
    ADD CONSTRAINT search_documents_embedding_contract_id_embedding_dimension_fkey FOREIGN KEY (embedding_contract_id, embedding_dimensions) REFERENCES public.embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT;


--
-- Name: search_documents search_documents_projection_generation_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_documents
    ADD CONSTRAINT search_documents_projection_generation_fk FOREIGN KEY (team_id, projection_generation_id) REFERENCES public.search_projection_generations(team_id, projection_generation_id) ON DELETE RESTRICT;


--
-- Name: search_documents search_documents_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_documents
    ADD CONSTRAINT search_documents_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: search_index_generations search_index_generations_embedding_contract_id_embedding_d_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_index_generations
    ADD CONSTRAINT search_index_generations_embedding_contract_id_embedding_d_fkey FOREIGN KEY (embedding_contract_id, embedding_dimensions) REFERENCES public.embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT;


--
-- Name: search_projection_generations search_projection_generations_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_projection_generations
    ADD CONSTRAINT search_projection_generations_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE RESTRICT;


--
-- Name: semantic_profile_refs semantic_profile_refs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.semantic_profile_refs
    ADD CONSTRAINT semantic_profile_refs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE CASCADE;


--
-- Name: semantic_profile_refs semantic_profile_refs_team_id_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.semantic_profile_refs
    ADD CONSTRAINT semantic_profile_refs_team_id_profile_id_fkey FOREIGN KEY (team_id, profile_id) REFERENCES public.team_profiles(team_id, id) ON DELETE CASCADE;


--
-- Name: semantic_team_refs semantic_team_refs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.semantic_team_refs
    ADD CONSTRAINT semantic_team_refs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;


--
-- Name: sso_control_admin_groups sso_control_admin_groups_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_admin_groups
    ADD CONSTRAINT sso_control_admin_groups_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE RESTRICT;


--
-- Name: sso_control_oauth_states sso_control_oauth_states_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_oauth_states
    ADD CONSTRAINT sso_control_oauth_states_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE RESTRICT;


--
-- Name: sso_control_sessions sso_control_sessions_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_sessions
    ADD CONSTRAINT sso_control_sessions_identity_id_fkey FOREIGN KEY (identity_id) REFERENCES public.sso_identities(id) ON DELETE RESTRICT;


--
-- Name: sso_control_sessions sso_control_sessions_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_sessions
    ADD CONSTRAINT sso_control_sessions_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE RESTRICT;


--
-- Name: sso_directory_connectors sso_directory_connectors_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_connectors
    ADD CONSTRAINT sso_directory_connectors_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE RESTRICT;


--
-- Name: sso_directory_group_bindings sso_directory_group_bindings_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_bindings
    ADD CONSTRAINT sso_directory_group_bindings_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_group_bindings sso_directory_group_bindings_connector_id_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_bindings
    ADD CONSTRAINT sso_directory_group_bindings_connector_id_group_id_fkey FOREIGN KEY (connector_id, group_id) REFERENCES public.sso_directory_groups(connector_id, id) ON DELETE RESTRICT;


--
-- Name: sso_directory_group_bindings sso_directory_group_bindings_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_bindings
    ADD CONSTRAINT sso_directory_group_bindings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: sso_directory_group_memberships sso_directory_group_memberships_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_memberships
    ADD CONSTRAINT sso_directory_group_memberships_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_group_memberships sso_directory_group_memberships_connector_id_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_memberships
    ADD CONSTRAINT sso_directory_group_memberships_connector_id_group_id_fkey FOREIGN KEY (connector_id, group_id) REFERENCES public.sso_directory_groups(connector_id, id) ON DELETE CASCADE;


--
-- Name: sso_directory_group_memberships sso_directory_group_memberships_connector_id_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_memberships
    ADD CONSTRAINT sso_directory_group_memberships_connector_id_user_id_fkey FOREIGN KEY (connector_id, user_id) REFERENCES public.sso_directory_users(connector_id, id) ON DELETE CASCADE;


--
-- Name: sso_directory_groups sso_directory_groups_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_groups
    ADD CONSTRAINT sso_directory_groups_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_issues sso_directory_issues_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_issues
    ADD CONSTRAINT sso_directory_issues_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_issues sso_directory_issues_connector_id_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_issues
    ADD CONSTRAINT sso_directory_issues_connector_id_group_id_fkey FOREIGN KEY (connector_id, group_id) REFERENCES public.sso_directory_groups(connector_id, id) ON DELETE CASCADE;


--
-- Name: sso_directory_oauth_tokens sso_directory_oauth_tokens_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_oauth_tokens
    ADD CONSTRAINT sso_directory_oauth_tokens_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_users sso_directory_users_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_users
    ADD CONSTRAINT sso_directory_users_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_users sso_directory_users_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_users
    ADD CONSTRAINT sso_directory_users_identity_id_fkey FOREIGN KEY (identity_id) REFERENCES public.sso_identities(id) ON DELETE RESTRICT;


--
-- Name: sso_entitlement_cache sso_entitlement_cache_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_entitlement_cache
    ADD CONSTRAINT sso_entitlement_cache_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE CASCADE;


--
-- Name: sso_group_mappings sso_group_mappings_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_group_mappings
    ADD CONSTRAINT sso_group_mappings_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE CASCADE;


--
-- Name: sso_group_mappings sso_group_mappings_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_group_mappings
    ADD CONSTRAINT sso_group_mappings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;


--
-- Name: sso_identities sso_identities_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_identities
    ADD CONSTRAINT sso_identities_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE CASCADE;


--
-- Name: sso_oauth_states sso_oauth_states_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_oauth_states
    ADD CONSTRAINT sso_oauth_states_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE CASCADE;


--
-- Name: sso_sessions sso_sessions_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_sessions
    ADD CONSTRAINT sso_sessions_identity_id_fkey FOREIGN KEY (identity_id) REFERENCES public.sso_identities(id) ON DELETE CASCADE;


--
-- Name: sso_sessions sso_sessions_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_sessions
    ADD CONSTRAINT sso_sessions_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE CASCADE;


--
-- Name: sso_sessions sso_sessions_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_sessions
    ADD CONSTRAINT sso_sessions_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;


--
-- Name: sso_sessions sso_sessions_team_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_sessions
    ADD CONSTRAINT sso_sessions_team_profile_id_fkey FOREIGN KEY (team_profile_id) REFERENCES public.team_profiles(id) ON DELETE CASCADE;


--
-- Name: submission_holds submission_holds_team_id_assessment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_team_id_assessment_id_fkey FOREIGN KEY (team_id, assessment_id) REFERENCES public.placement_assessments(team_id, assessment_id) ON DELETE RESTRICT;


--
-- Name: submission_holds submission_holds_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: submission_holds submission_holds_team_id_placement_run_id_ingest_id_owner__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_team_id_placement_run_id_ingest_id_owner__fkey FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: submission_quarantine_payloads submission_quarantine_payload_team_id_placement_run_id_ing_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_quarantine_payloads
    ADD CONSTRAINT submission_quarantine_payload_team_id_placement_run_id_ing_fkey FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: submission_quarantine_tombstones submission_quarantine_tombsto_team_id_fragment_id_ingest_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_quarantine_tombstones
    ADD CONSTRAINT submission_quarantine_tombsto_team_id_fragment_id_ingest_i_fkey FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: team_predicate_definitions team_predicate_definitions_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_predicate_definitions
    ADD CONSTRAINT team_predicate_definitions_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE RESTRICT;


--
-- Name: team_profiles team_profiles_sso_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_profiles
    ADD CONSTRAINT team_profiles_sso_identity_id_fkey FOREIGN KEY (sso_identity_id) REFERENCES public.sso_identities(id) ON DELETE SET NULL;


--
-- Name: team_profiles team_profiles_sso_owner_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_profiles
    ADD CONSTRAINT team_profiles_sso_owner_identity_id_fkey FOREIGN KEY (sso_owner_identity_id) REFERENCES public.sso_identities(id) ON DELETE SET NULL;


--
-- Name: team_profiles team_profiles_sso_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_profiles
    ADD CONSTRAINT team_profiles_sso_provider_id_fkey FOREIGN KEY (sso_provider_id) REFERENCES public.sso_providers(id) ON DELETE SET NULL;


--
-- Name: team_profiles team_profiles_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_profiles
    ADD CONSTRAINT team_profiles_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: teams teams_directory_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_directory_connector_id_fkey FOREIGN KEY (directory_connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE RESTRICT;


--
-- Name: teams teams_directory_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_directory_group_id_fkey FOREIGN KEY (directory_group_id) REFERENCES public.sso_directory_groups(id) ON DELETE RESTRICT;


--
-- Name: usage_metric_buckets usage_metric_buckets_key_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_metric_buckets
    ADD CONSTRAINT usage_metric_buckets_key_id_fkey FOREIGN KEY (key_id) REFERENCES public.team_profiles(id) ON DELETE CASCADE;


--
-- Name: usage_metric_buckets usage_metric_buckets_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_metric_buckets
    ADD CONSTRAINT usage_metric_buckets_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;


--
-- Name: user_portal_sessions user_portal_sessions_key_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_portal_sessions
    ADD CONSTRAINT user_portal_sessions_key_id_fkey FOREIGN KEY (key_id) REFERENCES public.team_profiles(id) ON DELETE CASCADE;


--
-- Name: v2_compatibility_markers v2_compatibility_markers_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_compatibility_markers
    ADD CONSTRAINT v2_compatibility_markers_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE RESTRICT;


--
-- Name: v2_migration_checkpoints v2_migration_checkpoints_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_checkpoints
    ADD CONSTRAINT v2_migration_checkpoints_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE CASCADE;


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE CASCADE;


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.team_profiles(team_id, id) ON DELETE RESTRICT;


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_team_id_placement_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_team_id_placement_item_id_fkey FOREIGN KEY (team_id, placement_item_id) REFERENCES public.placement_items(team_id, placement_item_id) ON DELETE RESTRICT;


--
-- Name: v2_migration_errors v2_migration_errors_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_errors
    ADD CONSTRAINT v2_migration_errors_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE CASCADE;


--
-- Name: v2_migration_exclusions v2_migration_exclusions_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_exclusions
    ADD CONSTRAINT v2_migration_exclusions_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE CASCADE;


--
-- Name: v2_migration_gate_results v2_migration_gate_results_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_gate_results
    ADD CONSTRAINT v2_migration_gate_results_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE CASCADE;


--
-- Name: v2_migration_operator_actions v2_migration_operator_actions_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_operator_actions
    ADD CONSTRAINT v2_migration_operator_actions_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE SET NULL;


--
-- Name: v2_migration_source_maps v2_migration_source_maps_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_source_maps
    ADD CONSTRAINT v2_migration_source_maps_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE CASCADE;


--
-- Name: value_records value_records_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.value_records
    ADD CONSTRAINT value_records_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE RESTRICT;


--
-- Name: verification_events verification_events_assessment_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_events
    ADD CONSTRAINT verification_events_assessment_ref FOREIGN KEY (team_id, assessment_id) REFERENCES public.placement_assessments(team_id, assessment_id) ON DELETE RESTRICT;


--
-- Name: verification_events verification_events_team_id_observation_id_owner_profile_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_events
    ADD CONSTRAINT verification_events_team_id_observation_id_owner_profile_i_fkey FOREIGN KEY (team_id, observation_id, owner_profile_id) REFERENCES public.relationship_observations(team_id, observation_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: verification_events verification_events_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_events
    ADD CONSTRAINT verification_events_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: app_config; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.app_config ENABLE ROW LEVEL SECURITY;

--
-- Name: app_config app_config_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY app_config_system_access ON public.app_config USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: audit_log; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.audit_log ENABLE ROW LEVEL SECURITY;

--
-- Name: audit_log audit_log_insert_all; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY audit_log_insert_all ON public.audit_log FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text, 'system'::text])) AND ((current_setting('app.tx_mode'::text, true) = 'system'::text) OR (team_id IS NULL) OR (team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))));


--
-- Name: audit_log audit_log_self_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY audit_log_self_access ON public.audit_log FOR SELECT USING ((team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)));


--
-- Name: audit_log audit_log_system_read_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY audit_log_system_read_access ON public.audit_log FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: community_memberships; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.community_memberships ENABLE ROW LEVEL SECURITY;

--
-- Name: community_memberships community_memberships_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_memberships_insert ON public.community_memberships FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_memberships community_memberships_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_memberships_select ON public.community_memberships FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_memberships community_memberships_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_memberships_update ON public.community_memberships FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_records; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.community_records ENABLE ROW LEVEL SECURITY;

--
-- Name: community_records community_records_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_records_insert ON public.community_records FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_records community_records_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_records_select ON public.community_records FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_records community_records_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_records_update ON public.community_records FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_snapshot_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.community_snapshot_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: community_snapshot_runs community_snapshot_runs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_snapshot_runs_insert ON public.community_snapshot_runs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_snapshot_runs community_snapshot_runs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_snapshot_runs_select ON public.community_snapshot_runs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_snapshot_runs community_snapshot_runs_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_snapshot_runs_update ON public.community_snapshot_runs FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_sources; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.community_sources ENABLE ROW LEVEL SECURITY;

--
-- Name: community_sources community_sources_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_sources_insert ON public.community_sources FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_sources community_sources_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_sources_select ON public.community_sources FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_sources community_sources_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_sources_update ON public.community_sources FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_summary_attempts; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.community_summary_attempts ENABLE ROW LEVEL SECURITY;

--
-- Name: community_summary_attempts community_summary_attempts_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_summary_attempts_insert ON public.community_summary_attempts FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_summary_attempts community_summary_attempts_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_summary_attempts_select ON public.community_summary_attempts FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: dream_cycle_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.dream_cycle_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: dream_cycle_runs dream_cycle_runs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY dream_cycle_runs_insert ON public.dream_cycle_runs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (initiated_by_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: dream_cycle_runs dream_cycle_runs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY dream_cycle_runs_select ON public.dream_cycle_runs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: dream_cycle_runs dream_cycle_runs_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY dream_cycle_runs_update ON public.dream_cycle_runs FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (initiated_by_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (initiated_by_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: dream_path_evaluations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.dream_path_evaluations ENABLE ROW LEVEL SECURITY;

--
-- Name: dream_path_evaluations dream_path_evaluations_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY dream_path_evaluations_insert ON public.dream_path_evaluations FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: dream_path_evaluations dream_path_evaluations_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY dream_path_evaluations_select ON public.dream_path_evaluations FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: embedding_jobs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.embedding_jobs ENABLE ROW LEVEL SECURITY;

--
-- Name: embedding_jobs embedding_jobs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY embedding_jobs_insert ON public.embedding_jobs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: embedding_jobs embedding_jobs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY embedding_jobs_select ON public.embedding_jobs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: embedding_jobs embedding_jobs_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY embedding_jobs_update ON public.embedding_jobs FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_correction_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.entity_correction_events ENABLE ROW LEVEL SECURITY;

--
-- Name: entity_correction_events entity_correction_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_correction_events_insert ON public.entity_correction_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_correction_events entity_correction_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_correction_events_select ON public.entity_correction_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_correction_plans; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.entity_correction_plans ENABLE ROW LEVEL SECURITY;

--
-- Name: entity_correction_plans entity_correction_plans_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_correction_plans_insert ON public.entity_correction_plans FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_correction_plans entity_correction_plans_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_correction_plans_select ON public.entity_correction_plans FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_correction_plans entity_correction_plans_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_correction_plans_update ON public.entity_correction_plans FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_names; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.entity_names ENABLE ROW LEVEL SECURITY;

--
-- Name: entity_names entity_names_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_names_insert ON public.entity_names FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_names entity_names_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_names_select ON public.entity_names FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_names entity_names_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_names_update ON public.entity_names FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_records; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.entity_records ENABLE ROW LEVEL SECURITY;

--
-- Name: entity_records entity_records_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_records_insert ON public.entity_records FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_records entity_records_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_records_select ON public.entity_records FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_records entity_records_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_records_update ON public.entity_records FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_resolution_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.entity_resolution_events ENABLE ROW LEVEL SECURITY;

--
-- Name: entity_resolution_events entity_resolution_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_resolution_events_insert ON public.entity_resolution_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_resolution_events entity_resolution_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_resolution_events_select ON public.entity_resolution_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_fragments; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_fragments ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_fragments evidence_fragments_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_fragments_insert ON public.evidence_fragments FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_fragments evidence_fragments_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_fragments_select ON public.evidence_fragments FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_lifecycle_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_lifecycle_events ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_lifecycle_events_insert ON public.evidence_lifecycle_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_lifecycle_events_select ON public.evidence_lifecycle_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_lifecycle_operations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_lifecycle_operations ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_lifecycle_operations_insert ON public.evidence_lifecycle_operations FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_lifecycle_operations_select ON public.evidence_lifecycle_operations FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_quarantines; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_quarantines ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_quarantines evidence_quarantines_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_quarantines_insert ON public.evidence_quarantines FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_quarantines evidence_quarantines_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_quarantines_select ON public.evidence_quarantines FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_quarantines evidence_quarantines_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_quarantines_update ON public.evidence_quarantines FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_security_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_security_events ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_security_events evidence_security_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_security_events_insert ON public.evidence_security_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_security_events evidence_security_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_security_events_select ON public.evidence_security_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_security_signals; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_security_signals ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_security_signals evidence_security_signals_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_security_signals_insert ON public.evidence_security_signals FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_security_signals evidence_security_signals_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_security_signals_select ON public.evidence_security_signals FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_source_revisions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_source_revisions ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_source_revisions evidence_source_revisions_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_source_revisions_insert ON public.evidence_source_revisions FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_source_revisions evidence_source_revisions_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_source_revisions_select ON public.evidence_source_revisions FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_sources; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_sources ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_sources evidence_sources_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_sources_insert ON public.evidence_sources FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_sources evidence_sources_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_sources_select ON public.evidence_sources FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_sources evidence_sources_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_sources_update ON public.evidence_sources FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: hypotheses; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.hypotheses ENABLE ROW LEVEL SECURITY;

--
-- Name: hypotheses hypotheses_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypotheses_insert ON public.hypotheses FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (created_by_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: hypotheses hypotheses_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypotheses_select ON public.hypotheses FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: hypotheses hypotheses_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypotheses_update ON public.hypotheses FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: hypothesis_derivation_sources; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.hypothesis_derivation_sources ENABLE ROW LEVEL SECURITY;

--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypothesis_derivation_sources_insert ON public.hypothesis_derivation_sources FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypothesis_derivation_sources_select ON public.hypothesis_derivation_sources FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: hypothesis_feedback_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.hypothesis_feedback_events ENABLE ROW LEVEL SECURITY;

--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypothesis_feedback_events_insert ON public.hypothesis_feedback_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (actor_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypothesis_feedback_events_select ON public.hypothesis_feedback_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: knowledge_ingests; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.knowledge_ingests ENABLE ROW LEVEL SECURITY;

--
-- Name: knowledge_ingests knowledge_ingests_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY knowledge_ingests_insert ON public.knowledge_ingests FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: knowledge_ingests knowledge_ingests_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY knowledge_ingests_select ON public.knowledge_ingests FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: knowledge_ingests knowledge_ingests_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY knowledge_ingests_update ON public.knowledge_ingests FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: operation_logs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.operation_logs ENABLE ROW LEVEL SECURITY;

--
-- Name: operation_logs operation_logs_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY operation_logs_system_access ON public.operation_logs USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: placement_assessments; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.placement_assessments ENABLE ROW LEVEL SECURITY;

--
-- Name: placement_assessments placement_assessments_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_assessments_insert ON public.placement_assessments FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_assessments placement_assessments_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_assessments_select ON public.placement_assessments FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_items; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.placement_items ENABLE ROW LEVEL SECURITY;

--
-- Name: placement_items placement_items_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_items_insert ON public.placement_items FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_items placement_items_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_items_select ON public.placement_items FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_items placement_items_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_items_update ON public.placement_items FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_outcomes; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.placement_outcomes ENABLE ROW LEVEL SECURITY;

--
-- Name: placement_outcomes placement_outcomes_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_outcomes_insert ON public.placement_outcomes FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_outcomes placement_outcomes_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_outcomes_select ON public.placement_outcomes FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.placement_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: placement_runs placement_runs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_runs_insert ON public.placement_runs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_runs placement_runs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_runs_select ON public.placement_runs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_runs placement_runs_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_runs_update ON public.placement_runs FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: predicate_registration_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.predicate_registration_events ENABLE ROW LEVEL SECURITY;

--
-- Name: predicate_registration_events predicate_registration_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY predicate_registration_events_insert ON public.predicate_registration_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: predicate_registration_events predicate_registration_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY predicate_registration_events_select ON public.predicate_registration_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: recall_feedback_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.recall_feedback_events ENABLE ROW LEVEL SECURITY;

--
-- Name: recall_feedback_events recall_feedback_events_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY recall_feedback_events_system_access ON public.recall_feedback_events USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: relationship_conflict_ai_assessment_attempts; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_ai_assessment_attempts ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_ai_assessment_attempts relationship_conflict_ai_assessment_attempts_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_ai_assessment_attempts_insert ON public.relationship_conflict_ai_assessment_attempts FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_ai_assessment_attempts relationship_conflict_ai_assessment_attempts_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_ai_assessment_attempts_select ON public.relationship_conflict_ai_assessment_attempts FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_ai_assessment_attempts relationship_conflict_ai_assessment_attempts_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_ai_assessment_attempts_update ON public.relationship_conflict_ai_assessment_attempts FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_ai_assessment_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_ai_assessment_events ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_ai_assessment_events relationship_conflict_ai_assessment_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_ai_assessment_events_insert ON public.relationship_conflict_ai_assessment_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_ai_assessment_events relationship_conflict_ai_assessment_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_ai_assessment_events_select ON public.relationship_conflict_ai_assessment_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_cases; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_cases ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_cases relationship_conflict_cases_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_cases_insert ON public.relationship_conflict_cases FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_cases relationship_conflict_cases_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_cases_select ON public.relationship_conflict_cases FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_cases relationship_conflict_cases_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_cases_update ON public.relationship_conflict_cases FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_derived_evidence_tasks; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_derived_evidence_tasks ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_evidence_tasks_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_derived_evidence_tasks_insert ON public.relationship_conflict_derived_evidence_tasks FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_evidence_tasks_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_derived_evidence_tasks_select ON public.relationship_conflict_derived_evidence_tasks FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_evidence_tasks_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_derived_evidence_tasks_update ON public.relationship_conflict_derived_evidence_tasks FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_events ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_events relationship_conflict_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_events_insert ON public.relationship_conflict_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_events relationship_conflict_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_events_select ON public.relationship_conflict_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_evidence_derivations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_evidence_derivations ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_derivations_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_evidence_derivations_insert ON public.relationship_conflict_evidence_derivations FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_derivations_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_evidence_derivations_select ON public.relationship_conflict_evidence_derivations FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_position_members; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_position_members ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_position_members_insert ON public.relationship_conflict_position_members FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_position_members_select ON public.relationship_conflict_position_members FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_position_members_update ON public.relationship_conflict_position_members FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_positions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_positions ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_positions relationship_conflict_positions_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_positions_insert ON public.relationship_conflict_positions FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_positions relationship_conflict_positions_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_positions_select ON public.relationship_conflict_positions FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_positions relationship_conflict_positions_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_positions_update ON public.relationship_conflict_positions FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_resolution_plans; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_resolution_plans ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolution_plans_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_resolution_plans_insert ON public.relationship_conflict_resolution_plans FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolution_plans_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_resolution_plans_select ON public.relationship_conflict_resolution_plans FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolution_plans_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_resolution_plans_update ON public.relationship_conflict_resolution_plans FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_review_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_review_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_review_runs relationship_conflict_review_runs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_review_runs_insert ON public.relationship_conflict_review_runs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_review_runs relationship_conflict_review_runs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_review_runs_select ON public.relationship_conflict_review_runs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_review_runs relationship_conflict_review_runs_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_review_runs_update ON public.relationship_conflict_review_runs FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_correction_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_correction_events ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_correction_events relationship_correction_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_correction_events_insert ON public.relationship_correction_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_correction_events relationship_correction_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_correction_events_select ON public.relationship_correction_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_correction_submissions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_correction_submissions ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_correction_submissions relationship_correction_submissions_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_correction_submissions_insert ON public.relationship_correction_submissions FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_correction_submissions relationship_correction_submissions_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_correction_submissions_select ON public.relationship_correction_submissions FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_correction_submissions relationship_correction_submissions_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_correction_submissions_update ON public.relationship_correction_submissions FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_cross_references; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_cross_references ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_cross_references relationship_cross_references_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_cross_references_insert ON public.relationship_cross_references FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (author_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_cross_references relationship_cross_references_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_cross_references_select ON public.relationship_cross_references FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_evidence_supports; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_evidence_supports ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_evidence_supports relationship_evidence_supports_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_evidence_supports_insert ON public.relationship_evidence_supports FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_evidence_supports relationship_evidence_supports_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_evidence_supports_select ON public.relationship_evidence_supports FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_observations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_observations ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_observations relationship_observations_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_observations_insert ON public.relationship_observations FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_observations relationship_observations_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_observations_select ON public.relationship_observations FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_records; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_records ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_records relationship_records_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_records_insert ON public.relationship_records FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_records relationship_records_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_records_select ON public.relationship_records FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_records relationship_records_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_records_update ON public.relationship_records FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_support_decision_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_support_decision_events ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_support_decision_events relationship_support_decision_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_support_decision_events_insert ON public.relationship_support_decision_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_support_decision_events relationship_support_decision_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_support_decision_events_select ON public.relationship_support_decision_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_transition_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_transition_events ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_transition_events relationship_transition_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_transition_events_insert ON public.relationship_transition_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_transition_events relationship_transition_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_transition_events_select ON public.relationship_transition_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: review_tasks; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.review_tasks ENABLE ROW LEVEL SECURITY;

--
-- Name: review_tasks review_tasks_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY review_tasks_insert ON public.review_tasks FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: review_tasks review_tasks_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY review_tasks_select ON public.review_tasks FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: review_tasks review_tasks_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY review_tasks_update ON public.review_tasks FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: search_documents; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.search_documents ENABLE ROW LEVEL SECURITY;

--
-- Name: search_documents search_documents_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_documents_insert ON public.search_documents FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: search_documents search_documents_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_documents_select ON public.search_documents FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: search_documents search_documents_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_documents_update ON public.search_documents FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: search_projection_generations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.search_projection_generations ENABLE ROW LEVEL SECURITY;

--
-- Name: search_projection_generations search_projection_generations_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_projection_generations_insert ON public.search_projection_generations FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: search_projection_generations search_projection_generations_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_projection_generations_select ON public.search_projection_generations FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: search_projection_generations search_projection_generations_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_projection_generations_update ON public.search_projection_generations FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: semantic_profile_refs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.semantic_profile_refs ENABLE ROW LEVEL SECURITY;

--
-- Name: semantic_profile_refs semantic_profile_refs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY semantic_profile_refs_insert ON public.semantic_profile_refs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: semantic_profile_refs semantic_profile_refs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY semantic_profile_refs_select ON public.semantic_profile_refs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)));


--
-- Name: semantic_team_refs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.semantic_team_refs ENABLE ROW LEVEL SECURITY;

--
-- Name: semantic_team_refs semantic_team_refs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY semantic_team_refs_insert ON public.semantic_team_refs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: semantic_team_refs semantic_team_refs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY semantic_team_refs_select ON public.semantic_team_refs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)));


--
-- Name: sso_control_admin_groups; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_control_admin_groups ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_control_admin_groups sso_control_admin_groups_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_control_admin_groups_system_access ON public.sso_control_admin_groups USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_control_oauth_states; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_control_oauth_states ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_control_oauth_states sso_control_oauth_states_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_control_oauth_states_system_access ON public.sso_control_oauth_states USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_control_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_control_sessions ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_control_sessions sso_control_sessions_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_control_sessions_system_access ON public.sso_control_sessions USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_directory_connectors; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_connectors ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_connectors sso_directory_connectors_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_connectors_system_access ON public.sso_directory_connectors USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_directory_group_bindings; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_group_bindings ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_group_bindings sso_directory_group_bindings_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_group_bindings_system_access ON public.sso_directory_group_bindings USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_directory_group_memberships; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_group_memberships ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_group_memberships sso_directory_group_memberships_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_group_memberships_system_access ON public.sso_directory_group_memberships USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_directory_groups; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_groups ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_groups sso_directory_groups_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_groups_system_access ON public.sso_directory_groups USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_directory_issues; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_issues ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_issues sso_directory_issues_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_issues_system_access ON public.sso_directory_issues USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_directory_oauth_tokens; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_oauth_tokens ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_oauth_tokens sso_directory_oauth_tokens_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_oauth_tokens_system_access ON public.sso_directory_oauth_tokens USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_directory_users; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_users ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_users sso_directory_users_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_users_system_access ON public.sso_directory_users USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_entitlement_cache; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_entitlement_cache ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_entitlement_cache sso_entitlement_cache_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_entitlement_cache_system_access ON public.sso_entitlement_cache USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_group_mappings; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_group_mappings ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_group_mappings sso_group_mappings_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_group_mappings_system_access ON public.sso_group_mappings USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_group_mappings sso_group_mappings_team_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_group_mappings_team_read ON public.sso_group_mappings FOR SELECT USING ((team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)));


--
-- Name: sso_identities; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_identities ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_identities sso_identities_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_identities_system_access ON public.sso_identities USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_oauth_states; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_oauth_states ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_oauth_states sso_oauth_states_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_oauth_states_system_access ON public.sso_oauth_states USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_providers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_providers ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_providers sso_providers_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_providers_system_access ON public.sso_providers USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_sessions ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_sessions sso_sessions_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_sessions_system_access ON public.sso_sessions USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: submission_holds; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.submission_holds ENABLE ROW LEVEL SECURITY;

--
-- Name: submission_holds submission_holds_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_holds_insert ON public.submission_holds FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: submission_holds submission_holds_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_holds_select ON public.submission_holds FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: submission_quarantine_payloads; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.submission_quarantine_payloads ENABLE ROW LEVEL SECURITY;

--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_owner_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_payloads_owner_insert ON public.submission_quarantine_payloads FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_system_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_payloads_system_delete ON public.submission_quarantine_payloads FOR DELETE USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_system_only; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_payloads_system_only ON public.submission_quarantine_payloads FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_system_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_payloads_system_update ON public.submission_quarantine_payloads FOR UPDATE USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text]))) WITH CHECK ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- Name: submission_quarantine_tombstones; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.submission_quarantine_tombstones ENABLE ROW LEVEL SECURITY;

--
-- Name: submission_quarantine_tombstones submission_quarantine_tombstones_owner_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_tombstones_owner_insert ON public.submission_quarantine_tombstones FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: submission_quarantine_tombstones submission_quarantine_tombstones_system_only; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_tombstones_system_only ON public.submission_quarantine_tombstones FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- Name: team_predicate_definitions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.team_predicate_definitions ENABLE ROW LEVEL SECURITY;

--
-- Name: team_predicate_definitions team_predicate_definitions_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_predicate_definitions_insert ON public.team_predicate_definitions FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: team_predicate_definitions team_predicate_definitions_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_predicate_definitions_select ON public.team_predicate_definitions FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: team_profiles; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.team_profiles ENABLE ROW LEVEL SECURITY;

--
-- Name: team_profiles team_profiles_self_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_profiles_self_access ON public.team_profiles USING ((team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))) WITH CHECK ((team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)));


--
-- Name: team_profiles team_profiles_system_conflict_insert_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_profiles_system_conflict_insert_access ON public.team_profiles FOR INSERT WITH CHECK ((((current_setting('app.tx_mode'::text, true) = 'migration'::text) OR ((current_setting('app.tx_mode'::text, true) = 'system'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))) AND ((auth_source)::text = 'system'::text) AND is_system AND (revoked_at IS NOT NULL)));


--
-- Name: team_profiles team_profiles_system_read_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_profiles_system_read_access ON public.team_profiles FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: team_profiles team_profiles_system_sso_insert_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_profiles_system_sso_insert_access ON public.team_profiles FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = 'system'::text) AND ((auth_source)::text = 'sso'::text) AND (sso_identity_id IS NOT NULL) AND (sso_provider_id IS NOT NULL) AND (NULLIF(sso_subject, ''::text) IS NOT NULL)));


--
-- Name: team_profiles team_profiles_system_update_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_profiles_system_update_access ON public.team_profiles FOR UPDATE USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: teams; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.teams ENABLE ROW LEVEL SECURITY;

--
-- Name: teams teams_directory_system_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY teams_directory_system_insert ON public.teams FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = 'system'::text) AND ((directory_managed AND (directory_connector_id IS NOT NULL) AND (directory_group_id IS NOT NULL)) OR ((NOT directory_managed) AND (directory_connector_id IS NULL) AND (directory_group_id IS NULL)))));


--
-- Name: teams teams_directory_system_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY teams_directory_system_update ON public.teams FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = 'system'::text) AND directory_managed)) WITH CHECK (((current_setting('app.tx_mode'::text, true) = 'system'::text) AND ((directory_managed AND (directory_connector_id IS NOT NULL) AND (directory_group_id IS NOT NULL)) OR ((NOT directory_managed) AND (directory_connector_id IS NULL) AND (directory_group_id IS NULL)))));


--
-- Name: teams teams_self_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY teams_self_access ON public.teams USING ((id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))) WITH CHECK ((id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)));


--
-- Name: teams teams_system_read_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY teams_system_read_access ON public.teams FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: telemetry_first_disposition_backfill_state; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.telemetry_first_disposition_backfill_state ENABLE ROW LEVEL SECURITY;

--
-- Name: telemetry_first_disposition_backfill_state telemetry_first_disposition_backfill_state_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY telemetry_first_disposition_backfill_state_system_access ON public.telemetry_first_disposition_backfill_state USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: usage_metric_buckets; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.usage_metric_buckets ENABLE ROW LEVEL SECURITY;

--
-- Name: usage_metric_buckets usage_metric_buckets_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY usage_metric_buckets_system_access ON public.usage_metric_buckets USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: usage_metric_buckets usage_metric_buckets_team_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY usage_metric_buckets_team_read ON public.usage_metric_buckets FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = 'team'::text) AND ((team_id)::text = current_setting('app.current_team_id'::text, true))));


--
-- Name: user_portal_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.user_portal_sessions ENABLE ROW LEVEL SECURITY;

--
-- Name: user_portal_sessions user_portal_sessions_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_portal_sessions_system_access ON public.user_portal_sessions USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: v2_compatibility_markers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_compatibility_markers ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_compatibility_markers v2_compatibility_markers_system_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_compatibility_markers_system_insert ON public.v2_compatibility_markers FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = 'system'::text) AND (marker_kind = 'v2_cutover'::text) AND (status = 'compatible'::text)));


--
-- Name: v2_compatibility_markers v2_compatibility_markers_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_compatibility_markers_system_select ON public.v2_compatibility_markers FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: v2_migration_checkpoints; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_checkpoints ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_checkpoints v2_migration_checkpoints_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_checkpoints_system_select ON public.v2_migration_checkpoints FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: v2_migration_corpus_items; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_corpus_items ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_corpus_items_system_select ON public.v2_migration_corpus_items FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: v2_migration_errors; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_errors ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_errors v2_migration_errors_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_errors_system_select ON public.v2_migration_errors FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: v2_migration_exclusions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_exclusions ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_exclusions v2_migration_exclusions_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_exclusions_system_select ON public.v2_migration_exclusions FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: v2_migration_gate_results; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_gate_results ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_gate_results v2_migration_gate_results_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_gate_results_system_select ON public.v2_migration_gate_results FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: v2_migration_operator_actions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_operator_actions ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_operator_actions v2_migration_operator_actions_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_operator_actions_system_select ON public.v2_migration_operator_actions FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: v2_migration_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_runs v2_migration_runs_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_runs_system_select ON public.v2_migration_runs FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: v2_migration_source_maps; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_source_maps ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_source_maps v2_migration_source_maps_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_source_maps_system_select ON public.v2_migration_source_maps FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: value_records; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.value_records ENABLE ROW LEVEL SECURITY;

--
-- Name: value_records value_records_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY value_records_insert ON public.value_records FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: value_records value_records_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY value_records_select ON public.value_records FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: value_records value_records_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY value_records_update ON public.value_records FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: verification_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.verification_events ENABLE ROW LEVEL SECURITY;

--
-- Name: verification_events verification_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY verification_events_insert ON public.verification_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: verification_events verification_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY verification_events_select ON public.verification_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
