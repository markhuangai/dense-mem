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
-- Name: relationship_cross_references; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_cross_references (
    team_id uuid NOT NULL,
    cross_reference_id uuid DEFAULT gen_random_uuid() NOT NULL,
    author_profile_id uuid NOT NULL,
    source_relationship_id uuid NOT NULL,
    source_relationship_version integer CONSTRAINT relationship_cross_referenc_source_relationship_versio_not_null NOT NULL,
    target_relationship_id uuid NOT NULL,
    target_relationship_version integer CONSTRAINT relationship_cross_referenc_target_relationship_versio_not_null NOT NULL,
    kind text NOT NULL,
    verification_event_id uuid NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_cross_references_kind_check CHECK ((kind = ANY (ARRAY['confirms'::text, 'challenges'::text, 'corrects'::text, 'adopts_evidence_from'::text]))),
    CONSTRAINT relationship_cross_references_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_cross_references_version_check CHECK (((source_relationship_version >= 1) AND (target_relationship_version >= 1)))
);

ALTER TABLE ONLY public.relationship_cross_references FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_cross_references relationship_cross_references_identity_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_identity_unique UNIQUE (team_id, author_profile_id, source_relationship_id, target_relationship_id, kind, verification_event_id);


--
-- Name: relationship_cross_references relationship_cross_references_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_pkey PRIMARY KEY (team_id, cross_reference_id);


--
-- Name: relationship_cross_references relationship_cross_references_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_cross_references_append_only BEFORE DELETE OR UPDATE ON public.relationship_cross_references FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_cross_references relationship_cross_references_team_id_author_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_team_id_author_profile_id_fkey FOREIGN KEY (team_id, author_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_cross_references relationship_cross_references_team_id_source_relationship__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_team_id_source_relationship__fkey FOREIGN KEY (team_id, source_relationship_id, author_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_cross_references relationship_cross_references_team_id_target_relationship__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_team_id_target_relationship__fkey FOREIGN KEY (team_id, target_relationship_id) REFERENCES public.relationship_records(team_id, relationship_id) ON DELETE RESTRICT;


--
-- Name: relationship_cross_references relationship_cross_references_team_id_verification_event_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_cross_references
    ADD CONSTRAINT relationship_cross_references_team_id_verification_event_i_fkey FOREIGN KEY (team_id, verification_event_id, author_profile_id) REFERENCES public.verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_cross_references; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_cross_references ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_cross_references relationship_cross_references_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_cross_references_insert ON public.relationship_cross_references FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (author_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_cross_references relationship_cross_references_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_cross_references_select ON public.relationship_cross_references FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
