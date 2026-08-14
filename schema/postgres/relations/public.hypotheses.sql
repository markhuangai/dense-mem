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
-- Name: hypotheses; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hypotheses (
    team_id uuid NOT NULL,
    hypothesis_id uuid DEFAULT gen_random_uuid() NOT NULL,
    created_by_profile_id uuid,
    status text DEFAULT 'proposed'::text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    statement text DEFAULT ''::text NOT NULL,
    rationale text DEFAULT ''::text NOT NULL,
    likelihood numeric(5,4),
    confidence numeric(5,4),
    subject_entity_id uuid,
    predicate_key text,
    predicate_version integer,
    object_entity_id uuid,
    object_value_id uuid,
    source_refs jsonb DEFAULT '[]'::jsonb NOT NULL,
    source_versions jsonb DEFAULT '{}'::jsonb NOT NULL,
    source_owner_profile_ids uuid[] DEFAULT ARRAY[]::uuid[] NOT NULL,
    content_hash text,
    cycle_run_id uuid,
    generator_kind text DEFAULT ''::text NOT NULL,
    generator_version text DEFAULT ''::text NOT NULL,
    invalidated_reason text DEFAULT ''::text NOT NULL,
    submitted_ingest_id uuid,
    submitted_at timestamp with time zone,
    canonical_hypothesis_id uuid,
    target_identity text,
    CONSTRAINT hypotheses_endpoint_choice_check CHECK ((((object_entity_id IS NULL) AND (object_value_id IS NULL)) OR ((object_entity_id IS NULL) <> (object_value_id IS NULL)))),
    CONSTRAINT hypotheses_payload_object_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT hypotheses_probability_check CHECK ((((likelihood IS NULL) OR ((likelihood >= (0)::numeric) AND (likelihood <= (1)::numeric))) AND ((confidence IS NULL) OR ((confidence >= (0)::numeric) AND (confidence <= (1)::numeric))))),
    CONSTRAINT hypotheses_source_refs_array_check CHECK ((jsonb_typeof(source_refs) = 'array'::text)),
    CONSTRAINT hypotheses_source_versions_object_check CHECK ((jsonb_typeof(source_versions) = 'object'::text)),
    CONSTRAINT hypotheses_statement_nonempty_when_sourced CHECK (((statement <> ''::text) OR (content_hash IS NULL))),
    CONSTRAINT hypotheses_status_check CHECK ((status = ANY (ARRAY['proposed'::text, 'reinforced'::text, 'stale'::text, 'rejected'::text, 'submitted'::text])))
);

ALTER TABLE ONLY public.hypotheses FORCE ROW LEVEL SECURITY;


--
-- Name: hypotheses hypotheses_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_pkey PRIMARY KEY (team_id, hypothesis_id);


--
-- Name: hypotheses_related_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hypotheses_related_active_idx ON public.hypotheses USING btree (team_id, status, updated_at DESC) WHERE ((canonical_hypothesis_id IS NULL) AND (status = ANY (ARRAY['proposed'::text, 'reinforced'::text])));


--
-- Name: hypotheses_team_content_hash_canonical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX hypotheses_team_content_hash_canonical_unique ON public.hypotheses USING btree (team_id, content_hash) WHERE ((content_hash IS NOT NULL) AND (canonical_hypothesis_id IS NULL));


--
-- Name: hypotheses_team_target_identity_canonical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX hypotheses_team_target_identity_canonical_unique ON public.hypotheses USING btree (team_id, target_identity) WHERE ((target_identity IS NOT NULL) AND (canonical_hypothesis_id IS NULL));


--
-- Name: hypotheses hypotheses_guard_provenance_trg; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER hypotheses_guard_provenance_trg BEFORE UPDATE ON public.hypotheses FOR EACH ROW EXECUTE FUNCTION public.hypotheses_guard_provenance();


--
-- Name: hypotheses hypotheses_canonical_hypothesis_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_canonical_hypothesis_fk FOREIGN KEY (team_id, canonical_hypothesis_id) REFERENCES public.hypotheses(team_id, hypothesis_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_cycle_run_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_cycle_run_fk FOREIGN KEY (team_id, cycle_run_id) REFERENCES public.dream_cycle_runs(team_id, run_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_object_entity_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_object_entity_fk FOREIGN KEY (team_id, object_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_object_value_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_object_value_fk FOREIGN KEY (team_id, object_value_id) REFERENCES public.value_records(team_id, value_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_subject_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_subject_fk FOREIGN KEY (team_id, subject_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_submitted_ingest_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_submitted_ingest_fk FOREIGN KEY (team_id, submitted_ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_team_id_created_by_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_team_id_created_by_profile_id_fkey FOREIGN KEY (team_id, created_by_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: hypotheses hypotheses_team_predicate_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypotheses
    ADD CONSTRAINT hypotheses_team_predicate_fk FOREIGN KEY (team_id, predicate_key, predicate_version) REFERENCES public.team_predicate_definitions(team_id, predicate_key, version) ON DELETE RESTRICT;


--
-- Name: hypotheses; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.hypotheses ENABLE ROW LEVEL SECURITY;

--
-- Name: hypotheses hypotheses_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypotheses_insert ON public.hypotheses FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (created_by_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: hypotheses hypotheses_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypotheses_select ON public.hypotheses FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: hypotheses hypotheses_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypotheses_update ON public.hypotheses FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
