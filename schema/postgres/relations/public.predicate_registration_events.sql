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
-- Name: predicate_registration_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.predicate_registration_events (
    team_id uuid NOT NULL,
    predicate_registration_event_id uuid DEFAULT gen_random_uuid() CONSTRAINT predicate_registration_even_predicate_registration_eve_not_null NOT NULL,
    placement_run_id uuid NOT NULL,
    assessment_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    relationship_ref text NOT NULL,
    registration_action text NOT NULL,
    predicate_key text NOT NULL,
    predicate_version integer NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT predicate_registration_events_action_check CHECK ((registration_action = ANY (ARRAY['created'::text, 'reused'::text]))),
    CONSTRAINT predicate_registration_events_key_nonempty CHECK ((btrim(predicate_key) <> ''::text)),
    CONSTRAINT predicate_registration_events_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT predicate_registration_events_ref_nonempty CHECK ((btrim(relationship_ref) <> ''::text)),
    CONSTRAINT predicate_registration_events_version_check CHECK ((predicate_version >= 1))
);

ALTER TABLE ONLY public.predicate_registration_events FORCE ROW LEVEL SECURITY;


--
-- Name: predicate_registration_events predicate_registration_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_pkey PRIMARY KEY (team_id, predicate_registration_event_id);


--
-- Name: predicate_registration_events predicate_registration_events_team_id_placement_run_id_rela_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_team_id_placement_run_id_rela_key UNIQUE (team_id, placement_run_id, relationship_ref);


--
-- Name: predicate_registration_events_run_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX predicate_registration_events_run_created_idx ON public.predicate_registration_events USING btree (team_id, placement_run_id, created_at, predicate_registration_event_id);


--
-- Name: predicate_registration_events predicate_registration_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER predicate_registration_events_append_only BEFORE DELETE OR UPDATE ON public.predicate_registration_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: predicate_registration_events predicate_registration_events_team_id_assessment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_team_id_assessment_id_fkey FOREIGN KEY (team_id, assessment_id) REFERENCES public.placement_assessments(team_id, assessment_id) ON DELETE RESTRICT;


--
-- Name: predicate_registration_events predicate_registration_events_team_id_placement_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_team_id_placement_run_id_fkey FOREIGN KEY (team_id, placement_run_id) REFERENCES public.placement_runs(team_id, placement_run_id) ON DELETE RESTRICT;


--
-- Name: predicate_registration_events predicate_registration_events_team_id_placement_run_id_own_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_team_id_placement_run_id_own_fkey FOREIGN KEY (team_id, placement_run_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: predicate_registration_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.predicate_registration_events ENABLE ROW LEVEL SECURITY;

--
-- Name: predicate_registration_events predicate_registration_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY predicate_registration_events_insert ON public.predicate_registration_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: predicate_registration_events predicate_registration_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY predicate_registration_events_select ON public.predicate_registration_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
