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
-- Name: relationship_conflict_derived_evidence_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_derived_evidence_tasks (
    team_id uuid NOT NULL,
    derived_evidence_task_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_conflict_derived_derived_evidence_task_id_not_null NOT NULL,
    resolution_plan_id uuid CONSTRAINT relationship_conflict_derived_evide_resolution_plan_id_not_null NOT NULL,
    conflict_id uuid CONSTRAINT relationship_conflict_derived_evidence_tas_conflict_id_not_null NOT NULL,
    target_fragment_id uuid CONSTRAINT relationship_conflict_derived_evide_target_fragment_id_not_null NOT NULL,
    target_owner_profile_id uuid CONSTRAINT relationship_conflict_derived__target_owner_profile_id_not_null NOT NULL,
    selected_position_id uuid CONSTRAINT relationship_conflict_derived_evi_selected_position_id_not_null NOT NULL,
    system_profile_id uuid CONSTRAINT relationship_conflict_derived_eviden_system_profile_id_not_null NOT NULL,
    source_group_key text CONSTRAINT relationship_conflict_derived_evidenc_source_group_key_not_null NOT NULL,
    origin_evidence_index integer CONSTRAINT relationship_conflict_derived_ev_origin_evidence_index_not_null NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    lease_worker_id text,
    lease_until timestamp with time zone,
    last_review_run_id uuid,
    last_failure_class text DEFAULT ''::text CONSTRAINT relationship_conflict_derived_evide_last_failure_class_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT now() CONSTRAINT relationship_conflict_derived_evidence_task_created_at_not_null NOT NULL,
    updated_at timestamp with time zone DEFAULT now() CONSTRAINT relationship_conflict_derived_evidence_task_updated_at_not_null NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT relationship_conflict_derived_evidence_tasks_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_failure_length_che CHECK ((char_length(last_failure_class) <= 128)),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_origin_index_check CHECK ((origin_evidence_index >= 0)),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_source_group_check CHECK ((btrim(source_group_key) <> ''::text)),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'completed'::text])))
);

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_evidence_tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_evidence_tasks_pkey PRIMARY KEY (team_id, derived_evidence_task_id);


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_team_id_conflict_id_target_fr_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_team_id_conflict_id_target_fr_key UNIQUE (team_id, conflict_id, target_fragment_id);


--
-- Name: relationship_conflict_derived_evidence_tasks_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_derived_evidence_tasks_claim_idx ON public.relationship_conflict_derived_evidence_tasks USING btree (team_id, status, lease_until, created_at, derived_evidence_task_id) WHERE (status = ANY (ARRAY['pending'::text, 'processing'::text]));


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_e_team_id_resolution_plan_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_e_team_id_resolution_plan_id_fkey FOREIGN KEY (team_id, resolution_plan_id) REFERENCES public.relationship_conflict_resolution_plans(team_id, resolution_plan_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_ev_team_id_system_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_ev_team_id_system_profile_id_fkey FOREIGN KEY (team_id, system_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_evidence_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_evidence_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_team_id_selected_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_team_id_selected_position_id_fkey FOREIGN KEY (team_id, selected_position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_team_id_target_fragment_id_t_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_derived_evidence_tasks
    ADD CONSTRAINT relationship_conflict_derived_team_id_target_fragment_id_t_fkey FOREIGN KEY (team_id, target_fragment_id, target_owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_derived_evidence_tasks; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_derived_evidence_tasks ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_evidence_tasks_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_derived_evidence_tasks_insert ON public.relationship_conflict_derived_evidence_tasks FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_evidence_tasks_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_derived_evidence_tasks_select ON public.relationship_conflict_derived_evidence_tasks FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_derived_evidence_tasks relationship_conflict_derived_evidence_tasks_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_derived_evidence_tasks_update ON public.relationship_conflict_derived_evidence_tasks FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
