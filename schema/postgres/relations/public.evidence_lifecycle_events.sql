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
-- Name: evidence_lifecycle_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_lifecycle_events (
    team_id uuid NOT NULL,
    lifecycle_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    lifecycle_operation_id uuid NOT NULL,
    target_fragment_id uuid NOT NULL,
    replacement_fragment_id uuid,
    owner_profile_id uuid NOT NULL,
    action text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_lifecycle_events_action_check CHECK ((action = ANY (ARRAY['supersede'::text, 'retract'::text]))),
    CONSTRAINT evidence_lifecycle_events_distinct_replacement_check CHECK (((replacement_fragment_id IS NULL) OR (replacement_fragment_id <> target_fragment_id))),
    CONSTRAINT evidence_lifecycle_events_replacement_check CHECK ((((action = 'supersede'::text) AND (replacement_fragment_id IS NOT NULL)) OR ((action = 'retract'::text) AND (replacement_fragment_id IS NULL))))
);

ALTER TABLE ONLY public.evidence_lifecycle_events FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_events
    ADD CONSTRAINT evidence_lifecycle_events_pkey PRIMARY KEY (team_id, lifecycle_event_id);


--
-- Name: evidence_lifecycle_events_operation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_lifecycle_events_operation_idx ON public.evidence_lifecycle_events USING btree (team_id, lifecycle_operation_id, created_at, lifecycle_event_id);


--
-- Name: evidence_lifecycle_events_replacement_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_lifecycle_events_replacement_idx ON public.evidence_lifecycle_events USING btree (team_id, replacement_fragment_id) WHERE (replacement_fragment_id IS NOT NULL);


--
-- Name: evidence_lifecycle_events_terminal_target_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX evidence_lifecycle_events_terminal_target_unique ON public.evidence_lifecycle_events USING btree (team_id, target_fragment_id);


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_lifecycle_events_append_only BEFORE DELETE OR UPDATE ON public.evidence_lifecycle_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_team_id_lifecycle_operation_id_o_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_events
    ADD CONSTRAINT evidence_lifecycle_events_team_id_lifecycle_operation_id_o_fkey FOREIGN KEY (team_id, lifecycle_operation_id, owner_profile_id) REFERENCES public.evidence_lifecycle_operations(team_id, lifecycle_operation_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_team_id_replacement_fragment_id__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_events
    ADD CONSTRAINT evidence_lifecycle_events_team_id_replacement_fragment_id__fkey FOREIGN KEY (team_id, replacement_fragment_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_team_id_target_fragment_id_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_events
    ADD CONSTRAINT evidence_lifecycle_events_team_id_target_fragment_id_owner_fkey FOREIGN KEY (team_id, target_fragment_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_lifecycle_events ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_lifecycle_events_insert ON public.evidence_lifecycle_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_lifecycle_events evidence_lifecycle_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_lifecycle_events_select ON public.evidence_lifecycle_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
