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
-- Name: placement_outcomes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.placement_outcomes (
    team_id uuid NOT NULL,
    outcome_id uuid DEFAULT gen_random_uuid() NOT NULL,
    placement_run_id uuid NOT NULL,
    placement_item_id uuid,
    owner_profile_id uuid NOT NULL,
    outcome_kind text NOT NULL,
    status text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    idempotency_key text DEFAULT ''::text NOT NULL,
    CONSTRAINT placement_outcomes_kind_nonempty CHECK ((btrim(outcome_kind) <> ''::text)),
    CONSTRAINT placement_outcomes_payload_object_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT placement_outcomes_status_nonempty CHECK ((btrim(status) <> ''::text))
);

ALTER TABLE ONLY public.placement_outcomes FORCE ROW LEVEL SECURITY;


--
-- Name: placement_outcomes placement_outcomes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_pkey PRIMARY KEY (team_id, outcome_id);


--
-- Name: placement_outcomes_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX placement_outcomes_idempotency_unique ON public.placement_outcomes USING btree (team_id, owner_profile_id, idempotency_key) WHERE (idempotency_key <> ''::text);


--
-- Name: placement_outcomes_run_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_outcomes_run_idx ON public.placement_outcomes USING btree (team_id, placement_run_id, created_at, outcome_id);


--
-- Name: placement_outcomes_telemetry_first_disposition_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX placement_outcomes_telemetry_first_disposition_unique ON public.placement_outcomes USING btree (team_id, placement_run_id) WHERE (outcome_kind = 'telemetry_first_disposition'::text);


--
-- Name: placement_outcomes placement_outcomes_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER placement_outcomes_append_only BEFORE DELETE OR UPDATE ON public.placement_outcomes FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: placement_outcomes placement_outcomes_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: placement_outcomes placement_outcomes_team_id_placement_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_team_id_placement_item_id_fkey FOREIGN KEY (team_id, placement_item_id) REFERENCES public.placement_items(team_id, placement_item_id) ON DELETE RESTRICT;


--
-- Name: placement_outcomes placement_outcomes_team_id_placement_item_id_placement_run_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_team_id_placement_item_id_placement_run_fkey FOREIGN KEY (team_id, placement_item_id, placement_run_id, owner_profile_id) REFERENCES public.placement_items(team_id, placement_item_id, placement_run_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_outcomes placement_outcomes_team_id_placement_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_team_id_placement_run_id_fkey FOREIGN KEY (team_id, placement_run_id) REFERENCES public.placement_runs(team_id, placement_run_id) ON DELETE RESTRICT;


--
-- Name: placement_outcomes placement_outcomes_team_id_placement_run_id_owner_profile__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_outcomes
    ADD CONSTRAINT placement_outcomes_team_id_placement_run_id_owner_profile__fkey FOREIGN KEY (team_id, placement_run_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_outcomes; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.placement_outcomes ENABLE ROW LEVEL SECURITY;

--
-- Name: placement_outcomes placement_outcomes_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_outcomes_insert ON public.placement_outcomes FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_outcomes placement_outcomes_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_outcomes_select ON public.placement_outcomes FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
