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
-- Name: entity_correction_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_correction_events (
    team_id uuid NOT NULL,
    correction_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    action text NOT NULL,
    survivor_entity_id uuid,
    new_entity_id uuid,
    selected_observation_ids uuid[] DEFAULT ARRAY[]::uuid[] NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entity_correction_events_action_check CHECK ((action = ANY (ARRAY['merge'::text, 'split'::text]))),
    CONSTRAINT entity_correction_events_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.entity_correction_events FORCE ROW LEVEL SECURITY;


--
-- Name: entity_correction_events entity_correction_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_events
    ADD CONSTRAINT entity_correction_events_pkey PRIMARY KEY (team_id, correction_event_id);


--
-- Name: entity_correction_events entity_correction_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER entity_correction_events_append_only BEFORE DELETE OR UPDATE ON public.entity_correction_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: entity_correction_events entity_correction_events_team_id_new_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_events
    ADD CONSTRAINT entity_correction_events_team_id_new_entity_id_fkey FOREIGN KEY (team_id, new_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_events entity_correction_events_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_events
    ADD CONSTRAINT entity_correction_events_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_events entity_correction_events_team_id_survivor_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_correction_events
    ADD CONSTRAINT entity_correction_events_team_id_survivor_entity_id_fkey FOREIGN KEY (team_id, survivor_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_correction_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.entity_correction_events ENABLE ROW LEVEL SECURITY;

--
-- Name: entity_correction_events entity_correction_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_correction_events_insert ON public.entity_correction_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_correction_events entity_correction_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_correction_events_select ON public.entity_correction_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
