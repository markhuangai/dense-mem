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
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
