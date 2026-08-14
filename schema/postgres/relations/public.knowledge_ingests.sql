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
-- PostgreSQL database dump complete
--
