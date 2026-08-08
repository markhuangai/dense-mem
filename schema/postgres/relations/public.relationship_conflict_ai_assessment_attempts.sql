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
-- Name: relationship_conflict_ai_assessment_case_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_ai_assessment_case_idx ON public.relationship_conflict_ai_assessment_attempts USING btree (team_id, conflict_id, case_version, created_at);


--
-- Name: relationship_conflict_ai_assessment_failure_count_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_ai_assessment_failure_count_idx ON public.relationship_conflict_ai_assessment_attempts USING btree (team_id, conflict_id, case_version, model, policy_version) WHERE (status = 'failed'::text);


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
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
