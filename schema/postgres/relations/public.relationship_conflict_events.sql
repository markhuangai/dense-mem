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
-- Name: relationship_conflict_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_events (
    team_id uuid NOT NULL,
    conflict_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    conflict_id uuid NOT NULL,
    position_id uuid,
    relationship_id uuid,
    owner_profile_id uuid,
    action text NOT NULL,
    outcome text DEFAULT ''::text NOT NULL,
    actor_kind text DEFAULT 'system'::text NOT NULL,
    actor_profile_id uuid,
    policy_version text DEFAULT 'cross_profile_conflict_v1'::text NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_conflict_events_action_check CHECK ((action = ANY (ARRAY['opened'::text, 'position_added'::text, 'member_added'::text, 'evaluated'::text, 'marked_overdue'::text, 'resolved'::text, 'relationship_updated'::text, 'dismissed'::text, 'ai_assessment_reserved'::text, 'ai_assessed'::text, 'resolution_pending'::text, 'evidence_retracted'::text, 'derived_replacement_staged'::text, 'derived_replacement_failed'::text]))),
    CONSTRAINT relationship_conflict_events_actor_check CHECK ((actor_kind = ANY (ARRAY['system'::text, 'profile'::text]))),
    CONSTRAINT relationship_conflict_events_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.relationship_conflict_events FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_events relationship_conflict_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_pkey PRIMARY KEY (team_id, conflict_event_id);


--
-- Name: relationship_conflict_events_case_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_events_case_idx ON public.relationship_conflict_events USING btree (team_id, conflict_id, created_at DESC, conflict_event_id DESC);


--
-- Name: relationship_conflict_events_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_conflict_events_idempotency_unique ON public.relationship_conflict_events USING btree (team_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: relationship_conflict_events relationship_conflict_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_conflict_events_append_only BEFORE DELETE OR UPDATE ON public.relationship_conflict_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_conflict_events relationship_conflict_events_team_id_actor_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_team_id_actor_profile_id_fkey FOREIGN KEY (team_id, actor_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_events relationship_conflict_events_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_events relationship_conflict_events_team_id_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_team_id_position_id_fkey FOREIGN KEY (team_id, position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_events relationship_conflict_events_team_id_relationship_id_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_team_id_relationship_id_owner_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_events ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_events relationship_conflict_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_events_insert ON public.relationship_conflict_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_events relationship_conflict_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_events_select ON public.relationship_conflict_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
