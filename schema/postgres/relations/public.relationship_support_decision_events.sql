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
-- Name: relationship_support_decision_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_support_decision_events (
    team_id uuid NOT NULL,
    support_decision_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_support_decision_even_support_decision_id_not_null NOT NULL,
    support_id uuid NOT NULL,
    relationship_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    actor_profile_id uuid,
    decision text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    CONSTRAINT relationship_support_decisions_decision_check CHECK ((decision = ANY (ARRAY['grant'::text, 'revoke'::text, 'reinstate'::text]))),
    CONSTRAINT relationship_support_decisions_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.relationship_support_decision_events FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_support_decision_events relationship_support_decision_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decision_events_pkey PRIMARY KEY (team_id, support_decision_id);


--
-- Name: relationship_support_decision_events relationship_support_decisions_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decisions_owner_ref_unique UNIQUE (team_id, support_decision_id, owner_profile_id);


--
-- Name: relationship_support_decision_events_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_support_decision_events_idempotency_unique ON public.relationship_support_decision_events USING btree (team_id, owner_profile_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: relationship_support_decisions_support_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_support_decisions_support_idx ON public.relationship_support_decision_events USING btree (team_id, support_id, created_at DESC, support_decision_id DESC);


--
-- Name: relationship_support_decision_events relationship_support_decisions_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_support_decisions_append_only BEFORE DELETE OR UPDATE ON public.relationship_support_decision_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_support_decision_events relationship_support_decision_eve_team_id_actor_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decision_eve_team_id_actor_profile_id_fkey FOREIGN KEY (team_id, actor_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_support_decision_events relationship_support_decision_eve_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decision_eve_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_support_decision_events relationship_support_decision_team_id_relationship_id_owne_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decision_team_id_relationship_id_owne_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_support_decision_events relationship_support_decision_team_id_support_id_owner_pro_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_support_decision_events
    ADD CONSTRAINT relationship_support_decision_team_id_support_id_owner_pro_fkey FOREIGN KEY (team_id, support_id, owner_profile_id) REFERENCES public.relationship_evidence_supports(team_id, support_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_support_decision_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_support_decision_events ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_support_decision_events relationship_support_decision_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_support_decision_events_insert ON public.relationship_support_decision_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_support_decision_events relationship_support_decision_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_support_decision_events_select ON public.relationship_support_decision_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
