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
-- Name: evidence_fragments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_fragments (
    team_id uuid NOT NULL,
    fragment_id uuid DEFAULT gen_random_uuid() NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    source_id uuid,
    source_revision_id uuid,
    evidence_index integer NOT NULL,
    content text NOT NULL,
    content_hash text NOT NULL,
    source_type text DEFAULT 'conversation'::text NOT NULL,
    authority text DEFAULT 'primary'::text NOT NULL,
    source_ref text DEFAULT ''::text NOT NULL,
    labels text[] DEFAULT ARRAY[]::text[] NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_fragments_authority_check CHECK ((authority = ANY (ARRAY['authoritative'::text, 'primary'::text, 'secondary'::text, 'inferred'::text, 'unknown'::text]))),
    CONSTRAINT evidence_fragments_content_nonempty CHECK ((btrim(content) <> ''::text)),
    CONSTRAINT evidence_fragments_hash_nonempty CHECK ((btrim(content_hash) <> ''::text)),
    CONSTRAINT evidence_fragments_index_check CHECK ((evidence_index >= 0)),
    CONSTRAINT evidence_fragments_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT evidence_fragments_source_revision_pair_check CHECK ((((source_id IS NULL) AND (source_revision_id IS NULL)) OR ((source_id IS NOT NULL) AND (source_revision_id IS NOT NULL)))),
    CONSTRAINT evidence_fragments_source_type_check CHECK ((source_type = ANY (ARRAY['conversation'::text, 'document'::text, 'observation'::text, 'manual'::text])))
);

ALTER TABLE ONLY public.evidence_fragments FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_fragments evidence_fragments_fragment_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_fragment_owner_ref_unique UNIQUE (team_id, fragment_id, owner_profile_id);


--
-- Name: evidence_fragments evidence_fragments_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_owner_ref_unique UNIQUE (team_id, fragment_id, ingest_id, owner_profile_id);


--
-- Name: evidence_fragments evidence_fragments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_pkey PRIMARY KEY (team_id, fragment_id);


--
-- Name: evidence_fragments evidence_fragments_team_id_ingest_id_evidence_index_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_ingest_id_evidence_index_key UNIQUE (team_id, ingest_id, evidence_index);


--
-- Name: evidence_fragments_ingest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_fragments_ingest_idx ON public.evidence_fragments USING btree (team_id, ingest_id, evidence_index);


--
-- Name: evidence_fragments_source_revision_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_fragments_source_revision_idx ON public.evidence_fragments USING btree (team_id, source_revision_id, evidence_index) WHERE (source_revision_id IS NOT NULL);


--
-- Name: evidence_fragments evidence_fragments_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_fragments_append_only BEFORE DELETE OR UPDATE ON public.evidence_fragments FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_fragments evidence_fragments_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_ingest_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_ingest_id_owner_profile_id_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_source_id_fkey FOREIGN KEY (team_id, source_id) REFERENCES public.evidence_sources(team_id, source_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_source_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_source_id_owner_profile_id_fkey FOREIGN KEY (team_id, source_id, owner_profile_id) REFERENCES public.evidence_sources(team_id, source_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_source_id_source_revision_id_ow_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_source_id_source_revision_id_ow_fkey FOREIGN KEY (team_id, source_id, source_revision_id, owner_profile_id) REFERENCES public.evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments evidence_fragments_team_id_source_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_fragments
    ADD CONSTRAINT evidence_fragments_team_id_source_revision_id_fkey FOREIGN KEY (team_id, source_revision_id) REFERENCES public.evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT;


--
-- Name: evidence_fragments; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_fragments ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_fragments evidence_fragments_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_fragments_insert ON public.evidence_fragments FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_fragments evidence_fragments_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_fragments_select ON public.evidence_fragments FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
