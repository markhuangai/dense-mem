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
-- Name: recall_feedback_events recall_feedback_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recall_feedback_events
    ADD CONSTRAINT recall_feedback_events_pkey PRIMARY KEY (recall_id);


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
-- Name: recall_feedback_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.recall_feedback_events ENABLE ROW LEVEL SECURITY;

--
-- Name: recall_feedback_events recall_feedback_events_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY recall_feedback_events_system_access ON public.recall_feedback_events USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
