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
-- Name: hypothesis_feedback_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hypothesis_feedback_events (
    team_id uuid NOT NULL,
    feedback_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    hypothesis_id uuid NOT NULL,
    actor_profile_id uuid NOT NULL,
    decision text NOT NULL,
    feedback text DEFAULT ''::text NOT NULL,
    submitted_ingest_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT hypothesis_feedback_events_decision_check CHECK ((decision = ANY (ARRAY['reject'::text, 'stale'::text, 'reinforce'::text, 'confirm_true'::text, 'confirm_false'::text, 'promote_candidate'::text])))
);

ALTER TABLE ONLY public.hypothesis_feedback_events FORCE ROW LEVEL SECURITY;


--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_feedback_events
    ADD CONSTRAINT hypothesis_feedback_events_pkey PRIMARY KEY (team_id, feedback_event_id);


--
-- Name: hypothesis_feedback_events_hypothesis_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hypothesis_feedback_events_hypothesis_created_idx ON public.hypothesis_feedback_events USING btree (team_id, hypothesis_id, created_at DESC);


--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_team_id_actor_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_feedback_events
    ADD CONSTRAINT hypothesis_feedback_events_team_id_actor_profile_id_fkey FOREIGN KEY (team_id, actor_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_team_id_hypothesis_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_feedback_events
    ADD CONSTRAINT hypothesis_feedback_events_team_id_hypothesis_id_fkey FOREIGN KEY (team_id, hypothesis_id) REFERENCES public.hypotheses(team_id, hypothesis_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_team_id_submitted_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_feedback_events
    ADD CONSTRAINT hypothesis_feedback_events_team_id_submitted_ingest_id_fkey FOREIGN KEY (team_id, submitted_ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_feedback_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.hypothesis_feedback_events ENABLE ROW LEVEL SECURITY;

--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypothesis_feedback_events_insert ON public.hypothesis_feedback_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (actor_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: hypothesis_feedback_events hypothesis_feedback_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypothesis_feedback_events_select ON public.hypothesis_feedback_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
