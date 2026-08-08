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
-- Name: relationship_evidence_supports; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_evidence_supports (
    team_id uuid NOT NULL,
    support_id uuid DEFAULT gen_random_uuid() NOT NULL,
    relationship_id uuid NOT NULL,
    observation_id uuid NOT NULL,
    verification_event_id uuid NOT NULL,
    fragment_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    source_group_key text NOT NULL,
    source_id uuid,
    source_revision_id uuid,
    span_start integer NOT NULL,
    span_end integer NOT NULL,
    quote text DEFAULT ''::text NOT NULL,
    authority text DEFAULT 'primary'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_supports_authority_check CHECK ((authority = ANY (ARRAY['authoritative'::text, 'primary'::text, 'secondary'::text, 'inferred'::text, 'unknown'::text]))),
    CONSTRAINT relationship_supports_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_supports_source_group_nonempty CHECK ((btrim(source_group_key) <> ''::text)),
    CONSTRAINT relationship_supports_source_revision_pair_check CHECK ((((source_id IS NULL) AND (source_revision_id IS NULL)) OR ((source_id IS NOT NULL) AND (source_revision_id IS NOT NULL)))),
    CONSTRAINT relationship_supports_span_check CHECK (((span_start >= 0) AND (span_end > span_start)))
);

ALTER TABLE ONLY public.relationship_evidence_supports FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_evidence_supports relationship_evidence_supports_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_pkey PRIMARY KEY (team_id, support_id);


--
-- Name: relationship_evidence_supports relationship_supports_identity_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_supports_identity_unique UNIQUE (team_id, relationship_id, owner_profile_id, fragment_id, span_start, span_end);


--
-- Name: relationship_evidence_supports relationship_supports_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_supports_owner_ref_unique UNIQUE (team_id, support_id, owner_profile_id);


--
-- Name: relationship_evidence_supports relationship_supports_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_supports_append_only BEFORE DELETE OR UPDATE ON public.relationship_evidence_supports FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_fragment_id_owner_pr_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_fragment_id_owner_pr_fkey FOREIGN KEY (team_id, fragment_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_observation_id_owner_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_observation_id_owner_fkey FOREIGN KEY (team_id, observation_id, owner_profile_id) REFERENCES public.relationship_observations(team_id, observation_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_relationship_id_owne_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_relationship_id_owne_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_source_id_owner_prof_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_source_id_owner_prof_fkey FOREIGN KEY (team_id, source_id, owner_profile_id) REFERENCES public.evidence_sources(team_id, source_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_source_id_source_rev_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_source_id_source_rev_fkey FOREIGN KEY (team_id, source_id, source_revision_id, owner_profile_id) REFERENCES public.evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_source_revision_id_o_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_source_revision_id_o_fkey FOREIGN KEY (team_id, source_revision_id, owner_profile_id) REFERENCES public.evidence_source_revisions(team_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_support_team_id_verification_event_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_support_team_id_verification_event_i_fkey FOREIGN KEY (team_id, verification_event_id, owner_profile_id) REFERENCES public.verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_supports_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_supports_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_supports_team_id_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_team_id_source_id_fkey FOREIGN KEY (team_id, source_id) REFERENCES public.evidence_sources(team_id, source_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports relationship_evidence_supports_team_id_source_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_team_id_source_revision_id_fkey FOREIGN KEY (team_id, source_revision_id) REFERENCES public.evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT;


--
-- Name: relationship_evidence_supports; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_evidence_supports ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_evidence_supports relationship_evidence_supports_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_evidence_supports_insert ON public.relationship_evidence_supports FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_evidence_supports relationship_evidence_supports_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_evidence_supports_select ON public.relationship_evidence_supports FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
