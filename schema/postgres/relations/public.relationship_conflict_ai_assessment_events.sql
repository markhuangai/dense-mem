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
-- Name: relationship_conflict_ai_assessment_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_ai_assessment_events (
    team_id uuid NOT NULL,
    assessment_event_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_conflict_ai_assessmen_assessment_event_id_not_null NOT NULL,
    assessment_attempt_id uuid CONSTRAINT relationship_conflict_ai_assess_assessment_attempt_id_not_null1 NOT NULL,
    action text NOT NULL,
    outcome text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_conflict_ai_assessment_event_action_check CHECK ((action = ANY (ARRAY['reserved'::text, 'selected'::text, 'abstained'::text, 'failed'::text, 'superseded'::text]))),
    CONSTRAINT relationship_conflict_ai_assessment_event_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_events FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_ai_assessment_events relationship_conflict_ai_assessment_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_events
    ADD CONSTRAINT relationship_conflict_ai_assessment_events_pkey PRIMARY KEY (team_id, assessment_event_id);


--
-- Name: relationship_conflict_ai_assessment_events_attempt_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_ai_assessment_events_attempt_idx ON public.relationship_conflict_ai_assessment_events USING btree (team_id, assessment_attempt_id, created_at, assessment_event_id);


--
-- Name: relationship_conflict_ai_assessment_events relationship_conflict_ai_assessment_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_conflict_ai_assessment_events_append_only BEFORE DELETE OR UPDATE ON public.relationship_conflict_ai_assessment_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_conflict_ai_assessment_events relationship_conflict_ai_asse_team_id_assessment_attempt_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_ai_assessment_events
    ADD CONSTRAINT relationship_conflict_ai_asse_team_id_assessment_attempt_i_fkey FOREIGN KEY (team_id, assessment_attempt_id) REFERENCES public.relationship_conflict_ai_assessment_attempts(team_id, assessment_attempt_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_ai_assessment_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_ai_assessment_events ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_ai_assessment_events relationship_conflict_ai_assessment_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_ai_assessment_events_insert ON public.relationship_conflict_ai_assessment_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_ai_assessment_events relationship_conflict_ai_assessment_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_ai_assessment_events_select ON public.relationship_conflict_ai_assessment_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
