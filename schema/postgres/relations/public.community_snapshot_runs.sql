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

SET default_tablespace = '';

SET default_table_access_method = heap;

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
-- Name: community_snapshot_runs_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_snapshot_runs_status_idx ON public.community_snapshot_runs USING btree (team_id, status, updated_at DESC);


--
-- Name: community_snapshot_runs community_snapshot_runs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_snapshot_runs
    ADD CONSTRAINT community_snapshot_runs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE RESTRICT;


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
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
