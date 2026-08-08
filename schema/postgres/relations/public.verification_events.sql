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
-- Name: verification_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.verification_events (
    team_id uuid NOT NULL,
    verification_event_id uuid DEFAULT gen_random_uuid() NOT NULL,
    observation_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    evidence_verdict text NOT NULL,
    confidence numeric(5,4),
    rationale text DEFAULT ''::text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    response_hash text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    assessment_id uuid,
    assessment_policy_version text,
    threshold_used numeric(12,10),
    gate_result text,
    CONSTRAINT verification_events_confidence_check CHECK (((confidence IS NULL) OR ((confidence >= (0)::numeric) AND (confidence <= (1)::numeric)))),
    CONSTRAINT verification_events_gate_result_check CHECK (((gate_result IS NULL) OR (gate_result = ANY (ARRAY['meets_write_threshold'::text, 'below_write_threshold'::text, 'not_applicable'::text])))),
    CONSTRAINT verification_events_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT verification_events_threshold_used_check CHECK (((threshold_used IS NULL) OR ((threshold_used >= (0)::numeric) AND (threshold_used <= (1)::numeric)))),
    CONSTRAINT verification_events_verdict_check CHECK ((evidence_verdict = ANY (ARRAY['entailed'::text, 'contradicted'::text, 'insufficient'::text])))
);

ALTER TABLE ONLY public.verification_events FORCE ROW LEVEL SECURITY;


--
-- Name: verification_events verification_events_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_events
    ADD CONSTRAINT verification_events_owner_ref_unique UNIQUE (team_id, verification_event_id, owner_profile_id);


--
-- Name: verification_events verification_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_events
    ADD CONSTRAINT verification_events_pkey PRIMARY KEY (team_id, verification_event_id);


--
-- Name: verification_events verification_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER verification_events_append_only BEFORE DELETE OR UPDATE ON public.verification_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: verification_events verification_events_assessment_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_events
    ADD CONSTRAINT verification_events_assessment_ref FOREIGN KEY (team_id, assessment_id) REFERENCES public.placement_assessments(team_id, assessment_id) ON DELETE RESTRICT;


--
-- Name: verification_events verification_events_team_id_observation_id_owner_profile_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_events
    ADD CONSTRAINT verification_events_team_id_observation_id_owner_profile_i_fkey FOREIGN KEY (team_id, observation_id, owner_profile_id) REFERENCES public.relationship_observations(team_id, observation_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: verification_events verification_events_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_events
    ADD CONSTRAINT verification_events_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: verification_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.verification_events ENABLE ROW LEVEL SECURITY;

--
-- Name: verification_events verification_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY verification_events_insert ON public.verification_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: verification_events verification_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY verification_events_select ON public.verification_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
