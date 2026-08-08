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
-- Name: review_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.review_tasks (
    team_id uuid NOT NULL,
    review_task_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    ingest_id uuid,
    placement_item_id uuid,
    relationship_id uuid,
    observation_id uuid,
    task_type text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    resolution jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    dedupe_key text DEFAULT ''::text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    expires_at timestamp with time zone,
    assessment_id uuid,
    CONSTRAINT review_tasks_payload_object_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT review_tasks_resolution_object_check CHECK ((jsonb_typeof(resolution) = 'object'::text)),
    CONSTRAINT review_tasks_status_check CHECK ((status = ANY (ARRAY['open'::text, 'acknowledged'::text, 'resolved'::text, 'canceled'::text, 'expired'::text]))),
    CONSTRAINT review_tasks_type_check CHECK ((task_type = ANY (ARRAY['identity_needs_review'::text, 'predicate_needs_review'::text, 'relationship_needs_review'::text, 'correction_needs_review'::text, 'policy_needs_review'::text]))),
    CONSTRAINT review_tasks_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.review_tasks FORCE ROW LEVEL SECURITY;


--
-- Name: review_tasks review_tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_pkey PRIMARY KEY (team_id, review_task_id);


--
-- Name: review_tasks_open_dedupe_key_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX review_tasks_open_dedupe_key_unique ON public.review_tasks USING btree (team_id, dedupe_key) WHERE ((dedupe_key <> ''::text) AND (status = ANY (ARRAY['open'::text, 'acknowledged'::text])));


--
-- Name: review_tasks_open_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX review_tasks_open_expiry_idx ON public.review_tasks USING btree (team_id, expires_at, review_task_id) WHERE ((status = ANY (ARRAY['open'::text, 'acknowledged'::text])) AND (expires_at IS NOT NULL));


--
-- Name: review_tasks_open_owner_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX review_tasks_open_owner_idx ON public.review_tasks USING btree (team_id, owner_profile_id, created_at) WHERE (status = ANY (ARRAY['open'::text, 'acknowledged'::text]));


--
-- Name: review_tasks_open_placement_item_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX review_tasks_open_placement_item_idx ON public.review_tasks USING btree (team_id, placement_item_id) WHERE ((placement_item_id IS NOT NULL) AND (status = ANY (ARRAY['open'::text, 'acknowledged'::text])));


--
-- Name: review_tasks review_tasks_assessment_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_assessment_ref FOREIGN KEY (team_id, assessment_id) REFERENCES public.placement_assessments(team_id, assessment_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_ingest_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_ingest_id_owner_profile_id_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_observation_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_observation_id_owner_profile_id_fkey FOREIGN KEY (team_id, observation_id, owner_profile_id) REFERENCES public.relationship_observations(team_id, observation_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_placement_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_placement_item_id_fkey FOREIGN KEY (team_id, placement_item_id) REFERENCES public.placement_items(team_id, placement_item_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_placement_item_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_placement_item_id_owner_profile_id_fkey FOREIGN KEY (team_id, placement_item_id, owner_profile_id) REFERENCES public.placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: review_tasks review_tasks_team_id_relationship_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.review_tasks
    ADD CONSTRAINT review_tasks_team_id_relationship_id_owner_profile_id_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: review_tasks; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.review_tasks ENABLE ROW LEVEL SECURITY;

--
-- Name: review_tasks review_tasks_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY review_tasks_insert ON public.review_tasks FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: review_tasks review_tasks_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY review_tasks_select ON public.review_tasks FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: review_tasks review_tasks_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY review_tasks_update ON public.review_tasks FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
