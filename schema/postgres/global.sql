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


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
