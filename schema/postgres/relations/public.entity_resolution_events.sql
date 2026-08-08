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
-- Name: entity_resolution_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_resolution_events (
    team_id uuid NOT NULL,
    resolution_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    ingest_id uuid NOT NULL,
    placement_item_id uuid,
    owner_profile_id uuid NOT NULL,
    mention_ref text NOT NULL,
    action text NOT NULL,
    entity_id uuid,
    fragment_id uuid,
    span_start integer,
    span_end integer,
    verifier_result jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    assessment_id uuid,
    CONSTRAINT entity_resolution_events_action_check CHECK ((action = ANY (ARRAY['reuse'::text, 'create'::text, 'ambiguous'::text]))),
    CONSTRAINT entity_resolution_events_action_entity_check CHECK ((((action = ANY (ARRAY['reuse'::text, 'create'::text])) AND (entity_id IS NOT NULL)) OR ((action = 'ambiguous'::text) AND (entity_id IS NULL)))),
    CONSTRAINT entity_resolution_events_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT entity_resolution_events_ref_nonempty CHECK ((btrim(mention_ref) <> ''::text)),
    CONSTRAINT entity_resolution_events_span_check CHECK ((((span_start IS NULL) AND (span_end IS NULL)) OR ((span_start IS NOT NULL) AND (span_end IS NOT NULL) AND (span_start >= 0) AND (span_end > span_start)))),
    CONSTRAINT entity_resolution_events_verifier_object_check CHECK ((jsonb_typeof(verifier_result) = 'object'::text))
);

ALTER TABLE ONLY public.entity_resolution_events FORCE ROW LEVEL SECURITY;


--
-- Name: entity_resolution_events entity_resolution_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_pkey PRIMARY KEY (team_id, resolution_event_id);


--
-- Name: entity_resolution_events entity_resolution_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER entity_resolution_events_append_only BEFORE DELETE OR UPDATE ON public.entity_resolution_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: entity_resolution_events entity_resolution_events_assessment_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_assessment_ref FOREIGN KEY (team_id, assessment_id) REFERENCES public.placement_assessments(team_id, assessment_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_entity_id_fkey FOREIGN KEY (team_id, entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_fragment_id_owner_profile_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_fragment_id_owner_profile_fkey FOREIGN KEY (team_id, fragment_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_ingest_id_owner_profile_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_ingest_id_owner_profile_i_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_placement_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_placement_item_id_fkey FOREIGN KEY (team_id, placement_item_id) REFERENCES public.placement_items(team_id, placement_item_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events entity_resolution_events_team_id_placement_item_id_owner_p_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_team_id_placement_item_id_owner_p_fkey FOREIGN KEY (team_id, placement_item_id, owner_profile_id) REFERENCES public.placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: entity_resolution_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.entity_resolution_events ENABLE ROW LEVEL SECURITY;

--
-- Name: entity_resolution_events entity_resolution_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_resolution_events_insert ON public.entity_resolution_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_resolution_events entity_resolution_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_resolution_events_select ON public.entity_resolution_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
