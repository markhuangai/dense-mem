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
-- Name: community_records community_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_records
    ADD CONSTRAINT community_records_pkey PRIMARY KEY (team_id, community_id);


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
-- Name: community_records community_records_team_id_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_records
    ADD CONSTRAINT community_records_team_id_run_id_fkey FOREIGN KEY (team_id, run_id) REFERENCES public.community_snapshot_runs(team_id, run_id) ON DELETE RESTRICT;


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
-- PostgreSQL database dump complete
--
