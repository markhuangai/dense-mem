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
-- Name: relationship_observations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_observations (
    team_id uuid NOT NULL,
    observation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    relationship_id uuid,
    ingest_id uuid NOT NULL,
    placement_item_id uuid,
    owner_profile_id uuid NOT NULL,
    subject_ref text NOT NULL,
    original_predicate text NOT NULL,
    object_ref text NOT NULL,
    subject_entity_id uuid,
    predicate_key text,
    predicate_version integer,
    object_entity_id uuid,
    object_value_id uuid,
    polarity text DEFAULT '+'::text NOT NULL,
    scope_key text,
    valid_from timestamp with time zone,
    valid_to timestamp with time zone,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_observations_evidence_array_check CHECK ((jsonb_typeof(evidence) = 'array'::text)),
    CONSTRAINT relationship_observations_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_observations_object_check CHECK ((NOT ((object_entity_id IS NOT NULL) AND (object_value_id IS NOT NULL)))),
    CONSTRAINT relationship_observations_object_ref_nonempty CHECK ((btrim(object_ref) <> ''::text)),
    CONSTRAINT relationship_observations_polarity_check CHECK ((polarity = ANY (ARRAY['+'::text, '-'::text]))),
    CONSTRAINT relationship_observations_predicate_nonempty CHECK ((btrim(original_predicate) <> ''::text)),
    CONSTRAINT relationship_observations_subject_ref_nonempty CHECK ((btrim(subject_ref) <> ''::text)),
    CONSTRAINT relationship_observations_valid_window_check CHECK (((valid_to IS NULL) OR (valid_from IS NULL) OR (valid_to >= valid_from)))
);

ALTER TABLE ONLY public.relationship_observations FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_observations relationship_observations_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_owner_ref_unique UNIQUE (team_id, observation_id, owner_profile_id);


--
-- Name: relationship_observations relationship_observations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_pkey PRIMARY KEY (team_id, observation_id);


--
-- Name: relationship_observations_relationship_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_observations_relationship_idx ON public.relationship_observations USING btree (team_id, relationship_id, created_at) WHERE (relationship_id IS NOT NULL);


--
-- Name: relationship_observations relationship_observations_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_observations_append_only BEFORE DELETE OR UPDATE ON public.relationship_observations FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_observations relationship_observations_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_ingest_id_owner_profile__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_ingest_id_owner_profile__fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_object_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_object_entity_id_fkey FOREIGN KEY (team_id, object_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_object_value_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_object_value_id_fkey FOREIGN KEY (team_id, object_value_id) REFERENCES public.value_records(team_id, value_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_placement_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_placement_item_id_fkey FOREIGN KEY (team_id, placement_item_id) REFERENCES public.placement_items(team_id, placement_item_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_placement_item_id_owner__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_placement_item_id_owner__fkey FOREIGN KEY (team_id, placement_item_id, owner_profile_id) REFERENCES public.placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_relationship_id_owner_pr_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_relationship_id_owner_pr_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_id_subject_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_id_subject_entity_id_fkey FOREIGN KEY (team_id, subject_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_observations relationship_observations_team_predicate_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_observations
    ADD CONSTRAINT relationship_observations_team_predicate_fkey FOREIGN KEY (team_id, predicate_key, predicate_version) REFERENCES public.team_predicate_definitions(team_id, predicate_key, version) ON DELETE RESTRICT;


--
-- Name: relationship_observations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_observations ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_observations relationship_observations_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_observations_insert ON public.relationship_observations FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_observations relationship_observations_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_observations_select ON public.relationship_observations FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
