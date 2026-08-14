--
-- PostgreSQL database dump
--



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

SET default_tablespace = '';

SET default_table_access_method = heap;

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
-- Name: v2_migration_runs v2_migration_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_runs
    ADD CONSTRAINT v2_migration_runs_pkey PRIMARY KEY (run_id);


--
-- Name: idx_v2_migration_runs_single_active; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_v2_migration_runs_single_active ON public.v2_migration_runs USING btree ((true)) WHERE (state = ANY (ARRAY['required'::text, 'preflight'::text, 'ready'::text, 'running'::text, 'paused_retryable'::text, 'verifying'::text, 'ready_to_cutover'::text]));


--
-- Name: idx_v2_migration_runs_state_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_migration_runs_state_updated ON public.v2_migration_runs USING btree (state, updated_at DESC);


--
-- Name: v2_migration_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_runs v2_migration_runs_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_runs_system_select ON public.v2_migration_runs FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
