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
-- Name: evidence_source_revisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_source_revisions (
    team_id uuid NOT NULL,
    source_revision_id uuid DEFAULT gen_random_uuid() NOT NULL,
    source_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    revision_token text NOT NULL,
    expected_previous_revision_token text DEFAULT ''::text CONSTRAINT evidence_source_revisions_expected_previous_revision_t_not_null NOT NULL,
    supersedes_revision_id uuid,
    content_hash text NOT NULL,
    envelope jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_source_revisions_envelope_object_check CHECK ((jsonb_typeof(envelope) = 'object'::text)),
    CONSTRAINT evidence_source_revisions_hash_nonempty CHECK ((btrim(content_hash) <> ''::text)),
    CONSTRAINT evidence_source_revisions_token_nonempty CHECK ((btrim(revision_token) <> ''::text))
);

ALTER TABLE ONLY public.evidence_source_revisions FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_source_revisions evidence_source_revisions_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_owner_ref_unique UNIQUE (team_id, source_revision_id, owner_profile_id);


--
-- Name: evidence_source_revisions evidence_source_revisions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_pkey PRIMARY KEY (team_id, source_revision_id);


--
-- Name: evidence_source_revisions evidence_source_revisions_source_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_source_owner_ref_unique UNIQUE (team_id, source_id, source_revision_id, owner_profile_id);


--
-- Name: evidence_source_revisions_token_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX evidence_source_revisions_token_unique ON public.evidence_source_revisions USING btree (team_id, source_id, revision_token);


--
-- Name: evidence_source_revisions evidence_source_revisions_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_source_revisions_append_only BEFORE DELETE OR UPDATE ON public.evidence_source_revisions FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_source_revisions evidence_source_revisions_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_source_revisions evidence_source_revisions_team_id_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_team_id_source_id_fkey FOREIGN KEY (team_id, source_id) REFERENCES public.evidence_sources(team_id, source_id) ON DELETE RESTRICT;


--
-- Name: evidence_source_revisions evidence_source_revisions_team_id_source_id_owner_profile__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_team_id_source_id_owner_profile__fkey FOREIGN KEY (team_id, source_id, owner_profile_id) REFERENCES public.evidence_sources(team_id, source_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_source_revisions evidence_source_revisions_team_id_source_id_supersedes_rev_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_team_id_source_id_supersedes_rev_fkey FOREIGN KEY (team_id, source_id, supersedes_revision_id, owner_profile_id) REFERENCES public.evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_source_revisions evidence_source_revisions_team_id_supersedes_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_source_revisions
    ADD CONSTRAINT evidence_source_revisions_team_id_supersedes_revision_id_fkey FOREIGN KEY (team_id, supersedes_revision_id) REFERENCES public.evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT;


--
-- Name: evidence_source_revisions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_source_revisions ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_source_revisions evidence_source_revisions_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_source_revisions_insert ON public.evidence_source_revisions FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_source_revisions evidence_source_revisions_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_source_revisions_select ON public.evidence_source_revisions FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
