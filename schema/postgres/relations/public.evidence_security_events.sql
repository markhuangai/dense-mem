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
-- Name: evidence_security_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_security_events (
    team_id uuid NOT NULL,
    security_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    fragment_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    event_kind text NOT NULL,
    decision text NOT NULL,
    actor_profile_id uuid,
    reason text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_security_decision_check CHECK ((decision = ANY (ARRAY['pass'::text, 'guarded'::text, 'quarantine'::text, 'released'::text]))),
    CONSTRAINT evidence_security_event_kind_check CHECK ((event_kind = ANY (ARRAY['deterministic_scan'::text, 'reviewer_signal'::text, 'verifier_signal'::text, 'quarantine_release'::text]))),
    CONSTRAINT evidence_security_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.evidence_security_events FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_security_events evidence_security_events_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_owner_ref_unique UNIQUE (team_id, security_event_id, owner_profile_id);


--
-- Name: evidence_security_events evidence_security_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_pkey PRIMARY KEY (team_id, security_event_id);


--
-- Name: evidence_security_events_decision_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_security_events_decision_idx ON public.evidence_security_events USING btree (team_id, decision, created_at DESC);


--
-- Name: evidence_security_events_fragment_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_security_events_fragment_idx ON public.evidence_security_events USING btree (team_id, fragment_id, created_at, security_event_id);


--
-- Name: evidence_security_events evidence_security_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_security_events_append_only BEFORE DELETE OR UPDATE ON public.evidence_security_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_security_events evidence_security_events_team_id_actor_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_actor_profile_id_fkey FOREIGN KEY (team_id, actor_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_fragment_id_ingest_id_own_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_fragment_id_ingest_id_own_fkey FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_ingest_id_owner_profile_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_ingest_id_owner_profile_i_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events evidence_security_events_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_events
    ADD CONSTRAINT evidence_security_events_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_security_events ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_security_events evidence_security_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_security_events_insert ON public.evidence_security_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_security_events evidence_security_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_security_events_select ON public.evidence_security_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
