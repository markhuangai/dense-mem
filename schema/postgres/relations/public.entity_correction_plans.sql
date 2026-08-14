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
-- Name: entity_correction_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_correction_plans (
    team_id uuid NOT NULL,
    plan_token uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    action text NOT NULL,
    source_entity_id uuid NOT NULL,
    target_entity_id uuid,
    new_entity_id uuid,
    selected_observation_ids uuid[] DEFAULT ARRAY[]::uuid[] NOT NULL,
    blocked_observation_ids uuid[] DEFAULT ARRAY[]::uuid[] NOT NULL,
    affected_relationships jsonb DEFAULT '[]'::jsonb NOT NULL,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    impact_summary text DEFAULT ''::text NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'planned'::text NOT NULL,
    correction_event_id uuid,
    expires_at timestamp with time zone NOT NULL,
    applied_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entity_correction_plans_action_check CHECK ((action = ANY (ARRAY['merge'::text, 'split'::text]))),
    CONSTRAINT entity_correction_plans_affected_array_check CHECK ((jsonb_typeof(affected_relationships) = 'array'::text)),
    CONSTRAINT entity_correction_plans_evidence_array_check CHECK ((jsonb_typeof(evidence) = 'array'::text)),
    CONSTRAINT entity_correction_plans_merge_target_check CHECK ((((action = 'merge'::text) AND (target_entity_id IS NOT NULL)) OR ((action = 'split'::text) AND (target_entity_id IS NULL)))),
    CONSTRAINT entity_correction_plans_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT entity_correction_plans_status_check CHECK ((status = ANY (ARRAY['planned'::text, 'applied'::text])))
);

ALTER TABLE ONLY public.entity_correction_plans FORCE ROW LEVEL SECURITY;


--
-- Name: entity_correction_plans entity_correction_plans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_pkey PRIMARY KEY (team_id, plan_token);


--
-- Name: entity_correction_plans_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX entity_correction_plans_idempotency_unique ON public.entity_correction_plans USING btree (team_id, owner_profile_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: entity_correction_plans entity_correction_plans_team_id_correction_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_team_id_correction_event_id_fkey FOREIGN KEY (team_id, correction_event_id) REFERENCES public.entity_correction_events(team_id, correction_event_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_plans entity_correction_plans_team_id_new_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_team_id_new_entity_id_fkey FOREIGN KEY (team_id, new_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_plans entity_correction_plans_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_plans entity_correction_plans_team_id_source_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_team_id_source_entity_id_fkey FOREIGN KEY (team_id, source_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_plans entity_correction_plans_team_id_target_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_plans
    ADD CONSTRAINT entity_correction_plans_team_id_target_entity_id_fkey FOREIGN KEY (team_id, target_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_plans; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.entity_correction_plans ENABLE ROW LEVEL SECURITY;

--
-- Name: entity_correction_plans entity_correction_plans_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_correction_plans_insert ON public.entity_correction_plans FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_correction_plans entity_correction_plans_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_correction_plans_select ON public.entity_correction_plans FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_correction_plans entity_correction_plans_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_correction_plans_update ON public.entity_correction_plans FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
