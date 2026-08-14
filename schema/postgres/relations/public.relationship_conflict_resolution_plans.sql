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
-- Name: relationship_conflict_resolution_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_resolution_plans (
    team_id uuid NOT NULL,
    resolution_plan_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_conflict_resolution_pl_resolution_plan_id_not_null NOT NULL,
    conflict_id uuid NOT NULL,
    expected_case_version integer CONSTRAINT relationship_conflict_resolution_expected_case_version_not_null NOT NULL,
    preferred_position_id uuid CONSTRAINT relationship_conflict_resolution_preferred_position_id_not_null NOT NULL,
    assessment_attempt_id uuid,
    method text NOT NULL,
    effective_at timestamp with time zone NOT NULL,
    effective_time_basis text DEFAULT 'recorded_at'::text CONSTRAINT relationship_conflict_resolution__effective_time_basis_not_null NOT NULL,
    status text DEFAULT 'resolution_pending'::text NOT NULL,
    failure_reason text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    applied_at timestamp with time zone,
    CONSTRAINT relationship_conflict_resolution_plans_effective_basis_check CHECK ((effective_time_basis = ANY (ARRAY['valid_from'::text, 'recorded_at'::text]))),
    CONSTRAINT relationship_conflict_resolution_plans_method_check CHECK ((method = ANY (ARRAY['ai'::text, 'last_write_wins'::text]))),
    CONSTRAINT relationship_conflict_resolution_plans_status_check CHECK ((status = ANY (ARRAY['resolution_pending'::text, 'applied'::text, 'superseded'::text, 'failed'::text]))),
    CONSTRAINT relationship_conflict_resolution_plans_version_check CHECK ((expected_case_version >= 1))
);

ALTER TABLE ONLY public.relationship_conflict_resolution_plans FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolut_team_id_conflict_id_expected__key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_resolution_plans
    ADD CONSTRAINT relationship_conflict_resolut_team_id_conflict_id_expected__key UNIQUE (team_id, conflict_id, expected_case_version);


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolution_plans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_resolution_plans
    ADD CONSTRAINT relationship_conflict_resolution_plans_pkey PRIMARY KEY (team_id, resolution_plan_id);


--
-- Name: relationship_conflict_resolution_plans_applied_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_resolution_plans_applied_idx ON public.relationship_conflict_resolution_plans USING btree (team_id, applied_at) WHERE ((status = 'applied'::text) AND (method = 'last_write_wins'::text));


--
-- Name: relationship_conflict_resolution_plans_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_resolution_plans_pending_idx ON public.relationship_conflict_resolution_plans USING btree (team_id, status, created_at) WHERE (status = 'resolution_pending'::text);


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolut_team_id_assessment_attempt_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_resolution_plans
    ADD CONSTRAINT relationship_conflict_resolut_team_id_assessment_attempt_i_fkey FOREIGN KEY (team_id, assessment_attempt_id) REFERENCES public.relationship_conflict_ai_assessment_attempts(team_id, assessment_attempt_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolut_team_id_preferred_position_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_resolution_plans
    ADD CONSTRAINT relationship_conflict_resolut_team_id_preferred_position_i_fkey FOREIGN KEY (team_id, preferred_position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolution_plans_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_resolution_plans
    ADD CONSTRAINT relationship_conflict_resolution_plans_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_resolution_plans; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_resolution_plans ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolution_plans_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_resolution_plans_insert ON public.relationship_conflict_resolution_plans FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolution_plans_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_resolution_plans_select ON public.relationship_conflict_resolution_plans FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_resolution_plans relationship_conflict_resolution_plans_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_resolution_plans_update ON public.relationship_conflict_resolution_plans FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
