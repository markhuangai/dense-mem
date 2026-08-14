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
-- Name: search_projection_generations_current_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX search_projection_generations_current_unique ON public.search_projection_generations USING btree (team_id, source_kind, projection_format_version) WHERE (state = 'current'::text);


--
-- Name: search_projection_generations_state_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_projection_generations_state_idx ON public.search_projection_generations USING btree (team_id, source_kind, projection_format_version, state);


--
-- Name: search_projection_generations search_projection_generations_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_projection_generations
    ADD CONSTRAINT search_projection_generations_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE RESTRICT;


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
-- PostgreSQL database dump complete
--
