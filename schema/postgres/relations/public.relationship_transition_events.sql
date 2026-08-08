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
-- Name: relationship_transition_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_transition_events (
    team_id uuid NOT NULL,
    transition_id uuid DEFAULT gen_random_uuid() NOT NULL,
    relationship_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    from_status text,
    to_status text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    verification_event_id uuid,
    support_decision_id uuid,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    CONSTRAINT relationship_transitions_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_transitions_status_check CHECK ((((from_status IS NULL) OR (from_status = ANY (ARRAY['pending_evidence'::text, 'active'::text, 'needs_review'::text, 'quarantined'::text, 'superseded'::text, 'disputed'::text, 'retracted'::text, 'rejected'::text]))) AND (to_status = ANY (ARRAY['pending_evidence'::text, 'active'::text, 'needs_review'::text, 'quarantined'::text, 'superseded'::text, 'disputed'::text, 'retracted'::text, 'rejected'::text]))))
);

ALTER TABLE ONLY public.relationship_transition_events FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_transition_events relationship_transition_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_transition_events
    ADD CONSTRAINT relationship_transition_events_pkey PRIMARY KEY (team_id, transition_id);


--
-- Name: relationship_transition_events_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_transition_events_idempotency_unique ON public.relationship_transition_events USING btree (team_id, owner_profile_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: relationship_transition_events_relationship_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_transition_events_relationship_created_idx ON public.relationship_transition_events USING btree (team_id, relationship_id, created_at DESC, transition_id DESC);


--
-- Name: relationship_transition_events relationship_transitions_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_transitions_append_only BEFORE DELETE OR UPDATE ON public.relationship_transition_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_transition_events relationship_transition_event_team_id_relationship_id_owne_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_transition_events
    ADD CONSTRAINT relationship_transition_event_team_id_relationship_id_owne_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_transition_events relationship_transition_event_team_id_support_decision_id__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_transition_events
    ADD CONSTRAINT relationship_transition_event_team_id_support_decision_id__fkey FOREIGN KEY (team_id, support_decision_id, owner_profile_id) REFERENCES public.relationship_support_decision_events(team_id, support_decision_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_transition_events relationship_transition_event_team_id_verification_event_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_transition_events
    ADD CONSTRAINT relationship_transition_event_team_id_verification_event_i_fkey FOREIGN KEY (team_id, verification_event_id, owner_profile_id) REFERENCES public.verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_transition_events relationship_transition_events_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_transition_events
    ADD CONSTRAINT relationship_transition_events_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_transition_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_transition_events ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_transition_events relationship_transition_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_transition_events_insert ON public.relationship_transition_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_transition_events relationship_transition_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_transition_events_select ON public.relationship_transition_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
