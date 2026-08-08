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
-- Name: community_summary_attempts community_summary_attempts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_summary_attempts
    ADD CONSTRAINT community_summary_attempts_pkey PRIMARY KEY (team_id, attempt_id);


--
-- Name: community_summary_attempts_lookup_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_summary_attempts_lookup_idx ON public.community_summary_attempts USING btree (team_id, community_id, created_at DESC);


--
-- Name: community_summary_attempts community_summary_attempts_team_id_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_summary_attempts
    ADD CONSTRAINT community_summary_attempts_team_id_run_id_fkey FOREIGN KEY (team_id, run_id) REFERENCES public.community_snapshot_runs(team_id, run_id) ON DELETE RESTRICT;


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
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
